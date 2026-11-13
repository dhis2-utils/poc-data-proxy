package store

import (
	"bytes"
	"path/filepath"
	"testing"
	"time"
)

// The public file is read while the store is locked, and its size is capped: the
// oldest copies go first (anyone on the network can ask for login app paths).
func TestPublicCopiesLockedAndCapped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hub.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Unlocked() {
		t.Fatal("unlocked")
	}
	big := bytes.Repeat([]byte("x"), 7<<20)
	for _, u := range []string{"/login/a.js", "/login/b.js", "/login/c.js"} {
		if err := s.PutPublic(u, &CachedResponse{Status: 200, Body: big}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond)
	}
	if s.HasPublic("/login/a.js") {
		t.Error("the oldest copy was kept past the cap")
	}
	for _, u := range []string{"/login/b.js", "/login/c.js"} {
		if c, err := s.GetPublic(u); err != nil || c == nil || len(c.Body) != len(big) {
			t.Errorf("%s: %v %v", u, c == nil, err)
		}
	}
	if err := s.Wipe(); err != nil {
		t.Fatal(err)
	}
	if s.HasPublic("/login/c.js") {
		t.Error("a reset kept the public copies")
	}
}
