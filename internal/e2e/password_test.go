package e2e

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	fc "github.com/dhis2/facility-hub/internal/fakecentral"
)

// B3: central changes a user's password. Online through the hub, the old password must
// stop working at once: a web login central refuses drops the user's cached Basic
// credentials, so the device's next request is checked with central instead of
// passing on the cache for up to BasicRecheck (5 minutes).
func TestB3OldPasswordDroppedWhenCentralRefusesWebLogin(t *testing.T) {
	e := newEnv(t)
	const user, oldPw, newPw = "b3_user", "Old-Pw-2026!", "New-Pw-2026!"
	// A user with its own password on central, assigned to the facility.
	body, _ := json.Marshal(map[string]any{"id": "B3Usr000001", "username": user, "password": oldPw,
		"organisationUnits": []any{map[string]any{"id": fc.Facility}}})
	req, _ := http.NewRequest("POST", e.cSrv.URL+"/api/users", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth("hub_admin", fc.Password)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 201 {
		t.Fatalf("creating the user on central: %d", resp.StatusCode)
	}

	e.setup()
	dev := e.androidPw(user, oldPw)
	if st, b := dev.do("GET", "/api/me?fields=id", nil); st != 200 {
		t.Fatalf("the device's first login: %d %s", st, b)
	}
	// Central changes the password (as the user does on its own page).
	ch, _ := json.Marshal(map[string]string{"oldPassword": oldPw, "newPassword": newPw})
	req, _ = http.NewRequest("PUT", e.cSrv.URL+"/api/me/changePassword", bytes.NewReader(ch))
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(user, oldPw)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 202 {
		t.Fatalf("changing the password on central: %d", resp.StatusCode)
	}

	// A web login through the hub with the old password: central refuses it (401).
	web := e.web(user)
	st, b := web.do("POST", "/api/auth/login", map[string]string{"username": user, "password": oldPw})
	if st != 401 {
		t.Fatalf("web login with the old password: %d %s, want 401", st, b)
	}
	// The device's old password now: checked with central, refused (before the fix it
	// passed on the cached credential for up to 5 minutes).
	st, b = dev.do("GET", "/api/me?fields=id", nil)
	if st != 401 {
		t.Errorf("B3: the device's old password still works online through the hub: %d %.200s", st, b)
	}
	// The new password works, from the device and the web.
	devNew := e.androidPw(user, newPw)
	if st, b := devNew.do("GET", "/api/me?fields=id", nil); st != 200 {
		t.Errorf("the device's new password: %d %.200s", st, b)
	}
	st, b = web.do("POST", "/api/auth/login", map[string]string{"username": user, "password": newPw})
	if st != 200 || !strings.Contains(string(b), "SUCCESS") {
		t.Errorf("web login with the new password: %d %.200s", st, b)
	}
}
