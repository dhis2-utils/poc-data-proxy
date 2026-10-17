package stick

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/dhis2/facility-hub/internal/store"
)

// The copy on the stick, under facility-hub/<hub ID>/:
//   - keys.db: the key file (key slots, identity, settings), whenever it changed;
//   - outbox/<seq>.json: each offline save still pending, sealed as in the store; a
//     save leaves the stick once pushed (or refused), so a restore never sends again
//     what central already has, which could overwrite newer edits there;
//   - snapshot/: the whole store, every USBSnapshotInterval.

const restoreMarker = "restored-from-stick" // in the data folder: saves to put back at unlock

func (s *Stick) hubDir() string { return filepath.Join(s.root(), s.H.HubID()) }

// mirror copies what is new; it needs the store unlocked (saves need it too).
func (s *Stick) mirror() error {
	if !s.H.Store.Unlocked() {
		return nil
	}
	dir := s.hubDir()
	if err := os.MkdirAll(filepath.Join(dir, "outbox"), 0o700); err != nil {
		return err
	}
	if t := s.H.Store.KeysChanged(); t != s.keysAt {
		if err := s.copyKeys(filepath.Join(dir, "keys.db")); err != nil {
			return fmt.Errorf("key file: %w", err)
		}
		s.keysAt = t
	}
	pending, err := s.H.Store.PendingRaw()
	if err != nil {
		return err
	}
	keep := map[string]bool{}
	for _, e := range pending {
		name := strconv.FormatInt(e.Seq, 10) + ".json"
		keep[name] = true
		path := filepath.Join(dir, "outbox", name)
		if _, err := os.Stat(path); err == nil {
			continue
		}
		b, _ := json.Marshal(e)
		if err := writeFileSync(path, b); err != nil {
			return fmt.Errorf("save %d: %w", e.Seq, err)
		}
	}
	files, _ := os.ReadDir(filepath.Join(dir, "outbox"))
	for _, f := range files {
		if !keep[f.Name()] {
			os.Remove(filepath.Join(dir, "outbox", f.Name())) // pushed or refused meanwhile
		}
	}
	s.setStatus(func(st *Status) { st.LastCopy = store.Now() })
	if time.Since(s.lastSnapshot()) >= s.H.Cfg.USBSnapshotInterval {
		if err := s.snapshot(); err != nil {
			return fmt.Errorf("snapshot: %w", err)
		}
	}
	return nil
}

func (s *Stick) copyKeys(dst string) error {
	tmp := dst + ".tmp"
	if err := s.H.Store.CopyKeysTo(tmp); err != nil {
		return err
	}
	if err := syncFile(tmp); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

// snapshot writes the whole store (still encrypted) beside the last one, then swaps.
func (s *Stick) snapshot() error {
	dir := s.hubDir()
	tmp, cur, old := filepath.Join(dir, "snapshot.tmp"), filepath.Join(dir, "snapshot"), filepath.Join(dir, "snapshot.old")
	os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		return err
	}
	dst := filepath.Join(tmp, "hub.db")
	if err := s.H.Store.BackupTo(dst); err != nil {
		return err
	}
	for _, p := range []string{dst, store.DataPath(dst)} {
		if err := syncFile(p); err != nil {
			return err
		}
	}
	os.RemoveAll(old)
	os.Rename(cur, old)
	if err := os.Rename(tmp, cur); err != nil {
		return err
	}
	os.RemoveAll(old)
	now := store.Now()
	writeFileSync(filepath.Join(cur, "taken-at"), []byte(now))
	s.setStatus(func(st *Status) { st.LastSnapshot = now })
	slog.Info("store snapshot written to the USB stick")
	return nil
}

func (s *Stick) lastSnapshot() time.Time {
	b, err := os.ReadFile(filepath.Join(s.hubDir(), "snapshot", "taken-at"))
	if err != nil {
		return time.Time{}
	}
	return store.ParseTime(strings.TrimSpace(string(b)))
}

func (s *Stick) setStatus(fn func(*Status)) {
	s.mu.Lock()
	fn(&s.st)
	s.mu.Unlock()
}

// Restore, before the store opens: a hub with no store, whose stick holds a snapshot,
// starts from it as the same hub (the newest snapshot, with the key file copy if that
// is newer). It is locked; the first staff login puts back the saves made after the
// snapshot (replay). It reports whether it restored.
func Restore(usbDir, storePath string) (bool, error) {
	if usbDir == "" {
		return false, nil
	}
	if _, err := os.Stat(storePath); err == nil {
		return false, nil // a store is there: nothing to restore
	}
	snaps, _ := filepath.Glob(filepath.Join(usbDir, "facility-hub", "*", "snapshot", "hub.db"))
	if len(snaps) == 0 {
		return false, nil
	}
	slices.SortFunc(snaps, func(a, b string) int { return modTime(b).Compare(modTime(a)) })
	snap := snaps[0]
	hubDir := filepath.Dir(filepath.Dir(snap))
	if err := os.MkdirAll(filepath.Dir(storePath), 0o700); err != nil {
		return false, err
	}
	keys := snap
	if k := filepath.Join(hubDir, "keys.db"); modTime(k).After(modTime(snap)) {
		keys = k
	}
	if err := copyFile(keys, storePath); err != nil {
		return false, err
	}
	if err := copyFile(store.DataPath(snap), store.DataPath(storePath)); err != nil {
		os.Remove(storePath)
		return false, err
	}
	if err := writeFileSync(filepath.Join(filepath.Dir(storePath), restoreMarker), []byte(hubDir)); err != nil {
		return false, err
	}
	slog.Warn("store restored from the USB stick; a staff login unlocks it and puts back the newer saves", "from", hubDir)
	return true, nil
}

// replay runs after an unlock: the saves copied after the snapshot go back in the
// outbox, as pending.
func (s *Stick) replay() {
	marker := filepath.Join(s.H.Cfg.DataDir, restoreMarker)
	b, err := os.ReadFile(marker)
	if err != nil {
		return
	}
	files, _ := filepath.Glob(filepath.Join(strings.TrimSpace(string(b)), "outbox", "*.json"))
	var entries []*store.RawOutbox
	for _, f := range files {
		var e store.RawOutbox
		if b, err := os.ReadFile(f); err == nil && json.Unmarshal(b, &e) == nil {
			entries = append(entries, &e)
		}
	}
	slices.SortFunc(entries, func(a, b *store.RawOutbox) int { return int(a.Seq - b.Seq) })
	n, err := s.H.Store.RestoreOutbox(entries)
	if err != nil {
		slog.Error("restoring saves from the USB stick", "err", err)
		return
	}
	os.Remove(marker)
	slog.Info("saves restored from the USB stick", "entries", n)
}

func modTime(p string) time.Time {
	if fi, err := os.Stat(p); err == nil {
		return fi.ModTime()
	}
	return time.Time{}
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

func syncFile(p string) error {
	f, err := os.OpenFile(p, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
