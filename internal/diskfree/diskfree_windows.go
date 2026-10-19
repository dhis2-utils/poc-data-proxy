// Package diskfree reports the space left for the hub on a file system.
package diskfree

import "golang.org/x/sys/windows"

// Free is the bytes available to the hub where dir lives (0 if unknown).
func Free(dir string) uint64 {
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return 0
	}
	var avail, total, free uint64
	if windows.GetDiskFreeSpaceEx(p, &avail, &total, &free) != nil {
		return 0
	}
	return avail
}
