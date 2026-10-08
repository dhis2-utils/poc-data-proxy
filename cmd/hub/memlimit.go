package main

import (
	"bufio"
	"log/slog"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
)

// setMemoryLimit gives Go's garbage collector a soft limit of 70 % of the memory the
// hub may use: its container's limit (cgroup), or else the device's RAM. By default
// the heap may grow to twice what is live, which on a 4 GB device turns a busy hour
// into an out-of-memory kill; near the limit the collector now works harder instead.
// GOMEMLIMIT, when set, wins.
func setMemoryLimit() {
	if os.Getenv("GOMEMLIMIT") != "" {
		return
	}
	limit := cgroupLimit()
	if limit == 0 {
		limit = totalRAM()
	}
	if limit == 0 {
		return
	}
	debug.SetMemoryLimit(limit / 10 * 7)
	slog.Info("memory limit for the garbage collector", "bytes", limit/10*7, "of", limit)
}

func cgroupLimit() int64 {
	for _, p := range []string{"/sys/fs/cgroup/memory.max", "/sys/fs/cgroup/memory/memory.limit_in_bytes"} {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		n, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
		if err == nil && n > 0 && n < 1<<50 { // "max" or a huge number: no limit
			return n
		}
	}
	return 0
}

func totalRAM() int64 {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		if f := strings.Fields(s.Text()); len(f) >= 2 && f[0] == "MemTotal:" {
			kb, _ := strconv.ParseInt(f[1], 10, 64)
			return kb * 1024
		}
	}
	return 0
}
