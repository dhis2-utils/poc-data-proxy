package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/dhis2/facility-hub/internal/hubcrypto"
)

// Saves central accepted leave the outbox SentOutboxKept after they were sent: the hub
// keeps no copy of patient data past what the country chose, and the outbox does not
// grow for ever. Refused and waiting saves stay until resolved, however old.
func TestPruneSentOutbox(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	k, _ := hubcrypto.NewKey()
	if err := s.SetDK(k); err != nil {
		t.Fatal(err)
	}
	add := func(uid string) int64 {
		t.Helper()
		var seq int64
		err := s.Tx(context.Background(), func(tx *sql.Tx) error {
			var err error
			seq, err = s.AppendOutbox(tx, 1, []byte(`{}`), "", []OutboxRef{{UID: uid, Kind: KindEvent}}, 0)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return seq
	}
	long := time.Now().Add(-SentOutboxKept - time.Hour).UTC().Format(time.RFC3339Nano)
	recent := time.Now().Add(-SentOutboxKept + time.Hour).UTC().Format(time.RFC3339Nano)
	age := func(seq int64, col, at string) {
		t.Helper()
		if _, err := s.DB().Exec(`UPDATE outbox SET `+col+`=? WHERE seq=?`, at, seq); err != nil {
			t.Fatal(err)
		}
	}

	oldSent, newSent, lateSent := add("EvOldSent01"), add("EvNewSent01"), add("EvLateSent1")
	refused, partial, pending := add("EvRefused01"), add("EvPartial01"), add("EvPending01")
	for _, seq := range []int64{oldSent, newSent, lateSent} {
		if err := s.FinishOutbox(seq, OutboxSynced, ""); err != nil {
			t.Fatal(err)
		}
	}
	s.FinishOutbox(refused, OutboxRejected, "refused")
	s.FinishOutbox(partial, OutboxPartial, "partly refused")
	for _, seq := range []int64{oldSent, newSent, lateSent, refused, partial, pending} {
		age(seq, "created_at", long)
	}
	age(oldSent, "sent_at", long)
	age(newSent, "sent_at", recent)
	// lateSent: made long ago, sent just now (it waited): kept from when it was sent.

	n, err := s.PruneSentOutbox()
	if err != nil || n != 1 {
		t.Fatalf("pruned %d, %v; want 1", n, err)
	}
	for seq, want := range map[int64]string{oldSent: "", newSent: OutboxSynced, lateSent: OutboxSynced,
		refused: OutboxRejected, partial: OutboxPartial, pending: OutboxPending} {
		if got := s.OutboxState(seq); got != want {
			t.Errorf("entry %d: %q, want %q", seq, got, want)
		}
	}
	var refs int
	s.DB().QueryRow(`SELECT count(*) FROM outbox_objects WHERE seq=?`, oldSent).Scan(&refs)
	if refs != 0 {
		t.Errorf("objects of a pruned entry left: %d", refs)
	}

	// Sent before this release (no time of sending): its creation stands in.
	legacy := add("EvLegacy001")
	s.FinishOutbox(legacy, OutboxSynced, "")
	age(legacy, "created_at", long)
	age(legacy, "sent_at", "")
	if n, err := s.PruneSentOutbox(); err != nil || n != 1 || s.OutboxState(legacy) != "" {
		t.Fatalf("legacy entry: pruned %d, %v, state %q", n, err, s.OutboxState(legacy))
	}
}
