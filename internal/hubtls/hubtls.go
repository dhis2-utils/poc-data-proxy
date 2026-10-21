// Package hubtls keeps the hub's certificates (docs/DEPLOYMENT.md, "Reaching the hub
// securely"): an authority made for this hub alone, which devices install once, and a
// server certificate it issues for dhis2.local, the host name, localhost and the hub's
// private addresses. The authority's name constraints limit it to those names and
// private ranges, so installing it cannot expose any other site. Its key is kept next
// to the server's key, so the hub can issue a new server certificate when its
// addresses change or the current one nears its end, without a restart.
package hubtls

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"github.com/dhis2/facility-hub/internal/atomicfile"
	"github.com/dhis2/facility-hub/internal/devkey"
	"io/fs"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Name is the address devices use for the hub.
const Name = "dhis2.local"

const (
	caLife    = 10 * 365 * 24 * time.Hour
	leafLife  = 397 * 24 * time.Hour // inside Apple's 825 days for any TLS certificate
	renewLeft = 60 * 24 * time.Hour
)

// The files in the data folder. ca.crt is what devices install.
var files = []string{"ca.crt", "ca.key", "tls.crt", "tls.key"}

// private is what a new authority permits; 100.64.0.0/10 is the shared address space
// some routers number a LAN from (carrier-grade NAT).
var private = func() []*net.IPNet {
	var out []*net.IPNet
	for _, c := range []string{"127.0.0.0/8", "::1/128", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16",
		"169.254.0.0/16", "100.64.0.0/10", "fc00::/7", "fe80::/10"} {
		_, n, _ := net.ParseCIDR(c)
		out = append(out, n)
	}
	return out
}()

// shared is 100.64.0.0/10, the shared address space (carrier-grade NAT).
var shared = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

// env is what the certificates depend on, replaced in tests.
type env struct {
	now   func() time.Time
	host  func() string
	addrs func() []net.IP
	extra []string // hub_hosts: further names devices use
}

func systemEnv() env {
	return env{now: time.Now,
		host: func() string { h, _ := os.Hostname(); return strings.ToLower(h) },
		addrs: func() []net.IP {
			var out []net.IP
			if as, err := net.InterfaceAddrs(); err == nil {
				for _, a := range as {
					if ipn, ok := a.(*net.IPNet); ok {
						out = append(out, ipn.IP)
					}
				}
			}
			return out
		}}
}

// Certs serves the hub's current server certificate.
type Certs struct {
	dir    string
	env    env
	mu     sync.Mutex // Refresh
	ca     *x509.Certificate
	caKey  *ecdsa.PrivateKey
	cur    atomic.Pointer[tls.Certificate]
	warned map[string]bool // names the authority does not cover, logged once
}

// Load reads the certificates in dir, making or renewing what is missing or stale.
// extra are the names in hub_hosts: the authority made at first boot permits them, so
// names added later are not covered until the hub makes a new authority.
func Load(dir string, extra []string) (*Certs, error) {
	e := systemEnv()
	e.extra = extra
	return load(dir, e)
}

func load(dir string, e env) (*Certs, error) {
	c := &Certs{dir: dir, env: e, warned: map[string]bool{}}
	if err := c.loadCA(); err != nil {
		return nil, err
	}
	return c, c.Refresh()
}

// Config serves whichever server certificate is current.
func (c *Certs) Config() *tls.Config {
	return &tls.Config{MinVersion: tls.VersionTLS12,
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return c.cur.Load(), nil }}
}

// Run checks every few minutes whether the server certificate needs renewing.
func (c *Certs) Run(stop <-chan struct{}) {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			if err := c.Refresh(); err != nil {
				slog.Error("renewing the hub's certificate", "err", err)
			}
		}
	}
}

// Refresh issues a new server certificate if the current one is missing, near its
// end, or does not match the hub's names and addresses.
func (c *Certs) Refresh() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	names, ips := c.wanted()
	pair, err := loadPair(c.path("tls.crt"), c.path("tls.key"))
	if err == nil {
		leaf, perr := x509.ParseCertificate(pair.Certificate[0])
		if perr == nil && c.fits(leaf, names, ips) {
			if cur := c.cur.Load(); cur == nil || !slices.EqualFunc(cur.Certificate, pair.Certificate, bytes.Equal) {
				c.cur.Store(&pair)
			}
			return nil
		}
	}
	return c.issue(names, ips)
}

func (c *Certs) fits(leaf *x509.Certificate, names []string, ips []net.IP) bool {
	if leaf.CheckSignatureFrom(c.ca) != nil || leaf.NotAfter.Sub(c.env.now()) < renewLeft {
		return false
	}
	have := slices.Clone(leaf.DNSNames)
	slices.Sort(have)
	if !slices.Equal(have, names) || len(leaf.IPAddresses) != len(ips) {
		return false
	}
	for _, ip := range ips {
		if !slices.ContainsFunc(leaf.IPAddresses, ip.Equal) {
			return false
		}
	}
	return true
}

