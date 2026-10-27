package testkit

import (
	"encoding/json"
	"maps"
	"net/url"
	"slices"
	"strings"
)

// Changes on central, as an administrator makes them while a hub holds offline work
// (docs/ADVERSARIAL-TESTING.md). Each works on a real DHIS2 through the Web API; the
// ones the fake central can model work on it too, the others skip there. A change
// that can be undone is undone when the test ends, so the instance stays usable.

func (e *Env) needLive(what string) {
	e.T.Helper()
	if !e.Live() {
		e.T.Skipf("%s: the fake central has no such metadata", what)
	}
}

// centralDo sends a request to central as the superuser and fails the test unless the
// answer is 2xx.
func (e *Env) centralDo(method, path string, body any) []byte {
	e.T.Helper()
	st, b, err := e.Central.Do(method, path, body)
	if err != nil || st/100 != 2 {
		e.T.Fatalf("central %s %s: %d %v %.800s", method, path, st, err, b)
	}
	return b
}

// putOwner reads a metadata object's own fields, lets edit change them, and puts it
// back. Nested lists such as a stage's data elements cannot be patched by position:
// central does not keep their order.
func (e *Env) putOwner(path string, edit func(map[string]any)) {
	e.T.Helper()
	var o map[string]any
	if err := json.Unmarshal(e.centralDo("GET", path+"?fields=:owner", nil), &o); err != nil {
		e.T.Fatal(err)
	}
	edit(o)
	e.centralDo("PUT", path, o)
}

func (e *Env) patch(path string, ops ...map[string]any) {
	e.T.Helper()
	e.centralDo("PATCH", path, ops)
}

func replaceOp(path string, v any) map[string]any {
	return map[string]any{"op": "replace", "path": path, "value": v}
}

// SetCompulsory makes a data element compulsory in a program stage, or not.
func (e *Env) SetCompulsory(stage, de string, compulsory bool) {
	e.T.Helper()
	e.needLive("SetCompulsory")
	var was bool
	set := func(v bool) {
		e.putOwner("/api/programStages/"+stage, func(o map[string]any) {
			for _, x := range asAnyList(o["programStageDataElements"]) {
				p := x.(map[string]any)
				if ref(p["dataElement"]) == de {
					was, p["compulsory"] = p["compulsory"] == true, v
					return
				}
			}
			e.T.Fatalf("%s is not in stage %s", de, stage)
		})
	}
	set(compulsory)
	prev := was
	e.T.Cleanup(func() { set(prev) })
}

// RemoveOption takes an option out of an option set (the option itself stays, as
// when an administrator removes it from the set's list).
func (e *Env) RemoveOption(optionSet, code string) {
	e.T.Helper()
	e.needLive("RemoveOption")
	var os struct {
		Options []struct{ ID, Code string } `json:"options"`
	}
	json.Unmarshal(e.centralDo("GET", "/api/optionSets/"+optionSet+"?fields=options[id,code]", nil), &os)
	i := slices.IndexFunc(os.Options, func(o struct{ ID, Code string }) bool { return o.Code == code })
	if i < 0 {
		e.T.Fatalf("option set %s has no option %s", optionSet, code)
	}
	path := "/api/optionSets/" + optionSet + "/options/" + os.Options[i].ID
	e.centralDo("DELETE", path, nil)
	e.T.Cleanup(func() { e.Central.Do("POST", path, nil) })
}

// SetRepeatable makes a program stage repeatable or not.
func (e *Env) SetRepeatable(stage string, repeatable bool) {
	e.T.Helper()
	e.needLive("SetRepeatable")
	var cur struct{ Repeatable bool }
	json.Unmarshal(e.centralDo("GET", "/api/programStages/"+stage+"?fields=repeatable", nil), &cur)
	e.patch("/api/programStages/"+stage, replaceOp("/repeatable", repeatable))
	e.T.Cleanup(func() {
		e.Central.Do("PATCH", "/api/programStages/"+stage, []any{replaceOp("/repeatable", cur.Repeatable)})
	})
}

