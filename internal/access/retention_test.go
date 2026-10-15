package access

import (
	"testing"
	"time"
)

func TestRetention(t *testing.T) {
	r := Retention{DefaultMonths: 24, Programs: map[string]int{"opd": 12, "keep": 0}}
	now := time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)
	if c, ok := r.Cutoff("opd", now); !ok || c != "2025-10-15" {
		t.Errorf("opd: %q %v", c, ok)
	}
	if c, ok := r.Cutoff("other", now); !ok || c != "2024-10-15" {
		t.Errorf("default: %q %v", c, ok)
	}
	if _, ok := r.Cutoff("keep", now); ok {
		t.Error("0 for a program keeps everything")
	}
	if _, ok := (Retention{}).Cutoff("any", now); ok {
		t.Error("no table keeps everything")
	}
}
