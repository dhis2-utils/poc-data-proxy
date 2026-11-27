package central

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// The hub checks central's certificate by its own clock (docs/design/2026-11-04-no-rtc.md). A hub whose
// clock is behind the start of central's certificate (Let's Encrypt renews every 60
// days) cannot reach central: every probe fails, so it stays offline until its clock is
// set right. Here the test's clock is the hub's, and central's certificate starts
// two days after it, as for a hub two days behind a certificate renewed today.
func TestNoRTCProbeFailsOnCertificateNotYetValid(t *testing.T) {
	for _, c := range []struct {
		what  string
		start time.Duration // the certificate's start, from the hub's now
		want  bool
	}{
		{"certificate from a month ago", -30 * 24 * time.Hour, true},
		{"certificate starting two days after the hub's clock", 48 * time.Hour, false},
	} {
		key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		now := time.Now()
		tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "central"},
			NotBefore: now.Add(c.start), NotAfter: now.Add(c.start + 90*24*time.Hour),
			IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, IsCA: true, BasicConstraintsValid: true,
			KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
		if err != nil {
			t.Fatal(err)
		}
		cert, _ := x509.ParseCertificate(der)
		srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("pong"))
		}))
		srv.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
		srv.StartTLS()
		pool := x509.NewCertPool()
		pool.AddCert(cert)
		m := NewMonitor(time.Second, time.Second)
		m.Client = &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}
		if got := m.Probe(context.Background(), srv.URL); got != c.want {
			t.Errorf("%s: probe %v, want %v", c.what, got, c.want)
		}
		srv.Close()
	}
}
