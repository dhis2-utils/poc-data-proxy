// Package stick keeps the hub's USB stick (docs/RECOVERY.md, option C2): before set-up
// it carries the setup code; after, a copy of the store that a new disk restores from.
package stick

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/dhis2/facility-hub/internal/core"
	"github.com/dhis2/facility-hub/internal/store"
)

// Status is the stick's health, for the admin page and the heartbeat.
type Status struct {
	Present   bool   `json:"present"`
	Writable  bool   `json:"writable"`
	LastCopy  string `json:"lastCopy,omitempty"`
	FreeBytes uint64 `json:"freeBytes,omitempty"`
	Error     string `json:"error,omitempty"`
}

// Stick runs on its own timer, so a slow or failing stick never delays a save.
type Stick struct {
	H        *core.Hub
	mu       sync.Mutex
	st       Status
	keysAt   int64              // the key file's last change copied
	sums     map[int64][32]byte // what each save on the stick holds
	stateSum [32]byte           // what state.rec holds
	certsSum [32]byte           // what tls.rec holds
	tickMu   sync.Mutex         // one tick at a time
}

func New(h *core.Hub) *Stick {
	s := &Stick{H: h}
	h.AfterUnlock(s.replay)
	h.USBStatus = func() any {
		if st := s.Status(); st != nil {
			return st
		}
		return nil
	}
	return s
}

// dir is where the stick is mounted ("" for none).
func (s *Stick) dir() string { return s.H.Cfg.USBDir }

// Run ticks every minute until ctx ends.
func (s *Stick) Run(ctx context.Context) {
	if s.dir() == "" {
		return
	}
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		s.Tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Status returns the last health check (nil without a stick configured).
func (s *Stick) Status() *Status {
	if s == nil || s.dir() == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.st
	return &st
}

// root is the hub's folder on the stick.
func (s *Stick) root() string { return filepath.Join(s.dir(), "facility-hub") }

// Tick checks the stick and does what is due.
func (s *Stick) Tick(ctx context.Context) {
	if s.dir() == "" {
		return
	}
	s.tickMu.Lock()
	defer s.tickMu.Unlock()
	st := s.check()
	s.setStatus(func(cur *Status) {
		cur.Present, cur.Writable, cur.FreeBytes, cur.Error = st.Present, st.Writable, st.FreeBytes, st.Error
	})
	if !st.Present || !st.Writable {
		return
	}
	var err error
	if s.H.Mode() == core.ModeSetup {
		err = s.writeSetupCode()
	} else {
		os.Remove(filepath.Join(s.root(), "setup-code.txt")) // used: it no longer works
		err = s.mirror()
	}
	if err != nil {
		s.setStatus(func(cur *Status) { cur.Error = err.Error() })
	}
}

// check finds the stick and writes and reads back a small file.
func (s *Stick) check() Status {
	var st Status
	fi, err := os.Stat(s.dir())
	if err != nil || !fi.IsDir() {
		return st
	}
	if s.H.Cfg.USBRequireMount && !mountPoint(s.dir()) {
		return st // the folder, not a stick: never fill the hub's own disk
	}
	st.Present = true
	if err := os.MkdirAll(s.root(), 0o700); err != nil {
		st.Error = err.Error()
		return st
	}
	probe := filepath.Join(s.root(), ".probe")
	want := []byte(store.Now())
	if err := writeFileSync(probe, want); err != nil {
		st.Error = err.Error()
		return st
	}
	if got, err := os.ReadFile(probe); err != nil || string(got) != string(want) {
		st.Error = "the stick did not read back what was written"
		return st
	}
	st.Writable = true
	var fs syscall.Statfs_t
	if syscall.Statfs(s.dir(), &fs) == nil {
		st.FreeBytes = uint64(fs.Bavail) * uint64(fs.Bsize)
	}
	return st
}

// writeSetupCode puts the setup code and hub ID on the stick while the hub waits to be
// set up: whoever holds the box can then set it up, and no one else on the network.
func (s *Stick) writeSetupCode() error {
	code, err := os.ReadFile(s.H.SetupCodePath())
	if err != nil {
		return nil // pinned or allow-list set-up: no code
	}
	text := "Facility hub " + s.H.HubID() + "\nSetup code: " + strings.TrimSpace(string(code)) + "\n"
	path := filepath.Join(s.root(), "setup-code.txt")
	if b, err := os.ReadFile(path); err == nil && string(b) == text {
		return nil
	}
	slog.Info("setup code written to the USB stick", "hub", s.H.HubID())
	return writeFileSync(path, []byte(text))
}

// writeFileSync writes through a temporary file, synced and renamed, so a stick pulled
// out midway never holds half a file.
func writeFileSync(path string, b []byte) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// mountPoint reports whether dir is a mount point, from the mount table where there is
// one (Linux: also right for automounts and bind mounts), else by comparing its device
// with its parent's.
func mountPoint(dir string) bool {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	if b, err := os.ReadFile("/proc/self/mountinfo"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			f := strings.Fields(line)
			if len(f) > 4 && unescapeMount(f[4]) == abs {
				return true
			}
		}
		return false
	}
	var a, p syscall.Stat_t
	if syscall.Stat(abs, &a) != nil || syscall.Stat(filepath.Dir(abs), &p) != nil {
		return false
	}
	return a.Dev != p.Dev
}

// unescapeMount undoes the mount table's octal escapes (a space is \040).
func unescapeMount(s string) string {
	var out strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				out.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		out.WriteByte(s[i])
	}
	return out.String()
}
