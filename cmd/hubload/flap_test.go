package main

import (
	"testing"
	"time"
)

func TestParseFlap(t *testing.T) {
	down, up, err := parseFlap("10m/5m")
	if err != nil || down != 10*time.Minute || up != 5*time.Minute {
		t.Fatalf("10m/5m: %s %s %v", down, up, err)
	}
	for _, bad := range []string{"10m", "10m/", "/5m", "x/5m", "0s/5m", "10m/-1m"} {
		if _, _, err := parseFlap(bad); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}
