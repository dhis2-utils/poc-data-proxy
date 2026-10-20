package devkey

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/go-tpm/tpm2"
	"github.com/google/go-tpm/tpm2/transport/tcp"

	"github.com/dhis2/facility-hub/internal/hubcrypto"
)

// swtpm starts a software TPM (swtpm) with its state in dir, or skips the test where
// swtpm is not installed. With persistent, it is provisioned as Windows does: a storage
// key at 0x81000001.
func swtpm(t *testing.T, dir string, persistent bool) *TPM {
	t.Helper()
	if _, err := exec.LookPath("swtpm"); err != nil {
		t.Skip("swtpm not installed")
	}
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	cmd := exec.Command("swtpm", "socket", "--tpm2", "--tpmstate", "dir="+dir,
		"--server", fmt.Sprintf("type=tcp,port=%d,bindaddr=127.0.0.1", port),
		"--ctrl", fmt.Sprintf("type=tcp,port=%d,bindaddr=127.0.0.1", port+1),
		"--flags", "not-need-init,startup-clear")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	var tr *tcp.TPM
	var err error
	for i := 0; i < 50; i++ {
		if tr, err = tcp.Open(tcp.Config{CommandAddress: fmt.Sprintf("127.0.0.1:%d", port),
			PlatformAddress: fmt.Sprintf("127.0.0.1:%d", port+1)}); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	d := NewTPM(tr)
	t.Cleanup(func() { d.Close() })
	if persistent {
		if _, err := (tpm2.ReadPublic{ObjectHandle: srkHandle}).Execute(tr); err != nil {
			p, err := (tpm2.CreatePrimary{PrimaryHandle: tpm2.TPMRHOwner, InPublic: tpm2.New2B(tpm2.ECCSRKTemplate)}).Execute(tr)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := (tpm2.EvictControl{Auth: tpm2.TPMRHOwner, ObjectHandle: &tpm2.NamedHandle{Handle: p.ObjectHandle, Name: p.Name},
				PersistentHandle: srkHandle}).Execute(tr); err != nil {
				t.Fatal(err)
			}
		}
	}
	return d
}

func TestTPMSealsAndUnseals(t *testing.T) {
	for _, persistent := range []bool{false, true} {
		t.Run(fmt.Sprint("persistent=", persistent), func(t *testing.T) {
			d := swtpm(t, t.TempDir(), persistent)
			secret := bytes.Repeat([]byte{7}, 32)
			blob, err := d.Seal(secret)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(blob, secret) {
				t.Fatal("the sealed blob holds the secret in the clear")
			}
			got, err := d.Unseal(blob)
			if err != nil || !bytes.Equal(got, secret) {
				t.Fatalf("unseal: %x %v", got, err)
			}
		})
	}
}

// A blob sealed by one TPM does not open on another: a disk moved to another machine.
func TestTPMBlobFromAnotherTPM(t *testing.T) {
	a, b := swtpm(t, t.TempDir(), false), swtpm(t, t.TempDir(), false)
	blob, err := a.Seal(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Unseal(blob); err == nil {
		t.Fatal("another TPM opened the blob")
	}
}

func TestLoadMakesKeepsAndMissesTheKey(t *testing.T) {
	dir := t.TempDir()
	t.Cleanup(func() { set(nil, "") })
	if err := Load(dir, nil); err != nil || Key() != nil {
		t.Fatalf("no TPM: key %x, err %v", Key(), err)
	}
	d := NewFake()
	if err := Load(dir, d); err != nil || Key() == nil {
		t.Fatalf("with a TPM: no key: %v", err)
	}
	first := Key()
	if err := Load(dir, d); err != nil || !bytes.Equal(Key(), first) {
		t.Fatal("the key changed at the next start")
	}
	if err := Load(dir, NewFake()); err != nil || Key() != nil || Problem() == "" {
		t.Fatal("another TPM: the key must be missing, with a problem to show")
	}
	if err := Load(dir, nil); err != nil || Key() != nil || Problem() == "" {
		t.Fatal("TPM gone: the key must be missing, with a problem to show")
	}
}

func TestSealedFiles(t *testing.T) {
	dir := t.TempDir()
	t.Cleanup(func() { set(nil, "") })
	p := filepath.Join(dir, "tls.key")
	os.WriteFile(p, []byte("secret key"), 0o600)
	Use(nil)
	SealExisting(dir, "tls.key", "absent")
	if Sealed(p) {
		t.Fatal("sealed without a device key")
	}
	k, _ := hubcrypto.NewKey()
	Use(k)
	if err := SealExisting(dir, "tls.key", "absent"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p)
	if !Sealed(p) || bytes.Contains(raw, []byte("secret key")) {
		t.Fatal("tls.key not sealed")
	}
	if b, err := ReadFile(p); err != nil || string(b) != "secret key" {
		t.Fatalf("read back %q %v", b, err)
	}
	// Bound to its name: a sealed file copied over another does not open.
	q := filepath.Join(dir, "ca.key")
	os.WriteFile(q, raw, 0o600)
	if _, err := ReadFile(q); err == nil {
		t.Fatal("a sealed file opened under another name")
	}
	Use(nil)
	if _, err := ReadFile(p); !errors.Is(err, ErrNoKey) {
		t.Fatalf("read without the key: %v", err)
	}
}
