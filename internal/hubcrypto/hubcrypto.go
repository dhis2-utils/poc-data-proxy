// Package hubcrypto holds the data key handling from spec 9.1: a random 256-bit data
// key (DK) encrypts everything sensitive with AES-256-GCM, and each user holds a key
// slot (DK wrapped under a key derived from their password with Argon2id).
package hubcrypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

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
	return argon2.IDKey([]byte(password), salt, ArgonTime, ArgonMemory, ArgonThreads, KeySize)
}

// WrapDK wraps the data key for one user's key slot.
func WrapDK(dk Key, password string, salt []byte) ([]byte, error) {
	return Seal(DeriveKEK(password, salt), dk, []byte("keyslot"))
}

// UnwrapDK opens a key slot. It fails if the password is wrong.
func UnwrapDK(wrapped []byte, password string, salt []byte) (Key, error) {
	return Open(DeriveKEK(password, salt), wrapped, []byte("keyslot"))
}

// HashPassword returns an encoded Argon2id hash for offline login.
func HashPassword(password string) (string, error) {
	salt, err := RandomBytes(16)
	if err != nil {
		return "", err
	}
	h := argon2.IDKey([]byte(password), salt, ArgonTime, ArgonMemory, ArgonThreads, 32)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", ArgonMemory, ArgonTime, ArgonThreads,
		b64(salt), b64(h)), nil
}

// VerifyPassword checks a password against an encoded hash from HashPassword.
func VerifyPassword(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false
	}
	salt, err1 := unb64(parts[4])
	want, err2 := unb64(parts[5])
	if err1 != nil || err2 != nil {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

func b64(b []byte) string            { return base64.RawStdEncoding.EncodeToString(b) }
func unb64(s string) ([]byte, error) { return base64.RawStdEncoding.DecodeString(s) }
