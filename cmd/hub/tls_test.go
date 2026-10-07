package main

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
)

// Review L5: the hub's CA vouches for the hub's own names only, and its key is gone.
func TestHubCertificate(t *testing.T) {
	dir := t.TempDir()
	cfg, err := selfSignedTLS(dir)
	if err != nil {
		t.Fatal(err)
	}
	caPEM, _ := os.ReadFile(filepath.Join(dir, "ca.crt"))
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		t.Fatal("ca.crt")
	}
	leaf, _ := x509.ParseCertificate(cfg.Certificates[0].Certificate[0])
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: "hub.local"}); err != nil {
		t.Fatalf("hub.local: %v", err)
	}
	b, _ := pem.Decode(caPEM)
	ca, _ := x509.ParseCertificate(b.Bytes)
	if len(ca.PermittedDNSDomains) == 0 || len(ca.PermittedIPRanges) == 0 || !ca.MaxPathLenZero {
		t.Fatal("CA without name constraints")
	}
	// The only private key on disk is the server certificate's, which cannot sign.
	pair, err := tls.LoadX509KeyPair(filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key"))
	if err != nil {
		t.Fatal(err)
	}
	if l, _ := x509.ParseCertificate(pair.Certificate[0]); l.IsCA || l.KeyUsage&x509.KeyUsageCertSign != 0 {
		t.Fatal("tls.key belongs to a certificate that can sign")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*"))
	if len(files) != 3 {
		t.Fatalf("files: %v", files)
	}
}
