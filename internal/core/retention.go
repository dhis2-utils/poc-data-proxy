package core

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/dhis2/facility-hub/internal/access"
	"github.com/dhis2/facility-hub/internal/central"
)

const retentionKey = "retention"

// Retention is the retention table read from central at the last catalog refresh.
func (h *Hub) Retention() access.Retention {
	var r access.Retention
	if b, err := h.Store.State(retentionKey); err == nil && b != nil {
		json.Unmarshal(b, &r)
	}
	return r
}

// refreshRetention reads facility-hub-config/retention; a missing key keeps everything.
func (h *Hub) refreshRetention(ctx context.Context) error {
	var r access.Retention
	err := h.Central.JSON(ctx, "GET", "/api/dataStore/facility-hub-config/retention", nil, h.HubAuth(), &r)
	if central.Status(err) == http.StatusNotFound {
		r, err = access.Retention{}, nil
	}
	if err != nil {
		return err
	}
	b, _ := json.Marshal(r)
	return h.Store.SetState(retentionKey, b)
}
