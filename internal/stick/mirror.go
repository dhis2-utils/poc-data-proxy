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

	"github.com/dhis2/facility-hub/internal/store"
)

// The copy on the stick, under facility-hub/<hub ID>/:
//   - keys.db: the key file (key slots, identity, settings), whenever it changed;
//   - outbox/<seq>.rec: each offline save still pending, sealed whole with the data key
//     (who saved it, its flags and order cannot be changed on the stick), rewritten
//     when it changes (a corrected aggregate value); a save leaves the stick once
//     pushed or refused;
//   - maxseq: the highest save the stick ever copied, so a restore knows which saves
//     the snapshot holds as pending were sent since (they are not sent again: that
//     could overwrite newer edits on central);
//   - snapshot/ (snapshot.old while a new one is swapped in): the whole store.
//
// Restoring is explicit (the setup page of a new disk, with the password of a staff
// member of that hub): Stage checks the password against the copy and copies what is
// needed to the disk; at the next start Finish puts it in place; after the first
// unlock, replay puts the saves back. The copying pauses until then.

const (
	restoreReady  = "restore-ready"       // in the data folder: staged, put in place at the next start
	restoreReplay = "restored-from-stick" // in the data folder: saves to put back after the first unlock
	snapshotRetry = time.Hour             // after a failed snapshot
)

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
	keep := map[string]bool{}
	maxSeq := readSeq(filepath.Join(dir, "maxseq"))
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
	for _, e := range pending {
		maxSeq = max(maxSeq, e.Seq)
	}
	if err := writeFileSync(filepath.Join(dir, "maxseq"), []byte(strconv.FormatInt(maxSeq, 10))); err != nil {
		return err
	}
	files, _ := os.ReadDir(filepath.Join(dir, "outbox"))
	for _, f := range files {
		if !keep[f.Name()] {
			os.Remove(filepath.Join(dir, "outbox", f.Name())) // pushed or refused meanwhile
		}
	}
	s.setStatus(func(st *Status) { st.LastCopy = store.Now() })
	if time.Since(s.lastSnapshot()) >= s.H.Cfg.USBSnapshotInterval && time.Since(s.snapFailed) >= snapshotRetry {
		if err := s.snapshot(); err != nil {
			s.snapFailed = time.Now()
			return fmt.Errorf("snapshot: %w", err)
		}
	}
	return nil
}

func recordAAD(seq int64) string { return "stick-outbox:" + strconv.FormatInt(seq, 10) }

