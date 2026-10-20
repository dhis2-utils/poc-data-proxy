//go:build !windows

package stick

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// mountPoint reports whether dir is a mount point, from the mount table where there is
// one (Linux: also right for automounts and bind mounts), else by comparing its device
// with its parent's.
func mountPoint(dir string) bool {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	if b, err := os.ReadFile("/proc/self/mountinfo"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			f := strings.Fields(line)
			if len(f) > 4 && unescapeMount(f[4]) == abs {
				return true
			}
		}
		return false
	}
	return mountPointByDevice(abs)
}

// mountPointByDevice is the check without a mount table (macOS): a folder on another
// device than its parent, or the root, which is its own parent.
func mountPointByDevice(abs string) bool {
	abs = filepath.Clean(abs)
	if filepath.Dir(abs) == abs {
		return true
	}
	var a, p syscall.Stat_t
	if syscall.Stat(abs, &a) != nil || syscall.Stat(filepath.Dir(abs), &p) != nil {
		return false
	}
	return a.Dev != p.Dev
}

// unescapeMount undoes the mount table's octal escapes (a space is \040).
func unescapeMount(s string) string {
	var out strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				out.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		out.WriteByte(s[i])
	}
	return out.String()
}
