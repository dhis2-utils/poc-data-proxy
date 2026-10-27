package e2e

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	fc "github.com/dhis2/facility-hub/internal/fakecentral"
)

// An online read of a person the hub already holds, unchanged on central, does not
// rewrite them: on the Pis the hub wrote about 250 MB a minute while online, storing
// every answer anew (docs/PERFORMANCE.md). A read after a change on central still
// stores the change.
func TestOnlineReadOfUnchangedPersonIsNotRewritten(t *testing.T) {
	e := newEnv(t)
	e.central.Seed("LearnTe0091", "LearnEn0091", "Ama", "NAT-9191", nil)
	e.setup()
	a := e.web("nurse_a")
	a.login()
	e.sync()
	stored := func() []byte {
		var b []byte
		if err := e.app.Hub.Store.DB().QueryRow(`SELECT payload_enc FROM objects WHERE uid = ?`, "LearnTe0091").Scan(&b); err != nil {
			t.Fatal(err)
		}
		return b
	}
	read := func() {
		if st, b := a.do("GET", "/api/tracker/trackedEntities/LearnTe0091?program="+fc.Program+"&fields=*", nil); st != 200 {
			t.Fatalf("online read: %d %s", st, b)
		}
	}
	before := stored()
	read()
	if !bytes.Equal(before, stored()) {
		t.Error("an unchanged person was written again")
	}

	time.Sleep(1100 * time.Millisecond) // a later change, in central's whole seconds
	e.central.Seed("LearnTe0091", "LearnEn0091", "Changed", "NAT-9191", nil)
	read()
	if o := e.app.Hub.Index.Get("LearnTe0091"); o == nil || !strings.Contains(fmt.Sprint(e.app.Hub.Index.Full(o)), "Changed") {
		t.Errorf("the change on central was not stored: %v", o)
	}
}
