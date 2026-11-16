package core

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/dhis2/facility-hub/internal/access"
	"github.com/dhis2/facility-hub/internal/aggregate"
	"github.com/dhis2/facility-hub/internal/central"
	"github.com/dhis2/facility-hub/internal/store"
)

func aggAccessKey(userID int64) string { return fmt.Sprintf("agg_access:%d", userID) }

// aggStampKey holds accessStamp of the user when their dataEntry/metadata was last
// fetched in full.
func aggStampKey(userID int64) string { return fmt.Sprintf("agg_access_stamp:%d", userID) }

// accessStamp is the part of a user's access that their dataEntry/metadata depends on
// but central's ETag does not cover: on 2.43.2.1 a user given a second org unit got
// new data set org units in the answer with the same ETag, and a 304 to the old one
// (FINDINGS.md, "Aggregate Data Entry through the hub"). Group membership is in it
// for the same reason; a data set's own sharing change did change the ETag.
func accessStamp(l *access.List) string {
	var b strings.Builder
	for _, part := range [][]string{l.OrgUnits, l.UserGroups, l.Authorities} {
		p := slices.Clone(part)
		slices.Sort(p)
		b.WriteString(strings.Join(p, ","))
		b.WriteByte(';')
	}
	return b.String()
}

// AggAccess is what the user may enter in the Aggregate Data Entry app (their
// dataEntry/metadata at the last access refresh), or nil.
func (h *Hub) AggAccess(userID int64) *aggregate.Access {
	h.mu.RLock()
	a, ok := h.aggAccess[userID]
	h.mu.RUnlock()
	if ok {
		return a
	}
	b, err := h.Store.State(aggAccessKey(userID))
	if err != nil || b == nil {
		return nil
	}
	a = &aggregate.Access{}
	if json.Unmarshal(b, a) != nil {
		return nil
	}
	h.mu.Lock()
	h.aggAccess[userID] = a
	h.mu.Unlock()
	return a
}

// APIMinor is the version path segment the apps use for central's version ("42").
func (h *Hub) APIMinor() string {
	parts := strings.Split(h.ServerVersion(), ".")
	if len(parts) < 2 {
		return ""
	}
	return parts[1]
}

