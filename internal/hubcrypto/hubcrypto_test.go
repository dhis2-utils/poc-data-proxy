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

// A slot bound to a device key opens only with it; the stick's copy opens with the
// password alone; a slot made without a device key opens with or without one.
func TestSlotsBoundToTheDevice(t *testing.T) {
	dk, _ := NewKey()
	dev, other := make(Key, KeySize), make(Key, KeySize)
	other[0] = 1
	salt := []byte("0123456789abcdef")
	slot, stick, err := WrapSlot(dk, "pw", salt, dev)
	if err != nil || stick == nil {
		t.Fatal(err)
	}
	if _, err := UnwrapDK(slot, "pw", salt); err == nil {
		t.Fatal("a bound slot opened with the password alone")
	}
	if _, _, err := UnwrapSlot(slot, "pw", salt, other); err == nil {
		t.Fatal("a bound slot opened with another device key")
	}
	if got, bound, err := UnwrapSlot(slot, "pw", salt, dev); err != nil || !bound || string(got) != string(dk) {
		t.Fatalf("bound slot: %v %v", bound, err)
	}
	if got, err := UnwrapDK(stick, "pw", salt); err != nil || string(got) != string(dk) {
		t.Fatalf("stick copy: %v", err)
	}
	plain, none, _ := WrapSlot(dk, "pw", salt, nil)
	if none != nil {
		t.Fatal("a stick copy without a device key")
	}
	if _, bound, err := UnwrapSlot(plain, "pw", salt, dev); err != nil || bound {
		t.Fatalf("an old slot with a device key: bound %v, %v", bound, err)
	}
	if _, _, err := UnwrapSlot(slot, "wrong", salt, dev); err == nil {
		t.Fatal("a wrong password opened the slot")
	}
}
