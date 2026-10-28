package e2e

import (
	"testing"
	"time"

	"github.com/dhis2/facility-hub/internal/store"
)

// The sync cycle's housekeeping deletes outbox entries central accepted more than
// store.SentOutboxKept ago; a refused entry stays, however old.
func TestSentOutboxPruned(t *testing.T) {
	e := newEnv(t)
	e.setup()
	a := e.web("nurse_a")
	a.login()
	e.sync()
	e.offline()
	a.json("POST", postSync, newPatient("PruneTe0001", "PruneEn0001", "Prune", "NAT-7001", nil), 200)
	e.online()
	e.sync()
	db := e.app.Hub.Store.DB()
	var sent, other int
	db.QueryRow(`SELECT count(*) FROM outbox WHERE state = ?`, store.OutboxSynced).Scan(&sent)
	if sent != 1 {
		t.Fatalf("sent entries: %d", sent)
	}
	// Refused entries stay whatever their age: make one by hand.
	db.Exec(`INSERT INTO outbox(user_id, payload_enc, created_at, state) VALUES(1, x'', '2020-01-01T00:00:00Z', ?)`, store.OutboxRejected)
	e.sync()
	db.QueryRow(`SELECT count(*) FROM outbox WHERE state = ?`, store.OutboxSynced).Scan(&sent)
	if sent != 1 {
		t.Fatalf("a recently sent entry was pruned: %d left", sent)
	}
	old := time.Now().Add(-store.SentOutboxKept - time.Hour).UTC().Format(time.RFC3339Nano)
	db.Exec(`UPDATE outbox SET sent_at = ? WHERE state = ?`, old, store.OutboxSynced)
	e.sync()
	db.QueryRow(`SELECT count(*) FROM outbox WHERE state = ?`, store.OutboxSynced).Scan(&sent)
	db.QueryRow(`SELECT count(*) FROM outbox WHERE state = ?`, store.OutboxRejected).Scan(&other)
	if sent != 0 || other != 1 {
		t.Fatalf("after %s: sent %d, refused %d; want 0 and 1", store.SentOutboxKept, sent, other)
	}
}