// RefreshAggAccess fetches the user's dataEntry/metadata: the data sets they may
// enter. The answer is also cached for the app, which asks for it at start-up, so
// the app opens offline for users who never opened it online through the hub. A
// user without the app's authority gets 403 or an empty list: no aggregate access.
func (h *Hub) RefreshAggAccess(ctx context.Context, userID int64, auth central.Auth, l *access.List) error {
	path := "/api/" + h.APIMinor() + "/dataEntry/metadata"
	authorities, stamp := l.Authorities, accessStamp(l)
	// Asked with the ETag of the copy the hub holds: unchanged, central answers 304
	// with no body (3.5 MB a user an hour with eRegisters' forms otherwise;
	// PERFORMANCE.md, "Central's load from one facility"). A change to the forms
	// changes the ETag; a change to the user's org units or groups does not, so the
	// hub asks without it when those changed since the copy (accessStamp).
	var cond http.Header
	old := h.AggAccess(userID)
	prev, _ := h.Store.State(aggStampKey(userID))
	if c, _ := h.Store.GetCache(userID, "GET", path); c != nil && c.Status == http.StatusOK && old != nil && string(prev) == stamp {
		if et := c.Header.Get("ETag"); et != "" {
			cond = http.Header{"If-None-Match": {et}}
		}
	}
	resp, err := h.Central.Do(ctx, "GET", path, nil, auth, cond)
	if err != nil {
		return err
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	a := &aggregate.Access{}
	switch resp.StatusCode {
	case http.StatusNotModified:
		// The authorities are the hub's own reading of the user, not in the answer.
		if cp := *old; cp.Bypass != aggregate.Bypass(authorities) {
			cp.Bypass = aggregate.Bypass(authorities)
			a = &cp
		} else {
			return nil
		}
	case http.StatusOK:
		if a, err = aggregate.ParseMetadata(b, authorities); err != nil {
			return fmt.Errorf("dataEntry/metadata: %w", err)
		}
		if err := h.fillAttributeCombos(ctx, a, auth); err != nil {
			return err
		}
		hdr := resp.Header.Clone()
		hdr.Del("Set-Cookie")
		if err := h.Store.PutCache(userID, "GET", path, &store.CachedResponse{Status: 200, Header: hdr, Body: b}); err != nil {
			return err
		}
		if err := h.Store.SetState(aggStampKey(userID), []byte(stamp)); err != nil {
			return err
		}
	case http.StatusForbidden, http.StatusNotFound: // no aggregate data entry for this user
	default: // keep the last answer
		return &central.HTTPError{Status: resp.StatusCode, Body: b}
	}
	js, _ := json.Marshal(a)
	if err := h.Store.SetState(aggAccessKey(userID), js); err != nil {
		return err
	}
	h.mu.Lock()
	h.aggAccess[userID] = a
	h.mu.Unlock()
	return nil
}

// fillAttributeCombos reads the option combinations of the data sets' attribute
// category combinations: dataEntry/metadata lists the combinations' categories but
// not their option combinations, which name the forms (and values) on central.
func (h *Hub) fillAttributeCombos(ctx context.Context, a *aggregate.Access, auth central.Auth) error {
	var ids []string
	for _, ds := range a.DataSets {
		if c := a.Combos[ds.CategoryCombo]; c != nil && len(c.COCs) == 0 && !slices.Contains(ids, c.ID) {
			ids = append(ids, c.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	var res struct {
		COCs []struct {
			ID            string `json:"id"`
			DisplayName   string `json:"displayName"`
			CategoryCombo struct {
				ID string `json:"id"`
			} `json:"categoryCombo"`
			Options []struct {
				ID string `json:"id"`
			} `json:"categoryOptions"`
		} `json:"categoryOptionCombos"`
	}
	path := central.Q("/api/categoryOptionCombos", "filter", "categoryCombo.id:in:["+strings.Join(ids, ",")+"]",
		"fields", "id,displayName,categoryCombo[id],categoryOptions[id]", "paging", "false")
	if err := h.Central.JSON(ctx, "GET", path, nil, auth, &res); err != nil {
		return fmt.Errorf("attribute option combinations: %w", err)
	}
	for _, coc := range res.COCs {
		c := a.Combos[coc.CategoryCombo.ID]
		if c == nil {
			continue
		}
		var opts []string
		for _, o := range coc.Options {
			opts = append(opts, o.ID)
		}
		c.COCs = append(c.COCs, aggregate.COC{ID: coc.ID, Name: coc.DisplayName, Options: opts})
	}
	return nil
}

// AggPulledKey marks a data set the hub has pulled at least once: before that its
// forms are answered locked offline, not as empty forms open for entry.
func AggPulledKey(dataSet string) string { return "agg_pulled:" + dataSet }

const aggRetentionKey = "agg_retention"

// maxPeriods is the most periods of a data set the Facility Hub app lets a country keep.
const maxPeriods = 1000

// AggRetention is the retention table read from central (facility-hub-config/
// aggregate) at the last catalog refresh, with aggregate.DefaultRetention periods where
// it names none.
func (h *Hub) AggRetention() aggregate.Retention {
	r := aggregate.Retention{}
	h.configState(aggRetentionKey, &r)
	if r.Default <= 0 {
		r.Default = aggregate.DefaultRetention
	}
	// Within the app's limit, whatever the entry holds (review R-07).
	r.Default = min(r.Default, maxPeriods)
	for k, n := range r.DataSets {
		r.DataSets[k] = min(n, maxPeriods)
	}
	return r
}

// AggLabel names an aggregate outbox cell ("agg:ds|pe|ou|aoc|de|coc|field") for people:
// data set, period, data element and category option combination, from any user's
// access.
func (h *Hub) AggLabel(cell string) string {
	parts := strings.Split(strings.TrimPrefix(cell, "agg:"), "|")
	if len(parts) < 4 {
		return cell
	}
	users, _ := h.Store.Users()
	for _, u := range users {
		a := h.AggAccess(u.ID)
		if a == nil || a.DataSets[parts[0]] == nil {
			continue
		}
		label := a.DataSets[parts[0]].Name + ", " + parts[1]
		if len(parts) >= 6 {
			if e := a.Elements[parts[4]]; e != nil {
				label += ", " + e.Name + " " + a.COCName(parts[5])
			}
		}
		return label
	}
	return cell
}
