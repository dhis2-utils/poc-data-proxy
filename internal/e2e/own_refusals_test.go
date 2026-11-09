package e2e

import (
	"encoding/json"
	"fmt"
	"html"
	"slices"
	"strings"
	"testing"

	fc "github.com/dhis2/facility-hub/internal/fakecentral"
)

// The hub's page shows signed-in staff their own refused changes with what they
// entered, and who closes each. Others' refusals stay in the issue table, masked, and
// a visitor who is not signed in sees none (plan of 4 November, item 4).
func TestOwnRefusalsOnHubPage(t *testing.T) {
	e := newEnv(t)
	e.setup()
	a, b := e.web("nurse_a"), e.web("nurse_b")
	a.login()
	b.login()
	e.offline()
	e.central.Seed("CentralTe81", "CentralEn81", "Other", "NAT-8181", nil)
	e.central.Seed("CentralTe82", "CentralEn82", "Other", "NAT-8282", nil)
	a.json("POST", postSync, newPatient("OwnTe000081", "OwnEn000081", "Amina", "NAT-8181", nil), 200)
	b.json("POST", postSync, newPatient("OwnTe000082", "OwnEn000082", "Bakari", "NAT-8282", nil), 200)
	e.online()
	e.sync()

	page := func(c *client) string {
		t.Helper()
		st, p := c.do("GET", "/hub/ui", nil)
		if st != 200 {
			t.Fatalf("%s: page %d", c.user, st)
		}
		return string(p)
	}
	pa := page(a)
	sa := ownSection(pa)
	for _, want := range []string{"Your sync errors", "OwnTe000081", "NAT-8181", "Amina",
		"Non-unique attribute value `NAT-8181`", "E1064", "Fixed by the author", " | ",
		"OwnEn000081", "with <span class=\"mono\">OwnTe000081</span>"} {
		if !strings.Contains(sa, want) {
			t.Errorf("nurse_a's sync errors lack %q:\n%s", want, sa)
		}
	}
	for _, other := range []string{"NAT-8282", "Bakari", "OwnTe000082"} {
		if strings.Contains(sa, other) {
			t.Errorf("nurse_a's sync errors show nurse_b's %q", other)
		}
	}
	// The issue table is as before: every issue, with its author, and values masked.
	if !strings.Contains(pa, "OwnTe000082") || !strings.Contains(pa, "nurse_b") {
		t.Error("the issue table no longer lists nurse_b's refusal")
	}
	if rest := strings.Replace(pa, sa, "", 1); strings.Contains(rest, "NAT-8181") || strings.Contains(rest, "NAT-8282") ||
		strings.Contains(rest, "Bakari") {
		t.Error("a value shows outside the user's own sync errors")
	}
	if sb := ownSection(page(b)); !strings.Contains(sb, "NAT-8282") || strings.Contains(sb, "NAT-8181") {
		t.Errorf("nurse_b's sync errors:\n%s", sb)
	}
	st, anon := e.web("nurse_a").do("GET", "/hub/ui", nil)
	if st == 200 || strings.Contains(string(anon), "NAT-8181") || strings.Contains(string(anon), "sync errors") {
		t.Errorf("a visitor not signed in: %d %.200s", st, anon)
	}

	// Fixed by the author: the error leaves their list.
	e.offline()
	a.json("POST", postSync, newPatient("OwnTe000081", "OwnEn000081", "Amina", "NAT-8383", nil), 200)
	e.online()
	e.sync()
	if s := ownSection(page(a)); s != "" {
		t.Errorf("fixed, still listed:\n%s", s)
	}
}

