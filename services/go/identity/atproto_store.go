package identity

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/CountableNewt/my-context-protocol/services/go/security"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
	"github.com/bluesky-social/indigo/atproto/syntax"
)

// ATProtoMigrationSQL is applied explicitly after accounts.github_id becomes
// nullable. It never changes the already shipped OAuth persistence migration.
const ATProtoMigrationSQL = `
CREATE TABLE account_atproto_identities(did TEXT PRIMARY KEY,account_id UUID NOT NULL UNIQUE REFERENCES accounts(id) ON DELETE CASCADE,handle TEXT NOT NULL,pds_url TEXT NOT NULL,session_id_encrypted TEXT NOT NULL,updated_at INTEGER NOT NULL);
CREATE TABLE atproto_login_flows(state_hash TEXT PRIMARY KEY,browser_hash TEXT NOT NULL,return_to TEXT NOT NULL,link_account_id UUID REFERENCES accounts(id) ON DELETE CASCADE,expires_at INTEGER NOT NULL);
CREATE INDEX atproto_login_flows_expiry ON atproto_login_flows(expires_at);
`

var ErrIdentityConflict = errors.New("ATProto identity is already linked to another account")

type ATProtoIdentity struct{ DID, AccountID, Handle, PDSURL, SessionID string }
type ATProtoLoginFlow struct{ ReturnTo, LinkAccountID string }
type ATProtoAccounts interface {
	LinkGitHubAccount(context.Context, string, GitHubUser, string) (*Account, error)
	UpsertATProtoAccount(context.Context, ATProtoIdentity, string) (*Account, error)
	ATProtoIdentity(context.Context, string) (*ATProtoIdentity, error)
	SaveLoginFlow(context.Context, string, string, ATProtoLoginFlow, time.Time) error
	ConsumeLoginFlow(context.Context, string, string, time.Time) (ATProtoLoginFlow, error)
}

type ATProtoDatabase interface {
	SQLExecutor
	Batch(context.Context, []turso.Statement) ([]turso.Result, error)
}
type ATProtoSQLStore struct {
	DB     ATProtoDatabase
	Cipher *security.TokenCipher
}

func (s ATProtoSQLStore) LinkGitHubAccount(ctx context.Context, account string, user GitHubUser, encrypted string) (*Account, error) {
	if user.ID <= 0 || user.Login == "" || encrypted == "" {
		return nil, errors.New("verified GitHub identity is required")
	}
	r, err := s.DB.Execute(ctx, atStatement(`UPDATE accounts SET github_id=?,login=?,avatar_url=?,email=COALESCE(?,email),github_token_encrypted=? WHERE id=? AND (github_id IS NULL OR github_id=?) AND NOT EXISTS(SELECT 1 FROM accounts other WHERE other.github_id=? AND other.id<>?) RETURNING `+accountColumns, turso.Integer(user.ID), turso.Text(user.Login), nullable(user.AvatarURL), nullable(user.Email), turso.Text(encrypted), turso.Text(account), turso.Integer(user.ID), turso.Integer(user.ID), turso.Text(account)))
	if err != nil {
		return nil, err
	}
	if len(r.Rows) != 1 {
		return nil, ErrIdentityConflict
	}
	return decodeAccount(r)
}

