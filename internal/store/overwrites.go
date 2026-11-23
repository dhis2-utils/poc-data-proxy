package store

import (
	"context"
	"database/sql"
	"encoding/json"
)

// The overwrite count (PENDING.md, item 3; docs/DEPLOYMENT.md, "Offline edits and
// newer changes on central"): telemetry only. For each object an offline save edits
// that the hub held as central's, the outbox entry keeps central's updatedAt of that
// copy (outbox_base, migration 8); before the push the hub reads central's current
// one, and counts the saves central had changed since. Nothing is held back.
//
// To remove the count: delete this file and sync/overwrites.go, their call sites
// (tracker.Importer's Bases, core.PersistImport, sync.pushEntry, sync.Status, the
// "overwrites" key in core.RefreshCatalog), the Facility Hub app's rows, and add a
// migration that drops outbox_base.

// OutboxBase is central's version of an object an outbox entry edits.
type OutboxBase struct{ UID, Kind, UpdatedAt string }

// AddOutboxBases records, for the outbox entry seq, central's updatedAt of the objects
// it edits (bases, by UID) among refs.
func (s *Store) AddOutboxBases(tx *sql.Tx, seq int64, bases map[string]string, refs []OutboxRef) error {
	for _, r := range refs {
		at, ok := bases[r.UID]
		if !ok {
			continue
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO outbox_base(seq, uid, kind, updated_at) VALUES(?,?,?,?)`,
			seq, r.UID, r.Kind, at); err != nil {
			return err
		}
	}
	return nil
}

// TakeOutboxBases returns and forgets the bases of the outbox entry seq: each entry is
// checked once, at its first push. Rows of entries no longer in the outbox go too.
func (s *Store) TakeOutboxBases(seq int64) ([]OutboxBase, error) {
	var out []OutboxBase
	err := s.Tx(context.Background(), func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT uid, kind, updated_at FROM outbox_base WHERE seq=?`, seq)
		if err != nil {
			return err
		}
		for rows.Next() {
			var b OutboxBase
			if err := rows.Scan(&b.UID, &b.Kind, &b.UpdatedAt); err != nil {
				rows.Close()
				return err
			}
			out = append(out, b)
		}
		rows.Close()
		if _, err := tx.Exec(`DELETE FROM outbox_base WHERE seq=? OR seq NOT IN (SELECT seq FROM outbox)`, seq); err != nil {
			return err
		}
		return nil
	})
	return out, err
}

// Overwrite is an offline save that replaced a newer change on central. No values.
type Overwrite struct {
	UID     string `json:"uid"`
	Kind    string `json:"kind"`
	Program string `json:"program,omitempty"`
	User    string `json:"user"`
	Base    string `json:"base"`    // central's updatedAt of the copy edited offline
	Central string `json:"central"` // central's updatedAt before the push
	Saved   string `json:"saved"`   // when the save was made on the hub
	Pushed  string `json:"pushed"`
}

// OverwritesKept is how many recent overwrites the hub keeps for its heartbeat.
const OverwritesKept = 20

const overwritesKey = "overwrites"

// Overwrites are the count since the hub started counting and the most recent ones.
type Overwrites struct {
	Count  int         `json:"count"`
	Recent []Overwrite `json:"recent"`
}

// AddOverwrite counts o.
func (s *Store) AddOverwrite(o Overwrite) error {
	all := s.Overwrites()
	all.Count++
	all.Recent = append(all.Recent, o)
	if n := len(all.Recent); n > OverwritesKept {
		all.Recent = all.Recent[n-OverwritesKept:]
	}
	b, _ := json.Marshal(all)
	return s.SetPrivate(overwritesKey, string(b))
}

// Overwrites returns the count and the recent ones.
func (s *Store) Overwrites() Overwrites {
	var all Overwrites
	json.Unmarshal([]byte(s.Private(overwritesKey)), &all)
	if all.Recent == nil {
		all.Recent = []Overwrite{}
	}
	return all
}
