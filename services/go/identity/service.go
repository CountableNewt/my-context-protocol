// Package identity serves GitHub browser/CLI authentication using persistent
// sessions and atomic one-time handoff tokens in the existing SQLite schema.
package identity

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/CountableNewt/my-context-protocol/services/go/security"
	"github.com/CountableNewt/my-context-protocol/services/go/session"
)

type Config struct {
	ClientID            string
	ClientSecret        string
	RedirectURI         string
	EncryptionKey       string
	FrontendOrigins     []string
	OAuthReturnOrigins  []string
	AppEnvironment      string
	GitHubAppConfigured bool
	AdminLogins         []string
	AdminIDs            []int64
}
type Service struct {
	config Config
	store  Store
	github GitHub
	cipher *security.TokenCipher
	key    []byte
	now    func() time.Time
}

func New(config Config, store Store, github GitHub) (*Service, error) {
	if store == nil || github == nil {
		return nil, errors.New("identity store and GitHub client are required")
	}
	if config.ClientID == "" || config.ClientSecret == "" {
		return nil, errors.New("GitHub OAuth credentials are required")
	}
	if len(config.FrontendOrigins) == 0 {
		return nil, errors.New("frontend origin is required")
	}
	origins := make([]string, 0, len(config.FrontendOrigins))
	for _, raw := range config.FrontendOrigins {
		origin, err := normalizeOrigin(raw)
		if err != nil {
			return nil, err
		}
		origins = append(origins, origin)
	}
	config.FrontendOrigins = origins
	callback, err := url.Parse(config.RedirectURI)
	if err != nil || callback.Host == "" || callback.User != nil || callback.RawQuery != "" || callback.Fragment != "" || callback.Path != "/auth/github/callback" || callback.Scheme != "https" && !(callback.Scheme == "http" && (callback.Hostname() == "localhost" || callback.Hostname() == "127.0.0.1")) {
		return nil, errors.New("invalid GitHub OAuth callback URL")
	}
	oauthOrigins := append([]string{}, config.FrontendOrigins...)
	for _, raw := range append(config.OAuthReturnOrigins, callback.Scheme+"://"+callback.Host) {
		origin, err := normalizeOrigin(raw)
		if err != nil {
			return nil, err
		}
		oauthOrigins = append(oauthOrigins, origin)
	}
	config.OAuthReturnOrigins = oauthOrigins
	cipher, err := security.NewTokenCipher(config.EncryptionKey)
	if err != nil {
		return nil, err
	}
	key, _ := base64.StdEncoding.DecodeString(config.EncryptionKey)
	return &Service{config: config, store: store, github: github, cipher: cipher, key: key, now: time.Now}, nil
}
func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	switch r.URL.Path {
	case "/health":
		if r.Method != "GET" {
			s.method(w)
			return
		}
		w.WriteHeader(200)
		w.Write([]byte("ok"))
	case "/auth/github":
		if r.Method != "GET" {
			s.method(w)
			return
		}
		s.initiate(w, r, false)
	case "/auth/cli/github":
		if r.Method != "GET" {
			s.method(w)
			return
		}
		s.initiate(w, r, true)
	case "/auth/github/callback":
		if r.Method != "GET" {
			s.method(w)
			return
		}
		s.callback(w, r)
	case "/auth/confirm":
		if r.Method != "GET" {
			s.method(w)
			return
		}
		s.confirm(w, r)
	case "/auth/me":
		if r.Method != "GET" {
			s.method(w)
			return
		}
		s.me(w, r)
	case "/auth/logout":
		if r.Method != "POST" {
			s.method(w)
			return
		}
		s.logout(w, r)
	default:
		http.NotFound(w, r)
	}
}
func (s *Service) method(w http.ResponseWriter) { s.fail(w, 405, "Method not allowed") }
func (s *Service) fail(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}
func (s *Service) initiate(w http.ResponseWriter, r *http.Request, cli bool) {
	if p := r.URL.Query().Get("prompt"); p != "" && p != "select_account" {
		s.fail(w, 400, "Unsupported GitHub sign-in prompt")
		return
	}
	target := r.URL.Query().Get("return_to")
	var err error
	if cli {
		target, err = ValidateCLIReturnTo(target)
	} else {
		if strings.TrimSpace(target) == "" {
			target = s.config.FrontendOrigins[0] + "/"
		}
		target, err = validateReturnTo(target, s.config.FrontendOrigins)
	}
	if err != nil {
		s.fail(w, 400, err.Error())
		return
	}
	state, err := signState(s.key, target, s.now())
	if err != nil {
		s.fail(w, 500, "Failed to build OAuth state")
		return
	}
	query := url.Values{"client_id": {s.config.ClientID}, "redirect_uri": {s.config.RedirectURI}, "state": {state}, "scope": {"read:user user:email repo read:org"}}
	if prompt := r.URL.Query().Get("prompt"); prompt != "" {
		query.Set("prompt", prompt)
	}
	http.Redirect(w, r, "https://github.com/login/oauth/authorize?"+query.Encode(), 302)
}
func addQuery(raw, key, value string) string {
	u, _ := url.Parse(raw)
	query := u.Query()
	query.Set(key, value)
	u.RawQuery = query.Encode()
	return u.String()
}
func (s *Service) callback(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	target, err := verifyState(s.key, state, s.now())
	if err != nil {
		code := "oauth_invalid_state"
		if state == "" {
			code = "oauth_missing_state"
		}
		http.Redirect(w, r, addQuery(s.config.FrontendOrigins[0]+"/login", "error", code), 302)
		return
	}
	// A signed state may have been minted before configuration changed. Recheck
	// current origins, or the exact CLI loopback grammar, before sending credentials.
	if _, err = validateReturnTo(target, s.config.OAuthReturnOrigins); err != nil {
		if _, err = ValidateCLIReturnTo(target); err != nil {
			s.fail(w, 400, "return_to origin is not allowed")
			return
		}
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		http.Redirect(w, r, addQuery(target, "error", "missing_code"), 302)
		return
	}
	token, err := s.github.Exchange(r.Context(), s.config.ClientID, s.config.ClientSecret, code, s.config.RedirectURI)
	if err != nil {
		http.Redirect(w, r, addQuery(target, "error", "token_exchange_failed"), 302)
		return
	}
	user, err := s.github.User(r.Context(), token)
	if err != nil {
		http.Redirect(w, r, addQuery(target, "error", "user_fetch_failed"), 302)
		return
	}
	encrypted, err := s.cipher.Encrypt(token)
	if err != nil {
		s.fail(w, 500, "Could not secure GitHub token")
		return
	}
	account, err := s.store.UpsertAccount(r.Context(), user, encrypted, s.admin(user.Login, user.ID))
	if err != nil || account == nil {
		s.fail(w, 503, "Auth service temporarily unavailable")
		return
	}
	handoff, err := security.NewOpaqueToken()
	if err != nil {
		s.fail(w, 500, "Could not create login handoff")
		return
	}
	if err = s.store.IssueHandoff(r.Context(), handoff, account.ID, s.now().Add(5*time.Minute)); err != nil {
		s.fail(w, 503, "Auth service temporarily unavailable")
		return
	}
	if err = s.establishSession(w, r, account.ID); err != nil {
		s.fail(w, 503, "Auth service temporarily unavailable")
		return
	}
	http.Redirect(w, r, addQuery(target, "auth_token", handoff), 302)
}
func (s *Service) admin(login string, id int64) bool {
	for _, allowed := range s.config.AdminLogins {
		if strings.EqualFold(strings.TrimSpace(allowed), login) {
			return true
		}
	}
	for _, allowed := range s.config.AdminIDs {
		if allowed == id {
			return true
		}
	}
	return false
}
func (s *Service) cookie(w http.ResponseWriter, key string, clear bool) {
	origin, _ := url.Parse(s.config.FrontendOrigins[0])
	secure := origin.Scheme == "https"
	cookie := &http.Cookie{Name: "vapor-session", Value: key, Path: "/", HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode}
	if clear {
		cookie.MaxAge = -1
		cookie.Expires = time.Unix(1, 0)
	}
	http.SetCookie(w, cookie)
}
func (s *Service) establishSession(w http.ResponseWriter, r *http.Request, account string) error {
	// Rotate instead of updating a caller-supplied session, preventing fixation and
	// making session establishment the only operation that creates rows.
	key, err := security.NewOpaqueToken()
	if err != nil {
		return err
	}
	if err = s.store.CreateSession(r.Context(), key, session.Payload{"accountId": account}); err != nil {
		return err
	}
	if previous, e := r.Cookie("vapor-session"); e == nil && previous.Value != "" {
		if err = s.store.DeleteSession(r.Context(), previous.Value); err != nil {
			_ = s.store.DeleteSession(r.Context(), key)
			return err
		}
	}
	s.cookie(w, key, false)
	return nil
}
func (s *Service) account(r *http.Request) (*Account, error) {
	cookie, err := r.Cookie("vapor-session")
	if err != nil || len(cookie.Value) != 64 {
		return nil, ErrUnauthenticated
	}
	payload, err := s.store.ReadSession(r.Context(), cookie.Value)
	if err != nil {
		return nil, err
	}
	if payload["accountId"] == "" {
		return nil, ErrUnauthenticated
	}
	account, err := s.store.Account(r.Context(), payload["accountId"])
	if err != nil {
		return nil, err
	}
	if account == nil {
		_ = s.store.DeleteSession(r.Context(), cookie.Value)
		return nil, ErrUnauthenticated
	}
	if !account.IsAdmin && s.admin(account.Login, account.GitHubID) {
		if err = s.store.GrantAdmin(r.Context(), account.ID); err != nil {
			return nil, err
		}
		account.IsAdmin = true
	}
	return account, nil
}
func (s *Service) respondUser(w http.ResponseWriter, r *http.Request, account *Account) {
	suggest := false
	if s.config.GitHubAppConfigured {
		var err error
		suggest, err = s.store.SuggestedGitHubApp(r.Context(), account.ID)
		if err != nil {
			s.fail(w, 503, "Auth service temporarily unavailable")
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"id": account.ID, "email": account.Email, "login": account.Login, "avatar_url": account.AvatarURL, "plan": "free", "is_admin": account.IsAdmin, "internal_pro_bypass": false, "can_manage_subscription": false, "suggested_github_app_install": suggest, "app_env": s.config.AppEnvironment, "non_production_bypasses": false, "needs_project_selection": false})
}
func (s *Service) me(w http.ResponseWriter, r *http.Request) {
	account, err := s.account(r)
	if errors.Is(err, ErrUnauthenticated) {
		s.fail(w, 401, "Not authenticated")
		return
	}
	if err != nil {
		s.fail(w, 503, "Auth service temporarily unavailable")
		return
	}
	s.respondUser(w, r, account)
}
func (s *Service) confirm(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		s.fail(w, 400, "Missing token")
		return
	}
	redirect := r.URL.Query().Get("redirect")
	if redirect != "" {
		var err error
		redirect, err = validateRelativePath(redirect)
		if err != nil {
			s.fail(w, 400, err.Error())
			return
		}
	}
	accountID, err := s.store.ConsumeHandoff(r.Context(), token, s.now())
	if errors.Is(err, ErrUnauthenticated) {
		s.fail(w, 401, "Invalid or expired token")
		return
	}
	if err != nil {
		s.fail(w, 503, "Auth service temporarily unavailable")
		return
	}
	account, err := s.store.Account(r.Context(), accountID)
	if err != nil {
		s.fail(w, 503, "Auth service temporarily unavailable")
		return
	}
	if account == nil {
		s.fail(w, 401, "Account not found")
		return
	}
	if !account.IsAdmin && s.admin(account.Login, account.GitHubID) {
		if s.store.GrantAdmin(r.Context(), account.ID) != nil {
			s.fail(w, 503, "Auth service temporarily unavailable")
			return
		}
		account.IsAdmin = true
	}
	if s.establishSession(w, r, accountID) != nil {
		s.fail(w, 503, "Auth service temporarily unavailable")
		return
	}
	if redirect != "" {
		http.Redirect(w, r, s.config.FrontendOrigins[0]+redirect, 302)
		return
	}
	w.Header().Set("X-Context-Frontend-Origin", s.config.FrontendOrigins[0])
	s.respondUser(w, r, account)
}
func (s *Service) logout(w http.ResponseWriter, r *http.Request) {
	allowed := false
	if origin := r.Header.Get("Origin"); origin != "" {
		for _, base := range s.config.FrontendOrigins {
			if strings.EqualFold(strings.TrimSpace(origin), base) {
				allowed = true
			}
		}
	} else if referer := r.Header.Get("Referer"); referer != "" {
		_, err := validateReturnTo(referer, s.config.FrontendOrigins)
		allowed = err == nil
	}
	if !allowed {
		s.fail(w, 403, "Invalid origin")
		return
	}
	if cookie, err := r.Cookie("vapor-session"); err == nil {
		if s.store.DeleteSession(r.Context(), cookie.Value) != nil {
			s.fail(w, 503, "Auth service temporarily unavailable")
			return
		}
	}
	s.cookie(w, "", true)
	w.WriteHeader(204)
}

// ParseAdminIDs reads the existing comma-separated bootstrap setting.
func ParseAdminIDs(raw string) []int64 {
	var ids []int64
	for _, part := range strings.Split(raw, ",") {
		if id, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64); err == nil {
			ids = append(ids, id)
		}
	}
	return ids
}

// Authenticate resolves an existing cookie session for other Go HTTP services.
func (s *Service) Authenticate(r *http.Request) (*Account, error) { return s.account(r) }
