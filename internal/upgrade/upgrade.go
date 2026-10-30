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
	"bytes"
	"cmp"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/dhis2/facility-hub/internal/atomicfile"
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
	"sync"
	"sync/atomic"
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
	Starts     int    `json:"starts,omitempty"` // starts of To before it reached serving (CountStart)
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

	downloading atomic.Bool // one release download at a time, beside the sync cycle
	download    sync.WaitGroup
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
	// The release list changed: forget which manifests were refused, so a republished
	// release is fetched again (G3).
	var choiceKey string
	if b, err := json.Marshal(c); err == nil {
		sum := sha256.Sum256(b)
		choiceKey = hex.EncodeToString(sum[:8])
	}
	if u.H.Store.Meta("upgrade_choice") != choiceKey {
		u.H.Store.SetMeta("upgrade_choice", choiceKey)
		u.H.Store.SetMeta("upgrade_refused", "")
	}
	if u.H.Store.Meta("upgrade_refused") == c.Version {
		return nil // this manifest was refused once; not fetched again until the list changes (G3)
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
		if u.H.Store.Meta("upgrade_refused") != c.Version { // record it once (G3)
			u.record(Result{Result: "ignored", From: u.H.Version, To: c.Version, Reason: "the release for " + c.Version + " says it is " + m.Version})
			u.H.Store.SetMeta("upgrade_refused", c.Version)
		}
		return nil
	}
	plat := runtime.GOOS + "/" + runtime.GOARCH
	if m.Files[plat] == "" || m.SHA256[plat] == "" {
		return fmt.Errorf("release %s has no binary for %s", m.Version, plat)
	}
	// 1. The binary is downloaded beside the sync cycle, never inside it: a slow link
	// may take as long as it needs without holding the cycle (security review
	// 2026-10-20, S13). A later cycle finds it verified and upgrades.
	binDir := u.dir("bin")
	newBin := filepath.Join(binDir, binaryName(m.Version, runtime.GOOS))
	if !hasSHA256(newBin, m.SHA256[plat]) {
		u.startDownload("v"+m.Version+"/"+m.Files[plat], newBin, m.Version, m.SHA256[plat])
		return nil
	}
	// 2. Push the outbox until empty (or only rejected entries remain). Entries whose
	// author must log in online again may never push; they carry over in the store
	// copy, so they do not hold back the upgrade, which may be a security fix
	// (REVIEW-2 F3). The status page and heartbeat report them as stuck.
	// On a central version this release does not support, nothing can be pushed: the
	// outbox goes over in the store copy, and the new release pushes it (N13).
	if u.H.API() == nil {
		slog.Warn("upgrading without pushing the outbox: this release does not support central's version",
			"central", u.H.ServerVersion(), "pending", u.H.Store.CountOutbox(store.OutboxPending))
	} else {
		pending, err := u.Drain(ctx)
		if err != nil {
			return fmt.Errorf("outbox not drained: %w", err)
		}
		if stuck := u.H.StuckOutbox(); pending > stuck {
			return fmt.Errorf("outbox not drained (%d pending)", pending)
		} else if stuck > 0 {
			slog.Warn("upgrading with stuck outbox entries; their authors must log in online", "entries", stuck)
		}
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
	next, prev := u.StorePath+".next", u.StorePath+".prev"
	removeStore(next)
	removeStore(prev) // the swap moves the live store here, and Recover moves it back
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
	if err := atomicfile.Copy(exe, prevBin, 0o755); err != nil {
		return fmt.Errorf("saving previous binary: %w", err)
	}
	mk := Marker{State: "swapped", From: u.H.Version, To: m.Version, ExePath: exe, PrevBinary: prevBin,
		PrevStore: prev, StartedAt: store.Now()}
	if err := writeMarker(u.dir("upgrade.json"), mk); err != nil {
		return err
	}
	ok = true
	// A swap that fails exits and restarts on the old binary: AfterBoot then lets this
	// version be tried once more, not every cycle.
	slog.Info("upgrading", "from", u.H.Version, "to", m.Version)
	u.Shutdown()
	// From here a failure exits, and the service manager starts the hub again: on the
	// old binary, Recover puts the old store back; on the new one, AfterBoot checks it.
	if err := swapStore(u.StorePath, next, prev); err != nil {
		slog.Error("store swap failed", "err", err)
		os.Exit(1)
	}
	if err := replaceBinary(newBin, exe); err != nil {
		slog.Error("binary swap failed", "err", err)
		os.Exit(1)
	}
	err = Reexec(exe, dk)
	slog.Error("re-exec failed", "err", err)
	os.Exit(1)
	return err
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
		if u.H.Store.Meta("upgrade_refused") != version { // report each such manifest once, and fetch it no more (G3)
			u.record(Result{Result: "ignored", From: u.H.Version, To: version, Reason: "the release has no valid signature"})
			u.H.Store.SetMeta("upgrade_refused", version)
		}
		return nil, nil
	}
	m := &Manifest{}
	if err := json.Unmarshal(raw, m); err != nil {
		return nil, fmt.Errorf("release manifest: %w", err)
	}
	return m, nil
}

