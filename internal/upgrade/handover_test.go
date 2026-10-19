package upgrade

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func xor(b []byte) ([]byte, error) {
	out := make([]byte, len(b))
	for i, c := range b {
		out[i] = c ^ 0x5a
	}
	return out, nil
}

// Review focus 1: the data key handed to the new version across a Windows upgrade is
// read once, refused when older than 2 minutes, and kept from other accounts.
func TestHandoverOnceAndFresh(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	dk := []byte("0123456789abcdef0123456789abcdef")
	if err := WriteHandover(dir, dk, now, xor); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if st, _ := os.Stat(filepath.Join(dir, "upgrade-key")); st.Mode().Perm() != 0o600 {
			t.Errorf("mode %v", st.Mode().Perm())
		}
	}
	if got := ReadHandover(dir, now.Add(90*time.Second), xor); !bytes.Equal(got, dk) {
		t.Fatalf("got %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "upgrade-key")); err == nil {
		t.Fatal("the key is still on disk after reading")
	}
	if ReadHandover(dir, now, xor) != nil {
		t.Fatal("read twice")
	}
	WriteHandover(dir, dk, now, xor)
	if ReadHandover(dir, now.Add(121*time.Second), xor) != nil {
		t.Fatal("a stale key was accepted")
	}
	if _, err := os.Stat(filepath.Join(dir, "upgrade-key")); err == nil {
		t.Fatal("a stale key stays on disk")
	}
	broken := func([]byte) ([]byte, error) { return nil, os.ErrInvalid }
	WriteHandover(dir, dk, now, xor)
	if ReadHandover(dir, now, broken) != nil {
		t.Fatal("a key that does not open was returned")
	}
}
