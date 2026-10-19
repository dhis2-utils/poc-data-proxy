package stick

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/dhis2/facility-hub/internal/hubcrypto"
	"github.com/dhis2/facility-hub/internal/hubtls"
	"github.com/dhis2/facility-hub/internal/store"
)

// The copy on the stick, under facility-hub/<hub ID>/, is what a restore needs and
// central does not have; the cached data is left out, since central sends it again:
//   - keys.db: the key file (key slots, identity), whenever it changed;
//   - state.rec: the users (their tokens, still sealed) and the hub's state (settings,
//     catalog, hub token), sealed with the data key, whenever it changed;
//   - tls.rec: the hub's certificate authority, its key and the server's certificate,
//     sealed with the data key, whenever they changed, so devices keep trusting a
//     restored hub (docs/DEPLOYMENT.md, "Reaching the hub securely");
//   - outbox/<seq>.rec: each offline save still pending, sealed whole with the data key
//     (who saved it, its flags and order cannot be changed on the stick), rewritten
//     when it changes (a corrected aggregate value); a save leaves the stick once
//     pushed or refused, so a restore never sends it again.
//
// Restoring is explicit (the setup page of a new disk, with the password of a staff
// member of that hub): Stage checks the password against the copy and copies it to
// the disk; at the next start Finish puts the key file in place; at the first unlock
// replay puts back the state and the saves, in a new data file, and the hub pulls the
// rest. Copying pauses until then.

const (
	restoreReady  = "restore-ready"       // in the data folder: staged, put in place at the next start
	restoreReplay = "restored-from-stick" // in the data folder: state and saves to put back at unlock
	stateAAD      = "stick-state"
	certsAAD      = "stick-tls"
	certsStaged   = "tls.json" // in the restore folder: the certificates, opened at Stage
)

// RestorePending reports whether a restore waits for its first unlock: the store may
// then create a new data file (store.NewDataFile).
func RestorePending(dataDir string) bool {
	_, err := os.Stat(filepath.Join(dataDir, restoreReplay))
	return err == nil
}

func (s *Stick) hubDir() string { return filepath.Join(s.root(), s.H.HubID()) }

