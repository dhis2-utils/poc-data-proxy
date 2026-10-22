package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"
)

// confirmed records every write the hub confirmed (-confirmed FILE), for the power-cut
// test (docs/HARDWARE-TESTING.md): after power returns, -verify FILE checks that each
// of them is still on the hub.
var confirmed struct {
	mu sync.Mutex
	f  *os.File
}

func openConfirmed(path string) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		log.Fatalf("confirmed log: %v", err)
	}
	confirmed.f = f
}

// accepted reports whether an import report says everything was stored: central's
// shape, which the hub answers in, with nothing ignored.
func accepted(status int, body []byte) bool {
	if status != 200 {
		return false
	}
	var r struct {
		Status string `json:"status"`
		Stats  struct {
			Ignored int `json:"ignored"`
		} `json:"stats"`
	}
	return json.Unmarshal(body, &r) == nil && r.Status == "OK" && r.Stats.Ignored == 0
}

// confirm logs the objects of a write the hub accepted: user, kind and identifier.
func confirm(user string, status int, body []byte, objs ...[2]string) {
	if confirmed.f == nil || !accepted(status, body) {
		return
	}
	confirmed.mu.Lock()
	defer confirmed.mu.Unlock()
	for _, o := range objs {
		fmt.Fprintf(confirmed.f, "%s\t%s\t%s\t%s\n", time.Now().UTC().Format(time.RFC3339Nano), user, o[0], o[1])
	}
	// On disk before the next write: when the load runs on the machine whose power is
	// cut, an unflushed tail would drop exactly the last confirmed writes, the ones most
	// at risk, from the check.
	confirmed.f.Sync()
}

// verify reads back every confirmed object from the hub, with central down so that the
// hub answers from its own store, as each object's author. It returns how many of the
// confirmed objects are missing.
func verify(path string) int {
	f, err := os.Open(path)
	if err != nil {
		log.Fatalf("verify: %v", err)
	}
	defer f.Close()
	post(*central+"/fake/down", nil)
	waitStatus(func(s map[string]any) bool { return s["mode"] == "offline" }, 2*time.Minute)
	users := map[string]*user{}
	total, missing := 0, 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		parts := strings.Split(sc.Text(), "\t")
		if len(parts) != 4 {
			continue
		}
		name, kind, id := parts[1], parts[2], parts[3]
		u := users[name]
		if u == nil {
			u = newUser(0)
			u.name = name
			users[name] = u
		}
		total++
		if st, b, _ := u.get("/api/tracker/" + kind + "/" + id); st != 200 || !found(b, id) {
			missing++
			log.Printf("missing: %s %s %s (confirmed %s): %d %.200s", name, kind, id, parts[0], st, b)
		}
	}
	log.Printf("verify: %d confirmed objects, %d missing", total, missing)
	return missing
}

func found(b []byte, id string) bool { return strings.Contains(string(b), `"`+id+`"`) }
