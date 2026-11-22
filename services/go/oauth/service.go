// Package oauth implements tenant-bound MCP OAuth with PKCE and database-backed
// one-time authorization and consent. It shares the existing Swift table formats.
package oauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/CountableNewt/my-context-protocol/services/go/identity"
	"github.com/CountableNewt/my-context-protocol/services/go/security"
	"github.com/CountableNewt/my-context-protocol/services/go/session"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
)

const Scope = "mcp:invoke"

type Database interface {
	Execute(context.Context, turso.Statement) (*turso.Result, error)
	Batch(context.Context, []turso.Statement) ([]turso.Result, error)
}
type Sessions interface {
	ReadSession(context.Context, string) (session.Payload, error)
	CreateSession(context.Context, string, session.Payload) error
	DeleteSession(context.Context, string) error
	IssueHandoff(context.Context, string, string, time.Time) error
	ConsumeHandoff(context.Context, string, time.Time) (string, error)
}
type Project struct{ ID, AccountID, Name, Origin string }
type Config struct {
	Enabled                       bool
	AllowedOrigins                []string
	MCPPath                       string
	APIOrigin                     string
	Resolve                       func(*http.Request) (*Project, error)
	ProjectByID                   func(context.Context, string) (*Project, error)
	GitHubLogin                   func(string) (string, error)
	CodeTTL, TokenTTL, PendingTTL time.Duration
}
type Service struct {
	config   Config
	db       Database
	sessions Sessions
	now      func() time.Time
}

