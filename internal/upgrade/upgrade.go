// Package upgrade implements hub self-upgrade (spec 12a): signed releases from a
// release channel, outbox drain, migration on a store copy, binary swap, re-exec,
// health check and rollback.
//
// Release channel layout (a static folder over HTTP):
//
//	release.json      {"version":"0.2.0","files":{"linux/arm64":"hub-linux-arm64",...},
//	                   "sha256":{"linux/arm64":"<hex>",...},"stage":{"percent":100,"hubIds":[]}}
//	release.json.sig  base64 ed25519 signature of release.json, by the release key
//	hub-linux-arm64   the binary (its sha256 is covered by the signed manifest)
package upgrade

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/dhis2/facility-hub/internal/core"
	"github.com/dhis2/facility-hub/internal/store"
)

type Manifest struct {
	Version string            `json:"version"`
	Files   map[string]string `json:"files"`
	SHA256  map[string]string `json:"sha256"`
	Stage   struct {
		Percent int      `json:"percent"`
		HubIDs  []string `json:"hubIds"`
	} `json:"stage"`
}

// Marker is written to data_dir/upgrade.json across the re-exec.
type Marker struct {
	State      string `json:"state"` // "swapped" (new binary must health-check) or "rolledback"
	From       string `json:"from"`
	To         string `json:"to"`
	ExePath    string `json:"exePath"`
	PrevBinary string `json:"prevBinary"`
	PrevStore  string `json:"prevStore"`
	StartedAt  string `json:"startedAt"`
	Reason     string `json:"reason,omitempty"`
}

// Result is what the heartbeat reports as lastUpgrade.
type Result struct {
	Result string `json:"result"` // ok, failed, ignored
	From   string `json:"from,omitempty"`
	To     string `json:"to,omitempty"`
	Reason string `json:"reason,omitempty"`
	At     string `json:"at"`
}

type Upgrader struct {
	H *core.Hub
	// Drain pushes the outbox; it reports whether entries are still pending.
	Drain func(ctx context.Context) (pending int, err error)
	// Shutdown stops serving and closes the store before the re-exec.
	Shutdown func()
	// StorePath is the live store file.
	StorePath string
	// HealthFail forces the health check to fail (test builds, spec 13.3 scenario 11).
	HealthFail bool
}

func (u *Upgrader) dir(p string) string { return filepath.Join(u.H.Cfg.DataDir, p) }

func (u *Upgrader) record(r Result) {
	r.At = store.Now()
	b, _ := json.Marshal(r)
	u.H.Store.SetMeta(core.MetaUpgrade, string(b))
	slog.Info("upgrade", "result", r.Result, "from", r.From, "to", r.To, "reason", r.Reason)
}

// Check runs one release channel check (called after each online sync cycle).
func (u *Upgrader) Check(ctx context.Context) {
	if u.H.Cfg.ReleaseChannel == "" {
		return
	}
	if err := u.check(ctx); err != nil {
		slog.Warn("upgrade check", "err", err)
	}
}

