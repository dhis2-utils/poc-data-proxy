package hubaccount

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"encoding/hex"
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

// The Facility Hub app seals accounts too (apps/facility-hub/src/hubaccount/box.ts):
// for fixed inputs, the box built by the documented steps (docs/FACILITY-HUB-APP.md,
// step 6) has these bytes, which the app's test pins as well, and the hub opens it.
func TestBoxVector(t *testing.T) {
	fill := func(n int, b byte) []byte { return bytes.Repeat([]byte{b}, n) }
	hubPriv, _ := ecdh.X25519().NewPrivateKey(fill(32, 5))
	eph, _ := ecdh.X25519().NewPrivateKey(fill(32, 3))
	nonce, plaintext, hubID := fill(12, 4), []byte(`{"username":"hub-x"}`), "hub-x"
	key, err := boxKey(eph, hubPriv.PublicKey(), hubID)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := aes.NewCipher(key)
	gcm, _ := cipher.NewGCM(block)
	box := append(append(eph.PublicKey().Bytes(), nonce...), gcm.Seal(nil, nonce, plaintext, []byte("hub-account:"+hubID))...)
	const want = "5dfedd3b6bd47f6fa28ee15d969d5bb0ea53774d488bdaf9df1c6e0124b3ef2204040404040404040404040479e45e7ffcb136196cd285a04af837229429c31346c1b39e97d56e3b482473413f97313b"
	if got := hex.EncodeToString(box); got != want {
		t.Errorf("box = %s", got)
	}
	if got, err := Open(hubPriv.Bytes(), box, hubID); err != nil || !bytes.Equal(got, plaintext) {
		t.Fatalf("the hub does not open the box: %q %v", got, err)
	}
}
