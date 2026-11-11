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
// grow for ever. Refused and waiting saves stay until resolved, however old; discarded
// ones stay as long as sent ones, from the discard.
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
	oldDiscarded, newDiscarded := add("EvOldDisc01"), add("EvNewDisc01")
	for _, seq := range []int64{oldSent, newSent, lateSent} {
		if err := s.FinishOutbox(seq, OutboxSynced, ""); err != nil {
			t.Fatal(err)
		}
	}
	s.FinishOutbox(refused, OutboxRejected, "refused")
	s.FinishOutbox(partial, OutboxPartial, "partly refused")
	for _, seq := range []int64{oldDiscarded, newDiscarded} {
		s.FinishOutbox(seq, OutboxRejected, "refused")
	}
	// Discarded by a hub manager: kept as long after the discard as a sent entry.
	if n, err := s.DiscardOutbox([]string{"EvOldDisc01", "EvNewDisc01"}, nil); err != nil || n != 2 {
		t.Fatalf("discarded %d, %v; want 2", n, err)
	}
	age(oldDiscarded, "sent_at", long)
	for _, seq := range []int64{oldSent, newSent, lateSent, refused, partial, pending, oldDiscarded, newDiscarded} {
		age(seq, "created_at", long)
	}
	age(oldSent, "sent_at", long)
	age(newSent, "sent_at", recent)
	// lateSent: made long ago, sent just now (it waited): kept from when it was sent.

	n, err := s.PruneSentOutbox()
	if err != nil || n != 2 {
		t.Fatalf("pruned %d, %v; want 2", n, err)
	}
	for seq, want := range map[int64]string{oldSent: "", newSent: OutboxSynced, lateSent: OutboxSynced,
		refused: OutboxRejected, partial: OutboxPartial, pending: OutboxPending, oldDiscarded: "", newDiscarded: OutboxDiscarded} {
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

// An entry that waits for another pending entry (its parent, whose author must log in
// again, say) is not one the push can send now (review R-21): its author is not
// listed for it.
func TestPendingAuthorsSkipsWaitingChildren(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	k, _ := hubcrypto.NewKey()
	if err := s.SetDK(k); err != nil {
		t.Fatal(err)
	}
	add := func(user, dep int64, uid string) int64 {
		t.Helper()
		var seq int64
		err := s.Tx(context.Background(), func(tx *sql.Tx) error {
			var err error
			seq, err = s.AppendOutbox(tx, user, []byte(`{}`), "", []OutboxRef{{UID: uid, Kind: KindEvent}}, dep)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return seq
	}
	parent := add(1, 0, "ParentEv001")
	add(2, parent, "ChildEv0001")
	if got := s.PendingAuthors(); len(got) != 1 || got[0] != 1 {
		t.Fatalf("authors %v, want only the parent's", got)
	}
	if err := s.FinishOutbox(parent, OutboxSynced, ""); err != nil {
		t.Fatal(err)
	}
	if got := s.PendingAuthors(); len(got) != 1 || got[0] != 2 {
		t.Fatalf("authors %v after the parent was sent, want the child's", got)
	}
}

// An object created on this hub counts as never on central until central accepts a
// save of it (review R-47); the stick's copy of the outbox keeps the mark.
func TestNeverOnCentral(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	k, _ := hubcrypto.NewKey()
	if err := s.SetDK(k); err != nil {
		t.Fatal(err)
	}
	err = s.Tx(context.Background(), func(tx *sql.Tx) error {
		_, err := s.AppendOutbox(tx, 1, []byte(`{}`), "", []OutboxRef{{UID: "EvNew000001", Kind: KindEvent, Created: true},
			{UID: "EvOld000001", Kind: KindEvent}}, 0)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if !s.NeverOnCentral("EvNew000001") || s.NeverOnCentral("EvOld000001") {
		t.Fatal("created mark not recorded")
	}
	raw, err := s.PendingRaw()
	if err != nil || len(raw) != 1 {
		t.Fatal(raw, err)
	}
	for _, o := range raw[0].Objects {
		if o.Created != (o.UID == "EvNew000001") {
			t.Errorf("stick copy: %+v", o)
		}
	}
	if err := s.AcceptedByCentral("EvNew000001"); err != nil {
		t.Fatal(err)
	}
	if s.NeverOnCentral("EvNew000001") {
		t.Error("still never on central after central accepted it")
	}
}
