//go:build !windows

// Package diskfree reports the space left for the hub on a file system.
package diskfree

import "syscall"

// Free is the bytes available to the hub where dir lives (0 if unknown).
func Free(dir string) uint64 {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0
	}
	return uint64(st.Bavail) * uint64(st.Bsize)
}
