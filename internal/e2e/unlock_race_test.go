package e2e

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	fc "github.com/dhis2/facility-hub/internal/fakecentral"
)

// Two staff logging in at once after a restart both unlock the store: one unlock
// must do it, the other find it done. Two rebuilt the index together, and the next
// offline saves failed with "UNIQUE constraint failed: objects.slot"
// (docs/ADVERSARIAL-TESTING.md, E1).
func TestConcurrentLoginsAfterRestart(t *testing.T) {
	e := newEnv(t)
	e.setup()
	a, b := e.web("nurse_a"), e.web("nurse_b")
	a.login()
	b.login()
	e.central.SetDown(true)
	// Each unlock logs "store unlocked": counted, one per restart.
	var unlocks atomic.Int32
	prev := slog.Default()
	slog.SetDefault(slog.New(countHandler{slog.NewTextHandler(os.Stderr, nil), "store unlocked", &unlocks}))
	t.Cleanup(func() { slog.SetDefault(prev) })
	for round := range 8 {
		unlocks.Store(0)
		e.stop()
		e.start()
		a, b = e.web("nurse_a"), e.web("nurse_b")
		// Each saves right after its own login, while the other may still be logging
		// in and unlocking: what staff do when the hub comes back from a power cut.
		var wg sync.WaitGroup
		res := make([]string, 2)
		for i, c := range []*client{a, b} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				st, body := c.do("POST", "/api/auth/login", map[string]string{"username": c.user, "password": fc.Password})
				if st != 200 || !strings.Contains(string(body), "SUCCESS") {
					res[i] = fmt.Sprintf("login %s: %d %s", c.user, st, body)
					return
				}
				for k := range 5 {
					te, en := fmt.Sprintf("Unl%d%d%06d", i, k, round), fmt.Sprintf("Unn%d%d%06d", i, k, round)
					if st, body := c.do("POST", postSync, newPatient(te, en, "Unlock", "", nil)); st != 200 {
						res[i] = fmt.Sprintf("offline save by %s after two logins at once: %d %s", c.user, st, body)
						return
					}
				}
			}()
		}
		wg.Wait()
		for _, r := range res {
			if r != "" {
				t.Fatalf("round %d: %s", round, r)
			}
		}
		if n := unlocks.Load(); n != 1 {
			t.Fatalf("round %d: the store was unlocked %d times by two logins at once", round, n)
		}
	}
}

// countHandler counts the records with one message and passes every record on.
type countHandler struct {
	slog.Handler
	msg string
	n   *atomic.Int32
}

func (h countHandler) Handle(ctx context.Context, r slog.Record) error {
	if r.Message == h.msg {
		h.n.Add(1)
	}
	return h.Handler.Handle(ctx, r)
}

func (h countHandler) WithAttrs(as []slog.Attr) slog.Handler {
	return countHandler{h.Handler.WithAttrs(as), h.msg, h.n}
}

func (h countHandler) WithGroup(name string) slog.Handler {
	return countHandler{h.Handler.WithGroup(name), h.msg, h.n}
}
