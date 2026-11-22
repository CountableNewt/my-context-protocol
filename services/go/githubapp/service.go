package githubapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/CountableNewt/my-context-protocol/services/go/identity"
	"github.com/CountableNewt/my-context-protocol/services/go/reposync"
	"github.com/CountableNewt/my-context-protocol/services/go/security"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
)

const MigrationSQL = `CREATE TABLE IF NOT EXISTS github_operation_locks(project_id TEXT PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,operation_id TEXT NOT NULL,lease_until INTEGER NOT NULL);`

type Database interface {
	Execute(context.Context, turso.Statement) (*turso.Result, error)
	Batch(context.Context, []turso.Statement) ([]turso.Result, error)
}
type Config struct {
	DB                  Database
	Sessions            identity.Store
	Client              *Client
	Cipher              *security.TokenCipher
	Slug, WebhookOrigin string
	AppWebhookSecret    string
	PushWebhook         http.Handler
	FrontendOrigins     []string
}
type Service struct{ config Config }
type PublicError struct {
	Status  int
	Reason  string
	Payload any
}

func (e *PublicError) Error() string   { return e.Reason }
func (e *PublicError) HTTPStatus() int { return e.Status }
func (e *PublicError) HTTPPayload() any {
	if e.Payload != nil {
		return e.Payload
	}
	return map[string]string{"reason": e.Reason}
}
func (e *APIError) HTTPStatus() int {
	if e.Status == 401 || e.Status == 403 || e.Status == 404 || e.Status == 422 {
		return e.Status
	}
	return 502
}
func (e *APIError) HTTPPayload() any { return map[string]string{"reason": e.Error()} }
func New(config Config) (*Service, error) {
	if config.DB == nil || config.Client == nil || config.Cipher == nil {
		return nil, errors.New("GitHub service requires database, client and token cipher")
	}
	if config.Slug != "" {
		if _, err := repoAPI(config.Slug, "repo"); err != nil {
			return nil, errors.New("invalid GitHub App slug")
		}
	}
	for _, origin := range config.FrontendOrigins {
		u, err := url.Parse(origin)
		if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1"))) {
			return nil, errors.New("invalid frontend origin")
		}
	}
	return &Service{config}, nil
}
func sql(sql string, args ...turso.Value) turso.Statement {
	return turso.Statement{SQL: sql, Args: args, WantRows: true}
}
func col(row []turso.Value, i int) string {
	if i >= len(row) || row[i].Type == "null" {
		return ""
	}
	return fmt.Sprint(row[i].Value)
}
func nullable(value string) turso.Value {
	if value == "" {
		return turso.Null()
	}
	return turso.Text(value)
}
func (s *Service) ownedProject(ctx context.Context, account, project string) error {
	r, err := s.config.DB.Execute(ctx, sql("SELECT id FROM projects WHERE id=? AND account_id=?", turso.Text(project), turso.Text(account)))
	if err != nil {
		return err
	}
	if len(r.Rows) != 1 {
		return &PublicError{404, "Project not found", nil}
	}
	return nil
}
func (s *Service) lock(ctx context.Context, project string) (func(), error) {
	id, err := identity.UUID()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	r, err := s.config.DB.Execute(ctx, sql("INSERT INTO github_operation_locks(project_id,operation_id,lease_until) VALUES (?,?,?) ON CONFLICT(project_id) DO UPDATE SET operation_id=excluded.operation_id,lease_until=excluded.lease_until WHERE github_operation_locks.lease_until<=? RETURNING operation_id", turso.Text(project), turso.Text(id), turso.Integer(now.Add(120*time.Second).Unix()), turso.Integer(now.Unix())))
	if err != nil {
		return nil, err
	}
	if len(r.Rows) != 1 {
		return nil, &PublicError{409, "A repository operation is already running", nil}
	}
	return func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		s.config.DB.Execute(ctx, sql("DELETE FROM github_operation_locks WHERE project_id=? AND operation_id=?", turso.Text(project), turso.Text(id)))
	}, nil
}
func (s *Service) accountToken(ctx context.Context, account string) (string, string, error) {
	r, err := s.config.DB.Execute(ctx, sql("SELECT github_token_encrypted,github_app_installation_id FROM accounts WHERE id=?", turso.Text(account)))
	if err != nil {
		return "", "", err
	}
	if len(r.Rows) != 1 {
		return "", "", &PublicError{401, "Not authenticated", nil}
	}
	token, err := s.config.Cipher.Decrypt(col(r.Rows[0], 0))
	if err != nil || token == "" {
		return "", "", &PublicError{400, "No GitHub token. Re-authorize with repository access.", nil}
	}
	return token, col(r.Rows[0], 1), nil
}
func (s *Service) InstallRequired(project, owner, repo string) error {
	params := url.Values{"project_id": {project}, "owner": {owner}, "repo": {repo}, "return_to": {"/projects/" + url.PathEscape(project)}}
	return &PublicError{409, "github_app_install_required", map[string]string{"reason": "github_app_install_required", "install_url": "/api/auth/github/app/install?" + params.Encode()}}
}