// mirror copies what is new; it needs the store unlocked (saves need it too).
func (s *Stick) mirror() error {
	if !s.H.Store.Unlocked() {
		return nil
	}
	if _, err := os.Stat(filepath.Join(s.H.Cfg.DataDir, restoreReplay)); err == nil {
		return nil // a restore's saves are not put back yet: copying now would drop them
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
	if s.sums == nil {
		s.sums = map[int64][32]byte{}
	}
	if err := s.copyState(filepath.Join(dir, "state.rec")); err != nil {
		return fmt.Errorf("state: %w", err)
	}
	if err := s.copyCerts(filepath.Join(dir, "tls.rec")); err != nil {
		return fmt.Errorf("certificates: %w", err)
	}
	keep := map[string]bool{}
	for _, e := range pending {
		name := strconv.FormatInt(e.Seq, 10) + ".rec"
		keep[name] = true
		b, _ := json.Marshal(e)
		sum := sha256.Sum256(b)
		path := filepath.Join(dir, "outbox", name)
		if _, err := os.Stat(path); err == nil && s.sums[e.Seq] == sum {
			continue
		}
		sealed, err := s.H.Store.Seal(recordAAD(e.Seq), b)
		if err != nil {
			return err
		}
		if err := writeFileSync(path, sealed); err != nil {
			return fmt.Errorf("save %d: %w", e.Seq, err)
		}
		s.sums[e.Seq] = sum
	}
	files, _ := os.ReadDir(filepath.Join(dir, "outbox"))
	for _, f := range files {
		if !keep[f.Name()] {
			os.Remove(filepath.Join(dir, "outbox", f.Name())) // pushed or refused meanwhile
		}
	}
	s.setStatus(func(st *Status) { st.LastCopy = store.Now() })
	return nil
}

// copyState writes the users and the hub's state, sealed, when they changed.
func (s *Stick) copyState(path string) error {
	b, err := s.H.Store.ExportState()
	if err != nil {
		return err
	}
	sum := sha256.Sum256(b)
	if _, err := os.Stat(path); err == nil && sum == s.stateSum {
		return nil
	}
	sealed, err := s.H.Store.Seal(stateAAD, b)
	if err != nil {
		return err
	}
	if err := writeFileSync(path, sealed); err != nil {
		return err
	}
	s.stateSum = sum
	return nil
}

// copyCerts writes the hub's certificates, sealed, when they changed. A hub without
// them (TLS off) has nothing to copy.
func (s *Stick) copyCerts(path string) error {
	b, err := hubtls.Bundle(s.H.Cfg.DataDir)
	if err != nil {
		return nil
	}
	sum := sha256.Sum256(b)
	if _, err := os.Stat(path); err == nil && sum == s.certsSum {
		return nil
	}
	sealed, err := s.H.Store.Seal(certsAAD, b)
	if err != nil {
		return err
	}
	if err := writeFileSync(path, sealed); err != nil {
		return err
	}
	s.certsSum = sum
	return nil
}

func recordAAD(seq int64) string { return "stick-outbox:" + strconv.FormatInt(seq, 10) }

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

func (s *Stick) setStatus(fn func(*Status)) {
	s.mu.Lock()
	fn(&s.st)
	s.mu.Unlock()
}

// Candidate is a hub copy on the stick that a new disk can be restored from.
type Candidate struct {
	HubID, Dir string
	TakenAt    time.Time // its key file's last copy
}

// Candidates lists the hub copies on the stick, newest first.
func Candidates(usbDir string) []Candidate {
	var out []Candidate
	dirs, _ := filepath.Glob(filepath.Join(usbDir, "facility-hub", "*"))
	for _, d := range dirs {
		keys, state := filepath.Join(d, "keys.db"), filepath.Join(d, "state.rec")
		if _, err := os.Stat(keys); err != nil {
			continue
		}
		if _, err := os.Stat(state); err != nil {
			continue
		}
		at := modTime(keys)
		if t := modTime(state); t.After(at) {
			at = t
		}
		out = append(out, Candidate{HubID: filepath.Base(d), Dir: d, TakenAt: at})
	}
	slices.SortFunc(out, func(a, b Candidate) int { return b.TakenAt.Compare(a.TakenAt) })
	return out
}

// Stage prepares restoring hub id from the stick: the password must open a key slot of
// that copy (someone of that hub is there), and everything the restore needs is
// copied to the disk, so pulling the stick out afterwards loses nothing. Finish puts it
// in place at the next start.
func Stage(usbDir, dataDir, id, username, password string) error {
	cands := Candidates(usbDir)
	i := slices.IndexFunc(cands, func(c Candidate) bool { return c.HubID == id })
	if i < 0 {
		return errors.New("no copy of that hub on the stick")
	}
	c := cands[i]
	keys := filepath.Join(c.Dir, "keys.db")
	dk, err := store.UnwrapFromKeyFile(keys, username, password)
	if err != nil {
		return err
	}
	staging := filepath.Join(dataDir, "restore")
	os.RemoveAll(staging)
	if err := os.MkdirAll(filepath.Join(staging, "outbox"), 0o700); err != nil {
		return err
	}
	copies := map[string]string{keys: filepath.Join(staging, "hub.db"),
		filepath.Join(c.Dir, "state.rec"): filepath.Join(staging, "state.rec")}
	recs, _ := filepath.Glob(filepath.Join(c.Dir, "outbox", "*.rec"))
	for _, r := range recs {
		copies[r] = filepath.Join(staging, "outbox", filepath.Base(r))
	}
	for src, dst := range copies {
		if err := copyFile(src, dst); err != nil {
			os.RemoveAll(staging)
			return err
		}
	}
	// The certificates are put in place at the next start, before the hub serves, so
	// they are opened now: without them devices install the new disk's authority.
	if sealed, err := os.ReadFile(filepath.Join(c.Dir, "tls.rec")); err == nil {
		if plain, err := hubcrypto.Open(dk, sealed, []byte(certsAAD)); err != nil {
			slog.Error("the certificates on the USB stick do not open: devices will install the new disk's", "err", err)
		} else if err := writeFileSync(filepath.Join(staging, certsStaged), plain); err != nil {
			os.RemoveAll(staging)
			return err
		}
	}
	return writeFileSync(filepath.Join(dataDir, restoreReady), []byte(staging))
}

// Finish, at start before the store opens: puts a staged restore in place of the new
// disk's store (a hub still waiting for set-up), as the same hub as the copy.
func Finish(dataDir, storePath string) error {
	b, err := os.ReadFile(filepath.Join(dataDir, restoreReady))
	if err != nil {
		return nil
	}
	staging := strings.TrimSpace(string(b))
	for _, p := range []string{storePath, store.DataPath(storePath)} {
		for _, sfx := range []string{"", "-wal", "-shm"} {
			os.Remove(p + sfx)
		}
	}
	if err := os.Rename(filepath.Join(staging, "hub.db"), storePath); err != nil {
		return err
	}
	if b, err := os.ReadFile(filepath.Join(staging, certsStaged)); err == nil {
		if err := hubtls.Restore(dataDir, b); err != nil {
			slog.Error("restoring the hub's certificates from the USB stick", "err", err)
		}
		os.Remove(filepath.Join(staging, certsStaged))
	}
	if err := writeFileSync(filepath.Join(dataDir, restoreReplay), []byte(staging)); err != nil {
		return err
	}
	os.Remove(filepath.Join(dataDir, restoreReady))
	slog.Warn("hub restored from the USB stick; a staff login unlocks it, puts back its state and saves, and pulls the rest")
	return nil
}

// replay runs right after the first unlock of a restored hub, in its new data file:
// the users and state go back, then the saves, as pending. If they cannot, the unlock
// fails: a hub without its settings must not start; the next unlock tries again.
func (s *Stick) replay() error {
	marker := filepath.Join(s.H.Cfg.DataDir, restoreReplay)
	b, err := os.ReadFile(marker)
	if err != nil {
		return nil
	}
	staging := strings.TrimSpace(string(b))
	sealed, err := os.ReadFile(filepath.Join(staging, "state.rec"))
	if err == nil {
		var plain []byte
		if plain, err = s.H.Store.OpenBlob(stateAAD, sealed); err == nil {
			err = s.H.Store.ImportState(plain)
		}
	}
	if err != nil {
		slog.Error("restoring the hub's state from the USB stick", "err", err)
		return fmt.Errorf("restoring the hub's state from the USB stick: %w", err)
	}
	files, _ := filepath.Glob(filepath.Join(staging, "outbox", "*.rec"))
	var entries []*store.RawOutbox
	for _, f := range files {
		seq, _ := strconv.ParseInt(strings.TrimSuffix(filepath.Base(f), ".rec"), 10, 64)
		sealed, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		plain, err := s.H.Store.OpenBlob(recordAAD(seq), sealed)
		var e store.RawOutbox
		if err != nil || json.Unmarshal(plain, &e) != nil || e.Seq != seq {
			slog.Error("a save on the USB stick does not open: not put back", "file", filepath.Base(f))
			continue
		}
		entries = append(entries, &e)
	}
	slices.SortFunc(entries, func(a, b *store.RawOutbox) int { return int(a.Seq - b.Seq) })
	n, err := s.H.Store.RestoreOutbox(entries)
	if err != nil {
		slog.Error("restoring saves from the USB stick", "err", err)
		return fmt.Errorf("restoring saves from the USB stick: %w", err)
	}
	os.Remove(marker)
	os.RemoveAll(staging)
	s.H.Store.NewDataFile = false
	slog.Info("hub state and saves restored from the USB stick", "saves", n)
	return nil
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

func syncDir(p string) {
	if f, err := os.Open(p); err == nil {
		f.Sync()
		f.Close()
	}
}
