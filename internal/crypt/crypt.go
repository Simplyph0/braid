// Package crypt is Braid's end-to-end encryption: everything is sealed on
// the PC before it reaches the hub.
package crypt

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/blake2b"
	"golang.org/x/crypto/chacha20poly1305"
)

const KeySize = chacha20poly1305.KeySize

// DeriveKey turns a passphrase into a 256-bit key with Argon2id.
func DeriveKey(passphrase string, salt []byte) []byte {
	return argon2.IDKey([]byte(passphrase), salt, 3, 64*1024, 4, KeySize)
}

// NewKey returns a random key (one per project).
func NewKey() []byte {
	k := make([]byte, KeySize)
	if _, err := rand.Read(k); err != nil {
		panic(err)
	}
	return k
}

// Seal encrypts with XChaCha20-Poly1305; the output is nonce||ciphertext.
func Seal(key, plaintext, ad []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	out := make([]byte, aead.NonceSize(), aead.NonceSize()+len(plaintext)+aead.Overhead())
	if _, err := rand.Read(out); err != nil {
		return nil, err
	}
	return aead.Seal(out, out, plaintext, ad), nil
}

// Open reverses Seal.
func Open(key, blob, ad []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	if len(blob) < aead.NonceSize()+aead.Overhead() {
		return nil, errors.New("encrypted blob too short")
	}
	pt, err := aead.Open(nil, blob[:aead.NonceSize()], blob[aead.NonceSize():], ad)
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w", err)
	}
	return pt, nil
}

// Namer names content without revealing it: a keyed BLAKE2b-256, so equal
// content gets the same name (stored once) but the hub can't test guesses.
type Namer struct{ key []byte }

func NewNamer(key []byte) Namer {
	k := blake2b.Sum256(append([]byte("braid-names\x00"), key...))
	return Namer{key: k[:]}
}

func (n Namer) ID(kind string, data []byte) string {
	h, _ := blake2b.New256(n.key)
	h.Write([]byte(kind))
	h.Write([]byte{0})
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}
