// Package ataccount binds hosted operations to an explicitly linked ATProto
// identity and serializes refresh-token use across serverless processes.
package ataccount

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/CountableNewt/my-context-protocol/services/go/atidentity"
	"github.com/CountableNewt/my-context-protocol/services/go/atproto"
	"github.com/CountableNewt/my-context-protocol/services/go/identity"
	"github.com/CountableNewt/my-context-protocol/services/go/security"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
	"github.com/bluesky-social/indigo/atproto/auth/oauth"
	atidentitysdk "github.com/bluesky-social/indigo/atproto/identity"
	"github.com/bluesky-social/indigo/atproto/syntax"
)

const MigrationSQL = `CREATE TABLE atproto_session_leases(did TEXT NOT NULL,session_hash TEXT NOT NULL,lease_owner TEXT NOT NULL,lease_until INTEGER NOT NULL,PRIMARY KEY(did,session_hash));`

var ErrReconnect = errors.New("ATProto authorization must be renewed")
var ErrBusy = errors.New("ATProto session is in use; retry shortly")

type Database interface {
	Execute(context.Context, turso.Statement) (*turso.Result, error)
}
type LinkedIdentities interface {
	ATProtoIdentity(context.Context, string) (*identity.ATProtoIdentity, error)
}
type Sessions interface {
	ResumeSession(context.Context, syntax.DID, string) (*oauth.ClientSession, error)
}

type Manager struct {
	DB         Database
	Identities LinkedIdentities
	Sessions   Sessions
	Store      *atidentity.Store
	Directory  atidentitysdk.Directory
}

type sessionScopeKey struct{}
type sessionScope struct {
	manager *Manager
	account string
	session *oauth.ClientSession
}

// WithSession holds a durable lease for the entire operation, including all
// nonce/refresh persistence. Never retain the session after the callback returns.
func (m *Manager) WithSession(ctx context.Context, account string, apply func(context.Context, *oauth.ClientSession) error) error {
	if m == nil || m.DB == nil || m.Identities == nil || m.Sessions == nil || m.Store == nil || m.Directory == nil || apply == nil {
		return errors.New("linked ATProto session manager is not configured")
	}
	if held, ok := ctx.Value(sessionScopeKey{}).(sessionScope); ok && held.manager == m && held.account == account {
		return apply(ctx, held.session)
	}
	linked, err := m.Identities.ATProtoIdentity(ctx, account)
	if err != nil {
		return err
	}
	if linked == nil || linked.AccountID != account {
		return ErrReconnect
	}
	did, err := syntax.ParseDID(linked.DID)
	if err != nil {
		return ErrReconnect
	}
	owner, err := security.NewOpaqueToken()
	if err != nil {
		return err
	}
	sessionHash := security.HashToken(linked.SessionID)
	lease, err := m.DB.Execute(ctx, turso.Statement{SQL: `INSERT INTO atproto_session_leases(did,session_hash,lease_owner,lease_until) VALUES(?,?,?,?) ON CONFLICT(did,session_hash) DO UPDATE SET lease_owner=excluded.lease_owner,lease_until=excluded.lease_until WHERE atproto_session_leases.lease_until<=? RETURNING lease_owner`, Args: []turso.Value{turso.Text(linked.DID), turso.Text(sessionHash), turso.Text(owner), turso.Integer(time.Now().Add(90 * time.Second).Unix()), turso.Integer(time.Now().Unix())}, WantRows: true})
	if err != nil {
		return err
	}
	if lease == nil || len(lease.Rows) != 1 {
		return ErrBusy
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, _ = m.DB.Execute(cleanup, turso.Statement{SQL: `DELETE FROM atproto_session_leases WHERE did=? AND session_hash=? AND lease_owner=?`, Args: []turso.Value{turso.Text(linked.DID), turso.Text(sessionHash), turso.Text(owner)}})
	}()
	ctx, cancel := context.WithTimeout(ctx, 70*time.Second)
	defer cancel()
	session, err := m.Sessions.ResumeSession(ctx, did, linked.SessionID)
	if err != nil || session == nil || session.Data == nil {
		return ErrReconnect
	}
	current, err := m.Directory.LookupDID(ctx, did)
	if err != nil {
		return err
	}
	if current == nil || current.DID != did || current.PDSEndpoint() != session.Data.HostURL || session.Data.AccountDID != did {
		return ErrReconnect
	}
	// Indigo's default persistence callback logs errors. Replace it so a failed
	// refresh-token write prevents reporting a completed hosted operation.
	var mu sync.Mutex
	var persistErr error
	session.PersistSessionCallback = func(ctx context.Context, data *oauth.ClientSessionData) {
		err := m.Store.SaveSession(ctx, *data)
		if err != nil {
			mu.Lock()
			persistErr = errors.Join(persistErr, err)
			mu.Unlock()
		}
	}
	ctx = context.WithValue(ctx, sessionScopeKey{}, sessionScope{m, account, session})
	err = apply(ctx, session)
	mu.Lock()
	defer mu.Unlock()
	return errors.Join(err, persistErr)
}

