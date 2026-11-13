package e2e

import (
	"context"
	"strings"
	"testing"
)

// The login page works offline after a restart, while the store is locked: staff
// unlock the store by logging in through it (in the field, 13 November 2026: "needs
// connection: /dhis-web-login/ was not cached while online", the page having been
// loaded only while the store was locked). Its copies are kept whether the store is
// locked or not, in the public file.
func TestLoginPageOfflineWhileLocked(t *testing.T) {
	for _, version := range []string{"2.42.6", "2.43.2"} {
		t.Run(version, func(t *testing.T) {
			e := newEnv(t)
			e.central.SetVersion(version)
			e.setup()
			e.web("nurse_a").login() // known to the hub: holds a key slot
			e.stop()
			e.start() // locked until a known user logs in
			if e.app.Hub.Store.Unlocked() {
				t.Fatal("store unlocked at boot")
			}
			// A device loads the login page online while the store is locked.
			anon := e.web("")
			for _, p := range []string{"/dhis-web-login/", "/login/", "/api/loginConfig"} {
				if st, _ := anon.do("GET", p, nil); st != 200 && st != 301 {
					t.Fatalf("online %s: %d", p, st)
				}
			}
			e.stop()
			e.start()
			e.offline()
			for _, p := range []string{"/dhis-web-login/", "/login/", "/api/loginConfig"} {
				st, b := anon.do("GET", p, nil)
				if p == "/dhis-web-login/" && strings.HasPrefix(version, "2.43") {
					if st != 301 {
						t.Errorf("offline, locked, %s: %d %.120s, want central's redirect to /login/", p, st, b)
					}
					continue
				}
				if st != 200 {
					t.Errorf("offline, locked, %s: %d %.120s", p, st, b)
				}
			}
			// The login through it unlocks the store, offline.
			e.web("nurse_a").login()
			if !e.app.Hub.Store.Unlocked() {
				t.Fatal("the offline login did not unlock the store")
			}
		})
	}
}

// The hub keeps the login page as soon as it reaches central, without a device loading
// it online first: the person setting up a hub may never open it online. It follows
// the page's references as a browser does (2.43: through central's redirect to
// /login/), and the settings and flag the page asks the API for.
func TestLoginPageKeptWithoutADevice(t *testing.T) {
	for _, version := range []string{"2.42.6", "2.43.2"} {
		t.Run(version, func(t *testing.T) {
			e := newEnv(t)
			e.central.SetVersion(version)
			e.setup()
			e.web("nurse_a").login() // known to the hub, so a login offline unlocks it
			n, _, err := e.app.Proxy.WarmLoginPage(context.Background())
			if err != nil || n < 5 {
				t.Fatalf("kept %d answers: %v", n, err)
			}
			e.stop()
			e.start()
			e.offline()
			root := "/dhis-web-login/"
			if strings.HasPrefix(version, "2.43") {
				root = "/login/"
			}
			anon := e.web("")
			for _, p := range []string{root, root + "assets/main.js", root + "assets/app.css", root + "assets/fonts/roboto.woff2", "/api/loginConfig"} {
				if st, b := anon.do("GET", p, nil); st != 200 {
					t.Errorf("offline, locked, never loaded by a device: %s: %d %.100s", p, st, b)
				}
			}
			e.web("nurse_a").login()
			if !e.app.Hub.Store.Unlocked() {
				t.Fatal("the offline login did not unlock the store")
			}
		})
	}
}
