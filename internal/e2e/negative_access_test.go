package e2e

import (
	"fmt"
	"strings"
	"testing"

	fc "github.com/dhis2/facility-hub/internal/fakecentral"
)

// Every local read path, as every restricted role, offline: the hub reveals nothing
// the role may not see (review section 10). Data pulled from central and data saved
// on the hub are both checked, since the hub decides their visibility differently
// (Index.CanRead). nurse_a, who may see everything, is the control: each path shows
// her what it is meant to hide from the others.
func TestLocalReadsHideWhatRolesMayNotSee(t *testing.T) {
	e := newEnv(t)
	// The facility's staff, with no data access to the tracked entity type.
	e.central.Users["notype_f"] = &fc.User{UID: "HubUsrNoTyF", Username: "notype_f", OrgUnits: []string{fc.Facility},
		ProgramRead: true, StageRead: map[string]bool{fc.Stage1: true, fc.Stage2: true, fc.Stage3: true}, NoTETRead: true}
	e.setup()
	roles := []string{"nurse_a", "nurse_b", "clerk_c", "outsider_d", "notype_f"}
	clients := map[string]*client{}
	for _, r := range roles {
		clients[r] = e.web(r)
		clients[r].login()
	}
	e.sync()

	// Saved on the hub offline by nurse_a: a person with events in stages 1 and 3, and
	// a relationship to the person pulled from central (SeedTe00001, with SeedEv00003
	// in stage 3).
	e.offline()
	a := clients["nurse_a"]
	a.json("POST", postSync, newPatient("NegTe000001", "NegEn000001", "Hidden", "NAT-9101",
		map[string]string{"NegEv000001": fc.Stage1, "NegEv000003": fc.Stage3}), 200)
	ref := func(uid string) map[string]any {
		return map[string]any{"trackedEntity": map[string]any{"trackedEntity": uid}}
	}
	a.json("POST", postSync, map[string]any{"relationships": []any{map[string]any{"relationship": "NegRel00001",
		"relationshipType": fc.RelType, "from": ref("NegTe000001"), "to": ref("SeedTe00001")}}}, 200)

	stage3 := []string{"SeedEv00003", "NegEv000003"}
	everything := []string{"SeedTe00001", "SeedEn00001", "SeedEv00001", "SeedEv00003",
		"NegTe000001", "NegEn000001", "NegEv000001", "NegEv000003", "NegRel00001"}
	hidden := map[string][]string{
		"nurse_b":    stage3, // no access to stage 3
		"clerk_c":    stage3, // reads the program, writes nothing, no stage 3
		"outsider_d": everything,
		// No tracked entity type: nothing of the program, whether pulled from central
		// or saved on the hub, as central hides it (2.42 and 2.43, checked live).
		"notype_f": everything,
	}

	const tt = "/api/tracker/"
	const nested = "&fields=*,enrollments[*,events[*]],relationships[*]"
	ou, prog := "orgUnit="+fc.Facility, "&program="+fc.Program
	paths := []struct {
		path  string
		shows []string // to nurse_a
	}{
		{tt + "trackedEntities?orgUnits=" + fc.Facility + prog + nested,
			[]string{"SeedTe00001", "SeedEn00001", "SeedEv00003", "NegTe000001", "NegEn000001", "NegEv000003", "NegRel00001"}},
		{tt + "trackedEntities/SeedTe00001?" + prog[1:] + nested, []string{"SeedTe00001", "SeedEv00003", "NegRel00001"}},
		{tt + "trackedEntities/NegTe000001?" + prog[1:] + nested, []string{"NegTe000001", "NegEv000003", "NegRel00001"}},
		{tt + "trackedEntities/NegTe000001", []string{"NegTe000001"}},
		{tt + "enrollments?orgUnits=" + fc.Facility + prog + "&fields=*,events[*]",
			[]string{"SeedEn00001", "SeedEv00003", "NegEn000001", "NegEv000003"}},
		{tt + "enrollments/SeedEn00001?fields=*,events[*]", []string{"SeedEn00001", "SeedEv00003"}},
		{tt + "enrollments/NegEn000001?fields=*,events[*]", []string{"NegEn000001", "NegEv000003"}},
		{tt + "events?" + ou + prog, []string{"SeedEv00001", "SeedEv00003", "NegEv000001", "NegEv000003"}},
		{tt + "events?" + ou + prog + "&trackedEntity=NegTe000001", []string{"NegEv000001", "NegEv000003"}},
		{tt + "events?" + ou + prog + "&event=SeedEv00003;NegEv000003", stage3},
		{tt + "events?" + ou + "&programStage=" + fc.Stage3, stage3},
		{tt + "events/SeedEv00003", []string{"SeedEv00003"}},
		{tt + "events/NegEv000003", []string{"NegEv000003"}},
		{tt + "events/NegEv000001", []string{"NegEv000001"}},
		{tt + "relationships?trackedEntity=SeedTe00001", []string{"NegRel00001"}},
		{tt + "relationships?trackedEntity=NegTe000001", []string{"NegRel00001"}},
		{tt + "relationships/NegRel00001", []string{"NegRel00001"}},
	}
	for _, p := range paths {
		st, body := a.do("GET", p.path, nil)
		for _, uid := range p.shows {
			if st != 200 || !strings.Contains(string(body), uid) {
				t.Errorf("control: %s as nurse_a: %d, %s missing: %.300s", p.path, st, uid, body)
			}
		}
		for _, role := range roles[1:] {
			c := clients[role]
			st, body := c.do("GET", p.path, nil)
			// Hidden UIDs the request names: an error may repeat them, but must be the
			// answer the hub gives for UIDs that do not exist.
			asked, unknown := p.path, string(body)
			for i, uid := range hidden[role] {
				if strings.Contains(p.path, uid) {
					none := fmt.Sprintf("NoSuchUid%02d", i)
					asked, unknown = strings.ReplaceAll(asked, uid, none), strings.ReplaceAll(unknown, uid, none)
				}
			}
			// Central refuses lists in the program without its tracked entity type
			// (2.43; 2.42 lists its events regardless, which the hub does not repeat).
			if role == "notype_f" && strings.Contains(p.path, prog) &&
				(st != 403 || !strings.Contains(string(body), "E1006")) {
				t.Errorf("%s as %s: %d %.200s, want 403 E1006", p.path, role, st, body)
			}
			for _, uid := range hidden[role] {
				if strings.Contains(string(body), uid) && (st == 200 || !strings.Contains(p.path, uid)) {
					t.Errorf("%s as %s: %d reveals %s: %.300s", p.path, role, st, uid, body)
				}
			}
			if asked != p.path {
				if st2, body2 := c.do("GET", asked, nil); st != st2 || unknown != string(body2) {
					t.Errorf("%s as %s: %d %.200s, but %d %.200s for UIDs that do not exist", p.path, role, st, body, st2, body2)
				}
			}
		}
	}
}
