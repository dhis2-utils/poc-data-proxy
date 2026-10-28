// Package stick keeps the hub's USB stick (docs/RECOVERY.md, option C2): before set-up
// it carries the setup code (in hub.txt); after, a copy of the store that a new disk
// restores from.
package stick

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"github.com/dhis2/facility-hub/internal/atomicfile"
	"github.com/dhis2/facility-hub/internal/devkey"
	"github.com/dhis2/facility-hub/internal/diskfree"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	// usb_dir: auto (Windows): the drives to choose from when none is chosen yet, and
	// whether the chosen one is not plugged in.
	Ask     []Drive `json:"ask,omitempty"`
	Missing bool    `json:"missing,omitempty"`
	// Unknown: the stick plugged in is not the box's own; it gets nothing.
	Unknown bool `json:"unknown,omitempty"`
}

// MetaChoice is the drive a staff member chose for the copy (usb_dir: auto).
const MetaChoice = "usb_choice"

// MetaStick is the mark of the box's own stick, also in facility-hub/<hub ID>/.stick-id
// on it: once set up, the hub copies only to the stick holding it. The stick in the
// box before set-up gets it, or one a staff member takes on later (Choose).
const MetaStick = "usb_stick"

// markFile holds the mark on the stick: hidden, so a Mac does not show it. Sticks
// marked before 2026-10-28 have it as oldMarkFile, renamed when read.
const markFile, oldMarkFile = ".stick-id", "stick-id"

// readMark reads the mark in the hub's folder d on the stick.
func readMark(d string) ([]byte, error) {
	path := filepath.Join(d, markFile)
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		old := filepath.Join(d, oldMarkFile)
		if b, err = os.ReadFile(old); err == nil {
			os.Rename(old, path)
		}
	}
	return b, err
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
	autoRoot string             // usb_dir: auto: the chosen drive's root, "" while none
	// Info is the text of facility-hub/hub.txt (the hub's ID, address and setup Wi-Fi,
	// for whoever holds the box); nil writes none.
	Info func() string
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

// dir is where the stick is mounted ("" for none; with usb_dir: auto, the chosen
// removable drive, "" while none is chosen or plugged in).
func (s *Stick) dir() string {
	if s.auto() {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.autoRoot
	}
	return s.H.Cfg.USBDir
}

func (s *Stick) auto() bool { return s.H.Cfg.USBDir == "auto" }

// configured: a stick is part of this hub (usb_dir set).
func (s *Stick) configured() bool { return s.H.Cfg.USBDir != "" }

// locate finds the chosen drive (usb_dir: auto) and reports the choice to make, or the
// drive missing, in the status.
func (s *Stick) locate() bool {
	root, ask, missing := Locate(Removable(), s.H.HubID(), s.H.Store.Meta(MetaChoice), hasHub(s.H.HubID()))
	s.mu.Lock()
	s.autoRoot = root
	s.st.Ask, s.st.Missing = ask, missing
	if root == "" {
		s.st.Present, s.st.Writable, s.st.FreeBytes, s.st.Unknown = false, false, 0, false
	}
	s.mu.Unlock()
	return root != ""
}

// Choose keeps the hub's copy on the removable drive at root from now on (usb_dir:
// auto), or on the stick plugged in (root ignored): a staff member's choice on the
// admin page, which also replaces a lost stick. A stick marked before gets nothing more.
func (s *Stick) Choose(root string) error {
	s.tickMu.Lock()
	defer s.tickMu.Unlock()
	if !s.auto() {
		if !s.plugged(s.dir()) {
			return errors.New("no stick is plugged in")
		}
		return s.mark(s.dir(), true)
	}
	for _, d := range Removable() {
		if d.Root == root {
			if err := s.mark(root, true); err != nil {
				return err
			}
			return s.H.Store.SetMeta(MetaChoice, d.ID())
		}
	}
	return errors.New("that drive is not plugged in")
}

// mark writes the hub's mark on the stick at dir: a new one with renew or when the hub
// has none yet, kept in the key file.
func (s *Stick) mark(dir string, renew bool) error {
	had := s.H.Store.Meta(MetaStick)
	tok := had
	if tok == "" || renew {
		b := make([]byte, 32)
		rand.Read(b)
		tok = hex.EncodeToString(b)
	}
	d := filepath.Join(dir, "facility-hub", s.H.HubID())
	if err := os.MkdirAll(d, 0o700); err != nil {
		return err
	}
	if b, err := readMark(d); err != nil || string(b) != tok {
		if err := atomicfile.Write(filepath.Join(d, markFile), []byte(tok), 0o600); err != nil {
			return err
		}
	}
	if tok == had {
		return nil
	}
	s.keysAt = 0 // a new stick needs the key file too
	return s.H.Store.SetMeta(MetaStick, tok)
}

