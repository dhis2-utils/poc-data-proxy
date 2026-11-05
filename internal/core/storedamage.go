package core

import (
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"

	"github.com/ncruces/go-sqlite3"
)

// A store SQLite calls malformed stays so: the hub cannot repair it, and staff saw only
// "not assigned to this facility" (a locked hub reads no access list) or pull errors
// while it went on serving (session B, 5 November 2026). The hub keeps the first such
// error until it restarts, and its status, page and login say what to do.

// StoreDamageAdvice is what staff do about a damaged store.
const StoreDamageAdvice = "Restore the hub from its USB stick, or stop the hub, delete its store (hub.db and hub.db.data with their -wal and -shm files) and set it up again. Changes not yet sent to central are lost without the stick."

// IsStoreDamage reports whether err says the store's file is damaged.
func IsStoreDamage(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, sqlite3.CORRUPT) || errors.Is(err, sqlite3.NOTADB) {
		return true
	}
	// Errors that crossed a string (the sync cycle joins its programmes' errors).
	s := err.Error()
	return strings.Contains(s, "database disk image is malformed") || strings.Contains(s, "file is not a database")
}

type storeDamage struct{ err atomic.Pointer[string] }

// NoteStoreErr records err if it says the store is damaged. It returns whether it did.
func (h *Hub) NoteStoreErr(err error) bool {
	if !IsStoreDamage(err) {
		return false
	}
	s := err.Error()
	if h.damage.err.CompareAndSwap(nil, &s) {
		slog.Error("the hub's store is damaged", "err", s)
	}
	return true
}

// StoreDamaged returns the first error that showed the store damaged, or "".
func (h *Hub) StoreDamaged() string {
	if p := h.damage.err.Load(); p != nil {
		return *p
	}
	return ""
}
