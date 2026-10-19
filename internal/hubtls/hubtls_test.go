package hubtls

import (
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type fakeEnv struct {
	now   time.Time
	host  string
	addrs []net.IP
}

func (f *fakeEnv) env() env {
	return env{now: func() time.Time { return f.now }, host: func() string { return f.host },
		addrs: func() []net.IP { return f.addrs }}
}

func newFake() *fakeEnv {
	return &fakeEnv{now: time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC), host: "dhis2",
		addrs: []net.IP{net.ParseIP("192.168.1.20")}}
}

func leafOf(t *testing.T, c *Certs) (*x509.Certificate, *x509.CertPool) {
	t.Helper()
	cert, err := c.Config().GetCertificate(&tls.ClientHelloInfo{})
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	b, err := os.ReadFile(filepath.Join(c.dir, "ca.crt"))
	if err != nil {
		t.Fatal(err)
	}
	pool.AppendCertsFromPEM(b)
	return leaf, pool
}

func verify(leaf *x509.Certificate, pool *x509.CertPool, name string, at time.Time) error {
	_, err := leaf.Verify(x509.VerifyOptions{Roots: pool, DNSName: name, CurrentTime: at})
	return err
}

// A device that installed ca.crt trusts the hub at dhis2.local and at its IP.
func TestTrustedByNameAndAddress(t *testing.T) {
	f := newFake()
	c, err := load(t.TempDir(), f.env())
	if err != nil {
		t.Fatal(err)
	}
	leaf, pool := leafOf(t, c)
	for _, name := range []string{"dhis2.local", "localhost", "192.168.1.20"} {
		if err := verify(leaf, pool, name, f.now); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := verify(leaf, pool, "other.local", f.now); err == nil {
		t.Error("other.local verified")
	}
	// Review L5: the server's key cannot sign certificates.
	if leaf.IsCA || leaf.KeyUsage&x509.KeyUsageCertSign != 0 {
		t.Error("the server certificate can sign")
	}
	if d := leaf.NotAfter.Sub(f.now); d > 397*24*time.Hour+time.Hour {
		t.Errorf("server certificate lasts %v, more than 397 days", d)
	}
}

// The authority can vouch only for the hub's names and private addresses.
func TestAuthorityIsConstrained(t *testing.T) {
	f := newFake()
	c, err := load(t.TempDir(), f.env())
	if err != nil {
		t.Fatal(err)
	}
	ca := c.ca
	if !ca.PermittedDNSDomainsCritical || len(ca.PermittedDNSDomains) == 0 || len(ca.PermittedIPRanges) == 0 {
		t.Fatalf("no name constraints: %+v", ca.PermittedDNSDomains)
	}
	for _, d := range ca.PermittedDNSDomains {
		switch d {
		case "dhis2.local", "localhost", "dhis2":
		default:
			t.Errorf("permitted %q", d)
		}
	}
	if ca.NotAfter.Sub(f.now) < 9*365*24*time.Hour {
		t.Errorf("authority lasts only until %v", ca.NotAfter)
	}
}

// Restarting keeps the authority and the server certificate.
func TestReloadKeepsCertificates(t *testing.T) {
	f, dir := newFake(), t.TempDir()
	c1, err := load(dir, f.env())
	if err != nil {
		t.Fatal(err)
	}
	l1, _ := leafOf(t, c1)
	c2, err := load(dir, f.env())
	if err != nil {
		t.Fatal(err)
	}
	l2, _ := leafOf(t, c2)
	if !l1.Equal(l2) || !c1.ca.Equal(c2.ca) {
		t.Error("a restart issued new certificates")
	}
}

// A new address gets a new server certificate from the same authority, without a restart.
func TestNewAddressReissues(t *testing.T) {
	f := newFake()
	c, err := load(t.TempDir(), f.env())
	if err != nil {
		t.Fatal(err)
	}
	ca := c.ca
	f.addrs = []net.IP{net.ParseIP("10.0.0.7")}
	if err := c.Refresh(); err != nil {
		t.Fatal(err)
	}
	leaf, pool := leafOf(t, c)
	if err := verify(leaf, pool, "10.0.0.7", f.now); err != nil {
		t.Error(err)
	}
	if !c.ca.Equal(ca) {
		t.Error("the authority changed")
	}
}

// Public addresses are left out: the authority could not vouch for them.
func TestPublicAddressLeftOut(t *testing.T) {
	f := newFake()
	f.addrs = append(f.addrs, net.ParseIP("8.8.4.4"))
	c, err := load(t.TempDir(), f.env())
	if err != nil {
		t.Fatal(err)
	}
	leaf, pool := leafOf(t, c)
	if err := verify(leaf, pool, "192.168.1.20", f.now); err != nil {
		t.Error(err)
	}
	for _, ip := range leaf.IPAddresses {
		if ip.Equal(net.ParseIP("8.8.4.4")) {
			t.Error("public address in the certificate")
		}
	}
}

// Fewer than 60 days left: a new server certificate.
func TestRenewsBeforeExpiry(t *testing.T) {
	f := newFake()
	c, err := load(t.TempDir(), f.env())
	if err != nil {
		t.Fatal(err)
	}
	old, _ := leafOf(t, c)
	f.now = old.NotAfter.Add(-59 * 24 * time.Hour)
	if err := c.Refresh(); err != nil {
		t.Fatal(err)
	}
	leaf, pool := leafOf(t, c)
	if leaf.Equal(old) {
		t.Fatal("not renewed")
	}
	if err := verify(leaf, pool, "dhis2.local", old.NotAfter.Add(24*time.Hour)); err != nil {
		t.Error(err)
	}
}

// A hub from before the authority's key was kept makes a new authority once.
func TestOldLayoutGetsNewAuthority(t *testing.T) {
	f, dir := newFake(), t.TempDir()
	c1, err := load(dir, f.env())
	if err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(dir, "ca.key"))
	c2, err := load(dir, f.env())
	if err != nil {
		t.Fatal(err)
	}
	if c2.ca.Equal(c1.ca) {
		t.Error("kept an authority without its key")
	}
	leaf, pool := leafOf(t, c2)
	if err := verify(leaf, pool, "dhis2.local", f.now); err != nil {
		t.Error(err)
	}
}

