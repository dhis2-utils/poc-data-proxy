package hubcrypto

import (
	"bytes"
	"testing"
)

func init() { ArgonMemory, ArgonTime = 1024, 1 }

func TestSealOpen(t *testing.T) {
	k, _ := NewKey()
	ct, err := Seal(k, []byte("hello"), []byte("a"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ct, []byte("hello")) {
		t.Fatal("plaintext visible")
	}
	pt, err := Open(k, ct, []byte("a"))
	if err != nil || string(pt) != "hello" {
		t.Fatalf("got %q %v", pt, err)
	}
	if _, err := Open(k, ct, []byte("b")); err == nil {
		t.Fatal("wrong aad accepted")
	}
}

func TestKeySlot(t *testing.T) {
	dk, _ := NewKey()
	salt, _ := RandomBytes(16)
	w, err := WrapDK(dk, "pw", salt)
	if err != nil {
		t.Fatal(err)
	}
	got, err := UnwrapDK(w, "pw", salt)
	if err != nil || !bytes.Equal(got, dk) {
		t.Fatal("unwrap failed")
	}
	if _, err := UnwrapDK(w, "nope", salt); err == nil {
		t.Fatal("wrong password unwrapped")
	}
}

func TestPasswordHash(t *testing.T) {
	h, err := HashPassword("secret")
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword(h, "secret") || VerifyPassword(h, "Secret") {
		t.Fatal("verify mismatch")
	}
}
