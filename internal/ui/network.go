package ui

import (
	"net"
	"net/http"

	"github.com/dhis2/facility-hub/internal/auth"
	"github.com/dhis2/facility-hub/internal/core"
	"github.com/dhis2/facility-hub/internal/netsetup"
)

// netView is the network section of the setup and admin pages (docs/DEPLOYMENT.md,
// "Setup Wi-Fi").
type netView struct {
	netsetup.Status
	ModeText string
	OnSetup  bool // the setup Wi-Fi is on: joining and case C are offered
	Admin    bool // the admin page: the facility Wi-Fi's password and "New password"
	NeedCode bool // before set-up: the forms ask for the setup code, as set-up does
}

// netView is the network section; the setup page, open before set-up to anyone on the
// network, leaves out the facility Wi-Fi's password.
func (u *UI) netView(admin bool) *netView {
	if u.Net == nil {
		return nil
	}
	st := u.Net.Status()
	if !admin {
		st.FacilityPassword = ""
	}
	v := &netView{Status: st, OnSetup: st.Mode == "setup", Admin: admin, NeedCode: !admin && needToken(u.H)}
	switch st.Mode {
	case "cable":
		v.ModeText = "Cable"
	case "modem":
		v.ModeText = "USB modem"
	case "wifi":
		v.ModeText = "Wi-Fi " + st.Name
	case "facility":
		v.ModeText = "This hub's Wi-Fi " + st.Name
	case "setup":
		v.ModeText = "Setup Wi-Fi " + st.SetupSSID
	default:
		v.ModeText = "No network"
	}
	return v
}

// handleNetwork joins a Wi-Fi or makes the hub the facility's Wi-Fi. Before set-up
// anyone on the setup Wi-Fi may, with the setup code where set-up needs one (the Wi-Fi's
// 8-digit password alone is found offline from one captured handshake, review R-29),
// but not anyone on the facility's cable LAN; afterwards staff of the facility only.
func (u *UI) handleNetwork(facility bool) http.HandlerFunc {
	do := func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || u.Net == nil {
			http.Redirect(w, r, "/hub/", http.StatusFound)
			return
		}
		var ssid, pw string // pw: the facility Wi-Fi's, which the phone joins next
		var err error
		if facility {
			ssid, pw, err = u.Net.MakeFacilityWifi()
		} else {
			ssid = r.FormValue("ssid")
			err = u.Net.Join(ssid, r.FormValue("password"))
		}
		u.render(w, "netdone", map[string]any{"HubID": u.H.HubID(), "SSID": ssid, "Password": pw, "Facility": facility, "Error": err})
	}
	admin := u.admin(do)
	return func(w http.ResponseWriter, r *http.Request) {
		if u.H.Mode() != core.ModeSetup {
			admin(w, r)
			return
		}
		if r.Method == http.MethodPost && !sameOrigin(r) {
			http.Error(w, "cross-origin request refused", http.StatusForbidden)
			return
		}
		if u.Net == nil || !u.Net.Captive() || !fromSetupWifi(r.RemoteAddr) {
			http.Error(w, "connect to the hub's setup Wi-Fi to change its network", http.StatusForbidden)
			return
		}
		if r.Method == http.MethodPost {
			if err := u.checkCode(auth.Source(r), r.FormValue("setup_token")); err != nil {
				http.Error(w, err.Error(), http.StatusForbidden)
				return
			}
		}
		do(w, r)
	}
}

// handleWifiPassword makes a new password for the facility's Wi-Fi (admin page).
func (u *UI) handleWifiPassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || u.Net == nil {
		http.Redirect(w, r, "/hub/ui", http.StatusFound)
		return
	}
	ssid, pw, err := u.Net.NewFacilityPassword()
	u.render(w, "netdone", map[string]any{"HubID": u.H.HubID(), "SSID": ssid, "Password": pw, "Facility": true, "Error": err})
}

// setupNet is the setup Wi-Fi's network (NetworkManager's shared mode, 10.42.0.1/24).
var setupNet = &net.IPNet{IP: net.IPv4(10, 42, 0, 0), Mask: net.CIDRMask(24, 32)}

// fromSetupWifi: a device on the setup Wi-Fi, or the hub itself.
func fromSetupWifi(remote string) bool {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || setupNet.Contains(ip))
}
