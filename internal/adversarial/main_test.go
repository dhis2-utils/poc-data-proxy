package adversarial

import (
	"log"
	"os"
	"sync"
	"testing"
)

// atEnd holds clean-ups that would load central while later tests run: they run once
// the whole package is done. A16's delete of 2000 data elements kept a Laos central busy
// for 1h40 (18 November 2026), and the tests that ran meanwhile timed out on its slowness.
var atEnd struct {
	sync.Mutex
	fns []func()
}

func afterAll(f func()) {
	atEnd.Lock()
	defer atEnd.Unlock()
	atEnd.fns = append(atEnd.fns, f)
}

func TestMain(m *testing.M) {
	code := m.Run()
	atEnd.Lock()
	for _, f := range atEnd.fns {
		f()
	}
	atEnd.Unlock()
	if len(atEnd.fns) > 0 {
		log.Printf("ran %d end-of-suite clean-up(s)", len(atEnd.fns))
	}
	os.Exit(code)
}
