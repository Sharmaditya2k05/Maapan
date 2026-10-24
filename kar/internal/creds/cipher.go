// Package creds stores each GSTIN's e-invoice / e-way portal API users
// encrypted at rest with AES-256-GCM under CREDENTIALS_KEY (same scheme as lenden's gateway keys).
package creds

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
)

// KeySize is the AES-256 key length in bytes.
const KeySize = 32

// blobVersion prefixes every sealed secret so the format can evolve.
const blobVersion = 1

// Cipher seals and opens secrets with AES-256-GCM.
type Cipher struct{ aead cipher.AEAD }

// NewCipher builds a Cipher from a 32-byte key.
func NewCipher(key []byte) (*Cipher, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("credentials key must be %d bytes, got %d", KeySize, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Cipher{aead: aead}, nil
}

// ParseKey decodes CREDENTIALS_KEY: 64 hex characters, or base64 (standard or
// URL alphabet, padded or not) of 32 bytes.
func ParseKey(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, errors.New("empty key")
	}
	if len(s) == 2*KeySize {
		if b, err := hex.DecodeString(s); err == nil {
			return b, nil
		}
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil && len(b) == KeySize {
			return b, nil
		}
	}
	return nil, errors.New("CREDENTIALS_KEY must be 32 bytes as 64 hex characters or base64")
}

// DeriveKey derives a key from SERVICE_SECRET with SHA-256 (local/dev only).
func DeriveKey(serviceSecret string) []byte {
	sum := sha256.Sum256([]byte("kar-credentials:" + serviceSecret))
	return sum[:]
}

// Load builds the Cipher from CREDENTIALS_KEY. Outside prod an empty value
// derives a key from SERVICE_SECRET and warns; prod refuses to start without one.
func Load(credentialsKey, serviceSecret string, prod bool, log *slog.Logger) (*Cipher, error) {
	if strings.TrimSpace(credentialsKey) == "" {
		if prod {
			return nil, errors.New("CREDENTIALS_KEY is required in prod (32 bytes, hex or base64)")
		}
		if serviceSecret == "" {
			return nil, errors.New("CREDENTIALS_KEY or SERVICE_SECRET is required")
		}
		if log != nil {
			log.Warn("CREDENTIALS_KEY is empty; deriving the GSP-credentials key from SERVICE_SECRET (local/dev only)")
		}
		return NewCipher(DeriveKey(serviceSecret))
	}
	key, err := ParseKey(credentialsKey)
	if err != nil {
		return nil, err
	}
	return NewCipher(key)
}

// Seal encrypts plain: version || nonce || ciphertext+tag. aad binds the blob
// to its row (tenant/GSTIN/column) so blobs cannot be swapped between rows.
func (c *Cipher) Seal(plain string, aad string) ([]byte, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	out := make([]byte, 0, 1+len(nonce)+len(plain)+c.aead.Overhead())
	out = append(out, blobVersion)
	out = append(out, nonce...)
	return c.aead.Seal(out, nonce, []byte(plain), []byte(aad)), nil
}

// Open decrypts a blob produced by Seal with the same aad.
func (c *Cipher) Open(blob []byte, aad string) (string, error) {
	ns := c.aead.NonceSize()
	if len(blob) < 1+ns+c.aead.Overhead() {
		return "", errors.New("sealed secret is too short")
	}
	if blob[0] != blobVersion {
		return "", fmt.Errorf("unknown sealed secret version %d", blob[0])
	}
	plain, err := c.aead.Open(nil, blob[1:1+ns], blob[1+ns:], []byte(aad))
	if err != nil {
		return "", errors.New("cannot decrypt secret (wrong CREDENTIALS_KEY?)")
	}
	return string(plain), nil
}

// Mask keeps only the last 4 characters of a key id for display.
func Mask(keyID string) string {
	if len(keyID) <= 4 {
		return strings.Repeat("*", len(keyID))
	}
	return "****" + keyID[len(keyID)-4:]
}
