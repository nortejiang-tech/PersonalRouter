package security

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
)

const vaultVersion = "v1"

// Vault encrypts small application secrets with AES-GCM.
type Vault struct {
	aead cipher.AEAD
}

// NewVault creates an AES-256-GCM vault from an exact 32-byte key.
func NewVault(key []byte) (*Vault, error) {
	if len(key) != 32 {
		return nil, errors.New("vault key must be exactly 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create vault cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create vault AEAD: %w", err)
	}
	return &Vault{aead: aead}, nil
}

// Seal encrypts plaintext with a fresh nonce and returns a versioned token.
func (v *Vault) Seal(plaintext []byte) (string, error) {
	if v == nil || v.aead == nil {
		return "", errors.New("nil vault")
	}
	nonce := make([]byte, v.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generate vault nonce: %w", err)
	}
	ciphertext := v.aead.Seal(nil, nonce, plaintext, []byte(vaultVersion))
	encoded := make([]byte, 0, len(nonce)+len(ciphertext))
	encoded = append(encoded, nonce...)
	encoded = append(encoded, ciphertext...)
	return vaultVersion + "." + base64.RawURLEncoding.EncodeToString(encoded), nil
}

// Open authenticates and decrypts a versioned vault token.
func (v *Vault) Open(encoded string) ([]byte, error) {
	if v == nil || v.aead == nil {
		return nil, errors.New("nil vault")
	}
	prefix, payload, ok := strings.Cut(encoded, ".")
	if !ok || prefix != vaultVersion || payload == "" {
		return nil, errors.New("malformed vault encoding")
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return nil, errors.New("malformed vault encoding")
	}
	if base64.RawURLEncoding.EncodeToString(raw) != payload {
		return nil, errors.New("malformed vault encoding")
	}
	minimum := v.aead.NonceSize() + v.aead.Overhead()
	if len(raw) < minimum {
		return nil, errors.New("malformed vault encoding")
	}
	nonce := raw[:v.aead.NonceSize()]
	ciphertext := raw[v.aead.NonceSize():]
	plaintext, err := v.aead.Open(nil, nonce, ciphertext, []byte(vaultVersion))
	if err != nil {
		return nil, errors.New("vault authentication failed")
	}
	return plaintext, nil
}
