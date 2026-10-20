// Package atomicfile replaces files so that a power cut leaves the old file or the new
// one, never an empty or half-written one: write a temporary file next to it, sync it,
// rename it into place, and sync the folder. Every file the hub must not lose half of
// (certificates, the device key, the USB stick's copies, a new hub binary) goes
// through here.
package atomicfile

import (
	"io"
	"os"
	"path/filepath"
)

// Write replaces path with b.
func Write(path string, b []byte, perm os.FileMode) error {
	return write(path, perm, func(f *os.File) error {
		_, err := f.Write(b)
		return err
	})
}

// Copy replaces dst with the content of src, streamed.
func Copy(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	return write(dst, perm, func(f *os.File) error {
		_, err := io.Copy(f, in)
		return err
	})
}

func write(path string, perm os.FileMode, fill func(*os.File) error) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, perm)
	if err != nil {
		return err
	}
	err = fill(f)
	if err == nil {
		err = f.Chmod(perm) // OpenFile's mode is cut by the umask, and kept for an existing tmp
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	// The rename itself is durable only once the folder is synced (not possible on
	// Windows or every USB stick's file system: best effort).
	if d, err := os.Open(filepath.Dir(path)); err == nil {
		d.Sync()
		d.Close()
	}
	return nil
}
