package operatorservice

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/CountableNewt/my-context-protocol/services/go/operatorbackup"
	"github.com/CountableNewt/my-context-protocol/services/go/operatorjob"
	"io"
	"net/http"
	"strings"
	"time"
)

type Config struct {
	DB             operatorjob.Database
	PublicKey      *rsa.PublicKey
	Audience, Host string
	Now            func() time.Time
}
type Service struct{ config Config }

func New(config Config) (*Service, error) {
	if config.DB == nil || config.PublicKey == nil || config.Audience == "" || strings.ContainsAny(config.Audience, "/:?# ") || !operatorjob.ValidHost(config.Host) {
		return nil, errors.New("invalid operator service configuration")
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	return &Service{config: config}, nil
}
func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	fail := func(status int, code string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(map[string]string{"error": code})
	}
	if r.Method != "POST" || r.URL.Path != "/operator" || r.URL.RawQuery != "" {
		fail(404, "NotFound")
		return
	}
	if r.Host != s.config.Audience || r.Header.Get("Content-Type") != "application/json" {
		fail(400, "InvalidRequest")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
	defer r.Body.Close()
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var command Command
	if decoder.Decode(&command) != nil {
		fail(400, "InvalidRequest")
		return
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		fail(400, "InvalidRequest")
		return
	}
	now := s.config.Now()
	if command.Verify(s.config.PublicKey, s.config.Audience, s.config.Host, now) != nil {
		fail(403, "AuthorizationRejected")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 250*time.Second)
	defer cancel()
	success := false
	if command.Mode != "preflight" {
		release, err := (Ledger{DB: s.config.DB, Prefix: "operatorjob:" + operatorbackup.KeyID(s.config.PublicKey) + ":"}).Acquire(ctx, command.Nonce, command.ExpiresAt, now)
		if err != nil {
			fail(409, "OperatorBusyOrReplay")
			return
		}
		defer func() {
			if ctx.Err() == nil && success {
				release()
			}
		}()
	}
	result, err := operatorjob.Run(ctx, s.config.DB, command.Mode, s.config.Host, s.config.PublicKey, command.Review)
	if err != nil {
		fail(409, "OperatorFailedKeepWritersPaused")
		return
	}
	success = true
	if len(result.Ciphertext) > 0 {
		proof, _ := json.Marshal(result.Proof)
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("X-Myctx-Operator-Proof", base64.RawURLEncoding.EncodeToString(proof))
		w.WriteHeader(200)
		w.Write(result.Ciphertext)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result.Proof)
}