// get fetches a small file from the release source, refusing more than limit bytes.
func (u *Upgrader) get(ctx context.Context, name string, limit int64) ([]byte, error) {
	var b bytes.Buffer
	err := u.fetch(ctx, name, limit, &b)
	return b.Bytes(), err
}

// fetch copies a file from the release source to w, refusing more than limit bytes. A
// download that stops delivering is given up after downloadIdle, while a slow one that
// keeps going may take as long as it needs.
func (u *Upgrader) fetch(ctx context.Context, name string, limit int64, w io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	idle := time.AfterFunc(downloadIdle, cancel)
	defer idle.Stop()
	req, err := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(u.Base, "/")+"/"+name, nil)
	if err != nil {
		return err
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = central.Proxy
	resp, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("release source %s: HTTP %d", name, resp.StatusCode)
	}
	n, err := io.Copy(w, io.LimitReader(progressReader{resp.Body, idle}, limit+1))
	if err == nil && n > limit {
		return fmt.Errorf("release source %s: larger than %d bytes", name, limit)
	}
	return err
}

// startDownload fetches a release binary to dst in the background, unless a download
// is already running. dst appears only once its checksum matches the signed manifest;
// a mismatch is recorded and that version is not downloaded again.
func (u *Upgrader) startDownload(name, dst, version, sum string) {
	if !u.downloading.CompareAndSwap(false, true) {
		return
	}
	u.download.Add(1)
	go func() {
		defer u.download.Done()
		defer u.downloading.Store(false)
		err := u.downloadTo(name, dst, sum)
		if errors.Is(err, errChecksum) {
			u.record(Result{Result: "ignored", From: u.H.Version, To: version, Reason: "binary checksum does not match signed manifest"})
			u.H.Store.SetMeta("upgrade_skip", version) // not downloaded again every cycle
		}
		if err != nil {
			slog.Warn("upgrade download", "version", version, "err", err)
			return
		}
		slog.Info("upgrade downloaded", "version", version)
		u.H.TriggerSync() // upgrade now rather than at the next cycle
	}()
}

var errChecksum = errors.New("checksum mismatch")

