// Package xrpcauth authenticates method-bound ATProto service authorization for
// account APIs. PDS OAuth tokens are never accepted directly by this service.
package xrpcauth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/CountableNewt/my-context-protocol/services/go/security"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
	"github.com/CountableNewt/my-context-protocol/services/go/xrpc"
	"github.com/bluesky-social/indigo/atproto/auth"
	"github.com/bluesky-social/indigo/atproto/identity"
	"github.com/bluesky-social/indigo/atproto/syntax"
	"github.com/golang-jwt/jwt/v5"
)

const MigrationSQL = `CREATE TABLE atproto_xrpc_replays(token_hash TEXT PRIMARY KEY,expires_at INTEGER NOT NULL);CREATE INDEX atproto_xrpc_replays_expiry ON atproto_xrpc_replays(expires_at);`

type Database interface {
	Execute(context.Context, turso.Statement) (*turso.Result, error)
}
type Verifier struct {
	Audience  string
	Directory identity.Directory
	DB        Database
}

func (v *Verifier) Verify(ctx context.Context, r *http.Request) (xrpc.Principal, error) {
	var empty xrpc.Principal
	if v.DB == nil || v.Directory == nil || v.Audience == "" {
		return empty, errors.New("XRPC service authorization is not configured")
	}
	if _, err := syntax.ParseDID(strings.Split(v.Audience, "#")[0]); err != nil {
		return empty, err
	}
	parts := strings.Fields(r.Header.Get("Authorization"))
	if len(parts) != 2 || parts[0] != "Bearer" || len(parts[1]) > 16<<10 {
		return empty, errors.New("method-bound service authorization required")
	}
	nsid, err := syntax.ParseNSID(strings.TrimPrefix(r.URL.Path, "/xrpc/"))
	if err != nil || r.URL.Path != "/xrpc/"+nsid.String() {
		return empty, errors.New("invalid XRPC method")
	}
	validator := auth.ServiceAuthValidator{Audience: v.Audience, Dir: v.Directory}
	did, err := validator.Validate(ctx, parts[1], &nsid)
	if err != nil {
		return empty, errors.New("invalid service authorization")
	}
	// The signature and method have already been verified. Inspect the same
	// verified bytes for the stricter short-lived credential policy.
	claims := &jwt.RegisteredClaims{}
	if _, _, err = jwt.NewParser().ParseUnverified(parts[1], claims); err != nil || claims.IssuedAt == nil || claims.ExpiresAt == nil || claims.ID == "" || claims.ExpiresAt.Sub(claims.IssuedAt.Time) > time.Minute || claims.ExpiresAt.Before(claims.IssuedAt.Time) {
		return empty, errors.New("service authorization must expire within one minute")
	}
	result, err := v.DB.Execute(ctx, turso.Statement{SQL: `SELECT a.id,a.is_admin FROM account_atproto_identities i JOIN accounts a ON a.id=i.account_id WHERE i.did=?`, Args: []turso.Value{turso.Text(did.String())}, WantRows: true})
	if err != nil {
		return empty, err
	}
	if result == nil || len(result.Rows) != 1 || len(result.Rows[0]) != 2 {
		return empty, errors.New("ATProto identity is not linked to an account")
	}
	account, ok := result.Rows[0][0].Value.(string)
	if !ok || account == "" {
		return empty, errors.New("invalid linked account")
	}
	admin := result.Rows[0][1].Type == "integer" && result.Rows[0][1].Value == "1"
	if r.Method != "GET" && r.Method != "HEAD" {
		used, err := v.DB.Execute(ctx, turso.Statement{SQL: `INSERT INTO atproto_xrpc_replays(token_hash,expires_at) VALUES(?,?) ON CONFLICT(token_hash) DO NOTHING RETURNING token_hash`, Args: []turso.Value{turso.Text(security.HashToken(parts[1])), turso.Integer(claims.ExpiresAt.Unix() + 5)}, WantRows: true})
		if err != nil {
			return empty, err
		}
		if used == nil || len(used.Rows) != 1 {
			return empty, errors.New("service authorization was already used")
		}
	}
	return xrpc.Principal{AccountID: account, DID: did.String(), IsAdmin: admin, Scopes: map[string]bool{"rpc:" + nsid.String(): true}}, nil
}

func (v *Verifier) Prune(ctx context.Context, limit int) error {
	if v.DB == nil || limit < 1 || limit > 1000 {
		return errors.New("bounded replay cleanup required")
	}
	_, err := v.DB.Execute(ctx, turso.Statement{SQL: `DELETE FROM atproto_xrpc_replays WHERE token_hash IN (SELECT token_hash FROM atproto_xrpc_replays WHERE expires_at<=? ORDER BY expires_at LIMIT ?)`, Args: []turso.Value{turso.Integer(time.Now().Unix()), turso.Integer(int64(limit))}})
	return err
}
