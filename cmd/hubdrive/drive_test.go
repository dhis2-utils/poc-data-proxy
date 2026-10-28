package main

import (
	"context"
	"encoding/pem"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	fc "github.com/dhis2/facility-hub/internal/fakecentral"
	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// TestDriveThroughFaults runs the driver against a hub process (HUB_MANIFEST's
// central, or the fake) while the uplink is cut and the hub killed hard and started
// again, then checks the ledger on central: nothing lost. A save then deleted on
// central must be reported lost.
func TestDriveThroughFaults(t *testing.T) {
	if testing.Short() {
		t.Skip("runs a hub process for about a minute")
	}
	e := tk.Start(t)
	prog := e.M.Primary()
	c := runConfig{Program: prog.ID, OrgUnit: e.M.Facility.ID, TET: prog.TET, FirstAttr: prog.SearchAttr,
		Attrs: map[string]string{}, Password: e.M.Password, Web: true, Android: true, Lo: 30, Hi: 90,
		Pace: time.Second, Tag: "r" + strings.ToLower(tk.UID()[:6]), Ledger: filepath.Join(t.TempDir(), "ledger.jsonl"),
		Timeout: 30 * time.Second, RT: http.DefaultTransport, Log: log.New(testWriter{t}, "", log.Ltime)}
	for a, v := range prog.RequiredAttrs {
		if a != prog.SearchAttr {
			c.Attrs[a], c.LastAttr = v, a
		}
	}
	if !e.Live() {
		c.LastAttr = fc.AttrLastName
	}
	if c.LastAttr == "" || len(prog.MandatoryGenerated) > 0 {
		t.Skip("the primary program needs a second name attribute and no mandatory generated one")
	}
	delete(c.Attrs, c.LastAttr)
	// The registration's stage has a number to change; the event stage is another.
	for _, st := range prog.Stages {
		if st == prog.RestrictedStage {
			continue
		}
		if c.RegStage == "" {
			c.RegStage = st
			c.RegValues = map[string]string{}
			for de, v := range prog.EventValues[st] {
				if _, err := strconv.Atoi(v); err == nil && c.RegDE == "" {
					c.RegDE = de
				} else {
					c.RegValues[de] = v
				}
			}
		} else if c.EventStage == "" {
			c.EventStage, c.EventValues = st, prog.EventValues[st]
		}
	}
	if c.RegDE == "" && !e.Live() {
		c.RegDE = "HubDrvNum01" // the fake takes any data element
	}
	if c.RegDE == "" {
		t.Skip("the primary program's first stage has no numeric value")
	}

	e.Setup()
	for _, role := range []string{tk.RoleNurse, tk.RoleRestricted} {
		if u, ok := e.M.User(role); ok {
			e.Web(role).Login() // a key slot, to unlock the hub after the kill
			c.Users = append(c.Users, u.Username)
		}
	}
	e.Sync()
	p := e.Detach()
	p.WaitFor("online", time.Minute, tk.Online)
	c.Hub = e.Srv.URL

	ctx, stop := context.WithCancel(context.Background())
	done := make(chan *totals)
	go func() {
		tot, err := drive(ctx, c)
		if err != nil {
			t.Error(err)
		}
		done <- tot
	}()
	time.Sleep(10 * time.Second)
	e.Net.SetDown(true) // the SIM pulled
	p.WaitFor("offline", time.Minute, tk.Offline)
	time.Sleep(10 * time.Second)
	t.Log("power cut: kill -9")
	p.Kill()
	time.Sleep(3 * time.Second)
	p.Start() // locked until a client logs in
	time.Sleep(15 * time.Second)
	e.Net.SetDown(false)
	time.Sleep(10 * time.Second)
	stop()
	tot := <-done
	if tot.confirmed() == 0 {
		t.Fatal("no save confirmed")
	}
	p.WaitFor("the outbox to drain", 5*time.Minute, tk.Drained)

	recs, err := readLedger(c.Ledger)
	if err != nil {
		t.Fatal(err)
	}
	var byHub int
	for _, r := range recs {
		if r.Confirmed && r.ByHub {
			byHub++
		}
	}
	d := &dhis{base: e.M.CentralURL, user: e.M.AdminUser, pw: e.M.AdminPassword, http: &http.Client{Timeout: time.Minute}}
	res, err := check(d, recs)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("check: %s; %d confirmed by the hub alone", res, byHub)
	if !res.ok() || byHub == 0 {
		t.Fatalf("want every save on central and some confirmed offline: lost %v partial %v duplicates %v wrong %v",
			res.Lost, res.Partial, res.Duplicates, res.Wrong)
	}

	// A confirmed registration deleted on central is reported lost.
	for _, r := range recs {
		if r.Confirmed && r.Kind == kindRegister {
			e.CentralDelete("events", r.Event)
			break
		}
	}
	res, err = check(d, recs)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("after deleting one event on central: %s; %v", res, res.Lost)
	if len(res.Lost) != 1 {
		t.Fatalf("want 1 lost, got %d", len(res.Lost))
	}
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(b []byte) (int, error) {
	w.t.Log(strings.TrimRight(string(b), "\n"))
	return len(b), nil
}

// TestCAFile: -ca trusts the hub's own authority; without it the certificate fails.
func TestCAFile(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"mode":"online"}`)) }))
	defer srv.Close()
	ca := filepath.Join(t.TempDir(), "ca.crt")
	crt := srv.Certificate()
	os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: crt.Raw}), 0o644)
	for _, tc := range []struct {
		ca       string
		insecure bool
		ok       bool
	}{{"", false, false}, {ca, false, true}, {"", true, true}} {
		h := hubFlags{hub: &srv.URL, ca: &tc.ca, insecure: &tc.insecure, timeout: new(time.Duration)}
		rt, err := h.transport()
		if err != nil {
			t.Fatal(err)
		}
		state, _ := hubState(&http.Client{Transport: rt}, srv.URL)
		if got := state == "up, online"; got != tc.ok {
			t.Errorf("ca %q insecure %v: state %q", tc.ca, tc.insecure, state)
		}
	}
}