func (u *Upgrader) downloadTo(name, dst, sum string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	part := dst + ".part"
	f, err := os.OpenFile(part, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	defer os.Remove(part)
	h := sha256.New()
	err = u.fetch(context.Background(), name, 512<<20, io.MultiWriter(f, h))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if hex.EncodeToString(h.Sum(nil)) != strings.ToLower(sum) {
		return errChecksum
	}
	return os.Rename(part, dst)
}

// WaitDownload waits for a running release download (tests).
func (u *Upgrader) WaitDownload() { u.download.Wait() }

// hasSHA256 reports whether the file at path exists with that checksum.
func hasSHA256(path, sum string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return false
	}
	return hex.EncodeToString(h.Sum(nil)) == strings.ToLower(sum)
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

// CompareVersions compares versions by semantic versioning's precedence: dotted
// numbers ("0.2.10" > "0.2.9"), and a pre-release before its release ("0.3.0-rc1" <
// "0.3.0"), so a hub on a release candidate takes the release.
func CompareVersions(a, b string) int {
	ca, pa, _ := strings.Cut(strings.TrimPrefix(a, "v"), "-")
	cb, pb, _ := strings.Cut(strings.TrimPrefix(b, "v"), "-")
	if c := compareFields(strings.Split(ca, "."), strings.Split(cb, "."), true); c != 0 {
		return c
	}
	switch {
	case pa == pb:
		return 0
	case pa == "":
		return 1
	case pb == "":
		return -1
	}
	return compareFields(strings.Split(pa, "."), strings.Split(pb, "."), false)
}

// compareFields compares dot-separated fields: numbers as numbers, below words, which
// compare as text. Missing release fields count as 0; a pre-release with fewer fields
// comes first.
func compareFields(fa, fb []string, release bool) int {
	for i := 0; i < max(len(fa), len(fb)); i++ {
		if i >= len(fa) || i >= len(fb) {
			if release {
				x, y := "0", "0"
				if i < len(fa) {
					x = fa[i]
				}
				if i < len(fb) {
					y = fb[i]
				}
				if c := compareField(x, y); c != 0 {
					return c
				}
				continue
			}
			return cmp.Compare(len(fa), len(fb))
		}
		if c := compareField(fa[i], fb[i]); c != 0 {
			return c
		}
	}
	return 0
}

func compareField(x, y string) int {
	nx, ex := strconv.Atoi(x)
	ny, ey := strconv.Atoi(y)
	switch {
	case ex == nil && ey == nil:
		return cmp.Compare(nx, ny)
	case ex == nil:
		return -1
	case ey == nil:
		return 1
	}
	return strings.Compare(x, y)
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
		u.record(Result{Result: "failed", From: mk.From, To: mk.To, Reason: "rolled back: " + mk.Reason})
		return
	case "swapped":
		if mk.To != u.H.Version {
			// The old binary runs: the upgrade stopped before the new one started (a
			// power cut, say), and Recover put the old store back. Such a stop says
			// nothing against the release, which may be the only one there is: it is
			// tried once more, and skipped after a second stop.
			os.Remove(path)
			reason := "the upgrade stopped before " + mk.To + " started"
			if u.H.Store.Meta("upgrade_cut") == mk.To {
				u.H.Store.SetMeta("upgrade_skip", mk.To)
				reason += ", a second time"
			} else {
				u.H.Store.SetMeta("upgrade_cut", mk.To)
				reason += "; it is tried once more"
			}
			u.record(Result{Result: "failed", From: mk.From, To: mk.To, Reason: reason})
			return
		}
	default:
		os.Remove(path)
		return
	}
	if !u.H.Store.Unlocked() {
		// Started locked (the key was not handed over): the migrated data has not been
		// opened yet. Check it at the first unlock, and keep the old store till then.
		slog.Warn("upgraded hub started locked; the upgrade is checked at the first staff login", "to", mk.To)
		mk.Starts = 0 // it started: restarts while locked say nothing against it (CountStart)
		writeMarker(path, mk)
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

// Unconfirmed reports whether this start runs a version an upgrade has swapped in and
// not yet health-checked. Until the check passes, the hub takes no local writes and
// pushes nothing (core.FreezeLocalWrites): a rollback puts the old store back, which
// would lose the one and push the other a second time (adversarial N5).
func Unconfirmed(dataDir, version string) bool {
	b, err := os.ReadFile(filepath.Join(dataDir, "upgrade.json"))
	var mk Marker
	return err == nil && json.Unmarshal(b, &mk) == nil && mk.State == "swapped" && mk.To == version
}

// confirm health-checks a swapped-in version: it keeps it, or rolls back.
func (u *Upgrader) confirm(ctx context.Context, selfStatusURL, path string, mk Marker) {
	reason := u.healthCheck(ctx, selfStatusURL)
	if reason == "" {
		os.Remove(path)
		removeStore(mk.PrevStore)
		u.record(Result{Result: "ok", From: mk.From, To: mk.To})
		u.H.ThawLocalWrites() // frozen since the start (Unconfirmed)
		return
	}
	slog.Error("upgrade health check failed, rolling back", "reason", reason)
	mk.State, mk.Reason = "rolledback", "health check failed: "+reason
	if err := writeMarker(path, mk); err != nil {
		slog.Error("rollback: marker not written; staying on this version", "err", err)
		return
	}
	dk, _ := u.H.Store.DK()
	u.Shutdown()
	// From here a failure exits, and Recover finishes the rollback at the next start.
	if err := swapStore(u.StorePath, mk.PrevStore, u.StorePath+".next"); err != nil {
		slog.Error("rollback: store restore failed", "err", err)
		os.Exit(1)
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

// healthCheck: the store opens and /hub/status responds. Central is not part of it: a
// good release that met a slow or unreachable central would roll back (adversarial
// N5), and the hub works offline anyway.
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
	return atomicfile.Write(path, b, 0o600)
}

// Recover runs at start-up, before the store is opened. An upgrade cut short (a power
// cut, a crash, a failed swap or re-exec) leaves the marker and the store's files
// (live hub.db, the migrated copy hub.db.next, the old store hub.db.prev) in one of
// these states; Recover ends each on the old release with the old store, or the new
// release with the new store:
//
//	marker      running  files                                   Recover
//	none        any      live                                    nothing
//	swapped     To       live = new, prev = old (swap complete)  nothing; AfterBoot checks it
//	swapped     other    the swap anywhere from not begun to     swaps back: the old store
//	                     complete (the binary was not replaced)  is live again, AfterBoot
//	                                                             reports the failure
//	rolledback  From     the swap back anywhere, or complete     finishes the swap back
//	rolledback  To       same                                    finishes it, puts the old
//	                                                             binary back and exits
//
// The swap moves live to prev, then next to live, one file at a time; swapping back
// moves live to next, then prev to live. From any point of either, swapping back ends
// with the old store live (swapStore).
func Recover(dataDir, storePath, version string) error {
	b, err := os.ReadFile(filepath.Join(dataDir, "upgrade.json"))
	if err != nil {
		return nil
	}
	var mk Marker
	if json.Unmarshal(b, &mk) != nil || mk.State == "swapped" && version == mk.To {
		return nil
	}
	if err := swapStore(storePath, mk.PrevStore, storePath+".next"); err != nil {
		return fmt.Errorf("upgrade: putting the old store back: %w", err)
	}
	if mk.State == "rolledback" && version == mk.To {
		if err := replaceBinary(mk.PrevBinary, mk.ExePath); err != nil {
			return fmt.Errorf("upgrade: putting the old binary back: %w", err)
		}
		return errors.New("upgrade: the old binary is back; exiting so that it is started")
	}
	return nil
}

// maxStarts is how many times a swapped-in release may start without reaching its
// health check (or, locked, serving) before it is rolled back.
const maxStarts = 3

// CountStart runs first thing at each start, before anything that may fail. A release
// an upgrade swapped in that exits before its health check (a panic, a store or port it
// cannot open) is started again and again by the service manager, and the health check
// that would roll it back never runs (adversarial G2). Each such start is counted in the
// marker; once maxStarts are spent, the upgrade is marked rolled back and Recover puts
// the old store and binary back. The error then asks the caller to exit, so that the
// service manager starts the old release, which reports the failure and skips the
// version (AfterBoot).
func CountStart(dataDir, storePath, version string) error {
	path := filepath.Join(dataDir, "upgrade.json")
	b, err := os.ReadFile(path)
	var mk Marker
	if err != nil || json.Unmarshal(b, &mk) != nil || mk.State != "swapped" || mk.To != version {
		return nil
	}
	mk.Starts++
	if mk.Starts > maxStarts {
		mk.State, mk.Reason = "rolledback", fmt.Sprintf("%s did not start (%d tries)", mk.To, maxStarts)
		slog.Error("upgrade: this release does not start, rolling back", "to", mk.To, "from", mk.From)
	}
	if err := writeMarker(path, mk); err != nil {
		return nil // nothing is counted on a disk that takes no writes; keep starting
	}
	return Recover(dataDir, storePath, version)
}

// swapStore moves live to saveAs and incoming to live, the key file and then its data
// file. The store must be closed, and saveAs free. Each move skips what is already
// done, so swapStore(live, saveAs, incoming) undoes a swap cut short at any point.
func swapStore(live, incoming, saveAs string) error {
	for _, m := range storeMoves(live, incoming, saveAs) {
		if err := moveFile(m[0], m[1]); err != nil {
			return err
		}
	}
	// The renames are durable only once the folder is synced (best effort, as in
	// atomicfile): before the binary is replaced, which may be on another disk.
	if d, err := os.Open(filepath.Dir(live)); err == nil {
		d.Sync()
		d.Close()
	}
	return nil
}

func storeMoves(live, incoming, saveAs string) [][2]string {
	return [][2]string{{live, saveAs}, {store.DataPath(live), store.DataPath(saveAs)},
		{incoming, live}, {store.DataPath(incoming), store.DataPath(live)}}
}

// moveFile renames a store file with its -wal and -shm, if any: a store closed cleanly
// has none, but one that was not keeps saves in its -wal. Without from, or with to
// already there, the move is done or not due.
func moveFile(from, to string) error {
	if !exists(from) || exists(to) {
		return nil
	}
	for _, sfx := range []string{"-wal", "-shm", ""} {
		if err := os.Rename(from+sfx, to+sfx); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
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

// binaryName is the downloaded version's file name: with .exe on Windows, which runs
// programs only by that name.
func binaryName(version, goos string) string {
	if goos == "windows" {
		return "hub-" + version + ".exe"
	}
	return "hub-" + version
}