// Token implements Tangled's service-token interface. The account argument must
// be supplied by the owner-checked connection resolver, never a request body.
func (m *Manager) ServiceToken(ctx context.Context, account, audience, method string) (string, error) {
	if _, err := syntax.ParseDID(stringsBeforeFragment(audience)); err != nil {
		return "", err
	}
	if _, err := syntax.ParseNSID(method); err != nil {
		return "", err
	}
	var token string
	err := m.WithSession(ctx, account, func(ctx context.Context, s *oauth.ClientSession) error {
		var response struct {
			Token string `json:"token"`
		}
		err := s.APIClient().Get(ctx, syntax.NSID("com.atproto.server.getServiceAuth"), map[string]any{"aud": audience, "lxm": method, "exp": time.Now().Add(60 * time.Second).Unix()}, &response)
		if err != nil {
			return err
		}
		if response.Token == "" {
			return errors.New("PDS did not issue service authorization")
		}
		token = response.Token
		return nil
	})
	return token, err
}

func stringsBeforeFragment(v string) string {
	for i, c := range v {
		if c == '#' {
			return v[:i]
		}
	}
	return v
}

func (m *Manager) WithRecords(ctx context.Context, account string, apply func(context.Context, atproto.Records) error) error {
	return m.WithSession(ctx, account, func(ctx context.Context, s *oauth.ClientSession) error {
		records, err := atidentity.SessionRecords(s)
		if err != nil {
			return err
		}
		return apply(ctx, records)
	})
}

// Token mints a target-method credential for an already-linked actor. Connection
// authorization must run before this internal provider adapter is called.
func (m *Manager) Token(ctx context.Context, actor, audience, method string) (string, error) {
	r, err := m.DB.Execute(ctx, turso.Statement{SQL: `SELECT account_id FROM account_atproto_identities WHERE did=?`, Args: []turso.Value{turso.Text(actor)}, WantRows: true})
	if err != nil {
		return "", err
	}
	if r == nil || len(r.Rows) != 1 || len(r.Rows[0]) != 1 {
		return "", ErrReconnect
	}
	account, ok := r.Rows[0][0].Value.(string)
	if !ok {
		return "", ErrReconnect
	}
	// Parallel tree reads share one rotating refresh token. Wait briefly for the
	// durable session lease rather than issuing concurrent refresh chains.
	deadline := time.NewTimer(8 * time.Second)
	defer deadline.Stop()
	for {
		token, err := m.ServiceToken(ctx, account, audience, method)
		if !errors.Is(err, ErrBusy) {
			return token, err
		}
		timer := time.NewTimer(75 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", ctx.Err()
		case <-deadline.C:
			timer.Stop()
			return "", ErrBusy
		case <-timer.C:
		}
	}
}
