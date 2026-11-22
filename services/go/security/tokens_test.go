package security

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"testing"
)

func TestAES256GCMKnownVectorCombinedFormat(t *testing.T) {
	// NIST AES-256-GCM vector: zero key, zero nonce, 16 zero plaintext bytes.
	// Swift's combined format prepends the nonce to ciphertext and authentication tag.
	combined, err := hex.DecodeString("000000000000000000000000cea7403d4d606b6e074ec5d3baf39d18d0d1c8a799996bf0265b98b5d48ab919")
	if err != nil {
		t.Fatal(err)
	}
	c, err := NewTokenCipher(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := c.Decrypt(base64.StdEncoding.EncodeToString(combined))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal([]byte(plain), make([]byte, 16)) {
		t.Fatalf("unexpected plaintext %x", plain)
	}
	combined[len(combined)-1] ^= 1
	if _, err = c.Decrypt(base64.StdEncoding.EncodeToString(combined)); err == nil {
		t.Fatal("tampered tag accepted")
	}
}
func TestTokenRoundTripAndNonceRandomness(t *testing.T) {
	c, err := NewTokenCipher(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	first, err := c.Encrypt("github-token-🔑")
	if err != nil {
		t.Fatal(err)
	}
	second, err := c.Encrypt("github-token-🔑")
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("nonce reused")
	}
	plain, err := c.Decrypt(first)
	if err != nil || plain != "github-token-🔑" {
		t.Fatalf("roundtrip failed: %v", err)
	}
	for _, value := range []string{"", "no base64", base64.StdEncoding.EncodeToString(make([]byte, 27))} {
		if _, err = c.Decrypt(value); err == nil {
			t.Errorf("invalid token accepted: %q", value)
		}
	}
}
func TestInvalidEncryptionKeys(t *testing.T) {
	for _, key := range []string{"", "bad", base64.StdEncoding.EncodeToString(make([]byte, 16))} {
		if _, err := NewTokenCipher(key); err == nil {
			t.Fatal("invalid key accepted")
		}
	}
}
func TestTokenHashAndEntropy(t *testing.T) {
	if HashToken("abc") != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Fatal("incompatible token hash")
	}
	first, err := NewOpaqueToken()
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewOpaqueToken()
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 64 || first == second {
		t.Fatal("invalid opaque token")
	}
	if _, err = hex.DecodeString(first); err != nil {
		t.Fatal(err)
	}
}
