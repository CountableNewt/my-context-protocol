// Package operatorservice exposes only signed, fixed operator commands in a
// temporary unaliased service. It is not part of the product API.
package operatorservice

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/CountableNewt/my-context-protocol/services/go/operatorbackup"
	"time"
)

type Command struct {
	Version       int                     `json:"version"`
	Method        string                  `json:"method"`
	Path          string                  `json:"path"`
	Mode          string                  `json:"mode"`
	Audience      string                  `json:"audience"`
	Host          string                  `json:"host"`
	Nonce         string                  `json:"nonce"`
	IssuedAt      int64                   `json:"issued_at"`
	ExpiresAt     int64                   `json:"expires_at"`
	WritersPaused bool                    `json:"writers_paused"`
	Review        *operatorbackup.Receipt `json:"review,omitempty"`
	Signature     string                  `json:"signature"`
}

func (c Command) payload() ([]byte, error) { c.Signature = ""; return json.Marshal(c) }
func (c *Command) Sign(key *rsa.PrivateKey) error {
	raw, err := c.payload()
	if err != nil {
		return err
	}
	hash := sha256.Sum256(raw)
	sig, err := rsa.SignPSS(rand.Reader, key, crypto.SHA256, hash[:], &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash})
	if err == nil {
		c.Signature = base64.StdEncoding.EncodeToString(sig)
	}
	return err
}
func (c Command) Verify(key *rsa.PublicKey, audience, host string, now time.Time) error {
	if c.Version != 1 || c.Method != "POST" || c.Path != "/operator" || c.Audience != audience || c.Host != host || c.IssuedAt > now.Unix()+30 || c.IssuedAt < now.Unix()-900 || c.ExpiresAt <= now.Unix() || c.ExpiresAt <= c.IssuedAt || c.ExpiresAt-c.IssuedAt > 900 {
		return errors.New("invalid operator authorization")
	}
	if c.Mode != "preflight" && c.Mode != "snapshot" && c.Mode != "apply" {
		return errors.New("unknown operator command")
	}
	nonce, err := hex.DecodeString(c.Nonce)
	if err != nil || len(nonce) != 32 {
		return errors.New("invalid operator nonce")
	}
	if c.Mode != "preflight" && !c.WritersPaused {
		return errors.New("writers-paused authorization required")
	}
	if c.Mode == "apply" {
		if c.Review == nil {
			return errors.New("review required")
		}
		if err = c.Review.Verify(key, host); err != nil {
			return err
		}
	} else if c.Review != nil {
		return errors.New("unexpected review")
	}
	raw, err := c.payload()
	if err != nil {
		return err
	}
	signature, err := base64.StdEncoding.DecodeString(c.Signature)
	if err != nil || len(signature) != key.Size() {
		return errors.New("invalid operator signature")
	}
	hash := sha256.Sum256(raw)
	if rsa.VerifyPSS(key, crypto.SHA256, hash[:], signature, &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash}) != nil {
		return errors.New("operator signature rejected")
	}
	return nil
}
