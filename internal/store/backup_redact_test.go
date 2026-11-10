package store

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/dhis2/facility-hub/internal/hubcrypto"
)

// A backup that fails does not quote the data key: SQLite's error can carry the
// VACUUM INTO URI, and the upgrade logs it (review R-24).
func TestBackupErrorHidesKey(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	k, _ := hubcrypto.NewKey()
	if err := s.SetDK(k); err != nil {
		t.Fatal(err)
	}
	key, _ := fileKey(k)
	err = s.BackupTo(filepath.Join(t.TempDir(), "missing", "keys.db"))
	if err == nil {
		t.Fatal("backup into a missing folder succeeded")
	}
	t.Log(err)
	if strings.Contains(err.Error(), key) {
		t.Errorf("error quotes the key: %v", err)
	}
}
