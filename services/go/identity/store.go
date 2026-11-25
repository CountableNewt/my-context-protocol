package identity

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/CountableNewt/my-context-protocol/services/go/session"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
)

var ErrUnauthenticated = errors.New("not authenticated")

type Account struct {
	ID             string
	GitHubID       int64
	Login          string
	AvatarURL      *string
	Email          *string
	EncryptedToken *string
	IsAdmin        bool
}
type GitHubUser struct {
	ID        int64   `json:"id"`
	Login     string  `json:"login"`
	AvatarURL *string `json:"avatar_url"`
	Email     *string `json:"email"`
}

// Store supports the current SQLite schema. Implementations must consume handoff
// tokens atomically and must never recreate a missing row during UpdateSession.
type Store interface {
	Account(context.Context, string) (*Account, error)
	UpsertAccount(context.Context, GitHubUser, string, bool) (*Account, error)
	GrantAdmin(context.Context, string) error
	ReadSession(context.Context, string) (session.Payload, error)
	CreateSession(context.Context, string, session.Payload) error
	UpdateSession(context.Context, string, session.Payload) error
	DeleteSession(context.Context, string) error
	IssueHandoff(context.Context, string, string, time.Time) error
	ConsumeHandoff(context.Context, string, time.Time) (string, error)
	SuggestedGitHubApp(context.Context, string) (bool, error)
}
type SQLExecutor interface {
	Execute(context.Context, turso.Statement) (*turso.Result, error)
}
type SQLStore struct{ DB SQLExecutor }

func UUID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return strings.ToUpper(fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])), nil
}
func nullable(v *string) turso.Value {
	if v == nil {
		return turso.Null()
	}
	return turso.Text(*v)
}
func scalar(row []turso.Value, i int) (string, error) {
	if i >= len(row) {
		return "", errors.New("incomplete database row")
	}
	if row[i].Type == "null" {
		return "", nil
	}
	value, ok := row[i].Value.(string)
	if !ok {
		return "", errors.New("invalid database scalar")
	}
	return value, nil
}
func ptrScalar(row []turso.Value, i int) (*string, error) {
	value, err := scalar(row, i)
	if err != nil {
		return nil, err
	}
	if row[i].Type == "null" {
		return nil, nil
	}
	return &value, nil
}
func decodeAccount(result *turso.Result) (*Account, error) {
	if len(result.Rows) == 0 {
		return nil, nil
	}
	row := result.Rows[0]
	if len(row) != 7 {
		return nil, errors.New("invalid account row")
	}
	id, err := scalar(row, 0)
	if err != nil {
		return nil, err
	}
	github, err := scalar(row, 1)
	if err != nil {
		return nil, err
	}
	var gid int64
	if github != "" {
		gid, err = strconv.ParseInt(github, 10, 64)
		if err != nil {
			return nil, err
		}
	}
	login, err := scalar(row, 2)
	if err != nil {
		return nil, err
	}
	avatar, err := ptrScalar(row, 3)
	if err != nil {
		return nil, err
	}
	email, err := ptrScalar(row, 4)
	if err != nil {
		return nil, err
	}
	token, err := ptrScalar(row, 5)
	if err != nil {
		return nil, err
	}
	admin, err := scalar(row, 6)
	if err != nil {
		return nil, err
	}
	return &Account{ID: id, GitHubID: gid, Login: login, AvatarURL: avatar, Email: email, EncryptedToken: token, IsAdmin: admin == "1"}, nil
}

const accountColumns = "id, github_id, login, avatar_url, email, github_token_encrypted, is_admin"

