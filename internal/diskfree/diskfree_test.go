package diskfree

import "testing"

func TestFree(t *testing.T) {
	if Free(t.TempDir()) == 0 {
		t.Fatal("no free space reported for a temporary folder")
	}
	if Free("/no/such/folder/at/all") != 0 {
		t.Fatal("free space for a folder that does not exist")
	}
}
