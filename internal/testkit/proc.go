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
	dir := e.T.TempDir()
	p := &Proc{e: e, bin: filepath.Join(dir, "hub"), cfg: filepath.Join(dir, "hub.yaml"), Log: filepath.Join(dir, "hub.log"),
		SyncInterval: 5 * time.Second, PingInterval: 2 * time.Second}
	build := exec.Command("go", "build", "-o", p.bin, "github.com/dhis2/facility-hub/cmd/hub")
	if out, err := build.CombinedOutput(); err != nil {
		e.T.Fatalf("building the hub: %v\n%s", err, out)
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
	if err := p.cmd.Start(); err != nil {
		p.e.T.Fatal(err)
	}
	p.exited = make(chan struct{})
	go func(cmd *exec.Cmd, ch chan struct{}) { cmd.Wait(); logf.Close(); close(ch) }(p.cmd, p.exited)
	p.WaitFor("the hub answers", time.Minute, func(map[string]any) bool { return true })
}

// Kill ends the process with SIGKILL, as a power cut: nothing is flushed or closed.
func (p *Proc) Kill() { p.signal(syscall.SIGKILL) }

// Stop ends it with SIGTERM, as a clean shutdown.
func (p *Proc) Stop() { p.signal(syscall.SIGTERM) }

func (p *Proc) signal(sig syscall.Signal) {
	if !p.Running() {
		return
	}
	p.cmd.Process.Signal(sig)
	<-p.exited
}

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
