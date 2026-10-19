package upgrade

import (
	"log/slog"
	"os"
	"os/exec"
	"time"

	"golang.org/x/sys/windows/svc"
)

// ExitRestart is the exit code after which the service manager's recovery action
// starts the hub again (cmd/hub service install sets it), now on the new binary.
const ExitRestart = 3

// replaceBinary puts src at dst. Windows cannot overwrite a running .exe but can
// rename it: the running one becomes dst.old, removed at the next upgrade.
func replaceBinary(src, dst string) error {
	old := dst + ".old"
	os.Remove(old)
	if err := os.Rename(dst, old); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := copyFile(src, dst, 0o755); err != nil {
		os.Rename(old, dst)
		return err
	}
	return nil
}

// Reexec starts exe in place of this process. A service cannot replace itself: it
// seals the data key for the new start (handover.go) and exits, and the service
// manager starts it again; a per-user hub starts the new process itself.
func Reexec(exe string, dk []byte) error {
	if len(dk) > 0 && HandoverDir != "" {
		if err := WriteHandover(HandoverDir, dk, time.Now(), dpapiSeal); err != nil {
			slog.Warn("upgrade: handing over the data key failed; the new version starts locked", "err", err)
		}
	}
	if is, _ := svc.IsWindowsService(); is {
		os.Exit(ExitRestart)
	}
	cmd := exec.Command(exe, os.Args[1:]...)
	if err := cmd.Start(); err != nil {
		return err
	}
	os.Exit(0)
	return nil
}

func handedOverByFile() []byte {
	if HandoverDir == "" {
		return nil
	}
	return ReadHandover(HandoverDir, time.Now(), dpapiOpen)
}
