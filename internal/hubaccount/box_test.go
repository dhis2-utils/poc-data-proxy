package hubaccount

import (
	"bytes"
	"testing"
)

// The account reaches the hub through its registry entry, which hub owners can read:
// encrypted to the hub's key, bound to its ID.
func TestBox(t *testing.T) {
	pub, priv, err := NewBoxKey()
	if err != nil {
		t.Fatal(err)
	}
	box, err := Seal(pub, []byte("secret"), "hub-A")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(box, []byte("secret")) {
		t.Fatal("plaintext in the box")
	}
	if got, err := Open(priv, box, "hub-A"); err != nil || string(got) != "secret" {
		t.Fatalf("open: %q %v", got, err)
	}
	if _, err := Open(priv, box, "hub-B"); err == nil {
		t.Fatal("a box for another hub opened")
	}
	_, other, _ := NewBoxKey()
	if _, err := Open(other, box, "hub-A"); err == nil {
		t.Fatal("opened with another key")
	}
}
