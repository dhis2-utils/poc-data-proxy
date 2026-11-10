package store

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/dhis2/facility-hub/internal/hubcrypto"
)

// Issues and reserved values are written through the store's write lock (review
// R-46): a write outside it waits in SQLite's busy handler, which gives up on a busy
// Pi. Each write below waits while the lock is held.
func TestWritesTakeTheWriteLock(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	k, _ := hubcrypto.NewKey()
	if err := s.SetDK(k); err != nil {
		t.Fatal(err)
	}
	rv := []ReservedValue{{Attribute: "a", Value: json.RawMessage(`{"value":"1"}`)}}
	for name, write := range map[string]func() error{
		"issue":   func() error { return s.AddIssue(1, "uid", KindEvent, 1, []byte(`{}`), "") },
		"add rv":  func() error { return s.AddReservedValues("a", rv) },
		"take rv": func() error { _, err := s.TakeReservedValues("a", 1, "2026-01-01"); return err },
	} {
		s.writeMu.Lock()
		done := make(chan error, 1)
		go func() { done <- write() }()
		select {
		case err := <-done:
			t.Errorf("%s: written while the write lock was held (%v)", name, err)
			s.writeMu.Unlock()
		case <-time.After(100 * time.Millisecond):
			s.writeMu.Unlock()
			if err := <-done; err != nil {
				t.Errorf("%s: %v", name, err)
			}
		}
	}
}
