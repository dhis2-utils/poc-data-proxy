package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

// selfSignedTLS loads or creates the hub's certificate (spec 12, POC only): a server
// certificate for hub.local, the host name, localhost and every LAN address, issued by
// a CA made for this hub alone. Devices install ca.crt (Android only trusts a CA as a
// user certificate; FINDINGS.md). The CA's private key is thrown away after signing,
// and its name constraints limit it to local names and private addresses, so neither
// the hub's files nor the CA itself can vouch for any other site (review L5).
func selfSignedTLS(dir string) (*tls.Config, error) {
	crt, key := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	if _, err := os.Stat(crt); err != nil {
		if err := writeSelfSigned(crt, key, filepath.Join(dir, "ca.crt")); err != nil {
			return nil, err
		}
	}
	pair, err := tls.LoadX509KeyPair(crt, key)
	if err != nil {
		return nil, err
	}
	return &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12}, nil
}

func writeSelfSigned(crtPath, keyPath, caPath string) error {
	host, _ := os.Hostname()
	names := []string{"hub.local", "localhost", host, host + ".local"}
	ips := []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && !ipn.IP.IsLoopback() {
				ips = append(ips, ipn.IP)
			}
		}
	}
	var private []*net.IPNet
	for _, c := range []string{"127.0.0.0/8", "::1/128", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16",
		"169.254.0.0/16", "fc00::/7", "fe80::/10"} {
		_, n, _ := net.ParseCIDR(c)
		private = append(private, n)
	}

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	ca := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{CommonName: "DHIS2 facility hub " + host, Organization: []string{"DHIS2 facility hub"}},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(5, 0, 0),
		KeyUsage:     x509.KeyUsageCertSign,
		IsCA:         true, BasicConstraintsValid: true, MaxPathLenZero: true,
		PermittedDNSDomainsCritical: true,
		PermittedDNSDomains:         names,
		PermittedIPRanges:           private,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if err != nil {
		return err
	}
	if ca, err = x509.ParseCertificate(caDER); err != nil {
		return err
	}

	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	leaf := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{CommonName: "hub.local", Organization: []string{"DHIS2 facility hub"}},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(5, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     names,
		IPAddresses:  ips,
	}
	der, err := x509.CreateCertificate(rand.Reader, leaf, ca, &k.PublicKey, caKey)
	if err != nil {
		return err
	}
	// caKey goes out of scope here and is never written.
	kb, err := x509.MarshalECPrivateKey(k)
	if err != nil {
		return err
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), 0o600); err != nil {
		return err
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	if err := os.WriteFile(caPath, caPEM, 0o644); err != nil {
		return err
	}
	chain := append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), caPEM...)
	return os.WriteFile(crtPath, chain, 0o644)
}

func serial() *big.Int {
	n, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	return n
}
