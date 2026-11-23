// Package operatorbackup seals private snapshots for an operator-held recipient.
// RSA-OAEP wraps an independent random AES-256-GCM key. The private RSA key never
// belongs in a deployment; authenticated decryption happens only on the operator.
package operatorbackup

import (
	"bytes"
	"compress/gzip"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"io"
)

const MaxPlaintext = 256 << 20

var legacyMagic = []byte("MYCTX-SNAPSHOT-ENVELOPE-1\n")
var magic = []byte("MYCTX-SNAPSHOT-ENVELOPE-2\n")
var label = []byte("mycontextprotocol-production-snapshot-v1")

func Generate() ([]byte, []byte, error) {
	key, err := rsa.GenerateKey(rand.Reader, 4096)
	if err != nil {
		return nil, nil, err
	}
	pub, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return nil, nil, err
	}
	priv, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pub}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: priv}), nil
}
func Public(raw []byte) (*rsa.PublicKey, error) {
	block, rest := pem.Decode(raw)
	if block == nil || len(bytes.TrimSpace(rest)) != 0 {
		return nil, errors.New("invalid recipient PEM")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, errors.New("invalid recipient key")
	}
	key, ok := parsed.(*rsa.PublicKey)
	if !ok || key.N.BitLen() < 4096 || key.N.BitLen() > 8192 || key.E != 65537 {
		return nil, errors.New("recipient requires RSA4096..8192 public key")
	}
	return key, nil
}
func Private(raw []byte) (*rsa.PrivateKey, error) {
	block, rest := pem.Decode(raw)
	if block == nil || len(bytes.TrimSpace(rest)) != 0 {
		return nil, errors.New("invalid private PEM")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("invalid private key")
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok || key.N.BitLen() < 4096 || key.N.BitLen() > 8192 {
		return nil, errors.New("invalid RSA private key")
	}
	return key, key.Validate()
}
func Seal(plain []byte, key *rsa.PublicKey) ([]byte, error) {
	if len(plain) == 0 || len(plain) > MaxPlaintext {
		return nil, errors.New("snapshot size outside bounds")
	}
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write(plain); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	plain = compressed.Bytes()
	if len(plain) > MaxPlaintext {
		return nil, errors.New("compressed snapshot exceeds bounds")
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	wrapped, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, key, secret, label)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(secret)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return nil, err
	}
	header := append([]byte{}, magic...)
	header = binary.BigEndian.AppendUint16(header, uint16(len(wrapped)))
	header = append(header, wrapped...)
	header = append(header, nonce...)
	return gcm.Seal(header, nonce, plain, header), nil
}
func Open(envelope []byte, key *rsa.PrivateKey) ([]byte, error) {
	if len(envelope) > MaxPlaintext+2048 || len(envelope) < len(magic)+2 || (!bytes.Equal(envelope[:len(magic)], magic) && !bytes.Equal(envelope[:len(magic)], legacyMagic)) {
		return nil, errors.New("invalid snapshot envelope")
	}
	n := int(binary.BigEndian.Uint16(envelope[len(magic):]))
	start := len(magic) + 2
	end := start + n
	if n != key.Size() || len(envelope) < end+12+16 {
		return nil, errors.New("invalid wrapped snapshot key")
	}
	secret, err := rsa.DecryptOAEP(sha256.New(), rand.Reader, key, envelope[start:end], label)
	if err != nil {
		return nil, errors.New("snapshot key authentication failed")
	}
	block, err := aes.NewCipher(secret)
	if err != nil {
		return nil, errors.New("invalid snapshot key")
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	header := envelope[:end+12]
	plain, err := gcm.Open(nil, envelope[end:end+12], envelope[end+12:], header)
	if err != nil {
		return nil, errors.New("snapshot authentication failed")
	}
	if len(plain) > MaxPlaintext {
		return nil, errors.New("snapshot plaintext exceeds bounds")
	}
	if bytes.Equal(envelope[:len(magic)], legacyMagic) {
		return plain, nil
	}
	reader, err := gzip.NewReader(bytes.NewReader(plain))
	if err != nil {
		return nil, errors.New("invalid compressed snapshot")
	}
	defer reader.Close()
	decompressed, err := io.ReadAll(io.LimitReader(reader, MaxPlaintext+1))
	if err != nil || len(decompressed) > MaxPlaintext {
		return nil, errors.New("decompressed snapshot exceeds bounds or is invalid")
	}
	return decompressed, nil
}