// RemoveStageDataElement takes a data element out of a program stage (the data
// element itself stays, and so do events' values of it on central).
func (e *Env) RemoveStageDataElement(stage, de string) {
	e.T.Helper()
	e.needLive("RemoveStageDataElement")
	var removed any
	e.putOwner("/api/programStages/"+stage, func(o map[string]any) {
		list := asAnyList(o["programStageDataElements"])
		i := slices.IndexFunc(list, func(x any) bool { return ref(x.(map[string]any)["dataElement"]) == de })
		if i < 0 {
			e.T.Fatalf("%s is not in stage %s", de, stage)
		}
		removed = list[i]
		o["programStageDataElements"] = slices.Delete(slices.Clone(list), i, i+1)
	})
	e.T.Cleanup(func() {
		e.putOwner("/api/programStages/"+stage, func(o map[string]any) {
			o["programStageDataElements"] = append(asAnyList(o["programStageDataElements"]), removed)
		})
	})
}

// SetUnique makes a tracked entity attribute unique, or not.
func (e *Env) SetUnique(attr string, unique bool) {
	e.T.Helper()
	e.needLive("SetUnique")
	var cur struct{ Unique bool }
	json.Unmarshal(e.centralDo("GET", "/api/trackedEntityAttributes/"+attr+"?fields=unique", nil), &cur)
	e.patch("/api/trackedEntityAttributes/"+attr, replaceOp("/unique", unique))
	e.T.Cleanup(func() {
		e.Central.Do("PATCH", "/api/trackedEntityAttributes/"+attr, []any{replaceOp("/unique", cur.Unique)})
	})
}

// TransferOwnership moves a tracked entity's ownership in a program to an org unit,
// as an administrator does on central. Tracker data: not undone.
func (e *Env) TransferOwnership(te, program, ou string) {
	e.T.Helper()
	e.needLive("TransferOwnership")
	param := "ou" // 2.42; 2.43 names it orgUnit
	if strings.HasPrefix(e.M.DHIS2Version, "2.43") {
		param = "orgUnit"
	}
	e.centralDo("PUT", "/api/tracker/ownership/transfer?"+url.Values{"trackedEntity": {te}, "program": {program}, param: {ou}}.Encode(), nil)
}

// CentralOwner is the org unit that owns a tracked entity in a program on central.
func (e *Env) CentralOwner(te, program string) string {
	e.T.Helper()
	var r struct {
		ProgramOwners []struct{ OrgUnit, Program string } `json:"programOwners"`
	}
	if err := e.Central.JSON("GET", "/api/tracker/trackedEntities/"+te+"?"+url.Values{"program": {program},
		"fields": {"programOwners"}}.Encode(), nil, &r); err != nil {
		e.T.Fatal(err)
	}
	for _, po := range r.ProgramOwners {
		if po.Program == program {
			return po.OrgUnit
		}
	}
	return ""
}

// RemoveProgramFromOrgUnit unassigns a program from an org unit.
func (e *Env) RemoveProgramFromOrgUnit(program, ou string) {
	e.T.Helper()
	e.needLive("RemoveProgramFromOrgUnit")
	path := "/api/programs/" + program + "/organisationUnits/" + ou
	e.centralDo("DELETE", path, nil)
	e.T.Cleanup(func() { e.Central.Do("POST", path, nil) })
}

