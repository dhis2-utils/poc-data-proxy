package fakecentral

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/dhis2/facility-hub/internal/tracker"
)

// SetStatus sets an enrollment's status (ACTIVE, COMPLETED, CANCELLED).
func (c *Central) SetStatus(enrollment, status string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if o := c.objects[enrollment]; o != nil {
		o["status"] = status
	}
}

// Backdate sets updatedAt on an object and what is under it: for a tracked entity,
// its enrollments and their events; for an enrollment, its events. Data on a real
// central cannot be aged; the fake's can, for retention tests.
func (c *Central) Backdate(uid, updatedAt string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, u := range c.order {
		o := c.objects[u]
		if u == uid || o["trackedEntity"] == uid || o["enrollment"] == uid {
			o["updatedAt"] = updatedAt
		}
	}
}

// enrollOnce is central's check of a new enrollment against the person's others in
// the program, counting those accepted earlier in the payload: E1016 and E1015, both
// when both apply, as 2.42 answers.
func (c *Central) enrollOnce(it *tracker.Item, accepted []*tracker.Item) [][2]string {
	status := cmp.Or(tracker.Str(it.Data, "status"), "ACTIVE")
	te, program := it.TE, tracker.Str(it.Data, "program")
	if status == "CANCELLED" {
		return nil
	}
	var others []tracker.M
	for _, u := range c.order {
		if o := c.objects[u]; c.kinds[u] == "enrollment" && o["trackedEntity"] == te && o["program"] == program && o["deleted"] != true {
			others = append(others, o)
		}
	}
	for _, x := range accepted {
		if x.Kind == "enrollment" && x.TE == te && tracker.Str(x.Data, "program") == program {
			others = append(others, x.Data)
		}
	}
	var e1015, e1016 bool
	for _, o := range others {
		st := cmp.Or(tracker.Str(o, "status"), "ACTIVE")
		e1015 = e1015 || (status == "ACTIVE" && st == "ACTIVE")
		e1016 = e1016 || (program == Program && c.OnlyEnrollOnce && (st == "ACTIVE" || st == "COMPLETED"))
	}
	var out [][2]string
	if e1016 {
		out = append(out, [2]string{"E1016", fmt.Sprintf("TrackedEntity: `%s` already has an Enrollment in Program: `%s`, and this Program only allows enrolling once.", te, program)})
	}
	if e1015 {
		out = append(out, [2]string{"E1015", fmt.Sprintf("TrackedEntity: `%s` already has an active Enrollment in Program `%s`.", te, program)})
	}
	return out
}

// FetchCount is how often a tracked entity was fetched by its identifier.
func (c *Central) FetchCount(te string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fetched[te]
}

// TEListQueries returns the query strings of the tracked entity listings and fetches
// central answered, in order.
func (c *Central) TEListQueries() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.teLists)
}

// TokensOf counts a user's personal access tokens.
func (c *Central) TokensOf(username string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, u := range c.tokens {
		if u == username {
			n++
		}
	}
	return n
}

// TokenOwner is the user a token belongs to ("" if it is not valid).
func (c *Central) TokenOwner(key string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tokens[key]
}

// entryID is a dataStore entry's identifier (its metaData id).
func entryID(ns, key string) string {
	sum := sha256.Sum256([]byte(ns + "/" + key))
	return "Ds" + hex.EncodeToString(sum[:])[:9]
}

