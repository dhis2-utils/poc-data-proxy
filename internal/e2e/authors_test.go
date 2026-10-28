package e2e

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	fc "github.com/dhis2/facility-hub/internal/fakecentral"
)

// What the hub serves offline of a person written at the hub names the author as
// central does, with first name and surname (eRegisters drops records without them,
// adversarial I7c). What the hub pushes is what the client sent: no author or times
// added.
func TestOfflineAuthorAsCentralNamesIt(t *testing.T) {
	e := newEnv(t)
	var mu sync.Mutex
	var pushed [][]byte
	e.cSrv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.URL.Path == "/api/tracker" {
			b, _ := io.ReadAll(r.Body)
			mu.Lock()
			pushed = append(pushed, b)
			mu.Unlock()
			r.Body = io.NopCloser(bytes.NewReader(b))
		}
		e.central.ServeHTTP(w, r)
	})
	e.setup()
	a := e.web("nurse_a")
	a.login()
	e.offline()
	a.json("POST", postSync, newPatient("AuthTe00001", "AuthEn00001", "Author", "NAT-7001",
		map[string]string{"AuthEv00001": fc.Stage1}), 200)
	user := "[uid,username,firstName,surname]"
	te := a.json("GET", "/api/tracker/trackedEntities/AuthTe00001?program="+fc.Program+"&fields=createdBy"+user+
		",updatedBy"+user+",enrollments[createdBy"+user+",updatedBy"+user+"]", nil, 200)
	enrs, _ := te["enrollments"].([]any)
	if len(enrs) != 1 {
		t.Fatalf("enrollments offline: %v", te)
	}
	for name, o := range map[string]map[string]any{"person": te, "enrollment": enrs[0].(map[string]any)} {
		for _, k := range []string{"createdBy", "updatedBy"} {
			by, _ := o[k].(map[string]any)
			if by["uid"] != "HubUsrNursA" || by["username"] != "nurse_a" || by["firstName"] != "Fake" || by["surname"] != "nurse_a" {
				t.Errorf("%s %s offline: %v, want uid, username, firstName and surname as central gives them", name, k, by)
			}
		}
	}

	e.online()
	e.sync()
	mu.Lock()
	defer mu.Unlock()
	if len(pushed) != 1 {
		t.Fatalf("pushes: %d", len(pushed))
	}
	for _, k := range []string{"createdBy", "updatedBy", "createdAt", "updatedAt", "firstName", "Fake"} {
		if strings.Contains(string(pushed[0]), `"`+k+`"`) {
			t.Errorf("the push carries %q, which the client did not send: %s", k, pushed[0])
		}
	}
	var body map[string]any
	if err := json.Unmarshal(pushed[0], &body); err != nil || len(body["trackedEntities"].([]any)) != 1 {
		t.Fatalf("push body: %s", pushed[0])
	}
}
