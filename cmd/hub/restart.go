package main

import "log/slog"

// restartFunc is how the hub restarts itself after a restore from the USB stick is
// staged (stick.Finish applies it at the next start): stop serving, close the store,
// and replace the process like an upgrade does (upgrade.Reexec: in place on Linux and
// macOS; on Windows the service manager starts it again). If the replacement fails the
// process exits, and systemd, the service manager or launchd starts it again.
func restartFunc(shutdown func(), closeApp func() error, exe func() (string, error),
	reexec func(string, []byte) error, exit func(int)) func() {
	return func() {
		shutdown()
		if err := closeApp(); err != nil {
			slog.Warn("restart: closing the store", "err", err)
		}
		path, err := exe()
		if err == nil {
			err = reexec(path, nil)
		}
		slog.Error("restart: could not replace the process; exiting so it is started again", "err", err)
		exit(1)
	}
}
