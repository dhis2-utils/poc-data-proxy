package store

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dhis2/facility-hub/internal/hubcrypto"
)

// The benchmarks below calibrate a constrained container against a real Raspberry Pi
// (docs/PERFORMANCE.md): the same binary runs on both, and --cpus is tuned until they
// match. They exercise what dominates the hub's work: sealing, writing, reading and
// opening encrypted tracker objects.

func benchStore(b *testing.B) *Store {
	b.Helper()
	s, err := Open(filepath.Join(b.TempDir(), "hub.db"))
	if err != nil {
		b.Fatal(err)
	}
	k, err := hubcrypto.NewKey()
	if err != nil {
		b.Fatal(err)
	}
	if err := s.SetDK(k); err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { s.Close() })
	return s
}

// payload is an event of about 1 KB, as in the performance plan.
var payload = []byte(`{"event":"%s","program":"PrgAbcdefgh","programStage":"StgAbcdefgh","orgUnit":"OuAbcdefghi",` +
	`"status":"ACTIVE","occurredAt":"2024-03-01T00:00:00.000","dataValues":[` +
	strings.Repeat(`{"dataElement":"DeAbcdefghi","value":"some value 12345","storedBy":"nurse"},`, 11) +
	`{"dataElement":"DeAbcdefghi","value":"x"}]}`)

func obj(i int) *Object {
	uid := fmt.Sprintf("Ev%09d", i)
	return &Object{UID: uid, Kind: KindEvent, TE: "Te0000000001", Enrollment: "En000000001", Program: "PrgAbcdefgh",
		Stage: "StgAbcdefgh", Origin: OriginCentral, SyncState: "synced", Slot: int64(i + 1), Payload: []byte(fmt.Sprintf(string(payload), uid))}
}

func fill(b *testing.B, s *Store, n int) {
	b.Helper()
	tx, err := s.DB().Begin()
	if err != nil {
		b.Fatal(err)
	}
	for i := 0; i < n; i++ {
		if err := s.PutObject(tx, obj(i)); err != nil {
			b.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}
}

// BenchmarkPutObjects writes objects in transactions of 200, as a pull page does.
func BenchmarkPutObjects(b *testing.B) {
	s := benchStore(b)
	b.SetBytes(int64(len(payload)))
	b.ResetTimer()
	for i := 0; i < b.N; i += 200 {
		tx, _ := s.DB().Begin()
		for j := i; j < i+200 && j < b.N; j++ {
			if err := s.PutObject(tx, obj(j)); err != nil {
				b.Fatal(err)
			}
		}
		if err := tx.Commit(); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkGetObject reads single objects at random, as a dashboard does.
func BenchmarkGetObject(b *testing.B) {
	s := benchStore(b)
	fill(b, s, 10000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.GetObject(fmt.Sprintf("Ev%09d", (i*7919)%10000)); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkAllObjects reads and decrypts everything, as the first unlock after an
// upgrade does (Index.Load fills the query columns).
func BenchmarkAllObjects(b *testing.B) {
	s := benchStore(b)
	fill(b, s, 10000)
	b.SetBytes(int64(10000 * len(payload)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := s.AllObjects(func(*Object) error { return nil }); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkCommitOne writes one object per transaction, as an offline save does: the
// cost of making each commit durable.
func BenchmarkCommitOne(b *testing.B) {
	s := benchStore(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tx, _ := s.DB().Begin()
		if err := s.PutObject(tx, obj(i)); err != nil {
			b.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			b.Fatal(err)
		}
	}
}
