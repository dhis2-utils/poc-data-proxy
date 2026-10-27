package core

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

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

// A login that comes while another unlocks the store waits for the index: the store
// takes its key before the index is loaded, and a save into an index not yet loaded
// reuses a slot (docs/ADVERSARIAL-TESTING.md, E1).
func TestUnlockedWaitsForAnUnlockInProgress(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	dk, _ := hubcrypto.NewKey()
	h := New(config.Default(), "test", st)
	release := make(chan struct{})
	reached := make(chan struct{})
	h.AfterUnlock(func() error {
		close(reached)
		<-release
		return nil
	})
	done := make(chan error)
	go func() { done <- h.Unlock(dk) }()
	<-reached
	answered := make(chan bool)
	go func() { answered <- h.Unlocked() }()
	select {
	case <-answered:
		t.Fatal("Unlocked answered while the unlock was still in progress")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !<-answered {
		t.Fatal("not unlocked after the unlock")
	}
}
