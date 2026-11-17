package upgrade

import (
	"encoding/json"
	"github.com/dhis2/facility-hub/internal/atomicfile"
	"os"
	"path/filepath"
	"time"
)

// The data key across a Windows upgrade (docs/design/2026-10-20-windows-
// service-design.md). A service cannot re-exec itself in place, so the old version
// seals the key for the new one in a file: sealed so only the hub's account opens it,
// read once, refused when older than handoverLife. Without it the new version starts
// locked until the next staff login, which loses nothing.
const (
	handoverFile = "upgrade-key"
	handoverLife = 2 * time.Minute
)

// HandoverDir is where the handover file goes (the data folder; set by main).
var HandoverDir string

type handover struct {
	At     time.Time `json:"at"`
	Sealed []byte    `json:"sealed"`
}

// WriteHandover seals dk for the next start.
func WriteHandover(dir string, dk []byte, now time.Time, seal func([]byte) ([]byte, error)) error {
	sealed, err := seal(dk)
	if err != nil {
		return err
	}
	b, _ := json.Marshal(handover{At: now, Sealed: sealed})
	return atomicfile.Write(filepath.Join(dir, handoverFile), b, 0o600)
}

// ReadHandover returns the handed-over key once and deletes the file; nil when there
// is none, it is too old, or it does not open.
func ReadHandover(dir string, now time.Time, open func([]byte) ([]byte, error)) []byte {
	p := filepath.Join(dir, handoverFile)
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	os.Remove(p)
	var h handover
	if json.Unmarshal(b, &h) != nil || now.Sub(h.At) > handoverLife || now.Before(h.At.Add(-time.Minute)) {
		return nil
	}
	dk, err := open(h.Sealed)
	if err != nil {
		return nil
	}
	return dk
}
