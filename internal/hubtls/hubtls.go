// Package hubtls keeps the hub's certificates (docs/DEPLOYMENT.md, "Reaching the hub
// securely"): an authority made for this hub alone, which devices install once, and a
// server certificate it issues for dhis2.local, the host name, localhost and the hub's
// private addresses. The authority's name constraints limit it to those names and
// private ranges, so installing it cannot expose any other site. Its key is kept next
// to the server's key, so the hub can issue a new server certificate when its
// addresses change or the current one nears its end, without a restart.
package hubtls

import (
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

var private = func() []*net.IPNet {
	var out []*net.IPNet
	for _, c := range []string{"127.0.0.0/8", "::1/128", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16",
		"169.254.0.0/16", "fc00::/7", "fe80::/10"} {
		_, n, _ := net.ParseCIDR(c)
		out = append(out, n)
	}
	return out
}()

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
	dir   string
	env   env
	mu    sync.Mutex // Refresh
	ca    *x509.Certificate
	caKey *ecdsa.PrivateKey
	cur   atomic.Pointer[tls.Certificate]
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
	c := &Certs{dir: dir, env: e}
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
	pair, err := tls.LoadX509KeyPair(c.path("tls.crt"), c.path("tls.key"))
	if err == nil {
		leaf, perr := x509.ParseCertificate(pair.Certificate[0])
		if perr == nil && c.fits(leaf, names, ips) {
			if cur := c.cur.Load(); cur == nil || !slices.EqualFunc(cur.Certificate, pair.Certificate, bytesEqual) {
				c.cur.Store(&pair)
			}
			return nil
		}
	}
	return c.issue(names, ips)
}

func bytesEqual(a, b []byte) bool { return string(a) == string(b) }

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
// that the authority permits, and its private addresses.
func (c *Certs) wanted() ([]string, []net.IP) {
	var names []string
	for _, n := range ownNames(c.env.host(), c.env.extra) {
		if slices.Contains(c.ca.PermittedDNSDomains, n) {
			names = append(names, n)
		} else if slices.Contains(c.env.extra, n) {
			slog.Warn("a name in hub_hosts is not covered by the hub's certificate: the authority was made before it was added", "name", n)
		}
	}
	slices.Sort(names)
	var ips []net.IP
	for _, ip := range c.env.addrs() {
		if slices.ContainsFunc(private, func(n *net.IPNet) bool { return n.Contains(ip) }) &&
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
		if net.ParseIP(x) == nil { // addresses are covered as addresses
			more = append(more, strings.ToLower(strings.TrimSuffix(x, ".")))
		}
	}
	for _, n := range more {
		if !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	return out
}

// loadCA reads the authority and its key, or makes a new one. A hub from before the
// key was kept has only ca.crt: it gets a new authority, and devices install it again.
func (c *Certs) loadCA() error {
	crtPEM, err1 := os.ReadFile(c.path("ca.crt"))
	keyPEM, err2 := os.ReadFile(c.path("ca.key"))
	if err1 == nil && err2 == nil {
		ca, key, err := parsePair(crtPEM, keyPEM)
		if err == nil && c.env.now().Before(ca.NotAfter) {
			c.ca, c.caKey = ca, key
			return nil
		}
		slog.Warn("the hub's certificate authority is unusable; making a new one, which devices must install", "err", err)
	} else if err1 == nil {
		slog.Warn("the hub's certificate authority has no key (made by an older version); making a new one, which devices must install")
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
		IsCA:         true, BasicConstraintsValid: true, MaxPathLenZero: true,
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
		b, err := os.ReadFile(filepath.Join(dir, f))
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

func writeAtomic(path string, b []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, mode); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func serial() *big.Int {
	n, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	return n
}

// Addresses is the hub's private IPv4 addresses, for the heartbeat and the hub's page:
// what a device uses where dhis2.local does not resolve.
func Addresses() []string {
	var out []string
	for _, ip := range systemEnv().addrs() {
		if ip.To4() != nil && ip.IsPrivate() {
			out = append(out, ip.String())
		}
	}
	return out
}
