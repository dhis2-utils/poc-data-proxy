package testkit

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Proc is the Env's hub run as the real binary (cmd/hub) in its own process, on the
// Env's data directory and uplink, so a test can kill it hard (kill -9, as a power
// cut) and start it again. Clients keep using e.Srv.URL: a small front proxy there
// passes requests to the process and, while it is down, drops the connections as a
// dead hub would.
type Proc struct {
	e    *Env
	bin  string
	cfg  string
	addr string // the process's own address
	Log  string // its log (standard error, where the hub logs on Linux)
	// SyncInterval and PingInterval are the process's cycles: short, as tests wait.
	SyncInterval, PingInterval time.Duration

	cmd    *exec.Cmd
	exited chan struct{}
}

// Detach moves the Env's hub to a separate process: the in-process hub stops (its data
// directory stays), and the process starts with the background sync and probes the
// in-process hub does not run. e.App is nil afterwards: use the Proc's Status.
func (e *Env) Detach() *Proc {
	e.T.Helper()
	bin := filepath.Join(e.T.TempDir(), "hub")
	BuildHub(e.T, "", bin, "")
	return e.DetachBinary(bin, false)
}

// BuildHub builds cmd/hub of the module in src ("" for this one) to out, with these
// -ldflags (e.g. "-X main.version=0.2.0"). A tree built through a go.work (the
// sandbox's, with local copies of modules) is given the same.
func BuildHub(t testing.TB, src, out, ldflags string) {
	t.Helper()
	if src != "" {
		if _, err := os.Stat(filepath.Join(src, "go.work")); err != nil {
			if w, _ := exec.Command("go", "env", "GOWORK").Output(); len(strings.TrimSpace(string(w))) > 0 {
				b, err := os.ReadFile(strings.TrimSpace(string(w)))
				if err != nil || os.WriteFile(filepath.Join(src, "go.work"), b, 0o644) != nil {
					t.Fatalf("copying %s: %v", w, err)
				}
			}
		}
	}
	build := exec.Command("go", "build", "-ldflags", ldflags, "-o", out, "github.com/dhis2/facility-hub/cmd/hub")
	build.Dir = src
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building the hub in %q: %v\n%s", src, err, out)
	}
}

// DetachRelease runs an older release's binary as the Env's hub, on an empty data
// directory, to be set up through /hub/setup and then upgraded.
func (e *Env) DetachRelease(bin string) *Proc { return e.DetachBinary(bin, true) }

// DetachBinary is Detach with a hub binary of the test's choosing, such as an older
// release. The process runs a copy of it, which an upgrade may replace. With empty,
// the process starts on an empty data directory, to be set up through it: an older
// release cannot open a store this tree's hub created.
func (e *Env) DetachBinary(bin string, empty bool) *Proc {
	e.T.Helper()
	dir := e.T.TempDir()
	p := &Proc{e: e, bin: filepath.Join(dir, "hub"), cfg: filepath.Join(dir, "hub.yaml"), Log: filepath.Join(dir, "hub.log"),
		SyncInterval: 5 * time.Second, PingInterval: 2 * time.Second}
	b, err := os.ReadFile(bin)
	if err == nil {
		err = os.WriteFile(p.bin, b, 0o755)
	}
	if err != nil {
		e.T.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		e.T.Fatal(err)
	}
	p.addr = ln.Addr().String()
	ln.Close()

	// The front keeps the address clients know.
	front := e.Srv.Listener.Addr().String()
	e.stop()
	e.App = nil
	if empty {
		ents, _ := os.ReadDir(e.Dir)
		for _, d := range ents {
			if err := os.RemoveAll(filepath.Join(e.Dir, d.Name())); err != nil {
				e.T.Fatal(err)
			}
		}
	}
	target, _ := url.Parse("http://" + p.addr)
	rp := httputil.NewSingleHostReverseProxy(target)
	rp.FlushInterval = -1
	rp.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) { cut(w) }
	l, err := net.Listen("tcp", front)
	if err != nil {
		e.T.Fatal(err)
	}
	e.Srv = httptest.NewUnstartedServer(rp)
	e.Srv.Listener.Close()
	e.Srv.Listener = l
	e.Srv.Start()
	e.Srv.URL = "http://" + front

	p.Start()
	e.T.Cleanup(func() {
		p.Kill()
		if e.T.Failed() {
			e.T.Logf("hub log (last lines):\n%s", p.Tail(60))
		}
	})
	return p
}

