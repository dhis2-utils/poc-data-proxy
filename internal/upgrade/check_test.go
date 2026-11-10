package upgrade

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/dhis2/facility-hub/internal/core"
	"github.com/dhis2/facility-hub/internal/store"
)

func testStore(t *testing.T) *store.Store {
	st, err := store.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// Review R-12: a signature file that could not be fetched (a timeout, a 404 while the
// release is uploaded) is tried again at the next check. Only a fetched signature that
// does not verify refuses the release.
func TestSignatureNotFetchedIsNotRefused(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	manifest := []byte(`{"version":"0.2.0"}`)
	sig := "" // "" answers 404
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v0.2.0/release.json":
			w.Write(manifest)
		case "/v0.2.0/release.json.sig":
			if sig == "" {
				http.NotFound(w, r)
				return
			}
			w.Write([]byte(sig))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	st := testStore(t)
	u := &Upgrader{H: &core.Hub{Store: st, Version: "0.1.0"}, Base: srv.URL, Key: base64.StdEncoding.EncodeToString(pub)}
	ctx := context.Background()

	if m, err := u.fetchManifest(ctx, "0.2.0"); err == nil || m != nil {
		t.Fatalf("signature missing: manifest %v, err %v; want an error", m, err)
	}
	if got := st.Meta("upgrade_refused"); got != "" {
		t.Fatalf("signature missing: refused %q", got)
	}

	sig = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, []byte(`{"version":"9.9.9"}`)))
	if m, err := u.fetchManifest(ctx, "0.2.0"); err != nil || m != nil {
		t.Fatalf("bad signature: manifest %v, err %v; want neither", m, err)
	}
	if got := st.Meta("upgrade_refused"); got != "0.2.0" {
		t.Fatalf("bad signature: refused %q, want 0.2.0", got)
	}

	sig = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, manifest))
	if m, err := u.fetchManifest(ctx, "0.2.0"); err != nil || m == nil || m.Version != "0.2.0" {
		t.Fatalf("good signature: manifest %v, err %v", m, err)
	}
}

// Review R-13: a migration that runs out of time is tried once more, and skipped after
// a second time; one that fails is skipped at once.
func TestMigrationTimeoutIsTriedOnceMore(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts as the new binary")
	}
	defer func(d time.Duration) { migrateTimeout = d }(migrateTimeout)
	migrateTimeout = 200 * time.Millisecond
	dir := t.TempDir()
	slow, bad := filepath.Join(dir, "slow"), filepath.Join(dir, "bad")
	os.WriteFile(slow, []byte("#!/bin/sh\nexec sleep 10\n"), 0o755)
	os.WriteFile(bad, []byte("#!/bin/sh\necho no such table\nexit 1\n"), 0o755)
	st := testStore(t)
	u := &Upgrader{H: &core.Hub{Store: st, Version: "0.1.0"}}
	ctx, next := context.Background(), filepath.Join(dir, "hub.db.next")

	if err := u.migrate(ctx, slow, next, "0.2.0", nil); err == nil {
		t.Fatal("a migration that ran out of time passed")
	}
	if skip := st.Meta("upgrade_skip"); skip != "" {
		t.Fatalf("skipped after one time-out: %q", skip)
	}
	if err := u.migrate(ctx, slow, next, "0.2.0", nil); err == nil || st.Meta("upgrade_skip") != "0.2.0" {
		t.Fatalf("not skipped after a second time-out: %v, skip %q", err, st.Meta("upgrade_skip"))
	}

	if err := u.migrate(ctx, bad, next, "0.3.0", nil); err == nil || st.Meta("upgrade_skip") != "0.3.0" {
		t.Fatalf("a failed migration is not skipped: %v, skip %q", err, st.Meta("upgrade_skip"))
	}
}
