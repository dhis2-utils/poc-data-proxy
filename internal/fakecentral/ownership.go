package fakecentral

import (
	"cmp"
	"net/http"
	"slices"
	"time"

	"github.com/dhis2/facility-hub/internal/tracker"
)

// Ownership of the program, as central keeps it per tracked entity and program.

// owner returns who owns te in the program: set by a transfer, else where it was
// registered. Call with c.mu held.
func (c *Central) owner(te string) string {
	if ou, ok := c.owners[te]; ok {
		return ou
	}
	return tracker.Str(c.objects[te], "orgUnit")
}

// Transfer moves te's ownership in the program to ou (as an admin would on central).
func (c *Central) Transfer(te, ou string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.setOwner(te, ou)
}

// setOwner moves ownership and, like DHIS2 2.42, bumps the tracked entity's updatedAt.
func (c *Central) setOwner(te, ou string) {
	c.owners[te] = ou
	if o := c.objects[te]; o != nil {
		o["updatedAt"] = c.now()
	}
}

func inScope(ous []string, ou string) bool {
	return slices.Contains(ous, ou) || slices.Contains(ous, "root")
}

// canRead applies ownership: the owner in the user's capture scope (or, for an open
// program, search scope), or unexpired temporary ownership. Call with c.mu held.
func (c *Central) canRead(u *User, te string) bool {
	owner := c.owner(te)
	if inScope(u.OrgUnits, owner) {
		return true
	}
	if until, ok := c.temp[u.Username+"|"+te]; ok && time.Now().Before(until) {
		return true
	}
	if c.Protected {
		return false
	}
	search := u.SearchOrgUnits
	if search == nil {
		search = u.OrgUnits
	}
	return inScope(search, owner)
}

// getTE answers GET /api/tracker/trackedEntities/{uid}, refusing a protected
// enrollment like central (403 OWNERSHIP_ACCESS_DENIED).
func (c *Central) getTE(w http.ResponseWriter, r *http.Request, u *User, uid string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	o := c.objects[uid]
	if o == nil || c.kinds[uid] != "trackedEntity" {
		webMessage(w, 404, "TrackedEntity with id "+uid+" could not be found.", "")
		return
	}
	program := r.URL.Query().Get("program")
	if program != "" && !c.canRead(u, uid) {
		webMessage(w, 403, "OWNERSHIP_ACCESS_DENIED", "")
		return
	}
	te := deepCopy(o)
	var enrs []any
	if program == Program {
		for _, eu := range c.order {
			if e := c.objects[eu]; c.kinds[eu] == "enrollment" && e["trackedEntity"] == uid {
				en := deepCopy(e)
				var evs []any
				for _, vu := range c.order {
					if ev := c.objects[vu]; c.kinds[vu] == "event" && ev["enrollment"] == eu && canSeeEvent(u, ev) {
						evs = append(evs, deepCopy(ev))
					}
				}
				en["events"] = orEmpty(evs)
				enrs = append(enrs, en)
			}
		}
		te["programOwners"] = []any{tracker.M{"orgUnit": c.owner(uid), "trackedEntity": uid, "program": Program}}
	}
	te["enrollments"] = orEmpty(enrs)
	te["relationships"] = []any{}
	writeJSON(w, 200, te)
}

// transfer answers PUT /api/tracker/ownership/transfer?trackedEntity=&program=&ou=
// (orgUnit= as 2.43 names it is accepted too).
func (c *Central) transfer(w http.ResponseWriter, r *http.Request, u *User) {
	q := r.URL.Query()
	te := q.Get("trackedEntity")
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.objects[te] == nil || q.Get("program") != Program || !c.canRead(u, te) {
		webMessage(w, 403, "OWNERSHIP_ACCESS_DENIED", "")
		return
	}
	c.setOwner(te, cmp.Or(q.Get("orgUnit"), q.Get("ou")))
	writeJSON(w, 200, map[string]any{"httpStatus": "OK", "httpStatusCode": 200, "status": "OK", "message": "Ownership transferred"})
}

// override answers POST /api/tracker/ownership/override?trackedEntity=&program=&reason=.
func (c *Central) override(w http.ResponseWriter, r *http.Request, u *User) {
	q := r.URL.Query()
	te := q.Get("trackedEntity")
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.objects[te] == nil || q.Get("program") != Program || q.Get("reason") == "" {
		webMessage(w, 409, "Temporary ownership requires a tracked entity, the program and a reason", "")
		return
	}
	c.temp[u.Username+"|"+te] = time.Now().Add(c.TempAccess)
	writeJSON(w, 200, map[string]any{"httpStatus": "OK", "httpStatusCode": 200, "status": "OK",
		"message": "Temporary Ownership granted"})
}

// ExpireTemporaryAccess ends every temporary ownership now.
func (c *Central) ExpireTemporaryAccess() {
	c.mu.Lock()
	defer c.mu.Unlock()
	clear(c.temp)
}
