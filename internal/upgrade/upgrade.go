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
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/dhis2/facility-hub/internal/central"
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
	// CentralURL moves every hub reading this manifest to a new central URL (a
	// domain migration), without a new image or a site visit. Signed like the rest.
	CentralURL string `json:"centralUrl,omitempty"`
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
	if m != nil && m.CentralURL != "" {
		u.followCentral(ctx, m.CentralURL)
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
	bin, err := u.get(ctx, m.Files[plat], 512<<20)
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
	removeStore(next)
	if err := u.H.Store.BackupTo(next); err != nil {
		return fmt.Errorf("store copy: %w", err)
	}
	dk, err := u.H.Store.DK()
	if err != nil {
		return err
	}
	// The data file's migrations need the data key: hand it over a pipe, as Reexec does.
	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	cmd := exec.CommandContext(cctx, newBin, "migrate", "-db", next)
	keyPipe, err := dkPipe(dk)
	if err != nil {
		cancel()
		return err
	}
	cmd.ExtraFiles = []*os.File{keyPipe}
	cmd.Env = append(os.Environ(), DKEnv+"=3")
	out, err := cmd.CombinedOutput()
	keyPipe.Close()
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

// fetchManifest returns the release manifest, or nil if it is not correctly signed.
// Nothing from an unsigned manifest is used or stored: the channel may be plain HTTP,
// so its content is only trusted once the signature checks out (REVIEW-2 F2).
func (u *Upgrader) fetchManifest(ctx context.Context) (*Manifest, error) {
	raw, err := u.get(ctx, "release.json", 64<<10)
	if err != nil {
		return nil, err
	}
	sig, err := u.get(ctx, "release.json.sig", 1<<10)
	if err != nil || !Verify(u.H.Cfg.ReleasePublicKey, raw, sig) {
		sum := sha256.Sum256(raw)
		id := hex.EncodeToString(sum[:8])
		if u.H.Store.Meta("upgrade_unsigned") != id { // report each such manifest once
			u.record(Result{Result: "ignored", From: u.H.Version, Reason: "release channel served a manifest without a valid signature"})
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

// followCentral switches the hub to the central URL a signed manifest names, once
// the new server knows this hub: its registry entry is there and the hub token works.
// The release key can already ship any code, so it may also move hubs; https is
// required all the same, except for a local test server.
func (u *Upgrader) followCentral(ctx context.Context, target string) {
	h := u.H
	target = strings.TrimRight(target, "/")
	if target == h.Central.BaseURL() || h.Runtime() == nil {
		return
	}
	pu, err := url.Parse(target)
	if err != nil || pu.Hostname() == "" || (pu.Scheme != "https" && !isLoopback(pu.Hostname())) {
		slog.Warn("release manifest names an unusable central URL; staying", "url", target)
		return
	}
	c := central.NewClient(target, central.NewMonitor(h.Cfg.PingInterval, h.Cfg.PingTimeout))
	var reg struct {
		HubID string `json:"hubId"`
	}
	if err := c.JSON(ctx, "GET", "/api/dataStore/facility-hub-registry/"+url.PathEscape(h.HubID()), nil, h.HubAuth(), &reg); err != nil || reg.HubID != h.HubID() {
		slog.Warn("new central URL does not know this hub yet; staying", "url", target, "err", err)
		return
	}
	if err := h.Store.SetMeta(core.MetaCentralURL, target); err != nil {
		slog.Error("saving new central URL", "err", err)
		return
	}
	h.Central.SetBaseURL(target)
	slog.Info("moved to new central URL from signed release manifest", "url", target)
}

func isLoopback(host string) bool {
	ip := net.ParseIP(host)
	return host == "localhost" || (ip != nil && ip.IsLoopback())
}

// get fetches a file from the release channel, refusing more than limit bytes.
func (u *Upgrader) get(ctx context.Context, name string, limit int64) ([]byte, error) {
	// The check runs inside the sync cycle; a hung channel must not stall push and pull.
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
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
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err == nil && int64(len(b)) > limit {
		return nil, fmt.Errorf("release channel %s: larger than %d bytes", name, limit)
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

// replaceBinary atomically puts src at dst (copy next to dst, then rename).
func replaceBinary(src, dst string) error { return copyFile(src, dst, 0o755) }

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

// dkPipe returns the read end of a pipe that yields the data key, hex encoded.
func dkPipe(dk []byte) (*os.File, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer w.Close()
	if _, err := w.Write([]byte(hex.EncodeToString(dk))); err != nil {
		r.Close()
		return nil, err
	}
	return r, nil
}

// DKEnv names the inherited file descriptor that carries the data key across a
// re-exec, so an upgrade does not lock the store.
const DKEnv = "HUB_DK_FD"

// Reexec replaces this process with exe, handing the data key over a pipe.
func Reexec(exe string, dk []byte) error {
	env := os.Environ()
	if len(dk) > 0 {
		r, err := dkPipe(dk)
		if err != nil {
			return err
		}
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
