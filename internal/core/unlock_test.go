package core

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/dhis2/facility-hub/internal/config"
	"github.com/dhis2/facility-hub/internal/hubcrypto"
	"github.com/dhis2/facility-hub/internal/store"
)

// An unlock that fails leaves the store locked, whatever step failed (security review
// 2026-10-20, S14): a half-unlocked hub would serve cached answers with no runtime.
func TestUnlockFailureLeavesStoreLocked(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	dk, _ := hubcrypto.NewKey()
	if err := st.SetDK(dk); err != nil {
		t.Fatal(err)
	}
	if err := st.SetState("runtime", []byte("{not json")); err != nil {
		t.Fatal(err)
	}
	st.SetDK(nil)
	h := New(config.Default(), "test", st)
	if err := h.Unlock(dk); err == nil {
		t.Fatal("unlock with a corrupt runtime succeeded")
	}
	if _, err := st.DK(); !errors.Is(err, store.ErrLocked) {
		t.Fatalf("store not locked after a failed unlock: %v", err)
	}
}
