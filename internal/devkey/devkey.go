// Package devkey keeps the hub's device key: 32 random bytes sealed by the machine's
// TPM 2.0, where it has one (docs/superpowers/specs/2026-10-20-hardening-and-settings-
// design.md, section 1). The sealed copy is device.key in the data folder; it opens only
// on the same TPM. The key protects what the hub needs before anyone logs in (its TLS
// keys, the setup code, the Wi-Fi files) and binds the password key slots on the disk,
// so a disk taken without its TPM gives neither. It does not unlock the hub: that still
// takes a staff login.
//
// No PCR policy: the Pi has no measured boot, and on Windows a firmware update would
// change PCR 7 and lock the hub out. So the key protects a disk or data folder taken
// alone, not a stolen whole device.
package devkey

import (
	"bytes"
	"errors"
	"fmt"
	"github.com/dhis2/facility-hub/internal/atomicfile"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/dhis2/facility-hub/internal/hubcrypto"
)

// Device seals and unseals small secrets with this machine's TPM.
type Device interface {
	Seal(secret []byte) ([]byte, error)
	Unseal(blob []byte) ([]byte, error)
	Close() error
}

// File is the device key, sealed, in the data folder.
const File = "device.key"

var (
	mu      sync.RWMutex
	current hubcrypto.Key
	problem string
)

// Key returns the device key, or nil when the hub has none (no TPM).
func Key() hubcrypto.Key {
	mu.RLock()
	defer mu.RUnlock()
	return current
}

// Problem says why the device key is missing although the data folder has one: the TPM
// is gone or changed, so what it sealed cannot be opened here. "" when all is well.
func Problem() string {
	mu.RLock()
	defer mu.RUnlock()
	return problem
}

func set(k hubcrypto.Key, p string) {
	mu.Lock()
	current, problem = k, p
	mu.Unlock()
}

// Load reads the device key from dir, or makes one when the data folder has none and d
// is a TPM. d is nil on a machine without one. A sealed key that does not open is a
// problem, not an error: the hub starts without it and says so (Problem).
func Load(dir string, d Device) error {
	path := filepath.Join(dir, File)
	blob, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist) && d == nil:
		set(nil, "")
		return nil
	case errors.Is(err, fs.ErrNotExist):
		k, err := hubcrypto.NewKey()
		if err != nil {
			return err
		}
		sealed, err := d.Seal(k)
		if err != nil {
			// A TPM that will not seal is as good as none.
			slog.Warn("the TPM did not seal a device key; going on without one", "err", err)
			set(nil, "")
			return nil
		}
		if err := atomicfile.Write(path, sealed, 0o600); err != nil {
			return err
		}
		slog.Info("device key made and sealed by the TPM")
		set(k, "")
		return nil
	case err != nil:
		return err
	case d == nil:
		set(nil, "This hub's security chip (TPM) is missing. Its data cannot be opened on this machine: restore the hub from its USB stick onto an empty disk.")
		return nil
	}
	k, err := d.Unseal(blob)
	if err != nil || len(k) != hubcrypto.KeySize {
		slog.Error("the TPM does not open this hub's device key", "err", err)
		set(nil, "This hub's security chip (TPM) does not open its key: the disk was moved to another machine, or the chip was reset. Restore the hub from its USB stick onto an empty disk.")
		return nil
	}
	set(k, "")
	return nil
}

// Use sets the device key directly (tests).
func Use(k hubcrypto.Key) { set(k, "") }

const magic = "HUBDEV1\n"

// ErrNoKey is reading a sealed file without the device key that sealed it.
var ErrNoKey = errors.New("file sealed with a device key this machine does not have")

// Protect returns what to write to path for data: sealed with the device key when there
// is one, else data itself. The file's name is bound to it, so sealed files cannot be
// swapped.
func Protect(path string, data []byte) ([]byte, error) {
	k := Key()
	if k == nil {
		return data, nil
	}
	ct, err := hubcrypto.Seal(k, data, []byte(filepath.Base(path)))
	if err != nil {
		return nil, err
	}
	return append([]byte(magic), ct...), nil
}

// Unprotect returns the content of a file read from path, sealed or not.
func Unprotect(path string, b []byte) ([]byte, error) {
	if !bytes.HasPrefix(b, []byte(magic)) {
		return b, nil
	}
	k := Key()
	if k == nil {
		return nil, fmt.Errorf("%s: %w", filepath.Base(path), ErrNoKey)
	}
	return hubcrypto.Open(k, b[len(magic):], []byte(filepath.Base(path)))
}

// WriteFile writes data to path, synced and atomically, sealed when the hub has a
// device key.
func WriteFile(path string, data []byte, perm os.FileMode) error {
	b, err := Protect(path, data)
	if err != nil {
		return err
	}
	return atomicfile.Write(path, b, perm)
}

// ReadFile reads a file written by WriteFile, sealed or not.
func ReadFile(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Unprotect(path, b)
}

// Files are the secrets in the data folder the hub needs before anyone logs in: they
// are sealed with the device key where the hub has one.
var Files = []string{"tls.key", "ca.key", "setup-token", "setup-wifi", "network.json"}

// OpenDevice opens the machine's TPM (replaced in tests).
var OpenDevice = Open

// Start loads the device key for the data folder dir, unless use is false (hub.yaml's
// tpm: false), and seals the secrets written before the hub had one.
func Start(dir string, use bool) error {
	var d Device
	if use {
		var err error
		if d, err = OpenDevice(); err != nil {
			slog.Warn("cannot open the TPM; going on without a device key", "err", err)
			d = nil
		}
	}
	if d != nil {
		defer d.Close()
	}
	if err := Load(dir, d); err != nil {
		return err
	}
	return SealExisting(dir, Files...)
}

// Sealed reports whether path is sealed with a device key.
func Sealed(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	head := make([]byte, len(magic))
	n, _ := f.Read(head)
	return string(head[:n]) == magic
}

// SealExisting seals the named files in dir that were written before the hub had a
// device key. Without a key it does nothing.
func SealExisting(dir string, names ...string) error {
	if Key() == nil {
		return nil
	}
	for _, n := range names {
		p := filepath.Join(dir, n)
		if Sealed(p) {
			continue
		}
		b, err := os.ReadFile(p)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		fi, _ := os.Stat(p)
		if err := WriteFile(p, b, fi.Mode().Perm()); err != nil {
			return err
		}
	}
	return nil
}
