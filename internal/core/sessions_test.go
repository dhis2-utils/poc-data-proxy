package core

import (
	"testing"
	"time"

	"github.com/dhis2/facility-hub/internal/central"
)

// Expired sessions leave the maps at the next login, not only when looked up (review R-18).
func TestNewSweepsExpiredSessions(t *testing.T) {
	s := NewSessions()
	old := s.New(1, "a", central.Auth{})
	s.SetBasic("a", "pw", "10.0.0.2", old)
	old.lastSeen = time.Now().Add(-2 * s.Idle)
	live := s.New(2, "b", central.Auth{})
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.byTok) != 1 || s.byTok[live.Token] == nil || len(s.basic) != 0 {
		t.Fatalf("sessions %d, basic %d", len(s.byTok), len(s.basic))
	}
}