// The bundle for the USB stick puts back the same authority and server certificate.
func TestBundleRestores(t *testing.T) {
	f, dir := newFake(), t.TempDir()
	c1, err := load(dir, f.env())
	if err != nil {
		t.Fatal(err)
	}
	b, err := Bundle(dir)
	if err != nil {
		t.Fatal(err)
	}
	dir2 := t.TempDir()
	if err := Restore(dir2, b); err != nil {
		t.Fatal(err)
	}
	c2, err := load(dir2, f.env())
	if err != nil {
		t.Fatal(err)
	}
	l1, _ := leafOf(t, c1)
	l2, _ := leafOf(t, c2)
	if !c1.ca.Equal(c2.ca) || !l1.Equal(l2) {
		t.Error("restore changed the certificates")
	}
	if Fingerprint(dir) == "" || Fingerprint(dir) != Fingerprint(dir2) {
		t.Errorf("fingerprints %q, %q", Fingerprint(dir), Fingerprint(dir2))
	}
	if st, _ := os.Stat(filepath.Join(dir2, "ca.key")); st.Mode().Perm() != 0o600 {
		t.Errorf("ca.key mode %v", st.Mode().Perm())
	}
}

// HTTPS answers tell browsers to keep using HTTPS for a week; plain HTTP never does.
func TestHSTS(t *testing.T) {
	h := HSTS(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	r := httptest.NewRequest("GET", "https://dhis2.local/", nil)
	r.TLS = &tls.ConnectionState{}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Header().Get("Strict-Transport-Security") != "max-age=604800" {
		t.Errorf("HTTPS: %q", w.Header().Get("Strict-Transport-Security"))
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "http://dhis2.local/", nil))
	if w.Header().Get("Strict-Transport-Security") != "" {
		t.Error("HSTS on plain HTTP")
	}
}

// Names in hub_hosts are names devices use: the certificate covers them too.
func TestExtraNamesCovered(t *testing.T) {
	f := newFake()
	e := f.env()
	e.extra = []string{"Hub.Clinic.Example"}
	c, err := load(t.TempDir(), e)
	if err != nil {
		t.Fatal(err)
	}
	leaf, pool := leafOf(t, c)
	if err := verify(leaf, pool, "hub.clinic.example", f.now); err != nil {
		t.Error(err)
	}
}

// Review: the authority vouches for web servers only, not for code, mail or clients
// (Windows applies no name constraints to code signing).
func TestAuthorityOnlyForServers(t *testing.T) {
	c, err := load(t.TempDir(), newFake().env())
	if err != nil {
		t.Fatal(err)
	}
	if len(c.ca.ExtKeyUsage) != 1 || c.ca.ExtKeyUsage[0] != x509.ExtKeyUsageServerAuth {
		t.Errorf("authority usages %v", c.ca.ExtKeyUsage)
	}
}

// Review: a hub_hosts entry cannot widen the authority beyond a specific name.
func TestWideHubHostsRefused(t *testing.T) {
	for _, bad := range []string{".", "", "org", "*.example.org", "co.uk", "example.org", "a..b.org"} {
		if ValidHostName(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
	for _, good := range []string{"hub.clinic.example.org", "hub.lan", "dhis2.home.arpa", "router.local"} {
		if !ValidHostName(good) {
			t.Errorf("%q refused", good)
		}
	}
	f := newFake()
	e := f.env()
	e.extra = []string{".", "org", "hub.clinic.example.org"}
	c, err := load(t.TempDir(), e)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range c.ca.PermittedDNSDomains {
		if d == "" || d == "org" {
			t.Errorf("authority permits %q", d)
		}
	}
}

// Review: a key that cannot be read (permissions, I/O) is not replaced: devices would
// all have to install a new authority after one transient error.
func TestUnreadableKeyNotReplaced(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads anything")
	}
	f, dir := newFake(), t.TempDir()
	c1, err := load(dir, f.env())
	if err != nil {
		t.Fatal(err)
	}
	os.Chmod(filepath.Join(dir, "ca.key"), 0)
	if _, err := load(dir, f.env()); err == nil {
		t.Fatal("started with an unreadable authority key")
	}
	os.Chmod(filepath.Join(dir, "ca.key"), 0o600)
	c2, err := load(dir, f.env())
	if err != nil || !c2.ca.Equal(c1.ca) {
		t.Fatalf("authority replaced: %v", err)
	}
}

// A corrupt key (power loss) is kept aside and replaced, so the hub still starts.
func TestCorruptKeyKeptAside(t *testing.T) {
	f, dir := newFake(), t.TempDir()
	if _, err := load(dir, f.env()); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "ca.key"), nil, 0o600)
	if _, err := load(dir, f.env()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "ca.key.bad")); err != nil {
		t.Error("the corrupt key was not kept aside")
	}
}