func (u *Upgrader) check(ctx context.Context) error {
	m, err := u.fetchManifest(ctx)
	if err != nil {
		return err
	}
	if m == nil || CompareVersions(m.Version, u.H.Version) <= 0 {
		return nil
	}
	if skip := u.H.Store.Meta("upgrade_skip"); skip == m.Version {
		return nil // this version already failed its health check here
	}
	if !InStage(m, u.H.HubID()) {
		slog.Info("upgrade available but this hub is not in the current stage", "version", m.Version)
		return nil
	}
	plat := runtime.GOOS + "/" + runtime.GOARCH
	if m.Files[plat] == "" || m.SHA256[plat] == "" {
		return fmt.Errorf("release %s has no binary for %s", m.Version, plat)
	}
	// 1. Push the outbox until empty (or only rejected entries remain).
	pending, err := u.Drain(ctx)
	if err != nil || pending > 0 {
		return fmt.Errorf("outbox not drained (%d pending): %v", pending, err)
	}
	// 2. Download and verify.
	bin, err := u.get(ctx, m.Files[plat])
	if err != nil {
		return err
	}
	sum := sha256.Sum256(bin)
	if hex.EncodeToString(sum[:]) != strings.ToLower(m.SHA256[plat]) {
		u.record(Result{Result: "ignored", From: u.H.Version, To: m.Version, Reason: "binary checksum does not match signed manifest"})
		return errors.New("checksum mismatch")
	}
	binDir := u.dir("bin")
	os.MkdirAll(binDir, 0o755)
	newBin := filepath.Join(binDir, "hub-"+m.Version)
	if err := os.WriteFile(newBin, bin, 0o755); err != nil {
		return err
	}
	// Refuse local writes from here on, so the store copy is complete.
	u.H.FreezeLocalWrites()
	ok := false
	defer func() {
		if !ok {
			u.H.ThawLocalWrites()
		}
	}()
	// 3. Migrations on a copy of the store, run by the new binary.
	next := u.StorePath + ".next"
	os.Remove(next)
	if err := u.H.Store.BackupTo(next); err != nil {
		return fmt.Errorf("store copy: %w", err)
	}
	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	out, err := exec.CommandContext(cctx, newBin, "migrate", "-db", next).CombinedOutput()
	cancel()
	if err != nil {
		os.Remove(next)
		u.record(Result{Result: "failed", From: u.H.Version, To: m.Version, Reason: "migration failed: " + strings.TrimSpace(string(out))})
		u.H.Store.SetMeta("upgrade_skip", m.Version)
		return fmt.Errorf("migration: %v: %s", err, out)
	}
	// 4. Swap binary and store, then re-exec.
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, _ = filepath.EvalSymlinks(exe)
	prevBin := filepath.Join(binDir, "hub.prev")
	if err := copyFile(exe, prevBin, 0o755); err != nil {
		return fmt.Errorf("saving previous binary: %w", err)
	}
	mk := Marker{State: "swapped", From: u.H.Version, To: m.Version, ExePath: exe, PrevBinary: prevBin,
		PrevStore: u.StorePath + ".prev", StartedAt: store.Now()}
	if err := writeMarker(u.dir("upgrade.json"), mk); err != nil {
		return err
	}
	dk, err := u.H.Store.DK()
	if err != nil {
		return err
	}
	ok = true
	slog.Info("upgrading", "from", u.H.Version, "to", m.Version)
	u.Shutdown()
	if err := swapStore(u.StorePath, next, mk.PrevStore); err != nil {
		slog.Error("store swap failed", "err", err)
		os.Exit(1) // previous binary + store are untouched; the service manager restarts us
	}
	if err := replaceBinary(newBin, exe); err != nil {
		swapStore(u.StorePath, mk.PrevStore, next) // put the old store back
		os.Remove(u.dir("upgrade.json"))
		slog.Error("binary swap failed", "err", err)
		os.Exit(1)
	}
	return Reexec(exe, dk)
}

func (u *Upgrader) fetchManifest(ctx context.Context) (*Manifest, error) {
	raw, err := u.get(ctx, "release.json")
	if err != nil {
		return nil, err
	}
	sig, err := u.get(ctx, "release.json.sig")
	m := &Manifest{}
	if jerr := json.Unmarshal(raw, m); jerr != nil {
		return nil, fmt.Errorf("release manifest: %w", jerr)
	}
	if err != nil || !Verify(u.H.Cfg.ReleasePublicKey, raw, sig) {
		if CompareVersions(m.Version, u.H.Version) > 0 && u.H.Store.Meta("upgrade_unsigned") != m.Version {
			u.record(Result{Result: "ignored", From: u.H.Version, To: m.Version, Reason: "release signature missing or invalid"})
			u.H.Store.SetMeta("upgrade_unsigned", m.Version)
		}
		return nil, nil
	}
	return m, nil
}

func (u *Upgrader) get(ctx context.Context, name string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(u.H.Cfg.ReleaseChannel, "/")+"/"+name, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("release channel %s: HTTP %d", name, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 512<<20))
}

// Verify checks a base64 ed25519 signature with a base64 public key.
func Verify(pubB64 string, msg, sigB64 []byte) bool {
	pub, err := base64.StdEncoding.DecodeString(strings.TrimSpace(pubB64))
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return false
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sigB64)))
	if err != nil {
		return false
	}
	return ed25519.Verify(pub, msg, sig)
}

// InStage decides from the hub ID whether this hub is in the rollout stage.
func InStage(m *Manifest, hubID string) bool {
	if slices.Contains(m.Stage.HubIDs, hubID) {
		return true
	}
	if m.Stage.Percent <= 0 && len(m.Stage.HubIDs) > 0 {
		return false
	}
	if m.Stage.Percent == 0 && len(m.Stage.HubIDs) == 0 {
		return true // no stage given: everyone
	}
	h := fnv.New32a()
	h.Write([]byte(hubID))
	return int(h.Sum32()%100) < m.Stage.Percent
}

