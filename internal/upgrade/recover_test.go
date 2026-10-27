package upgrade

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// A store's files, by role: the key file, its data file and their write-ahead logs.
var storeFiles = []string{"", "-wal", ".data", ".data-wal"}

func writeStore(t *testing.T, path, label string) {
	for _, f := range storeFiles {
		if err := os.WriteFile(path+f, []byte(label+f), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// storeIs fails unless path holds the store labelled label, every file with its own log.
func storeIs(t *testing.T, path, label, at string) {
	t.Helper()
	for _, f := range storeFiles {
		if b, err := os.ReadFile(path + f); err != nil || string(b) != label+f {
			t.Errorf("%s: %s%s holds %q (%v), want %q", at, filepath.Base(path), f, b, err, label+f)
		}
	}
}

// cut runs the first n renames of moves: the files as a power cut leaves them.
func cut(t *testing.T, moves [][2]string, n int) {
	for _, m := range moves[:n] {
		if err := moveFile(m[0], m[1]); err != nil {
			t.Fatal(err)
		}
	}
}

type crashHub struct{ dir, live, next, prev, exe, prevBin string }

func newCrashHub(t *testing.T, state string) crashHub {
	dir := t.TempDir()
	h := crashHub{dir: dir, live: filepath.Join(dir, "hub.db"), exe: filepath.Join(dir, "hub"),
		prevBin: filepath.Join(dir, "bin", "hub.prev")}
	h.next, h.prev = h.live+".next", h.live+".prev"
	writeStore(t, h.live, "old")
	writeStore(t, h.next, "new")
	os.MkdirAll(filepath.Dir(h.prevBin), 0o755)
	os.WriteFile(h.exe, []byte("hub 0.2.0"), 0o755)
	os.WriteFile(h.prevBin, []byte("hub 0.1.0"), 0o755)
	b, _ := json.Marshal(Marker{State: state, From: "0.1.0", To: "0.2.0", ExePath: h.exe, PrevBinary: h.prevBin, PrevStore: h.prev})
	os.WriteFile(filepath.Join(dir, "upgrade.json"), b, 0o600)
	return h
}

// Findings 1 and 2 (critical review 2026-10-27): an upgrade cut short anywhere in its
// store swap, or between the swap and the new binary, restarts on the old binary. The
// old store, with its data, must be in place before it is opened: not missing (the hub
// would start unconfigured) and not the migrated one (the old binary refuses it).
func TestRecoverSwapCutShort(t *testing.T) {
	for n := 0; n <= 4; n++ {
		h := newCrashHub(t, "swapped")
		cut(t, storeMoves(h.live, h.next, h.prev), n)
		at := fmt.Sprintf("swap cut after %d renames", n)
		if err := Recover(h.dir, h.live, "0.1.0"); err != nil {
			t.Fatalf("%s: %v", at, err)
		}
		storeIs(t, h.live, "old", at)
		storeIs(t, h.next, "new", at)
	}
}

// After a complete swap the new binary runs on the new store: nothing to put back.
func TestRecoverSwapComplete(t *testing.T) {
	h := newCrashHub(t, "swapped")
	if err := swapStore(h.live, h.next, h.prev); err != nil {
		t.Fatal(err)
	}
	if err := Recover(h.dir, h.live, "0.2.0"); err != nil {
		t.Fatal(err)
	}
	storeIs(t, h.live, "new", "swapped")
	storeIs(t, h.prev, "old", "swapped")
}

// A rollback cut short: the old store is put back, and when the new binary still runs,
// the old binary too, and the hub exits to be started on it.
func TestRecoverRollbackCutShort(t *testing.T) {
	for _, running := range []string{"0.1.0", "0.2.0"} {
		for n := 0; n <= 4; n++ {
			h := newCrashHub(t, "rolledback")
			if err := swapStore(h.live, h.next, h.prev); err != nil {
				t.Fatal(err)
			}
			cut(t, storeMoves(h.live, h.prev, h.next), n)
			at := fmt.Sprintf("%s running, rollback cut after %d renames", running, n)
			err := Recover(h.dir, h.live, running)
			if (err != nil) != (running == "0.2.0") {
				t.Fatalf("%s: %v", at, err)
			}
			storeIs(t, h.live, "old", at)
			storeIs(t, h.next, "new", at)
			if b, _ := os.ReadFile(h.exe); string(b) != "hub 0.1.0" && running == "0.2.0" {
				t.Errorf("%s: the binary is %q", at, b)
			}
		}
	}
}