// wanted is the names and addresses the server certificate covers: the hub's names
// and private addresses that the authority permits (one made before a range was added
// to private does not).
func (c *Certs) wanted() ([]string, []net.IP) {
	var names []string
	for _, n := range ownNames(c.env.host(), c.env.extra) {
		if slices.Contains(c.ca.PermittedDNSDomains, n) {
			names = append(names, n)
		} else if !c.warned[n] {
			c.warned[n] = true
			slog.Warn("a name is not covered by the hub's certificate: the authority was made before it (a host name or hub_hosts entry changed since)", "name", n)
		}
	}
	slices.Sort(names)
	var ips []net.IP
	for _, ip := range c.env.addrs() {
		if slices.ContainsFunc(c.ca.PermittedIPRanges, func(n *net.IPNet) bool { return n.Contains(ip) }) &&
			!slices.ContainsFunc(ips, ip.Equal) {
			ips = append(ips, ip)
		}
	}
	for _, lo := range []string{"127.0.0.1", "::1"} {
		if ip := net.ParseIP(lo); !slices.ContainsFunc(ips, ip.Equal) {
			ips = append(ips, ip)
		}
	}
	return names, ips
}

func ownNames(host string, extra []string) []string {
	out := []string{Name, "localhost"}
	var more []string
	if host != "" {
		more = append(more, host, host+".local")
	}
	for _, x := range extra {
		x = strings.ToLower(strings.TrimSuffix(x, "."))
		if net.ParseIP(x) != nil {
			continue // addresses are covered as addresses
		}
		if !ValidHostName(x) {
			slog.Warn("a name in hub_hosts is too wide for the hub's certificate; left out", "name", x)
			continue
		}
		more = append(more, x)
	}
	for _, n := range more {
		if !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	return out
}

// ValidHostName reports whether a hub_hosts name may go into the authority's name
// constraints, which also permit every name below it: a specific host, so at least
// three labels ("hub.clinic.example.org"), or two under a local-only suffix
// ("hub.lan"). "org", "example.org" or "co.uk" would let the authority vouch for whole
// domains, and an empty name for everything.
func ValidHostName(n string) bool {
	n = strings.ToLower(strings.TrimSuffix(n, "."))
	labels := strings.Split(n, ".")
	for _, l := range labels {
		if l == "" || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
			return false
		}
		for _, r := range l {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
				return false
			}
		}
	}
	switch {
	case strings.HasSuffix(n, ".home.arpa"):
		return len(labels) >= 3
	case slices.Contains([]string{"local", "lan", "internal"}, labels[len(labels)-1]):
		return len(labels) >= 2
	}
	return len(labels) >= 3
}

// loadCA reads the authority and its key, or makes a new one when there is no key
// (newCA writes the key first, so a ca.crt alone means the key was lost, and devices
// install the new authority again). A key that cannot be read (permissions, I/O)
// stops the hub rather than being replaced, since every device would have to install
// the replacement; a corrupt one (power loss) is kept aside as ca.key.bad and
// replaced, so the hub still starts.
func (c *Certs) loadCA() error {
	keyPEM, err := readFile(c.path("ca.key"))
	if errors.Is(err, fs.ErrNotExist) {
		return c.newCA()
	}
	if err != nil {
		return fmt.Errorf("the hub's certificate authority key: %w", err)
	}
	crtPEM, err := os.ReadFile(c.path("ca.crt"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("the hub's certificate authority: %w", err)
	}
	ca, key, err := parsePair(crtPEM, keyPEM)
	if err == nil && c.env.now().Before(ca.NotAfter) {
		c.ca, c.caKey = ca, key
		return nil
	}
	if err == nil {
		err = errors.New("expired")
	}
	slog.Error("the hub's certificate authority is unusable; making a new one, which devices must install", "err", err)
	for _, f := range []string{"ca.key", "ca.crt"} {
		os.Rename(c.path(f), c.path(f+".bad"))
	}
	return c.newCA()
}

func parsePair(crtPEM, keyPEM []byte) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	cb, _ := pem.Decode(crtPEM)
	kb, _ := pem.Decode(keyPEM)
	if cb == nil || kb == nil {
		return nil, nil, errors.New("not PEM")
	}
	crt, err := x509.ParseCertificate(cb.Bytes)
	if err != nil {
		return nil, nil, err
	}
	key, err := x509.ParseECPrivateKey(kb.Bytes)
	if err != nil {
		return nil, nil, err
	}
	if !key.PublicKey.Equal(crt.PublicKey) {
		return nil, nil, errors.New("key does not match the certificate")
	}
	return crt, key, nil
}

