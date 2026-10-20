// Package hubcrypto holds the data key handling from spec 9.1: a random 256-bit data
// key (DK) encrypts everything sensitive with AES-256-GCM, and each user holds a key
// slot (DK wrapped under a key derived from their password with Argon2id).
package hubcrypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"

	"golang.org/x/crypto/argon2"
)

const KeySize = 32

// Argon2id parameters, sized for a Raspberry Pi 4 (about 0.3 s per derivation).
// Kept as variables so tests can lower the cost.
var (
	ArgonTime    uint32 = 2
	ArgonMemory  uint32 = 64 * 1024
	ArgonThreads uint8  = 2
)

var ErrDecrypt = errors.New("decryption failed")

// argonSlots bounds concurrent Argon2id derivations, so a burst of logins (or of
// guesses) costs at most this many times ArgonMemory of RAM.
var argonSlots = make(chan struct{}, 2)

func idKey(password, salt []byte, t, m uint32, p uint8, n uint32) []byte {
	argonSlots <- struct{}{}
	defer func() { <-argonSlots }()
	return argon2.IDKey(password, salt, t, m, p, n)
}

// Key is a 256-bit AES key.
type Key []byte

func NewKey() (Key, error) { return RandomBytes(KeySize) }

func RandomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	_, err := rand.Read(b)
	return b, err
}

// Seal encrypts plaintext with a random nonce. aad binds the ciphertext to its
// context (for example the row it belongs to), so ciphertexts cannot be swapped.
func Seal(k Key, plaintext, aad []byte) ([]byte, error) {
	gcm, err := newGCM(k)
	if err != nil {
		return nil, err
	}
	nonce, err := RandomBytes(gcm.NonceSize())
	if err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plaintext, aad), nil
}

func Open(k Key, ciphertext, aad []byte) ([]byte, error) {
	gcm, err := newGCM(k)
	if err != nil {
		return nil, err
	}
	if len(ciphertext) < gcm.NonceSize() {
		return nil, ErrDecrypt
	}
	n := gcm.NonceSize()
	pt, err := gcm.Open(nil, ciphertext[:n], ciphertext[n:], aad)
	if err != nil {
		return nil, ErrDecrypt
	}
	return pt, nil
}

func newGCM(k Key) (cipher.AEAD, error) {
	if len(k) != KeySize {
		return nil, fmt.Errorf("key must be %d bytes", KeySize)
	}
	b, err := aes.NewCipher(k)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(b)
}

// DeriveKEK derives a key-encryption key from a password and per-user salt.
func DeriveKEK(password string, salt []byte) Key {
	return idKey([]byte(password), salt, ArgonTime, ArgonMemory, ArgonThreads, KeySize)
}

// WrapDK wraps the data key for one user's key slot.
func WrapDK(dk Key, password string, salt []byte) ([]byte, error) {
	return Seal(DeriveKEK(password, salt), dk, []byte("keyslot"))
}

// UnwrapDK opens a key slot. It fails if the password is wrong.
func UnwrapDK(wrapped []byte, password string, salt []byte) (Key, error) {
	return Open(DeriveKEK(password, salt), wrapped, []byte("keyslot"))
}

// A key slot bound to the hub's device key (internal/devkey) opens only with the
// password and the device key together, so a disk taken without its TPM gives no
// offline password guessing. The USB stick keeps the slot with the password alone, so
// the hub can be restored on new hardware.

func boundKEK(kek, dev Key) (Key, error) {
	return hkdf.Key(sha256.New, append(append([]byte{}, kek...), dev...), nil, "hub key slot bound to the device", KeySize)
}

// WrapSlot wraps the data key for one user's key slot, bound to the device key dev when
// there is one. stick is the slot with the password alone, for the USB stick's copy;
// nil without a device key, when the slot itself is that.
func WrapSlot(dk Key, password string, salt []byte, dev Key) (slot, stick []byte, err error) {
	kek := DeriveKEK(password, salt)
	plain, err := Seal(kek, dk, []byte("keyslot"))
	if err != nil || dev == nil {
		return plain, nil, err
	}
	bk, err := boundKEK(kek, dev)
	if err != nil {
		return nil, nil, err
	}
	slot, err = Seal(bk, dk, []byte("keyslot-bound"))
	return slot, plain, err
}

// UnwrapSlot opens a key slot, bound to dev or not; bound reports which. It fails if
// the password is wrong, or the slot is bound to another device key.
func UnwrapSlot(wrapped []byte, password string, salt []byte, dev Key) (dk Key, bound bool, err error) {
	kek := DeriveKEK(password, salt)
	if dev != nil {
		if bk, err := boundKEK(kek, dev); err == nil {
			if dk, err := Open(bk, wrapped, []byte("keyslot-bound")); err == nil {
				return dk, true, nil
			}
		}
	}
	dk, err = Open(kek, wrapped, []byte("keyslot"))
	return dk, false, err
}