// NewUserLike creates a user with the role user's roles, groups and org units, under
// a fresh name, so a scenario can change or delete it without touching the manifest's
// users. Live, it is deleted when the test ends, if central lets it.
func (e *Env) NewUserLike(role string) User {
	e.T.Helper()
	base := e.user(role)
	u := User{UID: UID(), Username: "adv_" + strings.ToLower(UID()[:8])}
	if e.Fake != nil {
		f := *e.Fake.Users[base.Username]
		f.UID, f.Username = u.UID, u.Username
		f.StageRead, f.StageWrite = maps.Clone(f.StageRead), maps.Clone(f.StageWrite)
		e.Fake.Users[u.Username] = &f
		return u
	}
	var cur map[string]any
	json.Unmarshal(e.centralDo("GET", "/api/users/"+base.UID+"?fields=userRoles[id],userGroups[id],organisationUnits[id],"+
		"teiSearchOrganisationUnits[id],dataViewOrganisationUnits[id]", nil), &cur)
	cur["id"], cur["username"], cur["password"] = u.UID, u.Username, e.M.Password
	cur["firstName"], cur["surname"] = "Adversarial", u.Username
	e.centralDo("POST", "/api/users", cur)
	e.T.Cleanup(func() { e.Central.Do("DELETE", "/api/users/"+u.UID, nil) })
	return u
}

// SetUserOrgUnits replaces a user's capture org units (search and data view stay).
func (e *Env) SetUserOrgUnits(u User, ous ...string) {
	e.T.Helper()
	if e.Fake != nil {
		e.Fake.Users[u.Username].OrgUnits = ous
		return
	}
	var refs []any
	for _, ou := range ous {
		refs = append(refs, map[string]any{"id": ou})
	}
	e.patch("/api/users/"+u.UID, replaceOp("/organisationUnits", refs))
}

// SetUserDisabled disables or enables a user's account.
func (e *Env) SetUserDisabled(u User, disabled bool) {
	e.T.Helper()
	if e.Fake != nil {
		e.Fake.SetDisabled(u.Username, disabled)
		return
	}
	e.patch("/api/users/"+u.UID, replaceOp("/disabled", disabled))
}

// RevokeTokens deletes every personal access token of a user, as they would in their
// profile (the superuser does not see other users' tokens). It returns how many.
func (e *Env) RevokeTokens(u User) int {
	e.T.Helper()
	if e.Fake != nil {
		n := e.Fake.TokensOf(u.Username)
		e.Fake.RevokeTokensOf(u.Username)
		return n
	}
	as := e.Central.As(u.Username, e.M.Password)
	var toks struct {
		APIToken []struct{ ID string } `json:"apiToken"`
	}
	if err := as.JSON("GET", "/api/apiToken?fields=id&paging=false", nil, &toks); err != nil {
		e.T.Fatal(err)
	}
	for _, t := range toks.APIToken {
		if err := as.JSON("DELETE", "/api/apiToken/"+t.ID, nil, nil); err != nil {
			e.T.Fatal(err)
		}
	}
	return len(toks.APIToken)
}

// ExpireTokenCache clears central's caches, as an hour does for its cache of personal
// access tokens: 2.42 keeps a token's authentication, with the user's access as it
// was, for an hour after its first use (apiTokensCache). The fake has no such cache.
func (e *Env) ExpireTokenCache() {
	e.T.Helper()
	if e.Live() {
		e.centralDo("POST", "/api/maintenance/cacheClear", nil)
	}
}

// DeleteUser deletes a user on central.
func (e *Env) DeleteUser(u User) {
	e.T.Helper()
	if e.Fake != nil {
		delete(e.Fake.Users, u.Username)
		return
	}
	e.centralDo("DELETE", "/api/users/"+u.UID, nil)
}

