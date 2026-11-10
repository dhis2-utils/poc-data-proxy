package core

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/dhis2/facility-hub/internal/config"
	"github.com/dhis2/facility-hub/internal/hubcrypto"
	"github.com/dhis2/facility-hub/internal/store"
)

// StuckOutbox counts in SQL, without opening entries (review R-19): pending entries
// of authors who must log in online first, or who are gone.
func TestStuckOutbox(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	k, _ := hubcrypto.NewKey()
	if err := st.SetDK(k); err != nil {
		t.Fatal(err)
	}
	ok, _ := st.UpsertCredentials("ok", "UsrOk000001", nil, nil)
	blocked, _ := st.UpsertCredentials("blocked", "UsrBlk00001", nil, nil)
	if err := st.SetNeedsLogin(blocked, true); err != nil {
		t.Fatal(err)
	}
	const gone = 99
	for _, id := range []int64{ok, ok, blocked, blocked, blocked, gone} {
		if err := st.Tx(context.Background(), func(tx *sql.Tx) error {
			_, err := st.AppendOutbox(tx, id, []byte(`{}`), "", nil, 0)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	// One of the blocked author's entries is no longer pending.
	if _, err := st.DB().Exec(`UPDATE outbox SET state=? WHERE seq=(SELECT max(seq) FROM outbox WHERE user_id=?)`, store.OutboxSynced, blocked); err != nil {
		t.Fatal(err)
	}
	h := New(config.Default(), "test", st)
	if n := h.StuckOutbox(); n != 3 {
		t.Errorf("StuckOutbox = %d, want 3 (two of the blocked author's, one of the gone author's)", n)
	}
}
