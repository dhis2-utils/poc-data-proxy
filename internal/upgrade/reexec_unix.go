//go:build !windows

package upgrade

import (
	"encoding/hex"
	"fmt"
	"os"
	"syscall"
)

// replaceBinary atomically puts src at dst (copy next to dst, then rename).
func replaceBinary(src, dst string) error { return copyFile(src, dst, 0o755) }

// dkPipe returns the read end of a pipe that yields the data key, hex encoded.
func dkPipe(dk []byte) (*os.File, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer w.Close()
	if _, err := w.Write([]byte(hex.EncodeToString(dk))); err != nil {
		r.Close()
		return nil, err
	}
	return r, nil
}

// Reexec replaces this process with exe, handing the data key over a pipe.
func Reexec(exe string, dk []byte) error {
	env := os.Environ()
	if len(dk) > 0 {
		r, err := dkPipe(dk)
		if err != nil {
			return err
		}
		fd := int(r.Fd())
		if _, _, e := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), syscall.F_SETFD, 0); e != 0 {
			return e
		}
		env = append(env, fmt.Sprintf("%s=%d", DKEnv, fd))
	}
	return syscall.Exec(exe, os.Args, env)
}

// handedOverByFile: on Unix the key comes over a pipe, never in a file.
func handedOverByFile() []byte { return nil }
