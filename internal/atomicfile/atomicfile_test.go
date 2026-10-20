package atomicfile

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestWriteAndCopy(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a")
	os.WriteFile(p, []byte("old"), 0o644)
	if err := Write(p, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "new" {
		t.Fatalf("content %q", b)
	}
	if fi, _ := os.Stat(p); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode().Perm())
	}
	q := filepath.Join(dir, "b")
	if err := Copy(p, q, 0o755); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(q); string(b) != "new" {
		t.Fatalf("copy %q", b)
	}
	if _, err := os.Stat(q + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("a temporary file is left")
	}
	if err := Copy(filepath.Join(dir, "absent"), q, 0o644); err == nil {
		t.Fatal("copying a missing file succeeded")
	}
	if b, _ := os.ReadFile(q); string(b) != "new" {
		t.Fatal("a failed copy changed the file")
	}
}