func (s *Service) ConnectRepository(ctx context.Context, account, project string, input map[string]json.RawMessage) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	if err := s.ownedProject(ctx, account, project); err != nil {
		return nil, err
	}
	var owner, repo, branch string
	for key, target := range map[string]*string{"owner": &owner, "repo": &repo, "branch": &branch} {
		if raw, ok := input[key]; ok {
			if err := json.Unmarshal(raw, target); err != nil {
				return nil, &PublicError{400, "Invalid repository fields", nil}
			}
		}
	}
	if branch == "" {
		branch = "main"
	}
	if _, err := reposURL(owner, repo, branch); err != nil {
		return nil, &PublicError{400, "Invalid repository owner, name, or branch", nil}
	}
	unlock, err := s.lock(ctx, project)
	if err != nil {
		return nil, err
	}
	defer unlock()
	oauth, installation, err := s.accountToken(ctx, account)
	if err != nil {
		return nil, err
	}
	current, err := s.config.DB.Execute(ctx, sql("SELECT id,repo_owner,repo_name,webhook_id,github_installation_id,token_encrypted FROM repo_connections WHERE project_id=? ORDER BY id LIMIT 1", turso.Text(project)))
	if err != nil {
		return nil, err
	}
	var old []turso.Value
	if len(current.Rows) > 0 {
		old = current.Rows[0]
		if id := col(old, 4); id != "" {
			installation = id
		}
	}
	appConfigured := s.config.Client.Key != nil && s.config.Client.ClientID != "" && s.config.Slug != ""
	if appConfigured && installation == "" {
		return nil, s.InstallRequired(project, owner, repo)
	}
	token := oauth
	if installation != "" {
		token, err = s.InstallationToken(ctx, installation, oauth)
		if err != nil {
			return nil, err
		}
	}
	if _, err = s.config.Client.VerifyRepository(ctx, owner, repo, token, oauthFallback(installation, oauth)); err != nil {
		if errors.Is(err, ErrInstallationScope) {
			return nil, &PublicError{422, "Your GitHub App installation does not include this repository. Add it in GitHub App repository access settings.", nil}
		}
		return nil, err
	}
	hookID, hookSecret, err := s.config.Client.CreateWebhook(ctx, owner, repo, token, s.config.WebhookOrigin)
	if err != nil {
		return nil, err
	}
	committed := false
	defer func() {
		if !committed {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			s.config.Client.DeleteWebhook(cleanup, owner, repo, hookID, token)
		}
	}()
	id := col(old, 0)
	var r *turso.Result
	if id != "" {
		r, err = s.config.DB.Execute(ctx, sql("UPDATE repo_connections SET repo_owner=?,repo_name=?,default_branch=?,provider='github',auth_type='oauth',webhook_id=?,webhook_secret=?,github_installation_id=?,token_encrypted=NULL WHERE id=? AND project_id=? AND EXISTS(SELECT 1 FROM projects WHERE id=? AND account_id=?) RETURNING id", turso.Text(owner), turso.Text(repo), turso.Text(branch), turso.Text(hookID), turso.Text(hookSecret), nullableInstallation(installation), turso.Text(id), turso.Text(project), turso.Text(project), turso.Text(account)))
	} else {
		id, err = identity.UUID()
		if err != nil {
			return nil, err
		}
		r, err = s.config.DB.Execute(ctx, sql("INSERT INTO repo_connections(id,project_id,provider,repo_owner,repo_name,default_branch,auth_type,webhook_id,webhook_secret,github_installation_id,created_at) SELECT ?,?,'github',?,?,?,'oauth',?,?,?,? WHERE EXISTS(SELECT 1 FROM projects WHERE id=? AND account_id=?) AND NOT EXISTS(SELECT 1 FROM repo_connections WHERE project_id=?) RETURNING id", turso.Text(id), turso.Text(project), turso.Text(owner), turso.Text(repo), turso.Text(branch), turso.Text(hookID), turso.Text(hookSecret), nullableInstallation(installation), turso.Integer(time.Now().Unix()), turso.Text(project), turso.Text(account), turso.Text(project)))
	}
	if err != nil {
		return nil, err
	}
	if len(r.Rows) != 1 {
		return nil, &PublicError{409, "Repository changed during connection", nil}
	}
	committed = true
	// Delete the previous hook only after the replacement is committed. API
	// cleanup failure remains recoverable; it does not disable the working hook.
	if col(old, 3) != "" {
		oldToken := oauth
		if col(old, 4) != "" {
			oldToken, err = s.InstallationToken(ctx, col(old, 4), oauth)
		}
		if err == nil {
			s.config.Client.DeleteWebhook(ctx, col(old, 1), col(old, 2), col(old, 3), oldToken)
		}
	}
	return map[string]any{"project_id": project, "provider": "github", "repo_owner": owner, "repo_name": repo, "default_branch": branch, "auth_type": "oauth", "webhook_id": hookID, "github_installation_id": installationValue(installation)}, nil
}
func oauthFallback(installation, token string) string {
	if installation != "" {
		return token
	}
	return ""
}
func nullableInstallation(id string) turso.Value {
	if id == "" {
		return turso.Null()
	}
	n, _ := strconv.ParseInt(id, 10, 64)
	return turso.Integer(n)
}
func installationValue(id string) any {
	if id == "" {
		return nil
	}
	n, _ := strconv.ParseInt(id, 10, 64)
	return n
}
func reposURL(owner, repo, branch string) (string, error) {
	if _, err := reposync.ArchiveURL(owner, repo, branch); err != nil {
		return "", err
	}
	return repoAPI(owner, repo)
}

// InstallationToken also clears stale references when GitHub confirms an App
// installation was removed; credentials are never downgraded silently.
func (s *Service) InstallationToken(ctx context.Context, id, oauth string) (string, error) {
	token, err := s.config.Client.InstallationToken(ctx, id, oauth)
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.Status == 404 {
		s.config.DB.Batch(ctx, []turso.Statement{sql("UPDATE repo_connections SET github_installation_id=NULL,webhook_id=NULL,webhook_secret=NULL WHERE github_installation_id=?", nullableInstallation(id)), sql("UPDATE accounts SET github_app_installation_id=NULL WHERE github_app_installation_id=?", nullableInstallation(id))})
	}
	return token, err
}
