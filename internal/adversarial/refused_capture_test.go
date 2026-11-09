package adversarial

import (
	"encoding/json"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/dhis2/facility-hub/internal/store"
	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// The author fixes their own refused change in Capture web through the hub
// (FINDINGS.md, "The author sees their own"). A person on central gets a phone number
// offline; the attribute becomes NUMBER on central (as in A17), which refuses it at
// push. On the hub page the nurse follows "Open in Capture"; the hub answers Capture's
// reads of the person from its copy, so the profile holds what she entered; she
// corrects it and saves, online, to central; the refusal closes and the link goes.
// HUB_CLIENTS=1 runs it (it needs a browser).
func TestRefusedFixedInCapture(t *testing.T) {
	if os.Getenv("HUB_CLIENTS") == "" {
		t.Skip("set HUB_CLIENTS=1 to drive the real Capture web app")
	}
	s := begin(t)
	if !s.Live() {
		t.Skip("Capture web on a real DHIS2")
	}
	p := s.M.Primary()
	attr := ""
	var res struct {
		PTEAs []struct {
			Mandatory bool
			Attr      struct {
				ID, ValueType     string
				Unique, Generated bool
				OptionSet         *struct{ ID string }
			} `json:"trackedEntityAttribute"`
		} `json:"programTrackedEntityAttributes"`
	}
	s.Central.JSON("GET", "/api/programs/"+p.ID+"?fields=programTrackedEntityAttributes[mandatory,"+
		"trackedEntityAttribute[id,valueType,unique,generated,optionSet[id]]]", nil, &res)
	for _, x := range res.PTEAs {
		if a := x.Attr; attr == "" && !x.Mandatory && !a.Unique && !a.Generated && a.OptionSet == nil &&
			(a.ValueType == "TEXT" || a.ValueType == "PHONE_NUMBER") && a.ID != p.SearchAttr {
			attr = a.ID
		}
	}
	if attr == "" {
		t.Skip("no optional text attribute to change")
	}
	nurse := s.connect(s.M.Users[tk.RoleNurse].Username)
	pr := s.register(nurse, p, "RefCapA")
	s.Offline()
	te := maps.Clone(pr.Payload["trackedEntities"].([]any)[0].(map[string]any))
	delete(te, "enrollments")
	te["attributes"] = []any{map[string]any{"attribute": attr, "value": "+256 700 111"}}
	if st, _ := s.save(nurse, "a phone number for a person on central", map[string]any{"trackedEntities": []any{te}}); st != 200 {
		t.Fatal("the hub refused a valid update offline")
	}
	s.SetField("/api/trackedEntityAttributes/"+attr, "valueType", "NUMBER")
	s.noRepeats(s.reconnect(2))
	if o := s.App.Hub.Index.Get(pr.TE); o == nil || o.SyncState != store.SyncRejected {
		t.Fatalf("central did not refuse the update: %+v", o)
	}

	out := t.TempDir()
	if r := os.Getenv("HUB_REPORT"); r != "" {
		out = filepath.Join(filepath.Dir(r), "refused-capture")
		os.MkdirAll(out, 0o755)
	}
	cmd := exec.Command("python3", "-I", "../../clients/refused_capture.py")
	cmd.Env = append(os.Environ(), "HUB_URL="+s.Srv.URL, "HUB_USER="+nurse.User, "HUB_PASSWORD="+s.M.Password,
		"ATTRIBUTE="+attr, "VALUE=700111", "OUT="+out)
	cmd.Stderr = os.Stderr
	b, err := cmd.Output()
	if err != nil {
		t.Fatalf("refused_capture.py: %v\n%s", err, b)
	}
	os.WriteFile(filepath.Join(out, "result.json"), b, 0o644)
	var r struct {
		Login, Links, LinksAfter, Save int
		Href, Shown, SaveQuery         string
		Requests                       []struct {
			Method, Mode, Path string
			Status             int
		}
	}
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	t.Logf("link %s; the profile form shows %q; the save: %d (%s); links after: %d; screenshots in %s",
		r.Href, r.Shown, r.Save, r.SaveQuery, r.LinksAfter, out)
	local := 0
	for _, q := range r.Requests {
		if q.Mode == "local" {
			local++
		}
	}
	t.Logf("%d tracker requests, %d answered by the hub", len(r.Requests), local)
	switch {
	case r.Login != 200:
		t.Fatalf("login through the hub: %d", r.Login)
	case r.Links != 1:
		t.Fatalf("the hub page shows %d links to Capture, want 1", r.Links)
	case local == 0:
		t.Error("the hub answered none of Capture's reads of the refused person")
	}
	// Capture shows the value as the attribute's new type reads it: "+256 700 111" as
	// 256700111. The value central has is not shown.
	if r.Shown != "256700111" {
		t.Errorf("the profile form shows %q, want what the nurse entered as a number", r.Shown)
	}
	if r.Save != 200 {
		t.Fatalf("Capture's save: %d", r.Save)
	}
	if r.LinksAfter != 0 {
		t.Error("after the save the hub page still links to Capture")
	}
	if o := s.App.Hub.Index.Get(pr.TE); o == nil || o.SyncState == store.SyncRejected {
		t.Error("the refusal is still open on the hub after central took the fix")
	}
	var onCentral struct {
		Attributes []struct{ Attribute, Value string }
	}
	s.Central.JSON("GET", "/api/tracker/trackedEntities/"+pr.TE+"?program="+p.ID+"&fields=attributes", nil, &onCentral)
	v := ""
	for _, a := range onCentral.Attributes {
		if a.Attribute == attr {
			v = a.Value
		}
	}
	if v != "700111" {
		t.Errorf("central has %s = %q, want 700111", attr, v)
	}
}
