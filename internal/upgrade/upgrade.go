// Package upgrade implements hub self-upgrade (spec 12a): the version the country
// chose on central, signed releases from the supplier's release source, outbox
// drain, migration on a store copy, binary swap, re-exec, health check and rollback.
//
// The country's choice is facility-hub-admin/release on central:
//
//	{"version":"0.3.0","pilotHubs":["hub-…"],"percent":10}
//
// A release on the release source (GitHub: <base>/v<version>/<file>):
//
//	release.json      {"version":"0.3.0","files":{"linux/arm64":"hub-linux-arm64",...},
//	                   "sha256":{"linux/arm64":"<hex>",...},"image":{"file":...,"sha256":...}}
//	release.json.sig  base64 ed25519 signature of release.json, by the supplier's key
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
	"time"

	"github.com/dhis2/facility-hub/internal/central"
	"github.com/dhis2/facility-hub/internal/core"
	"github.com/dhis2/facility-hub/internal/store"
)

// The release source and the supplier's ed25519 public key (base64), set when the
// binary is built (-ldflags -X github.com/dhis2/facility-hub/internal/upgrade.ReleaseKey=…).
// A build without a key never upgrades.
var (
	ReleaseBase = "https://github.com/dhis2-utils/poc-data-proxy/releases/download"
	ReleaseKey  = ""
)

type Manifest struct {
	Version string            `json:"version"`
	Files   map[string]string `json:"files"`
	SHA256  map[string]string `json:"sha256"`
	Image   *Image            `json:"image,omitempty"`
}

// Image is the Pi image published with the release.
type Image struct {
	File   string `json:"file"`
	SHA256 string `json:"sha256"`
}

// Choice is the version the country chose, and which hubs take it now.
type Choice struct {
	Version   string   `json:"version"`
	PilotHubs []string `json:"pilotHubs"`
	Percent   int      `json:"percent"`
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
	// Base and Key: the release source and its public key (ReleaseBase and ReleaseKey
	// unless a test sets them).
	Base, Key string
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
	if u.Base == "" {
		u.Base = ReleaseBase
	}
	if u.Key == "" {
		u.Key = ReleaseKey
	}
	if u.Key == "" {
		return // a development build
	}
	if err := u.check(ctx); err != nil {
		slog.Warn("upgrade check", "err", err)
	}
}