// RemoveSharing removes a user's or a user group's access to a metadata object (kind
// as /api/sharing names it: program, programStage, trackedEntityType ...). On the
// fake, only a user's program access can be removed.
func (e *Env) RemoveSharing(kind, id, userOrGroup string) {
	e.T.Helper()
	if e.Fake != nil {
		for _, u := range e.Fake.Users {
			if u.UID == userOrGroup && kind == "program" {
				u.ProgramRead, u.ProgramWrite = false, false
				return
			}
		}
		e.T.Skipf("RemoveSharing %s %s: the fake models only a user's program access", kind, userOrGroup)
	}
	path := "/api/sharing?" + url.Values{"type": {kind}, "id": {id}}.Encode()
	var cur map[string]any
	json.Unmarshal(e.centralDo("GET", path, nil), &cur)
	obj, _ := cur["object"].(map[string]any)
	if obj == nil {
		e.T.Fatalf("sharing of %s %s: %v", kind, id, cur)
	}
	was := map[string]any{}
	maps.Copy(was, obj)
	removed := false
	for _, k := range []string{"userAccesses", "userGroupAccesses"} {
		list := asAnyList(obj[k])
		keep := slices.DeleteFunc(slices.Clone(list), func(x any) bool { return ref(x) == userOrGroup })
		removed = removed || len(keep) < len(list)
		obj[k] = keep
	}
	if !removed {
		e.T.Fatalf("%s %s is not shared with %s", kind, id, userOrGroup)
	}
	e.centralDo("POST", path, map[string]any{"object": obj})
	e.T.Cleanup(func() { e.Central.Do("POST", path, map[string]any{"object": was}) })
}

func asAnyList(v any) []any {
	l, _ := v.([]any)
	return l
}

// ref is the id of a {"id": ...} reference.
func ref(v any) string {
	m, _ := v.(map[string]any)
	s, _ := m["id"].(string)
	return s
}

// Approval: the level and workflow ApproveDataSet makes. Central keeps them: once a
// form was approved and unapproved, its approval audit refers to both, and central
// refuses to delete them (E4030, a foreign key), so later runs use them again.
const (
	approvalLevel    = "AdvApprLev1"
	approvalWorkflow = "AdvApprWfl1"
)

// ApproveDataSet approves a data set's period at the facility on central, as a
// supervisor would. A data set without a workflow gets one, with one level at the
// facility's org unit level. The approval and the data set's workflow are undone when
// the test ends; the workflow and its level stay on central (approvalLevel).
func (e *Env) ApproveDataSet(ds, pe string) {
	e.T.Helper()
	e.needLive("ApproveDataSet")
	var cur struct {
		PeriodType string               `json:"periodType"`
		Workflow   *struct{ ID string } `json:"workflow"`
	}
	json.Unmarshal(e.centralDo("GET", "/api/dataSets/"+ds+"?fields=periodType,workflow[id]", nil), &cur)
	wf := approvalWorkflow
	if cur.Workflow != nil {
		wf = cur.Workflow.ID
	} else {
		if st, _, _ := e.Central.Do("GET", "/api/dataApprovalLevels/"+approvalLevel, nil); st == 404 {
			e.centralDo("POST", "/api/dataApprovalLevels", map[string]any{"id": approvalLevel, "name": "Adversarial approval",
				"orgUnitLevel": strings.Count(e.M.Facility.Path, "/")})
		}
		if st, _, _ := e.Central.Do("GET", "/api/dataApprovalWorkflows/"+wf, nil); st == 404 {
			e.centralDo("POST", "/api/dataApprovalWorkflows", map[string]any{"id": wf, "name": "Adversarial approval",
				"periodType": cur.PeriodType, "dataApprovalLevels": []any{map[string]any{"id": approvalLevel}}})
		}
		e.patch("/api/dataSets/"+ds, map[string]any{"op": "add", "path": "/workflow", "value": map[string]any{"id": wf}})
		e.T.Cleanup(func() {
			e.Central.Do("PATCH", "/api/dataSets/"+ds, []any{map[string]any{"op": "remove", "path": "/workflow"}})
		})
	}
	q := url.Values{"wf": {wf}, "pe": {pe}, "ou": {e.M.Facility.ID}}.Encode()
	e.centralDo("POST", "/api/dataApprovals?"+q, nil)
	e.T.Cleanup(func() { e.Central.Do("DELETE", "/api/dataApprovals?"+q, nil) })
}
