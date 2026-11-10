package stick

import (
	"os"
	"path/filepath"
	"testing"
)

// A Finish cut short by a power cut is run again at the next start. Once the staged key
// file is in place, the store is the restored one: the second Finish keeps it and
// completes the restore (review R-37).
func TestFinishAgainKeepsTheRestoredStore(t *testing.T) {
	dataDir, staging := t.TempDir(), t.TempDir()
	storePath := filepath.Join(dataDir, "hub.db")
	write := func(p, s string) {
		t.Helper()
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(storePath, "new disk")
	write(filepath.Join(staging, "hub.db"), "restored")
	write(filepath.Join(dataDir, restoreReady), staging)
	if err := Finish(dataDir, storePath); err != nil {
		t.Fatal(err)
	}
	// The cut: the restore is in place, but its marker was not yet removed.
	write(filepath.Join(dataDir, restoreReady), staging)
	if err := Finish(dataDir, storePath); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(storePath); err != nil || string(b) != "restored" {
		t.Fatalf("store after Finish ran again: %q %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, restoreReady)); !os.IsNotExist(err) {
		t.Fatalf("restore marker left: %v", err)
	}
	if !RestorePending(dataDir) {
		t.Fatal("the restored state is not put back at the next unlock")
	}
}
