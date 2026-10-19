package ui

import (
	"net/http"

	"github.com/dhis2/facility-hub/internal/core"
	"github.com/dhis2/facility-hub/internal/netsetup"
)

// netView is the network section of the setup and admin pages (docs/DEPLOYMENT.md,
// "Setup Wi-Fi").
type netView struct {
	netsetup.Status
	ModeText string
	OnSetup  bool // the setup Wi-Fi is on: joining and case C are offered
}

func (u *UI) netView() *netView {
	if u.Net == nil {
		return nil
	}
	st := u.Net.Status()
	v := &netView{Status: st, OnSetup: st.Mode == "setup"}
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
// anyone on the setup Wi-Fi may (its password is the gate, as for the setup page);
// afterwards staff of the facility only.
func (u *UI) handleNetwork(facility bool) http.HandlerFunc {
	do := func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || u.Net == nil {
			http.Redirect(w, r, "/hub/", http.StatusFound)
			return
		}
		ssid, pw := r.FormValue("ssid"), r.FormValue("password")
		var err error
		if facility {
			err = u.Net.MakeFacilityWifi(ssid, pw)
		} else {
			err = u.Net.Join(ssid, pw)
		}
		u.render(w, "netdone", map[string]any{"HubID": u.H.HubID(), "SSID": ssid, "Facility": facility, "Error": err})
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
		do(w, r)
	}
}
