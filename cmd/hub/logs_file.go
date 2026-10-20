//go:build windows || darwin

package main

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/dhis2/facility-hub/internal/logfile"
)

// logToFile: a Windows service, a per-user hub started without a window, or a macOS
// launchd agent has no console. The log goes to <data>/hub.log, capped at 10 MB, one
// old file kept; when run by hand in a terminal it goes there too.
func logToFile(dataDir string) {
	f, err := logfile.Open(filepath.Join(dataDir, "hub.log"), 10<<20)
	if err != nil {
		return
	}
	var w io.Writer = f
	if st, err := os.Stderr.Stat(); err == nil && st.Mode()&os.ModeCharDevice != 0 {
		w = io.MultiWriter(f, os.Stderr)
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelInfo})))
}
