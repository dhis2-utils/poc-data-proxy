package testkit

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Front stands where central's address leads, before the Netcut: a TCP forwarder on a
// fixed address that can stop listening (the address resolves, nothing answers) and
// can present a certificate of the test's choosing (central behind TLS, with a good,
// expired or wrong certificate). With a host name that resolves to it (HOSTALIASES),
// the hub reaches central through DNS, which a test can break while TCP works.
type Front struct {
	t       testing.TB
	Addr    string // host:port it listens on
	target  string // the Netcut's host:port
	cert    atomic.Pointer[tls.Certificate]
	tls     bool
	mu      sync.Mutex
	ln      net.Listener
	conns   map[net.Conn]bool
	Accepts atomic.Int64 // connections accepted
	// Handshakes counts TLS handshakes that completed: with a certificate the hub must
	// refuse, it must stay 0 (nothing sent to a central it cannot trust).
	Handshakes atomic.Int64
}

// NewFront listens on addr ("0.0.0.0:0" for any port) and forwards to the Netcut;
// with cert, it terminates TLS with it (SetCert changes it).
func NewFront(t testing.TB, addr string, n *Netcut, cert *tls.Certificate) *Front {
	t.Helper()
	f := &Front{t: t, Addr: addr, target: n.ln.Addr().String(), tls: cert != nil, conns: map[net.Conn]bool{}}
	if cert != nil {
		f.cert.Store(cert)
	}
	if err := f.Listen(); err != nil {
		t.Fatal(err)
	}
	f.Addr = f.ln.Addr().String()
	t.Cleanup(f.Close)
	return f
}

// Port is the port the front listens on.
func (f *Front) Port() string {
	_, p, _ := net.SplitHostPort(f.Addr)
	return p
}

// SetCert changes the certificate presented to new connections.
func (f *Front) SetCert(c *tls.Certificate) { f.cert.Store(c) }

// Listen starts listening again after Close (on the same address).
func (f *Front) Listen() error {
	ln, err := net.Listen("tcp", f.Addr)
	if err != nil {
		return err
	}
	f.mu.Lock()
	f.ln = ln
	f.mu.Unlock()
	go f.serve(ln)
	return nil
}

// Close stops listening and drops every open connection: connections to the address
// are refused until Listen.
func (f *Front) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ln != nil {
		f.ln.Close()
		f.ln = nil
	}
	for c := range f.conns {
		c.Close()
	}
}

func (f *Front) track(c net.Conn, on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if on {
		f.conns[c] = true
	} else {
		delete(f.conns, c)
	}
}

func (f *Front) serve(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		f.Accepts.Add(1)
		go f.pipe(c)
	}
}

func (f *Front) pipe(c net.Conn) {
	f.track(c, true)
	defer f.track(c, false)
	defer c.Close()
	if f.tls {
		cert := f.cert.Load()
		tc := tls.Server(c, &tls.Config{Certificates: []tls.Certificate{*cert}})
		tc.SetDeadline(time.Now().Add(10 * time.Second))
		if err := tc.Handshake(); err != nil {
			return
		}
		tc.SetDeadline(time.Time{})
		f.Handshakes.Add(1)
		c = tc
		f.track(c, true)
		defer f.track(c, false)
	}
	up, err := net.Dial("tcp", f.target)
	if err != nil {
		return
	}
	f.track(up, true)
	defer f.track(up, false)
	defer up.Close()
	done := make(chan struct{}, 2)
	go func() { io.Copy(up, c); done <- struct{}{} }()
	go func() { io.Copy(c, up); done <- struct{}{} }()
	<-done
}

// TestCA is a certificate authority for a test: its certificate goes into a file the
// hub trusts (SSL_CERT_FILE), and it issues central's certificates.
type TestCA struct {
	t    testing.TB
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	File string // PEM of the CA certificate
}

func NewTestCA(t testing.TB) *TestCA {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Adversarial test CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), IsCA: true,
		KeyUsage: x509.KeyUsageCertSign, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	file := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(file, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		t.Fatal(err)
	}
	return &TestCA{t: t, cert: cert, key: key, File: file}
}

// Issue makes a server certificate for host (a name or an IP) valid in [from, to),
// signed by the CA, or self-signed with selfSigned.
func (ca *TestCA) Issue(host string, from, to time.Time, selfSigned bool) *tls.Certificate {
	ca.t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	serial, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	tmpl := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: host}, NotBefore: from, NotAfter: to,
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	if ip := net.ParseIP(host); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
	} else {
		tmpl.DNSNames = []string{host}
	}
	parent, signer := ca.cert, ca.key
	if selfSigned {
		parent, signer = tmpl, key
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, &key.PublicKey, signer)
	if err != nil {
		ca.t.Fatal(err)
	}
	return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}