// Staff fix their own refused change in Capture through the hub (FINDINGS.md, "The
// author sees their own"): the hub page links to Capture for the refusals the author
// fixes; the hub answers the author's reads of such a record from its copy, online
// too, so Capture shows what they entered, a refused new registration included; others
// read central's. A save central accepts closes the refusal at once.
func TestOpenRefusalInCapture(t *testing.T) {
	e := newEnv(t)
	e.central.Seed("CapTe000092", "CapEn000092", "Existing", "NAT-9292", nil)
	e.setup()
	a, b := e.web("nurse_a"), e.web("nurse_b")
	a.login()
	b.login()
	e.sync()
	e.central.Seed("CentralTe91", "CentralEn91", "Other", "NAT-9191", nil)
	e.central.Seed("CentralTe94", "CentralEn94", "Other", "NAT-9494", nil)
	e.central.Seed("CentralTe95", "CentralEn95", "Other", "NAT-9595", nil)
	person := func(te, first, nat string) map[string]any {
		return map[string]any{"trackedEntities": []any{map[string]any{"trackedEntity": te, "trackedEntityType": fc.TET,
			"orgUnit": fc.Facility, "attributes": []any{map[string]any{"attribute": fc.AttrFirstName, "value": first},
				map[string]any{"attribute": fc.AttrNationalI, "value": nat}}}}}
	}
	e.offline()
	// A new registration and an update of a person central has, both refused for a
	// national ID central holds already (E1064, fixed by the author); an event refused
	// because central took away the stage's write access (E1095, fixed on central);
	// nurse_b's own refused registration.
	a.json("POST", postSync, newPatient("CapTe000091", "CapEn000091", "Amina", "NAT-9191",
		map[string]string{"CapEv000091": fc.Stage1}), 200)
	a.json("POST", postSync, person("CapTe000092", "Existing", "NAT-9595"), 200)
	a.json("POST", postSync, event("CapEv000093", "SeedEn00001", fc.Stage2), 200)
	b.json("POST", postSync, newPatient("CapTe000094", "CapEn000094", "Bakari", "NAT-9494", nil), 200)
	e.central.Users["nurse_a"].StageWrite[fc.Stage2] = false
	e.online()
	e.sync()
	e.central.Users["nurse_a"].StageWrite[fc.Stage2] = true

	page := func(c *client) string {
		t.Helper()
		st, p := c.do("GET", "/hub/ui", nil)
		if st != 200 {
			t.Fatalf("%s: page %d", c.user, st)
		}
		return string(p)
	}
	links := func(p string) []string {
		var out []string
		for _, s := range strings.Split(p, `<a href="/dhis-web-capture/index.html#/`)[1:] {
			out = append(out, html.UnescapeString(s[:strings.Index(s, `"`)]))
		}
		return out
	}
	pa := page(a)
	if got, want := links(pa), []string{"enrollment?teiId=CapTe000092&enrollmentId=CapEn000092",
		"enrollment?teiId=CapTe000091&enrollmentId=CapEn000091"}; !slices.Equal(got, want) {
		t.Errorf("nurse_a's links to Capture: %q, want %q (not for what waits, nor for E1095)", got, want)
	}
	if !strings.Contains(pa, "E1095") || !strings.Contains(pa, "Fixed on central") {
		t.Error("nurse_a's page lacks the event refused for its stage, fixed on central")
	}
	if got := links(page(b)); !slices.Equal(got, []string{"enrollment?teiId=CapTe000094&enrollmentId=CapEn000094"}) {
		t.Errorf("nurse_b's links to Capture: %q", got)
	}

	// Reads online: the author gets the hub's copy, the others central's.
	nat := func(c *client, te string) string {
		t.Helper()
		st, body := c.do("GET", "/api/tracker/trackedEntities/"+te+"?program="+fc.Program+"&fields=attributes", nil)
		if st != 200 {
			return fmt.Sprint(st)
		}
		var p struct {
			Attributes []struct{ Attribute, Value string }
		}
		json.Unmarshal(body, &p)
		for _, x := range p.Attributes {
			if x.Attribute == fc.AttrNationalI {
				return x.Value
			}
		}
		return ""
	}
	if got := nat(a, "CapTe000092"); got != "NAT-9595" {
		t.Errorf("the author reads CapTe000092 online: %s, want the refused NAT-9595", got)
	}
	if got := nat(b, "CapTe000092"); got != "NAT-9292" {
		t.Errorf("nurse_b reads CapTe000092 online: %s, want central's NAT-9292", got)
	}
	if got := nat(a, "CapTe000091"); got != "NAT-9191" {
		t.Errorf("the author reads the refused registration online: %s", got)
	}
	a.json("GET", "/api/tracker/enrollments/CapEn000091", nil, 200)
	a.json("GET", "/api/tracker/events/CapEv000091", nil, 200)
	if got := nat(b, "CapTe000091"); got != "404" {
		t.Errorf("nurse_b reads nurse_a's refused registration online: %s, want central's 404", got)
	}

	// Corrected and saved online, as Capture's profile form does: central takes it and
	// the refusal closes at once; reads go to central again.
	a.json("POST", "/api/tracker?async=false", person("CapTe000092", "Existing", "NAT-9299"), 200)
	if own := ownSection(page(a)); strings.Contains(own, "CapTe000092") {
		t.Errorf("fixed and saved online, still in the user's sync errors:\n%s", own)
	}

	if got := nat(a, "CapTe000092"); got != "NAT-9299" {
		t.Errorf("after the save the author reads %s", got)
	}
	// A new event central never had, saved as Capture saves an edit (UPDATE): the hub
	// sends the author's own refused objects as CREATE_AND_UPDATE, so central creates
	// it. Someone else's save of it goes as sent.
	ev := event("CapEv000093", "SeedEn00001", fc.Stage2)
	const update = "/api/tracker?async=false&importStrategy=UPDATE"
	if rep := b.json("POST", update, ev, 409); !slices.Equal(errorCodes(rep), []string{"E1032"}) {
		t.Errorf("nurse_b's update of an event central does not have: %v", errorCodes(rep))
	}
	a.json("POST", update, ev, 200)
	if e.central.Object("CapEv000093") == nil || strings.Contains(ownSection(page(a)), "CapEv000093") {
		t.Error("the author's refused new event, saved as an update: not on central, or still listed")
	}
	// The refused registration: its person reaches central, and the enrollment and
	// event that waited for it are sent again at the next push.
	a.json("POST", "/api/tracker?async=false", person("CapTe000091", "Amina", "NAT-9199"), 200)
	if e.central.Object("CapTe000091") == nil {
		t.Fatal("the corrected registration did not reach central")
	}
	e.sync()
	if e.central.Object("CapEn000091") == nil || e.central.Object("CapEv000091") == nil {
		t.Error("the enrollment or event that waited for the corrected person did not reach central")
	}
	if own := ownSection(page(a)); own != "" {
		t.Errorf("after the saves the author's sync errors still list:\n%s", own)
	}
}

// ownSection is the hub page's "Your sync errors" ("" if it has none).
func ownSection(p string) string {
	i := strings.Index(p, `id="own-refusals"`)
	if i < 0 {
		return ""
	}
	return p[i : i+strings.Index(p[i:], "Issues (rejected by central)")]
}
