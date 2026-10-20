//go:build !windows

// The Unix mount check; on Windows a stick is a removable drive (mount_windows.go).

package stick

import "testing"

// Review minor: the mount check reads the mount table: "/" is a mount point, a plain
// folder is not.
func TestMountPoint(t *testing.T) {
	if !mountPoint("/") {
		t.Error(`"/" is not seen as a mount point`)
	}
	if mountPoint(t.TempDir()) {
		t.Error("a plain folder is seen as a mount point")
	}
}

// macOS has no /proc mount table: the check compares devices, and "/" (its own parent)
// is a mount point there too.
func TestMountPointByDevice(t *testing.T) {
	if !mountPointByDevice("/") {
		t.Error(`"/" is not seen as a mount point`)
	}
	if mountPointByDevice(t.TempDir()) {
		t.Error("a plain folder is seen as a mount point")
	}
}
