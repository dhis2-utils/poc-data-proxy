package testkit

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Central's own lifecycle, for docs/ADVERSARIAL-TESTING.md group H: the instance
// stopped and started through the d2-broker (DHIS2_BROKER_URL and DHIS2_BROKER_TOKEN),
// and its database backed up and restored. The instance is the manifest's central,
// http://dhis2-<name>:8080; its database is dhis2-<name>-db. Without the broker, or on
// the fake, these skip.

func (e *Env) instance() string {
	e.T.Helper()
	if !e.Live() || os.Getenv("DHIS2_BROKER_URL") == "" || os.Getenv("DHIS2_BROKER_TOKEN") == "" {
		e.T.Skip("needs a broker instance as central (DHIS2_BROKER_URL, DHIS2_BROKER_TOKEN)")
	}
	u, err := url.Parse(e.M.CentralURL)
	name, ok := strings.CutPrefix(u.Hostname(), "dhis2-")
	if err != nil || !ok {
		e.T.Skipf("central %s is not a broker instance", e.M.CentralURL)
	}
	return name
}

// broker runs a job on the instance (start, stop) and waits for it.
func (e *Env) broker(op string) {
	e.T.Helper()
	base, tok := os.Getenv("DHIS2_BROKER_URL"), os.Getenv("DHIS2_BROKER_TOKEN")
	call := func(method, path string, out any) error {
		req, _ := http.NewRequest(method, strings.TrimRight(base, "/")+path, nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			return fmt.Errorf("broker %s %s: HTTP %d", method, path, resp.StatusCode)
		}
		return json.NewDecoder(resp.Body).Decode(out)
	}
	var job struct {
		Job struct{ ID, Status, Error string }
	}
	if err := call("POST", "/instances/"+e.instance()+"/"+op, &job); err != nil {
		e.T.Fatal(err)
	}
	for end := time.Now().Add(15 * time.Minute); time.Now().Before(end); time.Sleep(3 * time.Second) {
		var j struct{ Status, Error string }
		if err := call("GET", "/jobs/"+job.Job.ID, &j); err != nil {
			e.T.Fatal(err)
		}
		switch j.Status {
		case "succeeded":
			return
		case "queued", "running":
		default:
			e.T.Fatalf("broker %s: %s %s", op, j.Status, j.Error)
		}
	}
	e.T.Fatalf("broker %s: still running after 15 minutes", op)
}

// StopCentral stops central (Tomcat and its database), as for maintenance. It is
// started again when the test ends, if the test did not.
func (e *Env) StopCentral() {
	e.T.Helper()
	e.broker("stop")
	e.T.Cleanup(func() {
		if st, _, _ := e.Central.Do("GET", "/api/system/info", nil); st != 200 {
			e.StartCentral()
		}
	})
}

// StartCentral starts central and waits until it answers its administrator.
func (e *Env) StartCentral() {
	e.T.Helper()
	e.broker("start")
	for end := time.Now().Add(10 * time.Minute); time.Now().Before(end); time.Sleep(3 * time.Second) {
		if st, _, _ := e.Central.Do("GET", "/api/system/info", nil); st == 200 {
			return
		}
	}
	e.T.Fatal("central does not answer 10 minutes after its start")
}

// BackupCentral dumps central's database, as a nightly backup does, and returns the
// dump's path.
func (e *Env) BackupCentral() string {
	e.T.Helper()
	dump := filepath.Join(e.T.TempDir(), "central.dump")
	e.pg("pg_dump", "-Fc", "-d", "dhis2", "-f", dump)
	return dump
}

// RestoreCentral puts a dump back as central's database and restarts central, as an
// administrator restoring yesterday's backup would: everything written since the dump
// is gone.
func (e *Env) RestoreCentral(dump string) {
	e.T.Helper()
	e.pg("psql", "-d", "postgres", "-v", "ON_ERROR_STOP=1", "-c", "DROP DATABASE dhis2 WITH (FORCE)")
	e.pg("psql", "-d", "postgres", "-v", "ON_ERROR_STOP=1", "-c", "CREATE DATABASE dhis2 OWNER dhis TEMPLATE template0")
	e.pg("pg_restore", "-d", "dhis2", "-j", "4", dump)
	e.broker("stop") // Tomcat's caches still hold what the database no longer does
	e.StartCentral()
}

func (e *Env) pg(tool string, args ...string) {
	e.T.Helper()
	cmd := exec.Command(tool, append([]string{"-h", "dhis2-" + e.instance() + "-db", "-U", "dhis"}, args...)...)
	cmd.Env = append(os.Environ(), "PGPASSWORD=dhis")
	if out, err := cmd.CombinedOutput(); err != nil {
		e.T.Fatalf("%s %v: %v\n%s", tool, args, err, out)
	}
}