// own reports whether the stick is the box's own: it holds the hub's mark. A hub
// upgraded from a version without marks takes on the stick holding its copy (a state
// that opens with its data key).
func (s *Stick) own() bool {
	want := s.H.Store.Meta(MetaStick)
	if want == "" {
		sealed, err := os.ReadFile(filepath.Join(s.hubDir(), "state.rec"))
		if err != nil {
			return false
		}
		if _, err := s.H.Store.OpenBlob(stateAAD, sealed); err != nil {
			return false
		}
		return s.mark(s.dir(), false) == nil
	}
	got, err := readMark(s.hubDir())
	return err == nil && subtle.ConstantTimeCompare(got, []byte(want)) == 1
}

// Run ticks every minute until ctx ends.
func (s *Stick) Run(ctx context.Context) {
	if !s.configured() {
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
	if s == nil || !s.configured() {
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
	if !s.configured() {
		return
	}
	s.tickMu.Lock()
	defer s.tickMu.Unlock()
	if s.auto() && !s.locate() {
		return
	}
	st := s.check()
	s.setStatus(func(cur *Status) {
		cur.Present, cur.Writable, cur.FreeBytes, cur.Error, cur.Unknown = st.Present, st.Writable, st.FreeBytes, st.Error, st.Unknown
		if st.Unknown && s.auto() {
			cur.Ask = Removable() // choose again
		}
	})
	if !st.Present || !st.Writable {
		return
	}
	os.Remove(filepath.Join(s.root(), "setup-code.txt")) // before 2026-10-28; now in hub.txt
	err := s.writeInfo(s.info())
	if s.H.Mode() == core.ModeSetup {
		err = errors.Join(err, s.mark(s.dir(), false))
	} else {
		err = errors.Join(err, s.mirror())
	}
	if err != nil {
		s.setStatus(func(cur *Status) { cur.Error = err.Error() })
	}
}

// plugged reports whether a stick is at dir.
func (s *Stick) plugged(dir string) bool {
	fi, err := os.Stat(dir)
	if err != nil || !fi.IsDir() {
		return false
	}
	// The folder, not a stick: never fill the hub's own disk.
	return !s.H.Cfg.USBRequireMount || s.auto() || mountPoint(dir)
}

// check finds the stick and writes and reads back a small file.
func (s *Stick) check() Status {
	var st Status
	if !s.plugged(s.dir()) {
		return st
	}
	st.Present = true
	if s.H.Mode() != core.ModeSetup && !s.own() {
		st.Unknown = true // someone else's stick: nothing of the hub's goes on it
		return st
	}
	if err := os.MkdirAll(s.root(), 0o700); err != nil {
		st.Error = err.Error()
		return st
	}
	probe := filepath.Join(s.root(), ".probe")
	want := []byte(store.Now())
	if err := atomicfile.Write(probe, want, 0o600); err != nil {
		st.Error = err.Error()
		return st
	}
	if got, err := os.ReadFile(probe); err != nil || string(got) != string(want) {
		st.Error = "the stick did not read back what was written"
		return st
	}
	st.Writable = true
	st.FreeBytes = diskfree.Free(s.dir())
	return st
}

// info is the text of facility-hub/hub.txt: Info, or the hub's ID without it; while
// the hub waits to be set up, with its setup code after the first line, so whoever
// holds the box can set it up, and no one else on the network. Once set up, the code
// no longer works and its line goes.
func (s *Stick) info() string {
	text := "Facility hub " + s.H.HubID() + "\n"
	if s.Info != nil {
		text = s.Info()
	}
	if s.H.Mode() != core.ModeSetup {
		return text
	}
	code, err := devkey.ReadFile(s.H.SetupCodePath())
	if err != nil {
		return text // pinned or allow-list set-up: no code
	}
	first, rest, _ := strings.Cut(text, "\n")
	return first + "\nSetup code: " + strings.TrimSpace(string(code)) + "\n" + rest
}

// writeInfo keeps facility-hub/hub.txt current.
func (s *Stick) writeInfo(text string) error {
	if err := os.MkdirAll(s.root(), 0o700); err != nil {
		return err
	}
	path := filepath.Join(s.root(), "hub.txt")
	if b, err := os.ReadFile(path); err == nil && string(b) == text {
		return nil
	}
	slog.Info("hub.txt written to the USB stick", "hub", s.H.HubID(), "setupCode", strings.Contains(text, "\nSetup code: "))
	return atomicfile.Write(path, []byte(text), 0o600)
}
