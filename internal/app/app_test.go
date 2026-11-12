package app

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dhis2/facility-hub/internal/config"

	"github.com/dhis2/facility-hub/internal/core"
	"github.com/dhis2/facility-hub/internal/store"
)

// Paths that are not clean are refused before routing (review R-16).
func TestCleanPath(t *testing.T) {
	for p, want := range map[string]bool{
		"/": true, "/api/me": true, "/dhis-web-capture/": true, "/apps/capture": true, "/api/42/me.json": true,
		"//h/api/x.json": false, "/a/../api/me": false, "/api/./me": false, "/api//me": false, "/api/me//": false, "": false,
	} {
		if got := cleanPath(p); got != want {
			t.Errorf("cleanPath(%q) = %v, want %v", p, got, want)
		}
	}
}

// A hub keeps its ID: a fresh store gets one, and a key file that cannot be read gets
// no new one (review R-45).
func TestEnsureIdentityKeepsID(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := EnsureIdentity(s); err != nil {
		t.Fatal(err)
	}
	id := s.Meta(core.MetaHubID)
	if id == "" || s.Meta(core.MetaMode) != core.ModeSetup {
		t.Fatalf("fresh store: id %q mode %q", id, s.Meta(core.MetaMode))
	}
	if _, err := s.Keys.Exec(`ALTER TABLE meta RENAME TO meta_damaged`); err != nil {
		t.Fatal(err)
	}
	if err := EnsureIdentity(s); err == nil {
		t.Fatal("no error for a key file that cannot be read")
	}
	s.Keys.Exec(`ALTER TABLE meta_damaged RENAME TO meta`)
	if got := s.Meta(core.MetaHubID); got != id {
		t.Errorf("hub ID %q, was %q", got, id)
	}
}

// With log_requests on, hub.log gets a request's parameter names, never their values:
// a data value, its comment or a search term can name a patient (KISS-2026-11-12 C6).
func TestRequestLogKeepsNoValues(t *testing.T) {
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(old)
	h := logRequests(config.Config{LogRequests: true}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	r := httptest.NewRequest(http.MethodPost, "/api/dataValues?de=s46m5MS0hxu&value=Mary%20Kamara&comment=HIV%20positive&filter=w75KJ2mc4zz:like:Kamara", nil)
	h.ServeHTTP(httptest.NewRecorder(), r)
	got := buf.String()
	for _, v := range []string{"Mary", "Kamara", "HIV", "s46m5MS0hxu"} {
		if strings.Contains(got, v) {
			t.Errorf("hub.log holds %q: %s", v, got)
		}
	}
	if !strings.Contains(got, "comment,de,filter,value") {
		t.Errorf("hub.log lacks the parameter names: %s", got)
	}
}
