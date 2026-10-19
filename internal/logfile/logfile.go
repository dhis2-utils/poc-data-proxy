// Package logfile is the hub's log on Windows, where a service has no console: one
// file, moved to <file>.1 when it reaches its cap, so at most two files are kept.
package logfile

import (
	"os"
	"sync"
)

// File is a size-capped log file.
type File struct {
	mu   sync.Mutex
	path string
	max  int64
	f    *os.File
	size int64
}

// Open appends to path, keeping at most max bytes in it and max in path.1.
func Open(path string, max int64) (*File, error) {
	l := &File{path: path, max: max}
	return l, l.open()
}

func (l *File) open() error {
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	l.f, l.size = f, st.Size()
	return nil
}

func (l *File) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.size+int64(len(b)) > l.max && l.size > 0 {
		l.f.Close()
		os.Remove(l.path + ".1")
		os.Rename(l.path, l.path+".1")
		if err := l.open(); err != nil {
			return 0, err
		}
	}
	n, err := l.f.Write(b)
	l.size += int64(n)
	return n, err
}

func (l *File) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.f.Close()
}
