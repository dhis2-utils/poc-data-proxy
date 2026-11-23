package e2e

import (
	"context"
	"testing"

	"github.com/dhis2/facility-hub/internal/sync"
)

// "Last push" is when central last answered a push: a cycle with nothing to send does
// not move it, so a hub that cannot reach central does not look as if it just pushed
// (PENDING.md, item 35). The heartbeat says when the oldest waiting save was made.
func TestLastPushOnlyWhenCentralAnswers(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.sync()
	h := e.app.Hub
	if push, _ := h.LastPushPull(); push != "" {
		t.Errorf("last push %q with nothing ever sent", push)
	}
	a := e.web("nurse_a")
	a.login()
	e.offline()
	if rep := a.json("POST", postSync, newPatient("LpTe0000001", "LpEn0000001", "Lea", "NAT-4001", nil), 200); len(errorCodes(rep)) != 0 {
		t.Fatal(rep)
	}
	if got := sync.Status(h)["oldestPending"]; got == "" {
		t.Error("no oldestPending with a save waiting")
	}
	e.online()
	if err := e.app.Worker.Push(context.Background()); err != nil {
		t.Fatal(err)
	}
	pushed, _ := h.LastPushPull()
	if pushed == "" {
		t.Fatal("no last push after central took a save")
	}
	if got := sync.Status(h)["oldestPending"]; got != "" {
		t.Errorf("oldestPending %q with nothing waiting", got)
	}
	if err := e.app.Worker.Push(context.Background()); err != nil {
		t.Fatal(err)
	}
	if again, _ := h.LastPushPull(); again != pushed {
		t.Errorf("an empty push moved the last push from %s to %s", pushed, again)
	}
}
