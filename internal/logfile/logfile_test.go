package logfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A hub on Windows has no console: it logs to a file, capped, one old file kept.
func TestCapped(t *testing.T) {
	p := filepath.Join(t.TempDir(), "hub.log")
	w, err := Open(p, 1000)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.Repeat("x", 99) + "\n"
	for i := 0; i < 25; i++ {
		w.Write([]byte(line))
	}
	w.Close()
	cur, _ := os.Stat(p)
	old, err := os.Stat(p + ".1")
	if err != nil || cur.Size() > 1000 || old.Size() > 1000 {
		t.Fatalf("current %v, old %v (%v)", cur.Size(), old, err)
	}
	if _, err := os.Stat(p + ".2"); err == nil {
		t.Fatal("more than one old file kept")
	}
	w2, _ := Open(p, 1000) // a restart appends
	w2.Write([]byte("after restart\n"))
	w2.Close()
	b, _ := os.ReadFile(p)
	if !strings.HasSuffix(string(b), "after restart\n") || len(b) <= len("after restart\n") {
		t.Fatal("a restart did not append")
	}
}
