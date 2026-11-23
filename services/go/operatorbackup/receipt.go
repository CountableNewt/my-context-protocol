package operatorbackup

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
)

// Receipt is signed only after local decryption, SQLite restore and exact typed
// reconstruction verification. It is public metadata, not the backup itself.
type Receipt struct {
	Version        int    `json:"version"`
	Host           string `json:"host"`
	SnapshotID     string `json:"snapshot_id"`
	EnvelopeSHA256 string `json:"envelope_sha256"`
	Signature      string `json:"signature"`
}

func (r Receipt) payload() ([]byte, error) { r.Signature = ""; return json.Marshal(r) }
func (r *Receipt) Sign(key *rsa.PrivateKey) error {
	raw, err := r.payload()
	if err != nil {
		return err
	}
	hash := sha256.Sum256(raw)
	sig, err := rsa.SignPSS(rand.Reader, key, crypto.SHA256, hash[:], &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash})
	if err == nil {
		r.Signature = base64.StdEncoding.EncodeToString(sig)
	}
	return err
}
func (r Receipt) Verify(key *rsa.PublicKey, host string) error {
	if r.Version != 1 || r.Host != host || len(r.SnapshotID) != 64 || len(r.EnvelopeSHA256) != 64 {
		return errors.New("invalid reviewed backup receipt")
	}
	raw, err := r.payload()
	if err != nil {
		return err
	}
	sig, err := base64.StdEncoding.DecodeString(r.Signature)
	if err != nil {
		return errors.New("invalid review signature")
	}
	hash := sha256.Sum256(raw)
	if rsa.VerifyPSS(key, crypto.SHA256, hash[:], sig, &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash}) != nil {
		return errors.New("backup review signature rejected")
	}
	return nil
}
