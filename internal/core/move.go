package core

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/dhis2/facility-hub/internal/central"
)

// Central moves to a new address (docs/DEPLOYMENT.md, section 6). A hub must never be
// stranded by it, nor follow a server that is not the same DHIS2. Two ways, each
// vouched for by the current central:
//   - announced: facility-hub-admin/central-move {"url": ..., "from": RFC 3339}, read
//     from the current central with the hub's token. The namespace must be writable by
//     administrators only: hub owners write facility-hub-config and the others;
//   - redirect: the current address, over verified TLS, answers with a permanent
//     redirect to another host.
//
// The hub switches when the current address has not answered as central for
// central_move_after (an hour by default: a brief outage during a planned move must not
// move it early), the announcement's time has come, and the candidate holds this
// hub's registry entry. A hub whose hub.yaml pins the address never moves; one limited
// to central_hosts moves only within them. Only then does the hub talk to it as
// central; the check itself sends the hub's token, which the current central vouched
// for by pointing to the new address.

// MetaCentralNext is an announced new address, waiting for the old one to stop, and
// MetaCentralNextFrom when it may be used.
const (
	MetaCentralNext     = "central_next"
	MetaCentralNextFrom = "central_next_from"
)

// refreshCentralMove reads the announcement, if any, from the current central.
func (h *Hub) refreshCentralMove(ctx context.Context) {
	var m struct {
		URL  string `json:"url"`
		From string `json:"from"`
	}
	err := h.Central.JSON(ctx, "GET", "/api/dataStore/facility-hub-admin/central-move", nil, h.HubAuth(), &m)
	if central.IsNetwork(err) {
		return
	}
	next := strings.TrimRight(strings.TrimSpace(m.URL), "/")
	if err != nil || next == h.Central.BaseURL() || !h.movable(next) {
		h.Store.SetMeta(MetaCentralNext, "")
		h.Store.SetMeta(MetaCentralNextFrom, "")
		return
	}
	h.Store.SetMeta(MetaCentralNext, next)
	h.Store.SetMeta(MetaCentralNextFrom, m.From)
}

// movable: a new address must be https (http only when the current one is, in
// development and tests), and within central_hosts when the hub is limited to them.
func (h *Hub) movable(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil {
		return false
	}
	if len(h.Cfg.CentralHosts) > 0 && !h.CentralAllowed(raw) {
		return false
	}
	cur, _ := url.Parse(h.Central.BaseURL())
	return u.Scheme == "https" || (cur != nil && cur.Scheme == "http" && u.Scheme == "http")
}

// downLongEnough reports whether central has not answered for central_move_after.
func (h *Hub) downLongEnough() bool {
	if h.Cfg.CentralMoveAfter <= 0 {
		return true
	}
	since, err := time.Parse(time.RFC3339Nano, h.Store.Meta(offlineSince))
	return err == nil && time.Since(since) >= h.Cfg.CentralMoveAfter
}

// TryCentralMove, while central does not answer: checks the candidates (an announced
// address, a permanent redirect seen by the probes) and switches to the first that
// holds this hub's registry entry. It reports whether it switched.
func (h *Hub) TryCentralMove(ctx context.Context) bool {
	if h.Mode() != ModeActive || h.Runtime() == nil || h.SetupMode() == SetupPinned || !h.downLongEnough() {
		return false
	}
	announced := h.Store.Meta(MetaCentralNext)
	if from, err := time.Parse(time.RFC3339, h.Store.Meta(MetaCentralNextFrom)); err == nil && time.Now().Before(from) {
		announced = "" // not yet
	}
	for _, cand := range []string{h.Monitor.MovedTo(), announced} {
		cand = strings.TrimRight(cand, "/")
		if cand == "" || cand == h.Central.BaseURL() || !h.movable(cand) || !h.sameCentral(ctx, cand) {
			continue
		}
		old := h.Central.BaseURL()
		h.Store.SetMeta(MetaCentralURL, cand)
		h.Store.SetMeta(MetaCentralNext, "")
		h.Store.SetMeta(MetaCentralNextFrom, "")
		h.Central.SetBaseURL(cand)
		h.Monitor.Probe(ctx, cand)
		slog.Warn("central moved: the hub now uses the new address", "from", old, "to", cand)
		return true
	}
	return false
}

// sameCentral reports whether the server at base holds this hub's registry entry.
func (h *Hub) sameCentral(ctx context.Context, base string) bool {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", base+"/api/dataStore/facility-hub-registry/"+url.PathEscape(h.HubID()), nil)
	if err != nil {
		return false
	}
	req.Header.Set("Accept", "application/json")
	h.HubAuth().Apply(req.Header)
	resp, err := h.Central.HTTP.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var reg struct {
		HubID string `json:"hubId"`
	}
	return resp.StatusCode == http.StatusOK && json.NewDecoder(resp.Body).Decode(&reg) == nil && reg.HubID == h.HubID()
}