func (c *Certs) newCA() error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	now := c.env.now()
	host := c.env.host()
	tmpl := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{CommonName: "DHIS2 facility hub " + host, Organization: []string{"DHIS2 facility hub"}},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(caLife),
		KeyUsage:     x509.KeyUsageCertSign,
		// Web servers only: Windows applies no name constraints to code signing, mail
		// or client certificates, and this authority sits in its trusted roots.
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:        true, BasicConstraintsValid: true, MaxPathLenZero: true,
		PermittedDNSDomainsCritical: true,
		PermittedDNSDomains:         ownNames(host, c.env.extra),
		PermittedIPRanges:           private,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return err
	}
	ca, err := x509.ParseCertificate(der)
	if err != nil {
		return err
	}
	kb, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	// The key first: a ca.crt without its key would be replaced at the next start.
	if err := writeAtomic(c.path("ca.key"), pemBlock("EC PRIVATE KEY", kb), 0o600); err != nil {
		return err
	}
	if err := writeAtomic(c.path("ca.crt"), pemBlock("CERTIFICATE", der), 0o644); err != nil {
		return err
	}
	c.ca, c.caKey = ca, key
	os.Remove(c.path("tls.crt")) // issued by the old authority
	return nil
}

func (c *Certs) issue(names []string, ips []net.IP) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	now := c.env.now()
	tmpl := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{CommonName: Name, Organization: []string{"DHIS2 facility hub"}},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(leafLife),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     names,
		IPAddresses:  ips,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.ca, &key.PublicKey, c.caKey)
	if err != nil {
		return err
	}
	kb, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	keyPEM := pemBlock("EC PRIVATE KEY", kb)
	chain := append(pemBlock("CERTIFICATE", der), pemBlock("CERTIFICATE", c.ca.Raw)...)
	pair, err := tls.X509KeyPair(chain, keyPEM)
	if err != nil {
		return err
	}
	if err := writeAtomic(c.path("tls.key"), keyPEM, 0o600); err != nil {
		return err
	}
	if err := writeAtomic(c.path("tls.crt"), chain, 0o644); err != nil {
		return err
	}
	c.cur.Store(&pair)
	slog.Info("issued the hub's server certificate", "names", names, "addresses", fmt.Sprint(ips), "until", tmpl.NotAfter)
	return nil
}

func (c *Certs) path(name string) string { return filepath.Join(c.dir, name) }

// Fingerprint is the SHA-256 of the authority devices install, as colon-separated hex,
// for comparing the file a device got with the one central shows. Empty without one.
func Fingerprint(dir string) string {
	b, err := os.ReadFile(filepath.Join(dir, "ca.crt"))
	if err != nil {
		return ""
	}
	p, _ := pem.Decode(b)
	if p == nil {
		return ""
	}
	sum := sha256.Sum256(p.Bytes)
	parts := make([]string, len(sum))
	for i, x := range sum {
		parts[i] = fmt.Sprintf("%02X", x)
	}
	return strings.Join(parts, ":")
}

// Bundle is the certificate files, for the USB stick (sealed there like the hub's
// state). Restore writes them into a new data folder.
func Bundle(dir string) ([]byte, error) {
	m := map[string][]byte{}
	for _, f := range files {
		b, err := readFile(filepath.Join(dir, f))
		if err != nil {
			return nil, err
		}
		m[f] = b
	}
	return json.Marshal(m)
}

func Restore(dir string, bundle []byte) error {
	var m map[string][]byte
	if err := json.Unmarshal(bundle, &m); err != nil {
		return err
	}
	for _, f := range files {
		if len(m[f]) == 0 {
			return fmt.Errorf("certificate bundle without %s", f)
		}
	}
	for _, f := range files {
		mode := os.FileMode(0o644)
		if strings.HasSuffix(f, ".key") {
			mode = 0o600
		}
		if err := writeAtomic(filepath.Join(dir, f), m[f], mode); err != nil {
			return err
		}
	}
	return nil
}

// HSTS tells browsers to use HTTPS for the hub's name for a week (DEPLOYMENT.md: short,
// so devices reach a replaced hub's start page again soon).
func HSTS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS != nil {
			w.Header().Set("Strict-Transport-Security", "max-age=604800")
		}
		next.ServeHTTP(w, r)
	})
}

func pemBlock(typ string, b []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: b})
}

// The private keys are sealed with the hub's device key where it has one (devkey): a
// disk taken without its TPM then cannot pose as the hub.

func readFile(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil || !strings.HasSuffix(path, ".key") {
		return b, err
	}
	return devkey.Unprotect(path, b)
}

func loadPair(crt, key string) (tls.Certificate, error) {
	crtPEM, err := os.ReadFile(crt)
	if err != nil {
		return tls.Certificate{}, err
	}
	keyPEM, err := readFile(key)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.X509KeyPair(crtPEM, keyPEM)
}

// writeAtomic replaces path with b (internal/atomicfile), the private keys sealed.
func writeAtomic(path string, b []byte, mode os.FileMode) error {
	if strings.HasSuffix(path, ".key") {
		var err error
		if b, err = devkey.Protect(path, b); err != nil {
			return err
		}
	}
	return atomicfile.Write(path, b, mode)
}

func serial() *big.Int {
	n, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	return n
}

// Addresses is the hub's private IPv4 addresses (with the shared address space), for
// the heartbeat and the hub's page: what a device uses where dhis2.local does not resolve.
func Addresses() []string {
	var out []string
	for _, ip := range systemEnv().addrs() {
		if ip.To4() != nil && (ip.IsPrivate() || shared.Contains(ip)) {
			out = append(out, ip.String())
		}
	}
	return out
}