func New(c Config, db Database, sessions Sessions) (*Service, error) {
	if db == nil || sessions == nil || c.Resolve == nil || c.ProjectByID == nil || c.GitHubLogin == nil {
		return nil, errors.New("OAuth database, session and tenant adapters are required")
	}
	if c.MCPPath == "" {
		c.MCPPath = "/mcp"
	}
	if !strings.HasPrefix(c.MCPPath, "/") || strings.ContainsAny(c.MCPPath, "?#\\\r\n") {
		return nil, errors.New("invalid MCP path")
	}
	if c.APIOrigin != "" {
		u, e := url.Parse(c.APIOrigin)
		if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" || u.Scheme != "https" && u.Scheme != "http" {
			return nil, errors.New("invalid OAuth API origin")
		}
	}
	if c.CodeTTL <= 0 {
		c.CodeTTL = 10 * time.Minute
	}
	if c.TokenTTL <= 0 {
		c.TokenTTL = time.Hour
	}
	if c.PendingTTL <= 0 {
		c.PendingTTL = 15 * time.Minute
	}
	return &Service{c, db, sessions, time.Now}, nil
}
func Handles(path string) bool {
	return path == "/authorize" || path == "/register" || path == "/token" || path == "/oauth/consent" || path == "/auth/github/mcp-oauth-start" || path == "/auth/mcp-oauth-resume" || strings.HasPrefix(path, "/.well-known/oauth-protected-resource") || strings.HasPrefix(path, "/.well-known/oauth-authorization-server")
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"error": code, "error_description": message})
}
func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if !s.config.Enabled {
		http.NotFound(w, r)
		return
	}
	if r.URL.Path == "/auth/github/mcp-oauth-start" {
		if r.Method != "GET" {
			fail(w, 405, "invalid_request", "Method not allowed")
			return
		}
		s.start(w, r)
		return
	}
	if r.URL.Path == "/auth/mcp-oauth-resume" {
		if r.Method != "GET" {
			fail(w, 405, "invalid_request", "Method not allowed")
			return
		}
		s.resume(w, r)
		return
	}
	p, e := s.config.Resolve(r)
	if e != nil || p == nil || p.ID == "" || p.Origin == "" {
		fail(w, 403, "invalid_request", "MCP OAuth requires a project host")
		return
	}
	origin := r.Header.Get("Origin")
	if origin != "" {
		allowed := strings.EqualFold(origin, p.Origin)
		for _, candidate := range s.config.AllowedOrigins {
			allowed = allowed || strings.EqualFold(origin, candidate)
		}
		if !allowed {
			fail(w, 403, "access_denied", "Origin is not allowed")
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		w.Header().Set("Access-Control-Expose-Headers", "WWW-Authenticate")
		w.Header().Add("Vary", "Origin")
	}
	if r.Method == http.MethodOptions {
		method := r.Header.Get("Access-Control-Request-Method")
		if origin == "" || method != "GET" && method != "POST" {
			fail(w, 403, "access_denied", "Invalid preflight")
			return
		}
		for _, raw := range strings.Split(r.Header.Get("Access-Control-Request-Headers"), ",") {
			name := strings.ToLower(strings.TrimSpace(raw))
			if name != "" && name != "authorization" && name != "content-type" && name != "mcp-protocol-version" {
				fail(w, 403, "access_denied", "Header is not allowed")
				return
			}
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, MCP-Protocol-Version")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	switch {
	case strings.HasPrefix(r.URL.Path, "/.well-known/oauth-protected-resource") && r.Method == "GET":
		writeJSON(w, 200, map[string]any{"resource": p.Origin + s.config.MCPPath, "authorization_servers": []string{p.Origin}, "scopes_supported": []string{Scope}, "bearer_methods_supported": []string{"header"}})
	case strings.HasPrefix(r.URL.Path, "/.well-known/oauth-authorization-server") && r.Method == "GET":
		writeJSON(w, 200, map[string]any{"issuer": p.Origin, "authorization_endpoint": p.Origin + "/authorize", "token_endpoint": p.Origin + "/token", "registration_endpoint": p.Origin + "/register", "response_types_supported": []string{"code"}, "grant_types_supported": []string{"authorization_code"}, "code_challenge_methods_supported": []string{"S256"}, "token_endpoint_auth_methods_supported": []string{"none"}, "scopes_supported": []string{Scope}})
	case r.URL.Path == "/register" && r.Method == "POST":
		s.register(w, r, p)
	case r.URL.Path == "/authorize" && r.Method == "GET":
		s.authorize(w, r, p)
	case r.URL.Path == "/oauth/consent" && r.Method == "GET":
		s.consent(w, r, p)
	case r.URL.Path == "/oauth/consent" && r.Method == "POST":
		s.decide(w, r, p)
	case r.URL.Path == "/token" && r.Method == "POST":
		s.token(w, r, p)
	default:
		fail(w, 405, "invalid_request", "Method not allowed")
	}
}
func statement(sql string, args ...turso.Value) turso.Statement {
	return turso.Statement{SQL: sql, Args: args, WantRows: true}
}
func text(v string) turso.Value { return turso.Text(v) }
func num(v int64) turso.Value   { return turso.Integer(v) }
func (s *Service) query(ctx context.Context, sql string, args ...turso.Value) ([]map[string]string, error) {
	res, e := s.db.Execute(ctx, statement(sql, args...))
	if e != nil {
		return nil, e
	}
	if res == nil {
		return nil, errors.New("missing SQL result")
	}
	rows := make([]map[string]string, 0, len(res.Rows))
	for _, row := range res.Rows {
		if len(row) != len(res.Columns) {
			return nil, errors.New("invalid SQL row")
		}
		m := map[string]string{}
		for i, v := range row {
			if res.Columns[i].Name == nil {
				return nil, errors.New("unnamed SQL column")
			}
			if v.Type == "null" {
				continue
			}
			switch x := v.Value.(type) {
			case string:
				m[*res.Columns[i].Name] = x
			case float64:
				m[*res.Columns[i].Name] = strconv.FormatFloat(x, 'f', -1, 64)
			default:
				return nil, errors.New("invalid SQL scalar")
			}
		}
		rows = append(rows, m)
	}
	return rows, nil
}
func random(prefix string) (string, error) {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	return prefix + base64.RawURLEncoding.EncodeToString(b), nil
}
func allowedRedirect(raw string) bool {
	if len(raw) > 4096 || strings.ContainsAny(raw, "\\\r\n\x00") {
		return false
	}
	u, e := url.Parse(raw)
	if e != nil || u.Host == "" || u.User != nil || u.Fragment != "" {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	ip := net.ParseIP(u.Hostname())
	return u.Scheme == "http" && (u.Hostname() == "localhost" || ip != nil && ip.IsLoopback())
}
func resourceMatches(raw, origin, path string) bool {
	if strings.TrimSpace(raw) == "" {
		return true
	}
	normalize := func(raw string) string {
		u, e := url.Parse(raw)
		if e != nil || u.Host == "" || u.User != nil || u.Fragment != "" || u.Scheme != "http" && u.Scheme != "https" {
			return ""
		}
		u.Scheme = strings.ToLower(u.Scheme)
		u.Host = strings.ToLower(u.Host)
		if u.Port() == "443" && u.Scheme == "https" || u.Port() == "80" && u.Scheme == "http" {
			u.Host = u.Hostname()
			if strings.Contains(u.Host, ":") {
				u.Host = "[" + u.Host + "]"
			}
		}
		u.Path = strings.TrimSuffix(u.Path, "/")
		u.RawQuery = ""
		u.ForceQuery = false
		return u.String()
	}
	a := normalize(raw)
	return a != "" && a == normalize(origin+path)
}
func scopeValid(raw string) bool {
	for _, v := range strings.Fields(raw) {
		if v != Scope {
			return false
		}
	}
	return true
}
func redirect(w http.ResponseWriter, r *http.Request, target string, values url.Values) {
	u, e := url.Parse(target)
	if e != nil {
		fail(w, 400, "invalid_request", "Invalid redirect URI")
		return
	}
	q := u.Query()
	for k, vs := range values {
		q.Del(k)
		for _, v := range vs {
			q.Add(k, v)
		}
	}
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), 303)
}
func (s *Service) errorRedirect(w http.ResponseWriter, r *http.Request, target, state, code, message string) {
	redirect(w, r, target, url.Values{"state": {state}, "error": {code}, "error_description": {message}})
}
func (s *Service) register(w http.ResponseWriter, r *http.Request, p *Project) {
	var body struct {
		Redirects []string `json:"redirect_uris"`
		Name      string   `json:"client_name"`
		Grants    []string `json:"grant_types"`
		Auth      string   `json:"token_endpoint_auth_method"`
		Responses []string `json:"response_types"`
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	if d.Decode(&body) != nil || len(body.Redirects) == 0 || len(body.Redirects) > 100 {
		fail(w, 400, "invalid_request", "redirect_uris must be non-empty")
		return
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		fail(w, 400, "invalid_request", "Invalid JSON body")
		return
	}
	for _, v := range body.Redirects {
		if !allowedRedirect(v) {
			fail(w, 400, "invalid_redirect_uri", "Invalid redirect_uri")
			return
		}
	}
	if body.Auth != "" && body.Auth != "none" {
		fail(w, 400, "invalid_client_metadata", "Only public clients are supported")
		return
	}
	if body.Grants == nil {
		body.Grants = []string{"authorization_code"}
	}
	hasCode := false
	for _, v := range body.Grants {
		if v == "authorization_code" {
			hasCode = true
		} else if v != "refresh_token" {
			fail(w, 400, "invalid_client_metadata", "Unsupported grant type")
			return
		}
	}
	if !hasCode {
		fail(w, 400, "invalid_client_metadata", "authorization_code is required")
		return
	}
	if body.Responses == nil {
		body.Responses = []string{"code"}
	}
	for _, v := range body.Responses {
		if v != "code" {
			fail(w, 400, "invalid_client_metadata", "Only code response type is supported")
			return
		}
	}
	id, e := identity.UUID()
	if e != nil {
		fail(w, 503, "server_error", "Could not register client")
		return
	}
	internal, e := identity.UUID()
	if e != nil {
		fail(w, 503, "server_error", "Could not register client")
		return
	}
	uris, _ := json.Marshal(body.Redirects)
	_, e = s.db.Execute(r.Context(), statement("INSERT INTO mcp_oauth_clients(id,public_client_id,client_secret_hash,is_confidential,redirect_uris_json,allowed_grants,status,created_at,project_id) VALUES (?,?,NULL,0,?,'authorization_code','active',?,?)", text(internal), text(id), text(string(uris)), num(s.now().Unix()), text(p.ID)))
	if e != nil {
		fail(w, 503, "server_error", "Could not register client")
		return
	}
	writeJSON(w, 201, map[string]any{"client_id": id, "redirect_uris": body.Redirects, "grant_types": body.Grants, "response_types": body.Responses, "token_endpoint_auth_method": "none"})
}
func (s *Service) account(r *http.Request) string {
	c, e := r.Cookie("vapor-session")
	if e != nil {
		return ""
	}
	data, e := s.sessions.ReadSession(r.Context(), c.Value)
	if e != nil {
		return ""
	}
	return data["accountId"]
}
func (s *Service) owns(ctx context.Context, account, project string) bool {
	if account == "" {
		return false
	}
	rows, e := s.query(ctx, "SELECT id FROM projects WHERE id=? AND account_id=?", text(project), text(strings.ToUpper(account)))
	return e == nil && len(rows) == 1
}
func (s *Service) startURL(id string) string {
	return s.config.APIOrigin + "/auth/github/mcp-oauth-start?pending=" + url.QueryEscape(id)
}
func (s *Service) authorize(w http.ResponseWriter, r *http.Request, p *Project) {
	q := r.URL.Query()
	clientID, target, state := strings.TrimSpace(q.Get("client_id")), strings.TrimSpace(q.Get("redirect_uri")), strings.TrimSpace(q.Get("state"))
	if clientID == "" || target == "" || state == "" || len(state) > 4096 {
		fail(w, 400, "invalid_request", "Missing client_id, redirect_uri or state")
		return
	}
	rows, e := s.query(r.Context(), "SELECT id,redirect_uris_json,allowed_grants FROM mcp_oauth_clients WHERE public_client_id=? AND project_id=? AND status='active'", text(clientID), text(p.ID))
	if e != nil {
		fail(w, 503, "server_error", "Authorization unavailable")
		return
	}
	if len(rows) != 1 {
		fail(w, 400, "invalid_client", "Unknown client_id")
		return
	}
	client := rows[0]
	var uris []string
	if json.Unmarshal([]byte(client["redirect_uris_json"]), &uris) != nil {
		fail(w, 503, "server_error", "Invalid client configuration")
		return
	}
	found := false
	for _, v := range uris {
		if target == v && allowedRedirect(v) {
			found = true
		}
	}
	if !found {
		fail(w, 400, "invalid_request", "redirect_uri is not registered")
		return
	}
	reject := func(code, msg string) { s.errorRedirect(w, r, target, state, code, msg) }
	if q.Get("response_type") != "code" {
		reject("unsupported_response_type", "Only code is supported")
		return
	}
	granted := false
	for _, g := range strings.Split(client["allowed_grants"], ",") {
		if strings.TrimSpace(g) == "authorization_code" {
			granted = true
		}
	}
	if !granted {
		reject("unauthorized_client", "Client cannot use authorization_code")
		return
	}
	challenge := strings.TrimSpace(q.Get("code_challenge"))
	if challenge == "" || len(challenge) > 128 || !strings.EqualFold(q.Get("code_challenge_method"), "S256") {
		reject("invalid_request", "S256 code_challenge is required")
		return
	}
	if !resourceMatches(q.Get("resource"), p.Origin, s.config.MCPPath) {
		reject("invalid_target", "resource does not match this MCP host")
		return
	}
	if !scopeValid(q.Get("scope")) {
		fail(w, 400, "invalid_scope", "Unsupported scope")
		return
	}
	id, e := identity.UUID()
	if e != nil {
		fail(w, 503, "server_error", "Authorization unavailable")
		return
	}
	now := s.now()
	_, e = s.db.Execute(r.Context(), statement("INSERT INTO mcp_oauth_pending_authorizations(id,project_id,mcp_oauth_client_id,redirect_uri,state,scope,code_challenge,code_challenge_method,expires_at,created_at) VALUES (?,?,?,?,?,?,?,'S256',?,?)", text(id), text(p.ID), text(client["id"]), text(target), text(state), text(Scope), text(challenge), num(now.Add(s.config.PendingTTL).Unix()), num(now.Unix())))
	if e != nil {
		fail(w, 503, "server_error", "Authorization unavailable")
		return
	}
	if s.owns(r.Context(), s.account(r), p.ID) {
		http.Redirect(w, r, "/oauth/consent?pending="+id, 303)
	} else {
		http.Redirect(w, r, s.startURL(id), 303)
	}
}
func (s *Service) pending(ctx context.Context, id string) (map[string]string, error) {
	rows, e := s.query(ctx, "SELECT id,project_id,mcp_oauth_client_id,redirect_uri,state,scope,code_challenge,code_challenge_method FROM mcp_oauth_pending_authorizations WHERE id=? AND expires_at>?", text(strings.ToUpper(id)), num(s.now().Unix()))
	if e != nil {
		return nil, e
	}
	if len(rows) != 1 {
		return nil, errors.New("invalid pending authorization")
	}
	return rows[0], nil
}
func (s *Service) start(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("pending")
	p, e := s.pending(r.Context(), id)
	if e != nil {
		fail(w, 400, "invalid_request", "Invalid pending authorization")
		return
	}
	target := s.config.APIOrigin + "/auth/mcp-oauth-resume?pending=" + url.QueryEscape(p["id"])
	login, e := s.config.GitHubLogin(target)
	if e != nil {
		fail(w, 503, "server_error", "Login unavailable")
		return
	}
	http.Redirect(w, r, login, 303)
}
func (s *Service) handoff(ctx context.Context, account string) (string, error) {
	token, e := security.NewOpaqueToken()
	if e != nil {
		return "", e
	}
	e = s.sessions.IssueHandoff(ctx, token, account, s.now().Add(5*time.Minute))
	return token, e
}
func (s *Service) resume(w http.ResponseWriter, r *http.Request) {
	pending, e := s.pending(r.Context(), r.URL.Query().Get("pending"))
	if e != nil {
		fail(w, 400, "invalid_request", "Invalid pending authorization")
		return
	}
	if token := r.URL.Query().Get("auth_token"); token != "" {
		account, e := s.sessions.ConsumeHandoff(r.Context(), token, s.now())
		if e != nil || !s.owns(r.Context(), account, pending["project_id"]) {
			fail(w, 403, "access_denied", "Invalid login handoff")
			return
		}
		key, e := security.NewOpaqueToken()
		if e == nil {
			e = s.sessions.CreateSession(r.Context(), key, session.Payload{"accountId": account})
		}
		if e != nil {
			fail(w, 503, "server_error", "Session unavailable")
			return
		}
		if old, e := r.Cookie("vapor-session"); e == nil {
			_ = s.sessions.DeleteSession(r.Context(), old.Value)
		}
		http.SetCookie(w, &http.Cookie{Name: "vapor-session", Value: key, Path: "/", Secure: strings.HasPrefix(s.config.APIOrigin, "https://"), HttpOnly: true, SameSite: http.SameSiteLaxMode})
		http.Redirect(w, r, s.config.APIOrigin+"/auth/mcp-oauth-resume?pending="+pending["id"], 303)
		return
	}
	account := s.account(r)
	if !s.owns(r.Context(), account, pending["project_id"]) {
		fail(w, 403, "access_denied", "Not authorized for project")
		return
	}
	p, e := s.config.ProjectByID(r.Context(), pending["project_id"])
	if e != nil || p == nil || p.Origin == "" {
		fail(w, 503, "server_error", "Project unavailable")
		return
	}
	token, e := s.handoff(r.Context(), account)
	if e != nil {
		fail(w, 503, "server_error", "Handoff unavailable")
		return
	}
	redirect(w, r, p.Origin+"/oauth/consent", url.Values{"pending": {pending["id"]}, "auth_token": {token}})
}
func (s *Service) consent(w http.ResponseWriter, r *http.Request, p *Project) {
	pending, e := s.pending(r.Context(), r.URL.Query().Get("pending"))
	if e != nil || pending["project_id"] != p.ID {
		fail(w, 400, "invalid_request", "Invalid pending authorization")
		return
	}
	account := s.account(r)
	if token := r.URL.Query().Get("auth_token"); token != "" {
		account, e = s.sessions.ConsumeHandoff(r.Context(), token, s.now())
		if e != nil {
			fail(w, 401, "access_denied", "Invalid login handoff")
			return
		}
	}
	if account == "" {
		http.Redirect(w, r, s.startURL(pending["id"]), 303)
		return
	}
	if !s.owns(r.Context(), account, p.ID) {
		fail(w, 403, "access_denied", "Not authorized for project")
		return
	}
	token, e := s.handoff(r.Context(), account)
	if e != nil {
		fail(w, 503, "server_error", "Consent unavailable")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
	fmt.Fprintf(w, "<!doctype html><html><head><meta charset=\"utf-8\"><title>Authorize MCP Client</title></head><body><h1>Authorize MCP Client</h1><p>Project: <strong>%s</strong></p><p>Permissions: <code>%s</code></p><form method=\"post\" action=\"/oauth/consent\"><input type=\"hidden\" name=\"pending\" value=\"%s\"><input type=\"hidden\" name=\"consent_token\" value=\"%s\"><button name=\"decision\" value=\"approve\">Approve</button><button name=\"decision\" value=\"deny\">Deny</button></form></body></html>", html.EscapeString(p.Name), html.EscapeString(pending["scope"]), html.EscapeString(pending["id"]), html.EscapeString(token))
}
func readForm(w http.ResponseWriter, r *http.Request, limit int64) error {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	if r.Header.Get("Content-Type") == "application/json" {
		return errors.New("form encoding required")
	}
	return r.ParseForm()
}
func (s *Service) decide(w http.ResponseWriter, r *http.Request, p *Project) {
	if readForm(w, r, 256<<10) != nil {
		fail(w, 400, "invalid_request", "Invalid consent form")
		return
	}
	pending, e := s.pending(r.Context(), r.PostForm.Get("pending"))
	if e != nil || pending["project_id"] != p.ID {
		fail(w, 400, "invalid_request", "Invalid pending authorization")
		return
	}
	account, e := s.sessions.ConsumeHandoff(r.Context(), r.PostForm.Get("consent_token"), s.now())
	if e != nil || !s.owns(r.Context(), account, p.ID) {
		fail(w, 403, "access_denied", "Invalid or expired consent token")
		return
	}
	if r.PostForm.Get("decision") != "approve" {
		res, e := s.db.Execute(r.Context(), statement("DELETE FROM mcp_oauth_pending_authorizations WHERE id=? AND project_id=? AND expires_at>?", text(pending["id"]), text(p.ID), num(s.now().Unix())))
		if e != nil || res.AffectedRowCount != 1 {
			fail(w, 400, "invalid_request", "Pending authorization already consumed")
			return
		}
		s.errorRedirect(w, r, pending["redirect_uri"], pending["state"], "access_denied", "User denied authorization")
		return
	}
	code, e := random("mcp_oac_")
	if e != nil {
		fail(w, 503, "server_error", "Authorization unavailable")
		return
	}
	id, e := identity.UUID()
	if e != nil {
		fail(w, 503, "server_error", "Authorization unavailable")
		return
	}
	now := s.now()
	out, e := s.db.Batch(r.Context(), []turso.Statement{
		statement("DELETE FROM mcp_oauth_pending_authorizations WHERE id=? AND project_id=? AND expires_at>?", text(pending["id"]), text(p.ID), num(now.Unix())),
		statement("INSERT INTO mcp_oauth_authorization_codes(id,code_hash,project_id,mcp_oauth_client_id,account_id,redirect_uri,scope,code_challenge,code_challenge_method,expires_at,created_at) SELECT ?,?,?,?,?,?,?,?,'S256',?,? WHERE changes()=1", text(id), text(security.HashToken(code)), text(p.ID), text(pending["mcp_oauth_client_id"]), text(account), text(pending["redirect_uri"]), text(pending["scope"]), text(pending["code_challenge"]), num(now.Add(s.config.CodeTTL).Unix()), num(now.Unix())),
	})
	if e != nil {
		fail(w, 503, "server_error", "Authorization unavailable")
		return
	}
	if len(out) != 2 || out[0].AffectedRowCount != 1 || out[1].AffectedRowCount != 1 {
		fail(w, 400, "invalid_request", "Pending authorization already consumed")
		return
	}
	redirect(w, r, pending["redirect_uri"], url.Values{"code": {code}, "state": {pending["state"]}})
}
func (s *Service) token(w http.ResponseWriter, r *http.Request, p *Project) {
	if readForm(w, r, 64<<10) != nil {
		fail(w, 400, "invalid_request", "Invalid token request")
		return
	}
	f := r.PostForm
	if !resourceMatches(f.Get("resource"), p.Origin, s.config.MCPPath) {
		fail(w, 400, "invalid_target", "resource does not match this MCP host")
		return
	}
	if f.Get("grant_type") != "authorization_code" {
		fail(w, 400, "unsupported_grant_type", "Only authorization_code is supported")
		return
	}
	clientID, secret := f.Get("client_id"), f.Get("client_secret")
	if user, pass, ok := r.BasicAuth(); ok {
		if clientID == "" {
			clientID = user
		}
		if secret == "" {
			secret = pass
		}
	}
	code, target, verifier := f.Get("code"), f.Get("redirect_uri"), f.Get("code_verifier")
	if clientID == "" || code == "" || target == "" || verifier == "" {
		fail(w, 400, "invalid_request", "code, redirect_uri, client_id and code_verifier are required")
		return
	}
	clients, e := s.query(r.Context(), "SELECT id,is_confidential,client_secret_hash FROM mcp_oauth_clients WHERE public_client_id=? AND project_id=? AND status='active'", text(clientID), text(p.ID))
	if e != nil {
		fail(w, 503, "server_error", "Token service unavailable")
		return
	}
	if len(clients) != 1 {
		fail(w, 400, "invalid_client", "Unknown client")
		return
	}
	client := clients[0]
	if client["is_confidential"] == "1" && (secret == "" || subtle.ConstantTimeCompare([]byte(security.HashToken(secret)), []byte(client["client_secret_hash"])) != 1) {
		fail(w, 401, "invalid_client", "Invalid client credentials")
		return
	}
	codes, e := s.query(r.Context(), "SELECT id,account_id,scope,code_challenge FROM mcp_oauth_authorization_codes WHERE code_hash=? AND mcp_oauth_client_id=? AND project_id=? AND redirect_uri=? AND consumed_at IS NULL AND expires_at>?", text(security.HashToken(code)), text(client["id"]), text(p.ID), text(target), num(s.now().Unix()))
	if e != nil {
		fail(w, 503, "server_error", "Token service unavailable")
		return
	}
	if len(codes) != 1 {
		fail(w, 400, "invalid_grant", "Invalid or expired code")
		return
	}
	row := codes[0]
	hash := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(hash[:])
	if subtle.ConstantTimeCompare([]byte(challenge), []byte(row["code_challenge"])) != 1 {
		fail(w, 400, "invalid_grant", "PKCE verification failed")
		return
	}
	token, e := random("mcp_oat_")
	if e != nil {
		fail(w, 503, "server_error", "Token service unavailable")
		return
	}
	id, e := identity.UUID()
	if e != nil {
		fail(w, 503, "server_error", "Token service unavailable")
		return
	}
	now := s.now()
	out, e := s.db.Batch(r.Context(), []turso.Statement{
		statement("UPDATE mcp_oauth_authorization_codes SET consumed_at=? WHERE id=? AND project_id=? AND mcp_oauth_client_id=? AND redirect_uri=? AND code_challenge=? AND consumed_at IS NULL AND expires_at>?", num(now.Unix()), text(row["id"]), text(p.ID), text(client["id"]), text(target), text(challenge), num(now.Unix())),
		statement("INSERT INTO mcp_oauth_access_tokens(id,token_hash,project_id,mcp_oauth_client_id,account_id,subject_type,scope,expires_at,created_at) SELECT ?,?,?,?,?,'user',?,?,? WHERE changes()=1", text(id), text(security.HashToken(token)), text(p.ID), text(client["id"]), text(row["account_id"]), text(row["scope"]), num(now.Add(s.config.TokenTTL).Unix()), num(now.Unix())),
	})
	if e != nil {
		fail(w, 503, "server_error", "Token service unavailable")
		return
	}
	if len(out) != 2 || out[0].AffectedRowCount != 1 || out[1].AffectedRowCount != 1 {
		fail(w, 400, "invalid_grant", "Invalid or expired code")
		return
	}
	writeJSON(w, 200, map[string]any{"access_token": token, "token_type": "Bearer", "expires_in": int64(s.config.TokenTTL / time.Second), "scope": row["scope"]})
}
