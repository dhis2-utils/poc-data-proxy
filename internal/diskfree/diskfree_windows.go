// Package diskfree reports the space left for the hub on a file system.
package diskfree

import "golang.org/x/sys/windows"

// Free is the bytes available to the hub where dir lives (0 if unknown).
func Free(dir string) uint64 {
	n, _ := Space(dir)
	return n
}

// Space is Free, and whether it could be read: a full disk is 0 and known.
func Space(dir string) (uint64, bool) {
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return 0, false
	}
	var avail, total, free uint64
	if windows.GetDiskFreeSpaceEx(p, &avail, &total, &free) != nil {
		return 0, false
	}
	return avail, true
}
