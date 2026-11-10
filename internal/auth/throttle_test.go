package auth

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// R-04: attempts are reserved before the password is checked, so a burst of parallel
// guesses gets no more checks than the limit.
func TestThrottleReservesBeforeTheCheck(t *testing.T) {
	var thr throttle
	var checked atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range 60 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if thr.begin("10.0.0.5", "clerk") > 0 {
				return
			}
			checked.Add(1)
			time.Sleep(20 * time.Millisecond) // the Argon2 check
			thr.end("10.0.0.5", "clerk", true, false)
		}()
	}
	close(start)
	wg.Wait()
	if n := checked.Load(); n != 5 {
		t.Fatalf("%d of 60 parallel guesses were checked, want 5", n)
	}
	if thr.begin("10.0.0.5", "clerk") <= 0 {
		t.Fatal("not locked out after the limit")
	}
}

// R-04: a counter is forgotten only after a quiet window since its last failure, so
// guesses spread just under the limit per window still add up.
func TestThrottleWindowRunsFromLastFailure(t *testing.T) {
	var thr throttle
	for i := range 4 {
		if thr.begin("10.0.0.5", "clerk") > 0 {
			t.Fatalf("attempt %d refused", i)
		}
		thr.end("10.0.0.5", "clerk", true, false)
		// Age every counter: the first failure is long past, the last one recent.
		for _, f := range thr.fails {
			f.last = time.Now().Add(-throttleWindow + time.Minute)
		}
	}
	if thr.begin("10.0.0.5", "clerk") > 0 {
		t.Fatal("fifth attempt refused")
	}
	thr.end("10.0.0.5", "clerk", true, false)
	if thr.begin("10.0.0.5", "clerk") <= 0 {
		t.Fatal("five failures with less than a window between them did not lock")
	}
	for _, f := range thr.fails {
		f.last, f.until = time.Now().Add(-throttleWindow-time.Second), time.Time{}
	}
	if thr.begin("10.0.0.5", "clerk") > 0 {
		t.Fatal("still locked after a quiet window")
	}
}

// A successful or undecided attempt releases its reservation: it does not count
// against the device or the account.
func TestThrottleSuccessReleases(t *testing.T) {
	var thr throttle
	for i := range 30 {
		if thr.begin("10.0.0.5", "clerk") > 0 {
			t.Fatalf("attempt %d refused", i)
		}
		thr.end("10.0.0.5", "clerk", false, i%2 == 0)
	}
	for k, f := range thr.fails {
		if f.n != 0 || f.pending != 0 {
			t.Fatalf("%s: %+v", k, f)
		}
	}
}
