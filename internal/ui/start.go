package ui

import (
	"github.com/dhis2/facility-hub/internal/devkey"
	"net"
	"net/http"
	"os"
	"path/filepath"

	"github.com/dhis2/facility-hub/internal/hubtls"
)

// Start answers on plain HTTP (docs/DEPLOYMENT.md, "Reaching the hub securely"): the
// hub's authority for devices to install, and for every other path a start page that
// moves on to the same name and path over HTTPS once the device trusts the hub, or
// shows how to install the authority. Nothing else is served without TLS.
type Start struct {
	DataDir string
	// Captive reports whether the setup Wi-Fi is on (netsetup.Manager.Captive); may be nil.
	Captive func() bool
	// Reached notes the request's name and client (Hub.NoteReach); may be nil.
	Reached   func(host, remoteAddr string)
	HTTPSPort string                 // the HTTPS listener's port; 443 is left out of links
	Allowed   func(host string) bool // the hub's names and addresses (Hub.HostAllowed)
}

func (s *Start) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !s.Allowed(r.Host) {
		if s.Captive != nil && s.Captive() {
			// The setup Wi-Fi answers every name with the hub: lead the phone to it.
			http.Redirect(w, r, "http://10.42.0.1/", http.StatusFound)
			return
		}
		http.Error(w, "unknown host name: open the hub by its address (hub_hosts)", http.StatusMisdirectedRequest)
		return
	}
	if s.Reached != nil {
		s.Reached(r.Host, r.RemoteAddr)
	}
	if p := devkey.Problem(); p != "" {
		http.Error(w, p, http.StatusServiceUnavailable)
		return
	}
	if r.URL.Path == "/hub/ca.crt" {
		serveAuthority(w, r, s.DataDir)
		return
	}
	host := r.Host
	if h, _, err := net.SplitHostPort(r.Host); err == nil {
		host = h
	}
	target := "https://"
	if s.HTTPSPort == "443" || s.HTTPSPort == "" {
		if net.ParseIP(host) != nil && net.ParseIP(host).To4() == nil {
			target += "[" + host + "]"
		} else {
			target += host
		}
	} else {
		target += net.JoinHostPort(host, s.HTTPSPort)
	}
	origin := target
	target += r.URL.RequestURI()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := tmpl.ExecuteTemplate(w, "start.html", map[string]any{
		"Target": target, "Probe": origin + "/hub/status", "Fingerprint": hubtls.Fingerprint(s.DataDir),
		// On the setup Wi-Fi phones open this page in their sign-in window, which
		// cannot download the certificate.
		"SetupWifi": s.Captive != nil && s.Captive(),
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// serveAuthority sends the hub's authority (public; devices install it). Saved as a
// file, since Android installs it from storage only.
func serveAuthority(w http.ResponseWriter, r *http.Request, dataDir string) {
	b, err := os.ReadFile(filepath.Join(dataDir, "ca.crt"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/x-x509-ca-cert")
	w.Header().Set("Content-Disposition", `attachment; filename="dhis2-hub.crt"`)
	w.Write(b)
}
