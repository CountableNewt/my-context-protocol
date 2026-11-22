package githubapp

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/CountableNewt/my-context-protocol/services/go/identity"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
)

var installStatePattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// IsInstallationCallback selects the App's database-intent flow on a shared
// GitHub callback URL. UUID shape is routing only, never authorization: the App
// handler still requires the matching session, owner and live one-use intent.
// General browser/CLI OAuth uses signed dotted states and stays on identity.
func IsInstallationCallback(r *http.Request) bool {
	if r.Method != "GET" || r.URL.Path != "/auth/github/callback" {
		return false
	}
	state := strings.Trim(strings.TrimSpace(r.URL.Query().Get("state")), "\"'")
	return installStatePattern.MatchString(state)
}

func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Path == "/webhooks/github-app" && r.Method == "POST" {
		s.appWebhook(w, r)
		return
	}
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", 405)
		return
	}
	if s.config.Sessions == nil {
		http.Error(w, "Session service unavailable", 503)
		return
	}
	account := ""
	if cookie, err := r.Cookie("vapor-session"); err == nil {
		stored, readErr := s.config.Sessions.ReadSession(r.Context(), cookie.Value)
		if readErr != nil || stored["accountId"] == "" {
			http.Error(w, "Not authenticated", 401)
			return
		}
		account = stored["accountId"]
	}
	installationCallback := r.URL.Path == "/auth/github/app/callback" || r.URL.Path == "/auth/github/callback"
	if account == "" {
		// A code-bearing install callback can arrive at a different allowlisted alias
		// than the initial host-only session. OAuth identity plus the live DB nonce
		// authenticates it; no other route gains a cookie-free authentication path.
		if installationCallback && r.URL.Query().Get("code") != "" {
			s.finishInstall(w, r, "")
			return
		}
		http.Error(w, "Not authenticated", 401)
		return
	}
	switch r.URL.Path {
	case "/github/repos":
		token, _, err := s.accountToken(r.Context(), account)
		if err != nil {
			httpError(w, err)
			return
		}
		repos, err := s.config.Client.ListRepositories(r.Context(), token)
		if err != nil {
			httpError(w, err)
			return
		}
		items := []map[string]any{}
		for _, repo := range repos {
			branch := repo.DefaultBranch
			if branch == "" {
				branch = "main"
			}
			items = append(items, map[string]any{"full_name": repo.FullName, "owner_login": repo.Owner.Login, "name": repo.Name, "default_branch": branch, "is_private": repo.Private})
		}
		jsonResponse(w, 200, items)
	case "/auth/github/app/install":
		s.beginInstall(w, r, account)
	case "/auth/github/app/callback", "/auth/github/callback":
		s.finishInstall(w, r, account)
	default:
		http.NotFound(w, r)
	}
}

// requestOrigin uses the BFF-stamped browser origin only when it exactly names
// a configured frontend. The header grants no authentication or project access;
// sessions and ownership checks remain mandatory. Forwarded host/proto headers
// cannot introduce arbitrary return origins.
func (s *Service) requestOrigin(r *http.Request) (string, error) {
	if origin := r.Header.Get("X-Context-Frontend-Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || u.User != nil || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
			return "", &PublicError{400, "Invalid frontend origin", nil}
		}
		for _, allowed := range s.config.FrontendOrigins {
			if origin == strings.TrimRight(allowed, "/") {
				return origin, nil
			}
		}
		return "", &PublicError{400, "Frontend origin is not allowed", nil}
	}
	host := r.Host
	if host == "" || strings.ContainsAny(host, "/@\\?#, \r\n\t") {
		return "", &PublicError{400, "Untrusted installation request host", nil}
	}
	for _, allowed := range s.config.FrontendOrigins {
		u, err := url.Parse(strings.TrimRight(allowed, "/"))
		if err == nil && strings.EqualFold(u.Host, host) {
			return u.Scheme + "://" + u.Host, nil
		}
	}
	return "", &PublicError{400, "Installation request host is not allowed", nil}
}

