// Command hubdrive saves through a facility hub against a real DHIS2 while the hub's
// power or uplink is cut, and then checks that every save the hub confirmed reached
// central once and whole: the hardware counterpart of the sandbox's adversarial
// writers and ledger (internal/adversarial, docs/HARDWARE-TESTING.md).
//
//	hubdrive run   -hub https://192.168.8.20 -ca hub-ca.crt -password …
//	hubdrive check -hub https://192.168.8.20 -ca hub-ca.crt -admin-password … hubdrive-r1028-1532.jsonl
//
// The defaults are Ngelehun CHC's Child Programme on the online demo central.
package main

import (
	"crypto/tls"
	"crypto/x509"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

func usage() {
	fmt.Fprintf(os.Stderr, `hubdrive run   [flags]          save through the hub until Ctrl-C, logging every save to a ledger
hubdrive check [flags] LEDGER… check every save of the ledgers on central

Run "hubdrive run -h" or "hubdrive check -h" for the flags.
`)
	os.Exit(2)
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "run":
		err = runMain(os.Args[2:])
	case "check":
		err = checkMain(os.Args[2:])
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "hubdrive:", err)
		os.Exit(1)
	}
}

// hubFlags are the flags to reach the hub over its own certificate authority.
type hubFlags struct {
	hub, ca  *string
	insecure *bool
	timeout  *time.Duration
}

func addHubFlags(fs *flag.FlagSet, timeout time.Duration) hubFlags {
	return hubFlags{
		hub:      fs.String("hub", "", "the hub, e.g. 192.168.8.20 or https://192.168.8.20 (the address on the hub's start page)"),
		ca:       fs.String("ca", "", "the hub's certificate authority (ca.crt, from the hub's start page or /hub/ca.crt)"),
		insecure: fs.Bool("insecure", false, "do not check the hub's certificate"),
		timeout:  fs.Duration("timeout", timeout, "how long a client waits for one answer of the hub"),
	}
}

// url is the hub's address without a trailing slash; an address without a scheme,
// such as 192.168.1.100, is taken as https.
func (h hubFlags) url() string {
	u := strings.TrimRight(*h.hub, "/")
	if u != "" && !strings.Contains(u, "://") {
		u = "https://" + u
	}
	return u
}

// transport is the hub's TLS set-up: its own authority, or no check at all.
func (h hubFlags) transport() (*http.Transport, error) {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.TLSClientConfig = &tls.Config{InsecureSkipVerify: *h.insecure}
	if *h.ca != "" {
		pem, err := os.ReadFile(*h.ca)
		if err != nil {
			return nil, err
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("%s: no PEM certificate in it", *h.ca)
		}
		t.TLSClientConfig.RootCAs = pool
	}
	return t, nil
}

// values parses "a=1,b=2".
func values(s string) (map[string]string, error) {
	out := map[string]string{}
	for _, kv := range strings.Split(s, ",") {
		if kv = strings.TrimSpace(kv); kv == "" {
			continue
		}
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return nil, fmt.Errorf("%q: want uid=value", kv)
		}
		out[k] = v
	}
	return out, nil
}

// span parses "2500-4500".
func span(s string) (lo, hi int, err error) {
	a, b, _ := strings.Cut(s, "-")
	if lo, err = strconv.Atoi(a); err == nil {
		hi, err = strconv.Atoi(b)
	}
	if err != nil || hi < lo {
		return 0, 0, fmt.Errorf("%q: want low-high", s)
	}
	return lo, hi, nil
}

func env(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}
