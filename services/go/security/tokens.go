// Package security preserves credential formats used by the Swift gateway.
package security

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"unicode/utf8"
)

// TokenCipher uses Swift Crypto AES.GCM.SealedBox.combined serialization:
// a 12-byte nonce, ciphertext, then the 16-byte authentication tag.
type TokenCipher struct{ aead cipher.AEAD }

func NewTokenCipher(base64Key string) (*TokenCipher, error) {
	key, err := base64.StdEncoding.DecodeString(base64Key)
	if err != nil || len(key) != 32 {
		return nil, errors.New("ENCRYPTION_KEY must be a base64 encoded 32-byte key")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &TokenCipher{aead: aead}, nil
}

func (c *TokenCipher) Encrypt(plaintext string) (string, error) {
	if !utf8.ValidString(plaintext) {
		return "", errors.New("token is not valid UTF-8")
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	combined := c.aead.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(combined), nil
}

func (c *TokenCipher) Decrypt(encoded string) (string, error) {
	combined, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(combined) < c.aead.NonceSize()+c.aead.Overhead() {
		return "", errors.New("invalid encrypted token")
	}
	plaintext, err := c.aead.Open(nil, combined[:c.aead.NonceSize()], combined[c.aead.NonceSize():], nil)
	if err != nil || !utf8.Valid(plaintext) {
		return "", errors.New("invalid encrypted token")
	}
	return string(plaintext), nil
}

// HashToken matches lowercase SHA-256 digests used by API keys and OAuth tokens.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// NewOpaqueToken matches the existing 32 random bytes encoded as 64 hex characters.
func NewOpaqueToken() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}