func (s SQLStore) Account(ctx context.Context, id string) (*Account, error) {
	r, e := s.DB.Execute(ctx, turso.Statement{SQL: "SELECT " + accountColumns + " FROM accounts WHERE id = ?", Args: []turso.Value{turso.Text(strings.ToUpper(id))}, WantRows: true})
	if e != nil {
		return nil, e
	}
	return decodeAccount(r)
}
func (s SQLStore) UpsertAccount(ctx context.Context, user GitHubUser, encrypted string, admin bool) (*Account, error) {
	id, err := UUID()
	if err != nil {
		return nil, err
	}
	login := user.Login
	if login == "" {
		login = fmt.Sprintf("user%d", user.ID)
	}
	flag := int64(0)
	granted := turso.Null()
	if admin {
		flag = 1
		granted = turso.Integer(time.Now().Unix())
	}
	r, err := s.DB.Execute(ctx, turso.Statement{SQL: `INSERT INTO accounts (id,github_id,login,avatar_url,email,github_token_encrypted,created_at,is_admin,paywall_bypass,admin_granted_at) VALUES (?,?,?,?,?,?,?,?,0,?) ON CONFLICT(github_id) DO UPDATE SET login=excluded.login,avatar_url=excluded.avatar_url,email=COALESCE(excluded.email,accounts.email),github_token_encrypted=excluded.github_token_encrypted,is_admin=MAX(accounts.is_admin,excluded.is_admin),admin_granted_at=COALESCE(accounts.admin_granted_at,excluded.admin_granted_at) RETURNING ` + accountColumns, Args: []turso.Value{turso.Text(id), turso.Integer(user.ID), turso.Text(login), nullable(user.AvatarURL), nullable(user.Email), turso.Text(encrypted), turso.Integer(time.Now().Unix()), turso.Integer(flag), granted}, WantRows: true})
	if err != nil {
		return nil, err
	}
	return decodeAccount(r)
}
func (s SQLStore) GrantAdmin(ctx context.Context, id string) error {
	_, err := s.DB.Execute(ctx, turso.Statement{SQL: "UPDATE accounts SET is_admin=1,admin_granted_at=COALESCE(admin_granted_at,?) WHERE id=?", Args: []turso.Value{turso.Integer(time.Now().Unix()), turso.Text(id)}})
	return err
}
func (s SQLStore) ReadSession(ctx context.Context, key string) (session.Payload, error) {
	r, err := s.DB.Execute(ctx, turso.Statement{SQL: "SELECT payload FROM app_sessions WHERE session_key=?", Args: []turso.Value{turso.Text(key)}, WantRows: true})
	if err != nil {
		return nil, err
	}
	if len(r.Rows) == 0 {
		return nil, ErrUnauthenticated
	}
	payload, err := scalar(r.Rows[0], 0)
	if err != nil {
		return nil, err
	}
	return session.Decode(payload)
}
func (s SQLStore) CreateSession(ctx context.Context, key string, payload session.Payload) error {
	id, err := UUID()
	if err != nil {
		return err
	}
	encoded, err := session.Encode(payload)
	if err != nil {
		return err
	}
	_, err = s.DB.Execute(ctx, turso.Statement{SQL: "INSERT INTO app_sessions(id,session_key,payload,updated_at) VALUES (?,?,?,?)", Args: []turso.Value{turso.Text(id), turso.Text(key), turso.Text(encoded), turso.Integer(time.Now().Unix())}})
	return err
}
func (s SQLStore) UpdateSession(ctx context.Context, key string, payload session.Payload) error {
	encoded, err := session.Encode(payload)
	if err != nil {
		return err
	}
	r, err := s.DB.Execute(ctx, turso.Statement{SQL: "UPDATE app_sessions SET payload=?,updated_at=? WHERE session_key=?", Args: []turso.Value{turso.Text(encoded), turso.Integer(time.Now().Unix()), turso.Text(key)}})
	if err != nil {
		return err
	}
	if r.AffectedRowCount != 1 {
		return ErrUnauthenticated
	}
	return nil
}
func (s SQLStore) DeleteSession(ctx context.Context, key string) error {
	_, err := s.DB.Execute(ctx, turso.Statement{SQL: "DELETE FROM app_sessions WHERE session_key=?", Args: []turso.Value{turso.Text(key)}})
	return err
}
func (s SQLStore) IssueHandoff(ctx context.Context, token, account string, expires time.Time) error {
	id, err := UUID()
	if err != nil {
		return err
	}
	_, err = s.DB.Execute(ctx, turso.Statement{SQL: "INSERT INTO oauth_handoff_tokens(id,token,account_id,expires_at,created_at) VALUES (?,?,?,?,?)", Args: []turso.Value{turso.Text(id), turso.Text(token), turso.Text(account), turso.Integer(expires.Unix()), turso.Integer(time.Now().Unix())}})
	return err
}
func (s SQLStore) ConsumeHandoff(ctx context.Context, token string, now time.Time) (string, error) {
	r, err := s.DB.Execute(ctx, turso.Statement{SQL: "DELETE FROM oauth_handoff_tokens WHERE token=? AND expires_at>? RETURNING account_id", Args: []turso.Value{turso.Text(token), turso.Integer(now.Unix())}, WantRows: true})
	if err != nil {
		return "", err
	}
	if len(r.Rows) == 0 {
		return "", ErrUnauthenticated
	}
	return scalar(r.Rows[0], 0)
}
func (s SQLStore) SuggestedGitHubApp(ctx context.Context, account string) (bool, error) {
	r, err := s.DB.Execute(ctx, turso.Statement{SQL: "SELECT 1 FROM repo_connections r JOIN projects p ON p.id=r.project_id WHERE p.account_id=? AND r.provider='github' AND r.github_installation_id IS NULL LIMIT 1", Args: []turso.Value{turso.Text(account)}, WantRows: true})
	if err != nil {
		return false, err
	}
	return len(r.Rows) > 0, nil
}
