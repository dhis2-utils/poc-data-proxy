package fakecentral

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
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
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Username == "" {
		webMessage(w, 400, "bad user", "")
		return
	}
	var ous []string
	for _, o := range in.OrganisationUnits {
		ous = append(ous, o.ID)
	}
	c.mu.Lock()
	if c.passwords == nil {
		c.passwords = map[string]string{}
	}
	c.Users[in.Username] = &User{UID: in.ID, Username: in.Username, OrgUnits: ous, StageRead: map[string]bool{}, StageWrite: map[string]bool{}}
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
// readable by the listed users only (and the superuser).
func (c *Central) shareEntry(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Object struct {
			UserAccesses []struct{ ID, Access string } `json:"userAccesses"`
		} `json:"object"`
	}
	json.NewDecoder(r.Body).Decode(&in)
	rs := map[string]bool{}
	for _, a := range in.Object.UserAccesses {
		if strings.HasPrefix(a.Access, "r") {
			rs[a.ID] = true
		}
	}
	c.mu.Lock()
	if c.readers == nil {
		c.readers = map[string]map[string]bool{}
	}
	c.readers[r.URL.Query().Get("id")] = rs
	c.mu.Unlock()
	writeJSON(w, 200, map[string]any{"status": "OK"})
}
