// Package atidentity persists ATProto OAuth secrets privately. It does not store
// credentials or authentication state in public ATProto records.
package atidentity

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/CountableNewt/my-context-protocol/services/go/security"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
	"github.com/bluesky-social/indigo/atproto/auth/oauth"
	"github.com/bluesky-social/indigo/atproto/syntax"
)

const MigrationSQL = `
CREATE TABLE atproto_oauth_requests(state_hash TEXT PRIMARY KEY,payload_encrypted TEXT NOT NULL,expires_at INTEGER NOT NULL);
CREATE INDEX atproto_oauth_requests_expiry ON atproto_oauth_requests(expires_at);
CREATE TABLE atproto_oauth_sessions(did TEXT NOT NULL,session_hash TEXT NOT NULL,payload_encrypted TEXT NOT NULL,updated_at INTEGER NOT NULL,revoked INTEGER NOT NULL DEFAULT 0 CHECK(revoked IN (0,1)),PRIMARY KEY(did,session_hash));
`

var ErrNotFound = errors.New("ATProto authorization state not found or expired")

type Database interface {
	Execute(context.Context, turso.Statement) (*turso.Result, error)
}

// Store is durable across serverless requests. Auth requests are consumed in a
// single SQL statement before token exchange, preventing concurrent replay.
type Store struct {
	DB     Database
	Cipher *security.TokenCipher
	Now    func() time.Time
}

var _ oauth.ClientAuthStore = (*Store)(nil)

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Store) execute(ctx context.Context, sql string, args ...turso.Value) (*turso.Result, error) {
	if s.DB == nil || s.Cipher == nil {
		return nil, errors.New("ATProto private store is not configured")
	}
	return s.DB.Execute(ctx, turso.Statement{SQL: sql, Args: args, WantRows: true})
}

func (s *Store) seal(v any) (string, error) {
	if s.Cipher == nil {
		return "", errors.New("ATProto credential encryption is required")
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return s.Cipher.Encrypt(string(b))
}

func (s *Store) open(result *turso.Result, out any) error {
	if result == nil || len(result.Rows) == 0 {
		return ErrNotFound
	}
	if len(result.Rows) != 1 || len(result.Rows[0]) != 1 {
		return errors.New("invalid ATProto private store row")
	}
	encrypted, ok := result.Rows[0][0].Value.(string)
	if !ok {
		return errors.New("invalid ATProto private store value")
	}
	plain, err := s.Cipher.Decrypt(encrypted)
	if err != nil {
		return errors.New("could not decrypt ATProto authorization state")
	}
	if json.Unmarshal([]byte(plain), out) != nil {
		return errors.New("invalid ATProto authorization state")
	}
	return nil
}

func (s *Store) SaveAuthRequestInfo(ctx context.Context, info oauth.AuthRequestData) error {
	if info.State == "" || info.PKCEVerifier == "" || info.DPoPPrivateKeyMultibase == "" {
		return errors.New("incomplete ATProto authorization request")
	}
	value, err := s.seal(info)
	if err != nil {
		return err
	}
	_, err = s.execute(ctx, "INSERT INTO atproto_oauth_requests(state_hash,payload_encrypted,expires_at) VALUES(?,?,?)", turso.Text(security.HashToken(info.State)), turso.Text(value), turso.Integer(s.now().Add(10*time.Minute).Unix()))
	return err
}

func (s *Store) GetAuthRequestInfo(ctx context.Context, state string) (*oauth.AuthRequestData, error) {
	if state == "" {
		return nil, ErrNotFound
	}
	r, err := s.execute(ctx, "DELETE FROM atproto_oauth_requests WHERE state_hash=? AND expires_at>? RETURNING payload_encrypted", turso.Text(security.HashToken(state)), turso.Integer(s.now().Unix()))
	if err != nil {
		return nil, err
	}
	var out oauth.AuthRequestData
	if err = s.open(r, &out); err != nil {
		return nil, err
	}
	if out.State != state {
		return nil, errors.New("ATProto authorization state mismatch")
	}
	return &out, nil
}

func (s *Store) DeleteAuthRequestInfo(ctx context.Context, state string) error {
	_, err := s.execute(ctx, "DELETE FROM atproto_oauth_requests WHERE state_hash=?", turso.Text(security.HashToken(state)))
	return err
}

func (s *Store) SaveSession(ctx context.Context, data oauth.ClientSessionData) error {
	if _, err := syntax.ParseDID(data.AccountDID.String()); err != nil {
		return err
	}
	if data.SessionID == "" || data.AccessToken == "" || data.DPoPPrivateKeyMultibase == "" {
		return errors.New("incomplete ATProto OAuth session")
	}
	value, err := s.seal(data)
	if err != nil {
		return err
	}
	r, err := s.execute(ctx, "INSERT INTO atproto_oauth_sessions(did,session_hash,payload_encrypted,updated_at) VALUES(?,?,?,?) ON CONFLICT(did,session_hash) DO UPDATE SET payload_encrypted=excluded.payload_encrypted,updated_at=excluded.updated_at WHERE atproto_oauth_sessions.revoked=0 RETURNING session_hash", turso.Text(data.AccountDID.String()), turso.Text(security.HashToken(data.SessionID)), turso.Text(value), turso.Integer(s.now().Unix()))
	if err != nil {
		return err
	}
	if r == nil || len(r.Rows) != 1 {
		return errors.New("ATProto session has been revoked")
	}
	return nil
}

func (s *Store) GetSession(ctx context.Context, did syntax.DID, sessionID string) (*oauth.ClientSessionData, error) {
	r, err := s.execute(ctx, "SELECT payload_encrypted FROM atproto_oauth_sessions WHERE did=? AND session_hash=? AND revoked=0", turso.Text(did.String()), turso.Text(security.HashToken(sessionID)))
	if err != nil {
		return nil, err
	}
	var out oauth.ClientSessionData
	if err = s.open(r, &out); err != nil {
		return nil, err
	}
	if out.AccountDID != did || out.SessionID != sessionID {
		return nil, errors.New("ATProto session identity mismatch")
	}
	return &out, nil
}

func (s *Store) DeleteSession(ctx context.Context, did syntax.DID, sessionID string) error {
	// Keep a secret-free tombstone so an in-flight refresh cannot recreate a
	// logged-out session. New login flows always generate another session ID.
	_, err := s.execute(ctx, "INSERT INTO atproto_oauth_sessions(did,session_hash,payload_encrypted,updated_at,revoked) VALUES(?,?,'',?,1) ON CONFLICT(did,session_hash) DO UPDATE SET payload_encrypted='',updated_at=excluded.updated_at,revoked=1", turso.Text(did.String()), turso.Text(security.HashToken(sessionID)), turso.Integer(s.now().Unix()))
	return err
}

// PruneRequests is bounded maintenance, called explicitly rather than by a loop.
func (s *Store) PruneRequests(ctx context.Context, limit int) error {
	if limit < 1 || limit > 1000 {
		return errors.New("authorization prune limit must be between 1 and 1000")
	}
	_, err := s.execute(ctx, "DELETE FROM atproto_oauth_requests WHERE state_hash IN (SELECT state_hash FROM atproto_oauth_requests WHERE expires_at<=? ORDER BY expires_at LIMIT ?)", turso.Integer(s.now().Unix()), turso.Integer(int64(limit)))
	return err
}
