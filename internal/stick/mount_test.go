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
