// Package stick keeps the hub's USB stick (docs/RECOVERY.md, option C2): before set-up
// it carries the setup code; after, a copy of the store that a new disk restores from.
package stick

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/dhis2/facility-hub/internal/core"
	"github.com/dhis2/facility-hub/internal/store"
)

// Status is the stick's health, for the admin page and the heartbeat.
type Status struct {
	Present      bool   `json:"present"`
	Writable     bool   `json:"writable"`
	LastCopy     string `json:"lastCopy,omitempty"`
	LastSnapshot string `json:"lastSnapshot,omitempty"`
	FreeBytes    uint64 `json:"freeBytes,omitempty"`
	Error        string `json:"error,omitempty"`
}

// Stick runs on its own timer, so a slow or failing stick never delays a save.
type Stick struct {
	H      *core.Hub
	mu     sync.Mutex
	st     Status
	dir    string
	keysAt int64      // the key file's last change copied
	tickMu sync.Mutex // one tick at a time
}

func New(h *core.Hub) *Stick {
	s := &Stick{H: h, dir: h.Cfg.USBDir}
	if s.dir != "" {
		h.AfterUnlock(s.replay)
		h.USBStatus = func() any { return s.Status() }
	}
	return s
}

// Run ticks every minute until ctx ends.
func (s *Stick) Run(ctx context.Context) {
	if s.dir == "" {
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
	if s == nil || s.dir == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.st
	return &st
}

// root is the hub's folder on the stick.
func (s *Stick) root() string { return filepath.Join(s.dir, "facility-hub") }

// Tick checks the stick and does what is due.
func (s *Stick) Tick(ctx context.Context) {
	if s.dir == "" {
		return
	}
	s.tickMu.Lock()
	defer s.tickMu.Unlock()
	st := s.check()
	if st.Present && st.Writable {
		var err error
		if s.H.Mode() == core.ModeSetup {
			err = s.writeSetupCode()
		} else {
			os.Remove(filepath.Join(s.root(), "setup-code.txt")) // used: it no longer works
			err = s.mirror()
		}
		if err != nil {
			st.Error = err.Error()
		}
	}
	s.mu.Lock()
	s.st.Present, s.st.Writable, s.st.FreeBytes, s.st.Error = st.Present, st.Writable, st.FreeBytes, st.Error
	s.mu.Unlock()
}

// check finds the stick and writes and reads back a small file.
func (s *Stick) check() Status {
	var st Status
	fi, err := os.Stat(s.dir)
	if err != nil || !fi.IsDir() {
		return st
	}
	if s.H.Cfg.USBRequireMount && !mountPoint(s.dir) {
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
	if syscall.Statfs(s.dir, &fs) == nil {
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

// mountPoint reports whether dir is on another device than its parent.
func mountPoint(dir string) bool {
	var a, b syscall.Stat_t
	if syscall.Stat(dir, &a) != nil || syscall.Stat(filepath.Dir(dir), &b) != nil {
		return false
	}
	return a.Dev != b.Dev
}
