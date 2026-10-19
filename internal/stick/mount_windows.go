package stick

import (
	"path/filepath"

	"golang.org/x/sys/windows"
)

// mountPoint reports whether dir is on a removable drive: on Windows a stick is a
// drive of its own, never a folder of the PC's disk.
func mountPoint(dir string) bool {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	root := filepath.VolumeName(abs) + `\`
	p, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return false
	}
	return windows.GetDriveType(p) == windows.DRIVE_REMOVABLE
}