func (s *Service) returnTo(raw, project, requestOrigin string) (string, error) {
	expected := "/projects/" + url.PathEscape(project)
	if raw == "" {
		raw = expected
	}
	if strings.ContainsAny(raw, "\\\r\n\x00") {
		return "", &PublicError{400, "Invalid return URL", nil}
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Fragment != "" || u.Opaque != "" {
		return "", &PublicError{400, "Invalid return URL", nil}
	}
	if !u.IsAbs() {
		if requestOrigin == "" || u.Host != "" || !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") {
			return "", &PublicError{400, "Invalid relative return URL", nil}
		}
		base, _ := url.Parse(requestOrigin)
		u = base.ResolveReference(u)
	}
	if !strings.EqualFold(u.EscapedPath(), expected) {
		return "", &PublicError{400, "Return URL must name this project", nil}
	}
	origin := u.Scheme + "://" + u.Host
	for _, allowed := range s.config.FrontendOrigins {
		if strings.EqualFold(origin, strings.TrimRight(allowed, "/")) {
			return u.String(), nil
		}
	}
	return "", &PublicError{400, "Return URL origin is not allowed", nil}
}
func (s *Service) beginInstall(w http.ResponseWriter, r *http.Request, account string) {
	if s.config.Slug == "" || s.config.Client.ClientID == "" || s.config.Client.Key == nil {
		httpError(w, ErrNotConfigured)
		return
	}
	project := r.URL.Query().Get("project_id")
	if err := s.ownedProject(r.Context(), account, project); err != nil {
		httpError(w, err)
		return
	}
	origin, err := s.requestOrigin(r)
	if err != nil {
		httpError(w, err)
		return
	}
	returnTo, err := s.returnTo(r.URL.Query().Get("return_to"), project, origin)
	if err != nil {
		httpError(w, err)
		return
	}
	owner, repo := r.URL.Query().Get("owner"), r.URL.Query().Get("repo")
	if owner != "" || repo != "" {
		if _, err = reposURL(owner, repo, "main"); err != nil {
			httpError(w, &PublicError{400, "Invalid repository", nil})
			return
		}
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		allowed := false
		for _, candidate := range s.config.FrontendOrigins {
			allowed = allowed || origin == strings.TrimRight(candidate, "/")
		}
		if !allowed {
			httpError(w, &PublicError{403, "Untrusted request origin", nil})
			return
		}
	}
	state, err := identity.UUID()
	if err != nil {
		httpError(w, err)
		return
	}
	_, err = s.config.DB.Execute(r.Context(), sql("INSERT INTO github_app_install_intents(id,project_id,account_id,return_to,owner,repo,expires_at) VALUES (?,?,?,?,?,?,?)", turso.Text(state), turso.Text(project), turso.Text(account), turso.Text(returnTo), nullable(owner), nullable(repo), turso.Integer(time.Now().Add(time.Hour).Unix())))
	if err != nil {
		httpError(w, err)
		return
	}
	http.Redirect(w, r, "https://github.com/apps/"+url.PathEscape(s.config.Slug)+"/installations/new?state="+url.QueryEscape(state), 302)
}
func (s *Service) finishInstall(w http.ResponseWriter, r *http.Request, account string) {
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	state := strings.Trim(strings.TrimSpace(r.URL.Query().Get("state")), "\"'")
	installation := r.URL.Query().Get("installation_id")
	if state == "" || len(state) > 128 {
		httpError(w, &PublicError{400, "Invalid or expired installation state", nil})
		return
	}
	var id string
	var err error
	if installation != "" {
		id, err = idPath(installation)
		if err != nil {
			httpError(w, &PublicError{400, "Invalid installation id", nil})
			return
		}
	}
	if account == "" && r.URL.Query().Get("code") == "" {
		httpError(w, &PublicError{401, "A session or verified installation OAuth code is required", nil})
		return
	}
	query := `SELECT i.project_id,i.return_to,i.owner,i.repo,a.github_id,i.account_id FROM github_app_install_intents i JOIN projects p ON p.id=i.project_id AND p.account_id=i.account_id JOIN accounts a ON a.id=i.account_id WHERE i.id=? AND i.expires_at>?`
	args := []turso.Value{turso.Text(state), turso.Integer(time.Now().Unix())}
	if account != "" {
		query += " AND i.account_id=?"
		args = append(args, turso.Text(account))
	}
	row, err := s.config.DB.Execute(r.Context(), sql(query, args...))
	if err != nil {
		httpError(w, err)
		return
	}
	if len(row.Rows) != 1 {
		httpError(w, &PublicError{400, "Invalid or expired installation state", nil})
		return
	}
	intent := row.Rows[0]
	intentAccount := col(intent, 5)
	if intentAccount == "" || (account != "" && account != intentAccount) {
		httpError(w, &PublicError{403, "Installation state belongs to a different account", nil})
		return
	}
	account = intentAccount
	project := col(intent, 0)
	returnTo, err := s.returnTo(col(intent, 1), project, "")
	if err != nil {
		httpError(w, err)
		return
	}
	githubID, err := strconv.ParseInt(col(intent, 4), 10, 64)
	if err != nil {
		httpError(w, err)
		return
	}
	var encrypted, userToken string
	if code := r.URL.Query().Get("code"); code != "" {
		token, userID, err := s.config.Client.ExchangeInstallOAuth(r.Context(), code)
		if err != nil {
			httpError(w, err)
			return
		}
		if userID != githubID {
			httpError(w, &PublicError{403, "GitHub authorization belongs to a different user", nil})
			return
		}
		userToken = token
		encrypted, err = s.config.Cipher.Encrypt(token)
		if err != nil {
			httpError(w, err)
			return
		}
	}
	// OAuth-on-install guarantees a code at the App's first callback URL, not
	// an installation_id. Resolve an omitted id only from that verified App user
	// token, then independently verify the installation using the App JWT.
	if id == "" {
		if userToken == "" {
			httpError(w, &PublicError{400, "Installation id or authorization code is required", nil})
			return
		}
		id, err = s.config.Client.UserInstallation(r.Context(), userToken, githubID)
		if err != nil {
			httpError(w, err)
			return
		}
	}
	meta, err := s.config.Client.Installation(r.Context(), id)
	if err != nil {
		httpError(w, err)
		return
	}
	if strconv.FormatInt(meta.ID, 10) != id || meta.Account.Type != "User" || meta.Account.ID != githubID {
		httpError(w, &PublicError{403, "Installation does not belong to the signed-in GitHub account", nil})
		return
	}
	guard := `EXISTS(SELECT 1 FROM github_app_install_intents i JOIN projects p ON p.id=i.project_id AND p.account_id=i.account_id WHERE i.id=? AND i.account_id=? AND i.expires_at>?)`
	now := time.Now().Unix()
	args = []turso.Value{nullableInstallation(id), turso.Text(account), turso.Text(state), turso.Text(account), turso.Integer(now)}
	accountSQL := "UPDATE accounts SET github_app_installation_id=? WHERE id=? AND " + guard
	statements := []turso.Statement{sql(accountSQL, args...), sql("UPDATE repo_connections SET github_installation_id=? WHERE project_id=? AND "+guard, nullableInstallation(id), turso.Text(project), turso.Text(state), turso.Text(account), turso.Integer(now))}
	if encrypted != "" {
		statements = append(statements, sql("UPDATE accounts SET github_token_encrypted=? WHERE id=? AND "+guard, turso.Text(encrypted), turso.Text(account), turso.Text(state), turso.Text(account), turso.Integer(now)))
	}
	statements = append(statements, sql("DELETE FROM github_app_install_intents WHERE id=? AND account_id=? AND expires_at>? RETURNING id", turso.Text(state), turso.Text(account), turso.Integer(now)))
	results, err := s.config.DB.Batch(r.Context(), statements)
	if err != nil {
		httpError(w, err)
		return
	}
	if len(results) != len(statements) || len(results[len(results)-1].Rows) != 1 {
		httpError(w, &PublicError{409, "Installation state was already consumed", nil})
		return
	}
	target, _ := url.Parse(returnTo)
	params := target.Query()
	params.Set("github_app_installed", "1")
	if col(intent, 2) != "" && col(intent, 3) != "" {
		params.Set("resume_owner", col(intent, 2))
		params.Set("resume_repo", col(intent, 3))
	}
	target.RawQuery = params.Encode()
	http.Redirect(w, r, target.String(), 302)
}
func signatureValid(secret, header string, body []byte) bool {
	if secret == "" || !strings.HasPrefix(header, "sha256=") {
		return false
	}
	given, err := hex.DecodeString(strings.TrimPrefix(header, "sha256="))
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(given, mac.Sum(nil))
}
func (s *Service) appWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 512<<10))
	if err != nil {
		http.Error(w, "Payload too large", 413)
		return
	}
	if !signatureValid(s.config.AppWebhookSecret, r.Header.Get("X-Hub-Signature-256"), body) {
		http.Error(w, "Invalid signature", 401)
		return
	}
	event := r.Header.Get("X-GitHub-Event")
	if event == "push" {
		if s.config.PushWebhook == nil {
			httpError(w, &PublicError{501, "Push webhook service is unavailable", nil})
			return
		}
		clone := r.Clone(r.Context())
		copyURL := *r.URL
		copyURL.Path = "/webhooks/github"
		clone.URL = &copyURL
		clone.Body = io.NopCloser(bytes.NewReader(body))
		s.config.PushWebhook.ServeHTTP(w, clone)
		return
	}
	if event == "ping" {
		jsonResponse(w, 200, map[string]string{"status": "verified"})
		return
	}
	var payload struct {
		Action       string `json:"action"`
		Installation struct {
			ID int64 `json:"id"`
		} `json:"installation"`
		Account struct {
			ID int64 `json:"id"`
		} `json:"account"`
	}
	if json.Unmarshal(body, &payload) != nil {
		httpError(w, &PublicError{400, "Invalid webhook payload", nil})
		return
	}
	if event == "installation" && (payload.Action == "deleted" || payload.Action == "suspend") {
		if payload.Installation.ID <= 0 {
			httpError(w, &PublicError{400, "Missing installation id", nil})
			return
		}
		_, err = s.config.DB.Batch(r.Context(), []turso.Statement{sql("UPDATE repo_connections SET github_installation_id=NULL,webhook_id=NULL,webhook_secret=NULL WHERE github_installation_id=?", turso.Integer(payload.Installation.ID)), sql("UPDATE accounts SET github_app_installation_id=NULL WHERE github_app_installation_id=?", turso.Integer(payload.Installation.ID))})
	} else if event == "github_app_authorization" && payload.Action == "revoked" {
		if payload.Account.ID <= 0 {
			httpError(w, &PublicError{400, "Missing GitHub user id", nil})
			return
		}
		_, err = s.config.DB.Batch(r.Context(), []turso.Statement{sql("UPDATE repo_connections SET github_installation_id=NULL,webhook_id=NULL,webhook_secret=NULL WHERE project_id IN (SELECT p.id FROM projects p JOIN accounts a ON a.id=p.account_id WHERE a.github_id=?)", turso.Integer(payload.Account.ID)), sql("UPDATE accounts SET github_app_installation_id=NULL WHERE github_id=?", turso.Integer(payload.Account.ID))})
	} else if event == "installation" || event == "installation_repositories" || event == "github_app_authorization" {
		jsonResponse(w, 200, map[string]string{"status": "ignored", "event": event, "action": payload.Action})
		return
	} else {
		httpError(w, &PublicError{422, "Unsupported GitHub App event", nil})
		return
	}
	if err != nil {
		httpError(w, err)
		return
	}
	jsonResponse(w, 200, map[string]string{"status": "processed"})
}
func jsonResponse(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(payload)
}
func httpError(w http.ResponseWriter, err error) {
	var public interface{ HTTPStatus() int }
	if errors.As(err, &public) {
		payload := any(map[string]string{"reason": err.Error()})
		if e, ok := err.(interface{ HTTPPayload() any }); ok {
			payload = e.HTTPPayload()
		}
		jsonResponse(w, public.HTTPStatus(), payload)
		return
	}
	if errors.Is(err, ErrNotConfigured) {
		jsonResponse(w, 501, map[string]string{"reason": "GitHub App is not configured"})
		return
	}
	jsonResponse(w, 502, map[string]string{"reason": "GitHub operation failed"})
}
