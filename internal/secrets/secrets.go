// Package secrets encrypts credentials at rest (NFR-6) with AES-256-GCM.
// The key lives in a file next to the database, generated on first start.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"
	"os"
)

// Box encrypts and decrypts small secrets with a static key.
type Box struct {
	aead cipher.AEAD
}

// Open loads the 32-byte key from keyPath, creating it with restrictive
// permissions if it does not exist yet.
func Open(keyPath string) (*Box, error) {
	key, err := os.ReadFile(keyPath)
	if os.IsNotExist(err) {
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, fmt.Errorf("generating key: %w", err)
		}
		if err := os.WriteFile(keyPath, key, 0o600); err != nil {
			return nil, fmt.Errorf("writing key file: %w", err)
		}
	} else if err != nil {
		return nil, fmt.Errorf("reading key file: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("key file %s has %d bytes, want 32", keyPath, len(key))
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

// Encrypt seals plaintext into nonce||ciphertext.
func (b *Box) Encrypt(plaintext string) ([]byte, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return b.aead.Seal(nonce, nonce, []byte(plaintext), nil), nil
}

// Decrypt opens a blob produced by Encrypt.
func (b *Box) Decrypt(blob []byte) (string, error) {
	if len(blob) < b.aead.NonceSize() {
		return "", fmt.Errorf("ciphertext too short")
	}
	nonce, ciphertext := blob[:b.aead.NonceSize()], blob[b.aead.NonceSize():]
	plaintext, err := b.aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("decrypting: %w", err)
	}
	return string(plaintext), nil
}
