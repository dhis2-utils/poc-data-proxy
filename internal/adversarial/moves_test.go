package adversarial

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dhis2/facility-hub/internal/hubaccount"
	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// protectEntry shares a dataStore entry as the Facility Hub app does: readable by all,
// writable by administrators only.
func protectEntry(t *testing.T, c *tk.Central, ns, key string) {
	t.Helper()
	var meta struct{ ID string }
	if err := c.JSON("GET", "/api/dataStore/"+ns+"/"+key+"/metaData", nil, &meta); err != nil {
		t.Fatal(err)
	}
	if st, b, err := c.Do("POST", "/api/sharing?type=dataStore&id="+meta.ID,
		map[string]any{"object": map[string]any{"publicAccess": "r-------", "userAccesses": []any{}, "userGroupAccesses": []any{}}}); err != nil || st != 200 {
		t.Fatalf("sharing %s/%s: %d %v %s", ns, key, st, err, b)
	}
}

// probeDown makes the hub see its central address fail (two probes) and runs a cycle,
// in which it may move.
func (s *scene) probeDown() {
	h := s.App.Hub
	for range 2 {
		h.Monitor.Probe(context.Background(), h.Central.BaseURL())
	}
	if err := s.App.Worker.Cycle(context.Background()); err != nil {
		s.t.Logf("sync: %v", err)
	}
}

// offlineSave registers a person as the nurse while the hub is offline.
func (s *scene) offlineSave(c *tk.Client, name string) *tk.Person {
	s.t.Helper()
	p := s.M.Primary()
	pr := s.M.NewPerson(p, name)
	c.Complete(p, pr)
	if st, b := s.save(c, "register "+name, pr.Payload); st != 200 {
		s.t.Fatalf("offline save: %d %.300s", st, b)
	}
	return pr
}

// H5: central moves, on a real DHIS2: the new address is another proxy to the same
// instance (so it holds the hub's registry entry), the old one stops answering. First
// a move announced on central (facility-hub-admin/central-move, protected as the
// Facility Hub app protects it), then an unannounced one typed in on the hub page with
// the code the national team gives, and an announced address that is another server.
// Central's address must have been dead an hour before the hub follows an
// announcement: here 0 (Config.CentralMoveAfter, as the e2e tests set it). A redirect
// is followed only from https, which this central is not (not run here). Must hold:
// staff saves made meanwhile reach central once through the new address; nothing is
// sent to an address that is not the same central.
func TestH5CentralMoved(t *testing.T) {
	s := begin(t)
	if !s.Live() {
		t.Skip("needs a real DHIS2")
	}
	h := s.App.Hub
	h.Cfg.CentralURL = "" // the generic image: no address pinned in hub.yaml
	h.Cfg.CentralMoveAfter = 0
	nurse := s.connect(s.M.Users[tk.RoleNurse].Username)
	web := s.Web(tk.RoleNurse)
	web.Login()
	addr := func() string { return h.Central.BaseURL() }
	pushedVia := func(n *tk.Netcut) int {
		k := 0
		for _, c := range n.Calls() {
			if c.Method == "POST" && c.URL.Path == "/api/tracker" {
				k++
			}
		}
		return k
	}

	// 1. Announced, then the old address dies.
	second, err := tk.NewNetcut(s.M.CentralURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { second.Close() })
	// The Facility Hub app keeps the entry, protected, as {} when nothing is announced.
	const move = "/api/dataStore/facility-hub-admin/central-move"
	st, was, _ := s.Central.Do("GET", move, nil)
	if st == 200 && strings.Contains(string(was), "url") {
		t.Fatalf("a move is already announced on central: %s", was)
	}
	announce := func(v any) {
		t.Helper()
		b, _ := json.Marshal(v)
		s.PutSigned("facility-hub-admin/central-move", string(b)) // as the app writes it (review R-07)
	}
	announce(map[string]any{"url": second.URL()})
	if st == 200 {
		t.Cleanup(func() { s.Central.Do("PUT", move, json.RawMessage(was)) })
	} else {
		t.Cleanup(func() { s.Central.Do("DELETE", move, nil) })
		protectEntry(t, s.Central, "facility-hub-admin", "central-move")
		st = 200
	}
	s.Sync()
	if addr() != s.Net.URL() {
		t.Fatalf("moved while the old address answers: %s", addr())
	}
	s.Offline()
	a := s.offlineSave(nurse, "Zqmovea")
	s.probeDown()
	t.Logf("announced, old address dead: the hub uses %s (new %s), online %v", addr(), second.URL(), h.Online())
	if addr() != second.URL() {
		t.Fatalf("the hub did not follow the announced move")
	}
	s.Sync()
	t.Logf("pushes through the new address: %d; the save on central: %v", pushedVia(second), s.CentralObject("trackedEntities", a.TE) != nil)

	// 2. An announced address that is another server: nothing is sent to it.
	var got atomic.Bool
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.Store(r.Header.Get("Authorization") != "")
		http.NotFound(w, r)
	}))
	t.Cleanup(other.Close)
	announce(map[string]any{"url": other.URL})
	s.Sync()
	second.SetDown(true)
	b := s.offlineSave(nurse, "Zqmoveb")
	s.probeDown()
	t.Logf("announced another server, the address dies: the hub uses %s; the hub's token sent to it (the same-central check, as designed): %v", addr(), got.Load())
	if addr() == other.URL {
		t.Errorf("the hub moved to a server that is not its central")
	}
	second.SetDown(false)
	announce(map[string]any{})
	for range 2 {
		h.Monitor.Probe(context.Background(), addr())
	}
	s.Sync()

	// 3. Typed in on the hub page, with the national team's code.
	approveAccount(t, s)
	var keys struct{ MoveKey string }
	if err := s.Central.JSON("GET", "/api/dataStore/facility-hub-keys/"+h.HubID(), nil, &keys); err != nil {
		t.Fatal(err)
	}
	key, _ := base64.StdEncoding.DecodeString(keys.MoveKey)
	third, err := tk.NewNetcut(s.M.CentralURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { third.Close() })
	second.SetDown(true)
	s.probeDown()
	c := s.offlineSave(nurse, "Zqmovec")
	admin := s.Web(tk.RoleAdmin)
	admin.Login()
	typed := func(address, code string) string {
		resp, err := admin.PostForm("/hub/ui/move", url.Values{"central_url": {address}, "code": {code}})
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		loc, _ := url.QueryUnescape(resp.Header.Get("Location"))
		return loc
	}
	t.Logf("a wrong code: %s; the hub uses %s", typed(third.URL(), "AAAAA-AAAAA"), addr())
	if addr() == third.URL() {
		t.Errorf("moved with a wrong code")
	}
	t.Logf("the right code: %s; the hub uses %s", typed(third.URL(), hubaccount.MoveCode(key, third.URL())), addr())
	if addr() != third.URL() {
		t.Fatalf("the typed-in move was not followed")
	}
	for range 2 {
		h.Monitor.Probe(context.Background(), addr())
	}
	s.Sync()
	t.Logf("pushes through the typed address: %d", pushedVia(third))
	for _, pr := range []*tk.Person{a, b, c} {
		if s.CentralObject("trackedEntities", pr.TE) == nil {
			t.Errorf("I1: %s, saved while central moved, is not on central", pr.TE)
		}
	}
	s.expect(nil, "on central as saved")
}
