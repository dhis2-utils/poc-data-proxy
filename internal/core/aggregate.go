package core

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/dhis2/facility-hub/internal/aggregate"
	"github.com/dhis2/facility-hub/internal/central"
	"github.com/dhis2/facility-hub/internal/store"
)

func aggAccessKey(userID int64) string { return fmt.Sprintf("agg_access:%d", userID) }

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
func (h *Hub) RefreshAggAccess(ctx context.Context, userID int64, auth central.Auth, authorities []string) error {
	path := "/api/" + h.APIMinor() + "/dataEntry/metadata"
	resp, err := h.Central.Do(ctx, "GET", path, nil, auth, nil)
	if err != nil {
		return err
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	a := &aggregate.Access{}
	if resp.StatusCode == http.StatusOK {
		if a, err = aggregate.ParseMetadata(b, authorities); err != nil {
			return fmt.Errorf("dataEntry/metadata: %w", err)
		}
		hdr := resp.Header.Clone()
		hdr.Del("Set-Cookie")
		if err := h.Store.PutCache(userID, "GET", path, &store.CachedResponse{Status: 200, Header: hdr, Body: b}); err != nil {
			return err
		}
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

const aggRetentionKey = "agg_retention"

// AggRetention is the retention table read from central at the last catalog refresh,
// with the configured default.
func (h *Hub) AggRetention() aggregate.Retention {
	r := aggregate.Retention{}
	if b, err := h.Store.State(aggRetentionKey); err == nil && b != nil {
		json.Unmarshal(b, &r)
	}
	if r.Default <= 0 {
		r.Default = h.Cfg.AggregateRetention
	}
	return r
}

// refreshAggRetention reads facility-hub-config/aggregate from central's dataStore;
// a missing key keeps the configured default.
func (h *Hub) refreshAggRetention(ctx context.Context) error {
	var r aggregate.Retention
	err := h.Central.JSON(ctx, "GET", "/api/dataStore/facility-hub-config/aggregate", nil, h.HubAuth(), &r)
	if central.Status(err) == http.StatusNotFound {
		r, err = aggregate.Retention{}, nil
	}
	if err != nil {
		return err
	}
	b, _ := json.Marshal(r)
	return h.Store.SetState(aggRetentionKey, b)
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
