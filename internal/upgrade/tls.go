package upgrade

import (
	"crypto/tls"
	"net/http"
)

// insecureTransport is only used for the health check against the hub's own
// self-signed certificate on loopback.
func insecureTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	return t
}
