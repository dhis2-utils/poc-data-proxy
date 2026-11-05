package e2e

import (
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	fc "github.com/dhis2/facility-hub/internal/fakecentral"
)

// A store damaged on disk (session B, 5 November 2026: the VM's disk was unplugged)
// makes every unlock fail. Staff then saw "Only staff assigned to this facility can
// open this page", and the status only "locked". The hub says the store is damaged and
// what to do, on the login page and in its status.
func TestDamagedStoreSaysSo(t *testing.T) {
	e := newEnv(t)
	e.setup()
	a := e.web("nurse_a")
	a.login()
	e.sync()
	e.stop()

	// Every page but the first, as a disk that lost its last writes might leave it.
	data := filepath.Join(e.dir, "hub.db.data")
	os.Remove(data + "-wal")
	os.Remove(data + "-shm")
	f, err := os.OpenFile(data, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	fi, _ := f.Stat()
	junk := make([]byte, fi.Size()-4096)
	for i := range junk {
		junk[i] = byte(i*7 + 3)
	}
	if _, err := f.WriteAt(junk, 4096); err != nil {
		t.Fatal(err)
	}
	f.Close()
	e.start()

	resp, err := a.postForm("/hub/login", url.Values{"username": {"nurse_a"}, "password": {fc.Password}})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 503 || !strings.Contains(string(body), "store is damaged") || !strings.Contains(string(body), "USB stick") {
		t.Fatalf("login on a damaged store: %d\n%s", resp.StatusCode, body)
	}
	if strings.Contains(string(body), "assigned to this facility") {
		t.Error("the login page still blames the facility assignment")
	}
	st := a.json("GET", "/hub/status", nil, 200)
	if st["storeDamaged"] != true || !strings.Contains(st["message"].(string), "damaged") {
		t.Errorf("status on a damaged store: %v", st)
	}
}

// A hub that is unlocked when the damage shows (its pulls fail with "malformed") says
// so on its page, not only as a sync error.
func TestDamageSeenBySyncShowsOnPage(t *testing.T) {
	e := newEnv(t)
	e.setup()
	a := e.web("hub_admin")
	resp, err := a.postForm("/hub/login", url.Values{"username": {"hub_admin"}, "password": {fc.Password}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if st, p := a.do("GET", "/hub/ui", nil); st != 200 || strings.Contains(string(p), "store is damaged") {
		t.Fatalf("page before the damage: %d", st)
	}
	if !e.app.Hub.NoteStoreErr(errors.New("pull ngelehun1/IpHINAT79UW: sqlite3: database disk image is malformed")) {
		t.Fatal("a malformed-store error is not recognised")
	}
	if e.app.Hub.NoteStoreErr(errors.New("central unreachable")) {
		t.Fatal("a network error taken for damage")
	}
	st, p := a.do("GET", "/hub/ui", nil)
	if st != 200 || !strings.Contains(string(p), "store is damaged</b>") || !strings.Contains(string(p), "IpHINAT79UW") {
		t.Fatalf("page after the damage: %d\n%s", st, p)
	}
}
