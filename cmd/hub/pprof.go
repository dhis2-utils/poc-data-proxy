package main

import (
	"log/slog"
	"net"
	"net/http"
	_ "net/http/pprof" // registers on http.DefaultServeMux, which only this listener serves
	"os"
)

// HUB_PPROF=127.0.0.1:6060 serves Go's profiler there, for performance work
// (docs/PERFORMANCE.md). Off unless set, and refused on anything but loopback.
func init() {
	addr := os.Getenv("HUB_PPROF")
	if addr == "" {
		return
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil || !net.ParseIP(host).IsLoopback() {
		slog.Error("HUB_PPROF must be a loopback address", "addr", addr)
		return
	}
	go func() { slog.Warn("profiler listening", "addr", addr, "err", http.ListenAndServe(addr, nil)) }()
}
