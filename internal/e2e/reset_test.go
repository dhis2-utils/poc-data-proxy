package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"testing"

	fc "github.com/dhis2/facility-hub/internal/fakecentral"
)

// G6/N23: a reset edits the hub's registry entry instead of replacing it, so the
// fields the Facility Hub app shows (its tokens for "Revoke all tokens", among
// others) survive the reset.
func TestResetKeepsRegistryFields(t *testing.T) {
	e := newEnv(t)
	e.setup()
	a := e.web("nurse_a")
	a.login()
	e.sync()
	id := e.app.Hub.HubID()
	reg := e.central.DataStoreValue("facility-hub-registry", id)
	if reg == nil {
		t.Fatal("no registry entry on central")
	}
	// Fields written on central after the hub's own write, as the Facility Hub app does.
	reg["tokens"] = []any{"tok00000001", "tok00000002"}
	reg["appField"] = "keep-me"
	body, _ := json.Marshal(reg)
	req, _ := http.NewRequest("PUT", e.cSrv.URL+"/api/dataStore/facility-hub-registry/"+id, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth("hub_admin", fc.Password)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		t.Fatalf("writing the registry entry: %d", resp.StatusCode)
	}

	admin := e.web("hub_admin")
	admin.login()
	resp, err = admin.postForm("/hub/ui/reset", url.Values{"confirm": {"RESET"}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if e.app.Hub.Mode() != "setup" {
		t.Fatalf("mode %s after reset", e.app.Hub.Mode())
	}
	after := e.central.DataStoreValue("facility-hub-registry", id)
	t.Logf("registry entry after the reset: %v", after)
	if after["status"] != "retired" || after["retiredByUser"] != "hub_admin" {
		t.Errorf("the reset did not mark the entry retired: %v", after)
	}
	if after["appField"] != "keep-me" {
		t.Error("G6/N23: the reset dropped a registry field the app reads: appField")
	}
	if fmt.Sprint(after["tokens"]) != "[tok00000001 tok00000002]" {
		t.Errorf("G6/N23: the reset dropped the entry's tokens: %v", after["tokens"])
	}
}
