package store

import (
	"path/filepath"
	"testing"

	"github.com/dhis2/facility-hub/internal/hubcrypto"
)

// R-09: a set-up that stops after creating the data file but before writing a key
// slot leaves a file encrypted with a key that was only in memory. After a restart a
// new set-up must still be able to create its data file.
func TestSetDKReplacesDataFileNoSlotOpens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hub.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	k1, _ := hubcrypto.NewKey()
	if err := s.SetDK(k1); err != nil {
		t.Fatal(err)
	}
	s.Close() // set-up failed, then the hub restarted

	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	k2, _ := hubcrypto.NewKey()
	if err := s.SetDK(k2); err != nil {
		t.Fatalf("set-up after an interrupted one: %v", err)
	}
	if _, err := s.UpsertCredentials("nurse_a", "uid", []byte("salt"), []byte("slot")); err != nil {
		t.Fatal(err)
	}
	s.Close()

	// With a key slot, a key that does not open the file never removes it.
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	k3, _ := hubcrypto.NewKey()
	if err := s.SetDK(k3); err == nil {
		t.Fatal("a wrong key opened the data file")
	}
	if err := s.SetDK(k2); err != nil {
		t.Fatalf("the data file is gone after a wrong key: %v", err)
	}
}
