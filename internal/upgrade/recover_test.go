package upgrade

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

// G2 (adversarial, 28 October): a release that exits at every start never reached its
// health check, and Recover, which runs in it, left it alone. Each start of a swapped-in
// release that is not confirmed is counted; after maxStarts the next start puts the old
// store and binary back and exits, and the old release reports the failure.
func TestReleaseThatCannotStartRollsBack(t *testing.T) {
	h := newCrashHub(t, "swapped")
	if err := swapStore(h.live, h.next, h.prev); err != nil {
		t.Fatal(err)
	}
	if err := CountStart(h.dir, h.live, "0.1.0"); err != nil { // not the swapped-in version
		t.Fatal(err)
	}
	for i := 1; i <= maxStarts; i++ {
		if err := CountStart(h.dir, h.live, "0.2.0"); err != nil {
			t.Fatalf("start %d: %v", i, err)
		}
		storeIs(t, h.live, "new", fmt.Sprintf("start %d", i))
	}
	if err := CountStart(h.dir, h.live, "0.2.0"); err == nil {
		t.Fatalf("start %d: the release that cannot start is kept", maxStarts+1)
	}
	storeIs(t, h.live, "old", "rolled back")
	if b, _ := os.ReadFile(h.exe); string(b) != "hub 0.1.0" {
		t.Errorf("the binary is %q", b)
	}
	var mk Marker
	b, _ := os.ReadFile(filepath.Join(h.dir, "upgrade.json"))
	if json.Unmarshal(b, &mk); mk.State != "rolledback" || mk.Reason == "" {
		t.Errorf("marker %s", b)
	}
	// The old binary starts on the old store; AfterBoot reports the failure and skips it.
	if err := Recover(h.dir, h.live, "0.1.0"); err != nil {
		t.Fatal(err)
	}
	storeIs(t, h.live, "old", "old release")
}

// Windows refuses to rename a file someone still holds open (the store's -wal at the
// swap, VM, 9 November): the swap waits for it, and gives up after renameWait.
func TestSwapWaitsForHeldFile(t *testing.T) {
	defer func(f func(string, string) error, w time.Duration) { rename, renameWait = f, w }(rename, renameWait)
	held := errors.New("The process cannot access the file because it is being used by another process.")
	fails := 0
	rename = func(from, to string) error {
		if strings.HasSuffix(from, ".data") && fails < 3 { // the data file, held for a while
			fails++
			return &os.LinkError{Op: "rename", Old: from, New: to, Err: held}
		}
		return os.Rename(from, to)
	}
	renameWait = 10 * time.Second
	h := newCrashHub(t, "swapped")
	if err := swapStore(h.live, h.next, h.prev); err != nil {
		t.Fatalf("a file held for 1.5 s: %v", err)
	}
	storeIs(t, h.live, "new", "after the wait")
	storeIs(t, h.prev, "old", "after the wait")

	rename = func(from, to string) error { return &os.LinkError{Op: "rename", Old: from, New: to, Err: held} }
	renameWait = time.Second
	h = newCrashHub(t, "swapped")
	start := time.Now()
	if err := swapStore(h.live, h.next, h.prev); err == nil || time.Since(start) < time.Second {
		t.Fatalf("a file held for good: %v after %v, want an error after renameWait", err, time.Since(start))
	}
	if err := func() error { rename = os.Rename; return Recover(h.dir, h.live, "0.1.0") }(); err != nil {
		t.Fatal(err)
	}
	storeIs(t, h.live, "old", "after giving up")
}