// CompareVersions compares dotted numeric versions ("0.2.10" > "0.2.9").
func CompareVersions(a, b string) int {
	pa, pb := strings.Split(strings.TrimPrefix(a, "v"), "."), strings.Split(strings.TrimPrefix(b, "v"), ".")
	for i := 0; i < max(len(pa), len(pb)); i++ {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(strings.SplitN(pa[i], "-", 2)[0])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(strings.SplitN(pb[i], "-", 2)[0])
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

// --- boot side ------------------------------------------------------------------------

// AfterBoot runs on every start. If an upgrade just swapped in this binary, it health-
// checks and either confirms or rolls back. If a rollback just happened, it records
// the failure for the heartbeat.
func (u *Upgrader) AfterBoot(ctx context.Context, selfStatusURL string) {
	path := u.dir("upgrade.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var mk Marker
	if json.Unmarshal(b, &mk) != nil {
		os.Remove(path)
		return
	}
	switch mk.State {
	case "rolledback":
		os.Remove(path)
		u.H.Store.SetMeta("upgrade_skip", mk.To)
		u.record(Result{Result: "failed", From: mk.From, To: mk.To, Reason: "health check failed, rolled back: " + mk.Reason})
		return
	case "swapped":
	default:
		os.Remove(path)
		return
	}
	reason := u.healthCheck(ctx, selfStatusURL)
	if reason == "" {
		os.Remove(path)
		os.Remove(mk.PrevStore)
		u.record(Result{Result: "ok", From: mk.From, To: mk.To})
		return
	}
	slog.Error("upgrade health check failed, rolling back", "reason", reason)
	mk.State, mk.Reason = "rolledback", reason
	writeMarker(path, mk)
	dk, _ := u.H.Store.DK()
	u.Shutdown()
	failed := u.StorePath + ".failed"
	if err := swapStore(u.StorePath, mk.PrevStore, failed); err != nil {
		slog.Error("rollback: store restore failed", "err", err)
	}
	if err := replaceBinary(mk.PrevBinary, mk.ExePath); err != nil {
		slog.Error("rollback: binary restore failed", "err", err)
		os.Exit(1)
	}
	if err := Reexec(mk.ExePath, dk); err != nil {
		slog.Error("rollback: re-exec failed", "err", err)
		os.Exit(1)
	}
}

// healthCheck: store opens, central reachable, /hub/status responds (spec 12a).
func (u *Upgrader) healthCheck(ctx context.Context, statusURL string) string {
	if u.HealthFail {
		return "forced failure (test build)"
	}
	if err := u.H.Store.DB.PingContext(ctx); err != nil {
		return "store: " + err.Error()
	}
	var n int
	if err := u.H.Store.DB.QueryRowContext(ctx, `SELECT count(*) FROM objects`).Scan(&n); err != nil {
		return "store: " + err.Error()
	}
	if !u.H.Monitor.Probe(ctx, u.H.Central.BaseURL()) {
		return "central not reachable"
	}
	client := &http.Client{Timeout: 10 * time.Second, Transport: insecureTransport()}
	var lastErr error
	for i := 0; i < 20; i++ {
		resp, err := client.Get(statusURL)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return ""
			}
			lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
		} else {
			lastErr = err
		}
		time.Sleep(500 * time.Millisecond)
	}
	return "/hub/status: " + lastErr.Error()
}

// --- files and exec -----------------------------------------------------------------------

func writeMarker(path string, mk Marker) error {
	b, _ := json.MarshalIndent(mk, "", "  ")
	return os.WriteFile(path, b, 0o600)
}

func copyFile(src, dst string, mode os.FileMode) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, b, mode); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

// replaceBinary atomically puts src at dst (copy next to dst, then rename).
func replaceBinary(src, dst string) error { return copyFile(src, dst, 0o755) }

// swapStore moves live -> saveAs and incoming -> live. The store must be closed.
func swapStore(live, incoming, saveAs string) error {
	for _, sfx := range []string{"-wal", "-shm"} {
		os.Remove(live + sfx)
	}
	os.Remove(saveAs)
	if err := os.Rename(live, saveAs); err != nil {
		return err
	}
	if err := os.Rename(incoming, live); err != nil {
		os.Rename(saveAs, live)
		return err
	}
	return nil
}

// DKEnv names the inherited file descriptor that carries the data key across a
// re-exec, so an upgrade does not lock the store.
const DKEnv = "HUB_DK_FD"

// Reexec replaces this process with exe, handing the data key over a pipe.
func Reexec(exe string, dk []byte) error {
	env := os.Environ()
	if len(dk) > 0 {
		r, w, err := os.Pipe()
		if err != nil {
			return err
		}
		if _, err := w.Write([]byte(hex.EncodeToString(dk))); err != nil {
			return err
		}
		w.Close()
		fd := int(r.Fd())
		if _, _, e := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), syscall.F_SETFD, 0); e != 0 {
			return e
		}
		env = append(env, fmt.Sprintf("%s=%d", DKEnv, fd))
	}
	return syscall.Exec(exe, os.Args, env)
}

// HandedOverDK reads a data key passed by Reexec, if any.
func HandedOverDK() []byte {
	s := os.Getenv(DKEnv)
	if s == "" {
		return nil
	}
	os.Unsetenv(DKEnv)
	fd, err := strconv.Atoi(s)
	if err != nil {
		return nil
	}
	f := os.NewFile(uintptr(fd), "dk")
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil {
		return nil
	}
	dk, err := hex.DecodeString(strings.TrimSpace(string(b)))
	if err != nil {
		return nil
	}
	return dk
}