func readSeq(path string) int64 {
	b, _ := os.ReadFile(path)
	n, _ := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	return n
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

// snapshot writes the whole store (still encrypted) beside the last one, then swaps. A
// failure leaves no half snapshot behind.
func (s *Stick) snapshot() error {
	dir := s.hubDir()
	tmp, cur, old := filepath.Join(dir, "snapshot.tmp"), filepath.Join(dir, "snapshot"), filepath.Join(dir, "snapshot.old")
	if need := s.H.Store.Size(); s.Status().FreeBytes < uint64(need)+uint64(need)/10 {
		return fmt.Errorf("the stick is too full for a snapshot (%d MB free, %d MB needed)", s.Status().FreeBytes>>20, need>>20)
	}
	os.RemoveAll(tmp)
	err := func() error {
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
		return writeFileSync(filepath.Join(tmp, "taken-at"), []byte(store.Now()))
	}()
	if err != nil {
		os.RemoveAll(tmp)
		return err
	}
	os.RemoveAll(old)
	os.Rename(cur, old) // a power cut from here leaves snapshot.old, which a restore takes
	if err := os.Rename(tmp, cur); err != nil {
		return err
	}
	syncDir(dir)
	os.RemoveAll(old)
	syncDir(dir)
	s.setStatus(func(st *Status) { st.LastSnapshot = store.Now() })
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

// Candidate is a hub copy on the stick that a new disk can be restored from.
type Candidate struct {
	HubID, Snapshot string
	TakenAt         time.Time
}

// Candidates lists the copies on the stick: each hub folder's snapshot, or the
// previous one if a power cut came between the two renames of a new snapshot.
func Candidates(usbDir string) []Candidate {
	var out []Candidate
	dirs, _ := filepath.Glob(filepath.Join(usbDir, "facility-hub", "*"))
	for _, d := range dirs {
		for _, name := range []string{"snapshot", "snapshot.old"} {
			snap := filepath.Join(d, name)
			if _, err := os.Stat(filepath.Join(snap, "hub.db")); err != nil {
				continue
			}
			at := modTime(filepath.Join(snap, "hub.db"))
			if b, err := os.ReadFile(filepath.Join(snap, "taken-at")); err == nil {
				at = store.ParseTime(strings.TrimSpace(string(b)))
			}
			out = append(out, Candidate{HubID: filepath.Base(d), Snapshot: snap, TakenAt: at})
			break
		}
	}
	slices.SortFunc(out, func(a, b Candidate) int { return b.TakenAt.Compare(a.TakenAt) })
	return out
}

// Stage prepares restoring hub id from the stick: the password must open a key slot of
// that copy (someone of that hub is there), and everything the restore needs is
// copied to the disk, so pulling the stick out afterwards loses nothing. Finish puts it
// in place at the next start.
func Stage(usbDir, dataDir, id, username, password string) error {
	i := slices.IndexFunc(Candidates(usbDir), func(c Candidate) bool { return c.HubID == id })
	if i < 0 {
		return errors.New("no copy of that hub on the stick")
	}
	c := Candidates(usbDir)[i]
	hubDir := filepath.Dir(c.Snapshot)
	keys := filepath.Join(c.Snapshot, "hub.db")
	if k := filepath.Join(hubDir, "keys.db"); modTime(k).After(modTime(keys)) {
		keys = k
	}
	if _, err := store.UnwrapFromKeyFile(keys, username, password); err != nil {
		return err
	}
	staging := filepath.Join(dataDir, "restore")
	os.RemoveAll(staging)
	if err := os.MkdirAll(filepath.Join(staging, "outbox"), 0o700); err != nil {
		return err
	}
	copies := map[string]string{keys: filepath.Join(staging, "hub.db"),
		store.DataPath(filepath.Join(c.Snapshot, "hub.db")): store.DataPath(filepath.Join(staging, "hub.db"))}
	if _, err := os.Stat(filepath.Join(hubDir, "maxseq")); err == nil {
		copies[filepath.Join(hubDir, "maxseq")] = filepath.Join(staging, "maxseq")
	}
	recs, _ := filepath.Glob(filepath.Join(hubDir, "outbox", "*.rec"))
	for _, r := range recs {
		copies[r] = filepath.Join(staging, "outbox", filepath.Base(r))
	}
	for src, dst := range copies {
		if err := copyFile(src, dst); err != nil {
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
	if err := os.Rename(store.DataPath(filepath.Join(staging, "hub.db")), store.DataPath(storePath)); err != nil {
		return err
	}
	if err := writeFileSync(filepath.Join(dataDir, restoreReplay), []byte(staging)); err != nil {
		return err
	}
	os.Remove(filepath.Join(dataDir, restoreReady))
	slog.Warn("store restored from the USB stick; a staff login unlocks it and puts back the newer saves")
	return nil
}

// replay runs after an unlock: the saves copied after the snapshot go back in the
// outbox as pending; saves the snapshot holds as pending that the stick saw sent
// since are marked sent.
func (s *Stick) replay() {
	marker := filepath.Join(s.H.Cfg.DataDir, restoreReplay)
	b, err := os.ReadFile(marker)
	if err != nil {
		return
	}
	staging := strings.TrimSpace(string(b))
	files, _ := filepath.Glob(filepath.Join(staging, "outbox", "*.rec"))
	var entries []*store.RawOutbox
	pending := map[int64]bool{}
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
		pending[seq] = true
	}
	slices.SortFunc(entries, func(a, b *store.RawOutbox) int { return int(a.Seq - b.Seq) })
	n, err := s.H.Store.RestoreOutbox(entries)
	if err != nil {
		slog.Error("restoring saves from the USB stick", "err", err)
		return
	}
	sent, err := s.H.Store.MarkSentSince(readSeq(filepath.Join(staging, "maxseq")), pending)
	if err != nil {
		slog.Error("restoring saves from the USB stick", "err", err)
		return
	}
	os.Remove(marker)
	os.RemoveAll(staging)
	slog.Info("saves restored from the USB stick", "put back", n, "already sent", sent)
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