func (u *Upgrader) check(ctx context.Context) error {
	c, err := u.fetchChoice(ctx)
	if err != nil || c == nil || CompareVersions(c.Version, u.H.Version) <= 0 {
		return err
	}
	if skip := u.H.Store.Meta("upgrade_skip"); skip == c.Version {
		return nil // this version already failed here
	}
	if !InStage(*c, u.H.HubID()) {
		slog.Info("upgrade chosen but this hub is not in the current stage", "version", c.Version)
		return nil
	}
	m, err := u.fetchManifest(ctx, c.Version)
	if err != nil || m == nil {
		return err
	}
	if m.Version != c.Version {
		u.record(Result{Result: "ignored", From: u.H.Version, To: c.Version, Reason: "the release for " + c.Version + " says it is " + m.Version})
		return nil
	}
	plat := runtime.GOOS + "/" + runtime.GOARCH
	if m.Files[plat] == "" || m.SHA256[plat] == "" {
		return fmt.Errorf("release %s has no binary for %s", m.Version, plat)
	}
	// 1. Push the outbox until empty (or only rejected entries remain). Entries whose
	// author must log in online again may never push; they carry over in the store
	// copy, so they do not hold back the upgrade, which may be a security fix
	// (REVIEW-2 F3). The status page and heartbeat report them as stuck.
	pending, err := u.Drain(ctx)
	if err != nil {
		return fmt.Errorf("outbox not drained: %w", err)
	}
	if stuck := u.H.StuckOutbox(); pending > stuck {
		return fmt.Errorf("outbox not drained (%d pending)", pending)
	} else if stuck > 0 {
		slog.Warn("upgrading with stuck outbox entries; their authors must log in online", "entries", stuck)
	}
	// 2. Download and verify.
	bin, err := u.get(ctx, "v"+m.Version+"/"+m.Files[plat], 512<<20)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(bin)
	if hex.EncodeToString(sum[:]) != strings.ToLower(m.SHA256[plat]) {
		u.record(Result{Result: "ignored", From: u.H.Version, To: m.Version, Reason: "binary checksum does not match signed manifest"})
		u.H.Store.SetMeta("upgrade_skip", m.Version) // not downloaded again every cycle
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
	removeStore(next)
	if err := u.H.Store.BackupTo(next); err != nil {
		return fmt.Errorf("store copy: %w", err)
	}
	dk, err := u.H.Store.DK()
	if err != nil {
		return err
	}
	// The data file's migrations need the data key: on standard input (on every system;
	// Windows cannot pass other file handles to a child).
	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	cmd := exec.CommandContext(cctx, newBin, "migrate", "-db", next)
	cmd.Stdin = strings.NewReader(hex.EncodeToString(dk))
	cmd.Env = append(os.Environ(), DKEnv+"=stdin")
	out, err := cmd.CombinedOutput()
	cancel()
	if err != nil {
		removeStore(next)
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
	ok = true
	// A swap that fails exits and restarts on the old binary: this version is then not
	// tried again every cycle (after a success the hub runs it, and the mark is moot).
	u.H.Store.SetMeta("upgrade_skip", m.Version)
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

// fetchChoice reads the country's choice from central; nil when there is none.
func (u *Upgrader) fetchChoice(ctx context.Context) (*Choice, error) {
	var c Choice
	err := u.H.Central.JSON(ctx, "GET", "/api/dataStore/facility-hub-admin/release", nil, u.H.HubAuth(), &c)
	var he *central.HTTPError
	if errors.As(err, &he) && he.Status == http.StatusNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("release choice: %w", err)
	}
	if c.Version == "" {
		return nil, nil
	}
	return &c, nil
}

// fetchManifest returns the release manifest of version, or nil if it is not
// correctly signed. Nothing from an unsigned manifest is used (REVIEW-2 F2).
func (u *Upgrader) fetchManifest(ctx context.Context, version string) (*Manifest, error) {
	raw, err := u.get(ctx, "v"+version+"/release.json", 64<<10)
	if err != nil {
		return nil, err
	}
	sig, err := u.get(ctx, "v"+version+"/release.json.sig", 1<<10)
	if err != nil || !Verify(u.Key, raw, sig) {
		sum := sha256.Sum256(raw)
		id := hex.EncodeToString(sum[:8])
		if u.H.Store.Meta("upgrade_unsigned") != id { // report each such manifest once
			u.record(Result{Result: "ignored", From: u.H.Version, To: version, Reason: "the release has no valid signature"})
			u.H.Store.SetMeta("upgrade_unsigned", id)
		}
		return nil, nil
	}
	m := &Manifest{}
	if err := json.Unmarshal(raw, m); err != nil {
		return nil, fmt.Errorf("release manifest: %w", err)
	}
	return m, nil
}

// get fetches a file from the release source, refusing more than limit bytes.
func (u *Upgrader) get(ctx context.Context, name string, limit int64) ([]byte, error) {
	// The check runs inside the sync cycle: a download that stops delivering is given up
	// after downloadIdle, while a slow one that keeps going may take as long as it needs.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	idle := time.AfterFunc(downloadIdle, cancel)
	defer idle.Stop()
	req, err := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(u.Base, "/")+"/"+name, nil)
	if err != nil {
		return nil, err
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = central.Proxy
	resp, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("release source %s: HTTP %d", name, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(progressReader{resp.Body, idle}, limit+1))
	if err == nil && int64(len(b)) > limit {
		return nil, fmt.Errorf("release source %s: larger than %d bytes", name, limit)
	}
	return b, err
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

// InStage decides whether this hub takes the chosen version now: a pilot hub, or one
// whose ID hashes under the percentage (apps/facility-hub/src/hubs/stage.ts does the
// same; testdata/stages.json holds both to it).
func InStage(c Choice, hubID string) bool {
	if slices.Contains(c.PilotHubs, hubID) {
		return true
	}
	h := fnv.New32a()
	h.Write([]byte(hubID))
	return int(h.Sum32()%100) < c.Percent
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
	if !u.H.Store.Unlocked() {
		// Started locked (the key was not handed over): the migrated data has not been
		// opened yet. Check it at the first unlock, and keep the old store till then.
		slog.Warn("upgraded hub started locked; the upgrade is checked at the first staff login", "to", mk.To)
		u.H.AfterUnlock(func() error {
			if _, err := os.Stat(path); err == nil {
				go u.confirm(ctx, selfStatusURL, path, mk)
			}
			return nil
		})
		return
	}
	u.confirm(ctx, selfStatusURL, path, mk)
}

// confirm health-checks a swapped-in version: it keeps it, or rolls back.
func (u *Upgrader) confirm(ctx context.Context, selfStatusURL, path string, mk Marker) {
	reason := u.healthCheck(ctx, selfStatusURL)
	if reason == "" {
		os.Remove(path)
		removeStore(mk.PrevStore)
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
	if err := u.H.Store.DB().PingContext(ctx); err != nil {
		return "store: " + err.Error()
	}
	var n int
	if err := u.H.Store.DB().QueryRowContext(ctx, `SELECT count(*) FROM objects`).Scan(&n); err != nil {
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

// swapStore moves live -> saveAs and incoming -> live, for the key file and its data
// file. The store must be closed.
func swapStore(live, incoming, saveAs string) error {
	for _, f := range []string{live, store.DataPath(live)} {
		for _, sfx := range []string{"-wal", "-shm"} {
			os.Remove(f + sfx)
		}
	}
	removeStore(saveAs)
	if err := os.Rename(live, saveAs); err != nil {
		return err
	}
	if err := os.Rename(store.DataPath(live), store.DataPath(saveAs)); err != nil && !errors.Is(err, os.ErrNotExist) {
		os.Rename(saveAs, live)
		return err
	}
	if err := os.Rename(incoming, live); err != nil {
		os.Rename(saveAs, live)
		os.Rename(store.DataPath(saveAs), store.DataPath(live))
		return err
	}
	if err := os.Rename(store.DataPath(incoming), store.DataPath(live)); err != nil && !errors.Is(err, os.ErrNotExist) {
		os.Rename(live, incoming)
		os.Rename(saveAs, live)
		os.Rename(store.DataPath(saveAs), store.DataPath(live))
		return err
	}
	return nil
}

// removeStore deletes a key file and its data file.
func removeStore(path string) {
	for _, f := range []string{path, store.DataPath(path)} {
		for _, sfx := range []string{"", "-wal", "-shm"} {
			os.Remove(f + sfx)
		}
	}
}

// downloadIdle is how long a download may deliver nothing before it is given up.
var downloadIdle = 2 * time.Minute

// SetDownloadIdle changes downloadIdle (tests) and returns a function restoring it.
func SetDownloadIdle(d time.Duration) func() {
	old := downloadIdle
	downloadIdle = d
	return func() { downloadIdle = old }
}

// progressReader restarts the idle timer whenever bytes arrive.
type progressReader struct {
	r     io.Reader
	timer *time.Timer
}

func (p progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 {
		p.timer.Reset(downloadIdle)
	}
	return n, err
}
