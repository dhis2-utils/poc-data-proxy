// Package hubaccount gives a hub an account of its own on central (docs/OPERATIONS.md,
// "The hub's own account"): someone allowed to create users approves the hub, and the
// account reaches the hub encrypted to a key only the hub holds.
package hubaccount

import (
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"errors"

	"github.com/dhis2/facility-hub/internal/hubcrypto"
)

// NewBoxKey makes the hub's X25519 key pair for receiving its account.
func NewBoxKey() (pub, priv []byte, err error) {
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	return k.PublicKey().Bytes(), k.Bytes(), nil
}

// PublicKey is the public half of a box key.
func PublicKey(priv []byte) ([]byte, error) {
	k, err := ecdh.X25519().NewPrivateKey(priv)
	if err != nil {
		return nil, err
	}
	return k.PublicKey().Bytes(), nil
}

// Seal encrypts plaintext to the hub with this public key, bound to its ID: an
// ephemeral X25519 key, a key derived from the shared secret, AES-GCM.
func Seal(pub, plaintext []byte, hubID string) ([]byte, error) {
	to, err := ecdh.X25519().NewPublicKey(pub)
	if err != nil {
		return nil, err
	}
	eph, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	key, err := boxKey(eph, to, hubID)
	if err != nil {
		return nil, err
	}
	ct, err := hubcrypto.Seal(key, plaintext, []byte("hub-account:"+hubID))
	if err != nil {
		return nil, err
	}
	return append(eph.PublicKey().Bytes(), ct...), nil
}

// Open decrypts a box sealed to this hub.
func Open(priv, box []byte, hubID string) ([]byte, error) {
	if len(box) < 32 {
		return nil, errors.New("box too short")
	}
	k, err := ecdh.X25519().NewPrivateKey(priv)
	if err != nil {
		return nil, err
	}
	from, err := ecdh.X25519().NewPublicKey(box[:32])
	if err != nil {
		return nil, err
	}
	key, err := boxKey(k, from, hubID)
	if err != nil {
		return nil, err
	}
	return hubcrypto.Open(key, box[32:], []byte("hub-account:"+hubID))
}

func boxKey(priv *ecdh.PrivateKey, pub *ecdh.PublicKey, hubID string) (hubcrypto.Key, error) {
	shared, err := priv.ECDH(pub)
	if err != nil {
		return nil, err
	}
	return hkdf.Key(sha256.New, shared, nil, "facility-hub account box "+hubID, hubcrypto.KeySize)
}