// createUser creates a user as POST /api/users does (the fields hub accounts use).
func (c *Central) createUser(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID, Username, Password string
		OrganisationUnits      []struct{ ID string } `json:"organisationUnits"`
		DataView               []struct{ ID string } `json:"dataViewOrganisationUnits"`
		Search                 []struct{ ID string } `json:"teiSearchOrganisationUnits"`
		Groups                 []struct{ ID string } `json:"userGroups"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Username == "" {
		webMessage(w, 400, "bad user", "")
		return
	}
	ids := func(refs []struct{ ID string }) []string {
		out := []string{}
		for _, o := range refs {
			out = append(out, o.ID)
		}
		return out
	}
	ous := ids(in.OrganisationUnits)
	c.mu.Lock()
	if c.passwords == nil {
		c.passwords = map[string]string{}
	}
	if c.Users[in.Username] != nil {
		c.mu.Unlock()
		webMessage(w, 409, "username already taken", "E4054")
		return
	}
	// Created with a role without authorities, as hub accounts are.
	c.Users[in.Username] = &User{UID: in.ID, Username: in.Username, OrgUnits: ous, DataViewOrgUnits: ids(in.DataView),
		SearchOrgUnits: ids(in.Search), Authorities: []string{}, Groups: ids(in.Groups), StageRead: map[string]bool{}, StageWrite: map[string]bool{}}
	c.passwords[in.Username] = in.Password
	c.mu.Unlock()
	writeJSON(w, 201, map[string]any{"httpStatus": "Created", "httpStatusCode": 201, "status": "OK", "response": map[string]any{"uid": in.ID}})
}

// addToGroup handles POST /api/userGroups/{group}/users/{user}.
func (c *Central) addToGroup(w http.ResponseWriter, p string) {
	parts := strings.Split(strings.TrimPrefix(p, "/api/userGroups/"), "/")
	if len(parts) != 3 || parts[1] != "users" {
		webMessage(w, 404, "not found", "")
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, u := range c.Users {
		if u.UID == parts[2] {
			u.Groups = append(u.Groups, parts[0])
			writeJSON(w, 200, map[string]any{"status": "OK"})
			return
		}
	}
	webMessage(w, 404, "no such user", "")
}

// shareEntry handles POST /api/sharing?type=dataStore&id=...: the entry becomes
// readable by the listed users (and the superuser), and by all if its public access
// says so.
func (c *Central) shareEntry(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Object struct {
			PublicAccess      string                        `json:"publicAccess"`
			UserAccesses      []struct{ ID, Access string } `json:"userAccesses"`
			UserGroupAccesses []struct{ ID, Access string } `json:"userGroupAccesses"`
		} `json:"object"`
	}
	json.NewDecoder(r.Body).Decode(&in)
	rs, ws := map[string]bool{}, map[string]bool{}
	c.mu.Lock()
	for _, a := range in.Object.UserAccesses {
		rs[a.ID] = strings.HasPrefix(a.Access, "r")
		ws[a.ID] = strings.HasPrefix(a.Access, "rw")
	}
	// A group's access goes to its members.
	for _, a := range in.Object.UserGroupAccesses {
		for _, u := range c.Users {
			if slices.Contains(u.Groups, a.ID) {
				rs[u.UID] = rs[u.UID] || strings.HasPrefix(a.Access, "r")
				ws[u.UID] = ws[u.UID] || strings.HasPrefix(a.Access, "rw")
			}
		}
	}
	if c.readers == nil {
		c.readers, c.writers = map[string]map[string]bool{}, map[string]map[string]bool{}
	}
	c.readers[r.URL.Query().Get("id")] = rs
	c.writers[r.URL.Query().Get("id")] = ws
	if c.public == nil {
		c.public = map[string]string{}
	}
	c.public[r.URL.Query().Get("id")] = (in.Object.PublicAccess + "--------")[:8]
	c.mu.Unlock()
	writeJSON(w, 200, map[string]any{"status": "OK"})
}

// changePassword handles PUT /api/me/changePassword (users created through the API).
func (c *Central) changePassword(w http.ResponseWriter, r *http.Request, u *User) {
	var in struct{ OldPassword, NewPassword string }
	json.NewDecoder(r.Body).Decode(&in)
	c.mu.Lock()
	defer c.mu.Unlock()
	if old, ok := c.passwords[u.Username]; !ok || old != in.OldPassword || in.NewPassword == "" {
		webMessage(w, 400, "wrong old password", "")
		return
	}
	c.passwords[u.Username] = in.NewPassword
	w.WriteHeader(http.StatusAccepted)
}

// findUser handles GET /api/users?filter=username:eq:X.
func (c *Central) findUser(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Query().Get("filter"), "username:eq:")
	c.mu.Lock()
	defer c.mu.Unlock()
	users := []any{}
	if u := c.Users[name]; u != nil {
		ous := []any{}
		for _, o := range u.OrgUnits {
			ous = append(ous, map[string]any{"id": o})
		}
		refs := func(ids []string) []any {
			out := []any{}
			for _, o := range ids {
				out = append(out, map[string]any{"id": o})
			}
			return out
		}
		auths := u.Authorities
		if auths == nil {
			auths = []string{}
		}
		users = append(users, map[string]any{"id": u.UID, "username": u.Username, "disabled": u.Disabled, "organisationUnits": ous,
			"dataViewOrganisationUnits": refs(u.DataViewOrgUnits), "teiSearchOrganisationUnits": refs(u.SearchOrgUnits),
			"userRoles": []any{map[string]any{"id": "HubRoleAcct", "authorities": auths}}})
	}
	writeJSON(w, 200, map[string]any{"users": users})
}

// patchUser handles PATCH /api/users/{id} replacing the password (JSON patch).
func (c *Central) patchUser(w http.ResponseWriter, r *http.Request, id string) {
	var ops []struct {
		Op, Path string
		Value    json.RawMessage
	}
	json.NewDecoder(r.Body).Decode(&ops)
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, u := range c.Users {
		if u.UID != id {
			continue
		}
		for _, op := range ops {
			switch {
			case op.Op == "replace" && op.Path == "/password":
				var pw string
				json.Unmarshal(op.Value, &pw)
				if c.passwords == nil {
					c.passwords = map[string]string{}
				}
				c.passwords[u.Username] = pw
			case (op.Op == "add" || op.Op == "replace") && op.Path == "/userGroups":
				var gs []struct{ ID string }
				json.Unmarshal(op.Value, &gs)
				u.Groups = nil
				for _, g := range gs {
					u.Groups = append(u.Groups, g.ID)
				}
			}
		}
		writeJSON(w, 200, map[string]any{"status": "OK"})
		return
	}
	webMessage(w, 404, "no such user", "E1005")
}

// PasswordOf is the password of a user created through the API.
func (c *Central) PasswordOf(username string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.passwords[username]
}

// CreateToken makes a personal access token for a user, as they could themselves.
func (c *Central) CreateToken(username string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tokenSeq++
	key := "d2p_" + strings.Repeat("y", 10) + strconv.Itoa(c.tokenSeq) + username
	c.tokens[key], c.tokenIDs[fmt.Sprintf("Tok%08d", c.tokenSeq)] = username, key
	return key
}