func atStatement(sql string, args ...turso.Value) turso.Statement {
	return turso.Statement{SQL: sql, Args: args, WantRows: true}
}
func (s ATProtoSQLStore) UpsertATProtoAccount(ctx context.Context, ident ATProtoIdentity, linkAccount string) (*Account, error) {
	if s.DB == nil || s.Cipher == nil {
		return nil, errors.New("ATProto identity store is not configured")
	}
	if _, err := syntax.ParseDID(ident.DID); err != nil {
		return nil, err
	}
	if ident.SessionID == "" || ident.PDSURL == "" {
		return nil, errors.New("authenticated ATProto session is required")
	}
	if ident.Handle == "" {
		ident.Handle = ident.DID
	}
	encrypted, err := s.Cipher.Encrypt(ident.SessionID)
	if err != nil {
		return nil, err
	}
	id, err := UUID()
	if err != nil {
		return nil, err
	}
	if linkAccount != "" {
		id = strings.ToUpper(linkAccount)
	}
	statements := []turso.Statement{}
	if linkAccount == "" {
		statements = append(statements, atStatement(`INSERT INTO accounts(id,github_id,login,created_at,is_admin,paywall_bypass) SELECT ?,NULL,?,?,0,0 WHERE NOT EXISTS(SELECT 1 FROM account_atproto_identities WHERE did=?)`, turso.Text(id), turso.Text(ident.Handle), turso.Integer(time.Now().Unix()), turso.Text(ident.DID)))
	}
	accountExpr := "?"
	if linkAccount == "" {
		accountExpr = "COALESCE((SELECT account_id FROM account_atproto_identities WHERE did=?),?)"
	}
	args := []turso.Value{turso.Text(ident.DID)}
	if linkAccount == "" {
		args = append(args, turso.Text(ident.DID))
	}
	args = append(args, turso.Text(id), turso.Text(ident.Handle), turso.Text(ident.PDSURL), turso.Text(encrypted), turso.Integer(time.Now().Unix()))
	statements = append(statements, atStatement(`INSERT INTO account_atproto_identities(did,account_id,handle,pds_url,session_id_encrypted,updated_at) VALUES(?,`+accountExpr+`,?,?,?,?) ON CONFLICT(did) DO UPDATE SET handle=excluded.handle,pds_url=excluded.pds_url,session_id_encrypted=excluded.session_id_encrypted,updated_at=excluded.updated_at WHERE account_atproto_identities.account_id=excluded.account_id RETURNING account_id`, args...))
	identityResult := len(statements) - 1
	statements = append(statements, atStatement("UPDATE accounts SET login=? WHERE github_id IS NULL AND id=(SELECT account_id FROM account_atproto_identities WHERE did=?)", turso.Text(ident.Handle), turso.Text(ident.DID)))
	results, err := s.DB.Batch(ctx, statements)
	if err != nil {
		return nil, err
	}
	if len(results) != len(statements) {
		return nil, errors.New("invalid identity transaction result")
	}
	last := results[identityResult]
	if len(last.Rows) != 1 {
		return nil, ErrIdentityConflict
	}
	accountID, err := scalar(last.Rows[0], 0)
	if err != nil {
		return nil, err
	}
	return (SQLStore{DB: s.DB}).Account(ctx, accountID)
}
func (s ATProtoSQLStore) ATProtoIdentity(ctx context.Context, account string) (*ATProtoIdentity, error) {
	r, err := s.DB.Execute(ctx, atStatement("SELECT did,account_id,handle,pds_url,session_id_encrypted FROM account_atproto_identities WHERE account_id=?", turso.Text(strings.ToUpper(account))))
	if err != nil {
		return nil, err
	}
	if len(r.Rows) == 0 {
		return nil, nil
	}
	row := r.Rows[0]
	if len(row) != 5 {
		return nil, errors.New("invalid linked identity row")
	}
	v := make([]string, 5)
	for i := range v {
		v[i], err = scalar(row, i)
		if err != nil {
			return nil, err
		}
	}
	id, err := s.Cipher.Decrypt(v[4])
	if err != nil {
		return nil, err
	}
	return &ATProtoIdentity{DID: v[0], AccountID: v[1], Handle: v[2], PDSURL: v[3], SessionID: id}, nil
}
func (s ATProtoSQLStore) SaveLoginFlow(ctx context.Context, state, browser string, flow ATProtoLoginFlow, expires time.Time) error {
	if state == "" || browser == "" || flow.ReturnTo == "" {
		return errors.New("incomplete ATProto login flow")
	}
	link := turso.Null()
	if flow.LinkAccountID != "" {
		link = turso.Text(flow.LinkAccountID)
	}
	_, err := s.DB.Execute(ctx, atStatement("INSERT INTO atproto_login_flows(state_hash,browser_hash,return_to,link_account_id,expires_at) VALUES(?,?,?,?,?)", turso.Text(security.HashToken(state)), turso.Text(security.HashToken(browser)), turso.Text(flow.ReturnTo), link, turso.Integer(expires.Unix())))
	return err
}
func (s ATProtoSQLStore) ConsumeLoginFlow(ctx context.Context, state, browser string, now time.Time) (ATProtoLoginFlow, error) {
	if state == "" || browser == "" {
		return ATProtoLoginFlow{}, ErrUnauthenticated
	}
	r, err := s.DB.Execute(ctx, atStatement("DELETE FROM atproto_login_flows WHERE state_hash=? AND browser_hash=? AND expires_at>? RETURNING return_to,link_account_id", turso.Text(security.HashToken(state)), turso.Text(security.HashToken(browser)), turso.Integer(now.Unix())))
	if err != nil {
		return ATProtoLoginFlow{}, err
	}
	if len(r.Rows) != 1 {
		return ATProtoLoginFlow{}, ErrUnauthenticated
	}
	target, err := scalar(r.Rows[0], 0)
	if err != nil {
		return ATProtoLoginFlow{}, err
	}
	account, err := scalar(r.Rows[0], 1)
	return ATProtoLoginFlow{target, account}, err
}
