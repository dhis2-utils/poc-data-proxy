package sync

import (
	"context"

	"github.com/dhis2/facility-hub/internal/central"
)

// checkMetadata runs the hub's metadata checks (core/metadata.go) when local reads
// are on: the national refresh at every cycle, and, with the hourly access refresh
// (hourly), what changed on central, asked as one member of each access group.
func (w *Worker) checkMetadata(ctx context.Context, hourly bool) error {
	h := w.H
	if len(h.LocalReads().Programs) == 0 {
		return nil
	}
	if err := h.CheckMetadataReload(ctx); err != nil {
		return err
	}
	if !hourly {
		return nil
	}
	users, err := h.Store.Users()
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	var members []central.Auth
	for _, u := range users {
		acc := h.Access(u.ID)
		if u.CannotPush() || !acc.InFacility(h.FacilityPath()) || seen[acc.GroupKey()] {
			continue
		}
		auth, err := h.UpstreamAuth(u.ID)
		if err != nil {
			continue
		}
		seen[acc.GroupKey()] = true
		members = append(members, auth)
	}
	return h.CheckMetadata(ctx, members)
}
