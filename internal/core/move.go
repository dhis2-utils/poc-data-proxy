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
//   - announced: facility-hub-config/central-move {"url": ...}, read from the current
//     central with the hub's token;
//   - redirect: the current address, over verified TLS, answers with a permanent
//     redirect to another host.
//
// The hub switches when the current address no longer answers as central and the
// candidate holds this hub's registry entry. Only then does the hub talk to it as
// central; the check itself sends the hub's token, which the current central vouched
// for by pointing to the new address.

// MetaCentralNext is an announced new address, waiting for the old one to stop.
const MetaCentralNext = "central_next"

// refreshCentralMove reads the announcement, if any, from the current central.
func (h *Hub) refreshCentralMove(ctx context.Context) {
	var m struct {
		URL string `json:"url"`
	}
	err := h.Central.JSON(ctx, "GET", "/api/dataStore/facility-hub-config/central-move", nil, h.HubAuth(), &m)
	switch {
	case central.Status(err) == http.StatusNotFound:
		h.Store.SetMeta(MetaCentralNext, "")
	case err == nil:
		next := strings.TrimRight(strings.TrimSpace(m.URL), "/")
		if next != h.Central.BaseURL() && h.movable(next) {
			h.Store.SetMeta(MetaCentralNext, next)
		}
	}
}

// movable: a new address must be https (http only when the current one is, in
// development and tests).
func (h *Hub) movable(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil {
		return false
	}
	cur, _ := url.Parse(h.Central.BaseURL())
	return u.Scheme == "https" || (cur != nil && cur.Scheme == "http" && u.Scheme == "http")
}

// TryCentralMove, while central does not answer: checks the candidates (an announced
// address, a permanent redirect seen by the probes) and switches to the first that
// holds this hub's registry entry. It reports whether it switched.
func (h *Hub) TryCentralMove(ctx context.Context) bool {
	if h.Mode() != ModeActive || h.Runtime() == nil {
		return false
	}
	for _, cand := range []string{h.Monitor.MovedTo(), h.Store.Meta(MetaCentralNext)} {
		cand = strings.TrimRight(cand, "/")
		if cand == "" || cand == h.Central.BaseURL() || !h.movable(cand) || !h.sameCentral(ctx, cand) {
			continue
		}
		old := h.Central.BaseURL()
		h.Store.SetMeta(MetaCentralURL, cand)
		h.Store.SetMeta(MetaCentralNext, "")
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