func (p *Proc) writeConfig() {
	c := p.e.Config()
	lines := []string{
		"central_url: " + c.CentralURL,
		"listen: " + p.addr,
		"data_dir: " + c.DataDir,
		"tls: false",
		"tpm: false",
		"sync_interval: " + p.SyncInterval.String(),
		"ping_interval: " + p.PingInterval.String(),
		"ping_timeout: " + c.PingTimeout.String(),
		fmt.Sprintf("reserved_pool_size: %d", c.ReservedPoolSize),
		"ownership_check_interval: 1m",
		"access_refresh_interval: 1m",
		"reconnect_delay: 0s",
		"log_requests: true",
	}
	if len(c.HubHosts) > 0 {
		lines = append(lines, "hub_hosts: "+strings.Join(c.HubHosts, ","))
	}
	if err := os.WriteFile(p.cfg, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		p.e.T.Fatal(err)
	}
}

// Start runs the hub process and waits until it answers.
func (p *Proc) Start() {
	p.e.T.Helper()
	p.Launch()
	p.WaitFor("the hub answers", time.Minute, func(map[string]any) bool { return true })
}

// Launch runs the hub process without waiting for it, as a service manager does: it
// may exit at once.
func (p *Proc) Launch() {
	p.e.T.Helper()
	if p.Running() {
		p.e.T.Fatal("hub process already running")
	}
	p.writeConfig()
	logf, err := os.OpenFile(p.Log, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		p.e.T.Fatal(err)
	}
	p.cmd = exec.Command(p.bin, "serve", "-config", p.cfg)
	p.cmd.Stdout, p.cmd.Stderr = logf, logf
	ownGroup(p.cmd) // Kill ends its children too
	if err := p.cmd.Start(); err != nil {
		p.e.T.Fatal(err)
	}
	p.exited = make(chan struct{})
	go func(cmd *exec.Cmd, ch chan struct{}) { cmd.Wait(); logf.Close(); close(ch) }(p.cmd, p.exited)
}

// Kill ends the process with SIGKILL, as a power cut: nothing is flushed or closed,
// and the processes it started (an upgrade's migration) die with it.
func (p *Proc) Kill() {
	if p.Running() {
		killGroup(p.cmd)
		<-p.exited
	}
}

// Stop ends it with SIGTERM, as a clean shutdown.
func (p *Proc) Stop() { p.signal(syscall.SIGTERM) }

func (p *Proc) signal(sig syscall.Signal) {
	if !p.Running() {
		return
	}
	p.cmd.Process.Signal(sig)
	<-p.exited
}

// Bin is the binary the process runs, and starts from: an upgrade replaces it.
func (p *Proc) Bin() string { return p.bin }

func (p *Proc) Running() bool {
	if p.exited == nil {
		return false
	}
	select {
	case <-p.exited:
		return false
	default:
		return true
	}
}

// Status is the process's /hub/status, or nil if it does not answer.
func (p *Proc) Status() map[string]any {
	c := http.Client{Timeout: 5 * time.Second}
	resp, err := c.Get("http://" + p.addr + "/hub/status")
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	var s map[string]any
	if json.NewDecoder(resp.Body).Decode(&s) != nil {
		return nil
	}
	return s
}

// WaitFor waits until the status satisfies ok, and fails the test after timeout.
func (p *Proc) WaitFor(what string, timeout time.Duration, ok func(map[string]any) bool) map[string]any {
	p.e.T.Helper()
	end := time.Now().Add(timeout)
	var s map[string]any
	for time.Now().Before(end) {
		if !p.Running() {
			p.e.T.Fatalf("waiting for %s: the hub process exited\n%s", what, p.Tail(40))
		}
		if s = p.Status(); s != nil && ok(s) {
			return s
		}
		time.Sleep(200 * time.Millisecond)
	}
	p.e.T.Fatalf("waiting for %s: timed out after %s; status %v", what, timeout, s)
	return nil
}

// Online, Offline and Drained are common conditions for WaitFor.
func Online(s map[string]any) bool  { return s["mode"] == "online" }
func Offline(s map[string]any) bool { return s["mode"] == "offline" }

// Drained: online, unlocked and nothing pending.
func Drained(s map[string]any) bool {
	return s["mode"] == "online" && s["locked"] == false && s["pending"] == float64(0)
}

// Tail returns the last n lines of the process's log.
func (p *Proc) Tail(n int) string {
	f, err := os.Open(p.Log)
	if err != nil {
		return ""
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		lines = append(lines, sc.Text())
		if len(lines) > n {
			lines = lines[1:]
		}
	}
	return strings.Join(lines, "\n")
}

// Grep counts the log lines that contain s.
func (p *Proc) Grep(s string) int {
	b, _ := os.ReadFile(p.Log)
	return strings.Count(string(b), s)
}
