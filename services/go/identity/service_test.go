package identity

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CountableNewt/my-context-protocol/services/go/session"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
)

type fakeStore struct {
	mu        sync.Mutex
	account   *Account
	sessions  map[string]session.Payload
	handoffs  map[string]string
	expires   time.Time
	encrypted string
	fail      bool
}

func (f *fakeStore) Account(_ context.Context, id string) (*Account, error) {
	if f.fail {
		return nil, errors.New("db offline")
	}
	if f.account != nil && id == f.account.ID {
		return f.account, nil
	}
	return nil, nil
}
func (f *fakeStore) UpsertAccount(_ context.Context, u GitHubUser, encrypted string, admin bool) (*Account, error) {
	f.encrypted = encrypted
	f.account = &Account{ID: "AAAAAAAA-BBBB-4CCC-8DDD-EEEEEEEEEEEE", GitHubID: u.ID, Login: u.Login, IsAdmin: admin}
	return f.account, nil
}
func (f *fakeStore) GrantAdmin(context.Context, string) error { f.account.IsAdmin = true; return nil }
func (f *fakeStore) ReadSession(_ context.Context, key string) (session.Payload, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return nil, errors.New("db offline")
	}
	payload, ok := f.sessions[key]
	if !ok {
		return nil, ErrUnauthenticated
	}
	return payload, nil
}
func (f *fakeStore) CreateSession(_ context.Context, key string, payload session.Payload) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sessions == nil {
		f.sessions = map[string]session.Payload{}
	}
	f.sessions[key] = payload
	return nil
}
func (f *fakeStore) UpdateSession(_ context.Context, key string, payload session.Payload) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.sessions[key]; !ok {
		return ErrUnauthenticated
	}
	f.sessions[key] = payload
	return nil
}
func (f *fakeStore) DeleteSession(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.sessions, key)
	return nil
}
func (f *fakeStore) IssueHandoff(_ context.Context, token, account string, expires time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.handoffs == nil {
		f.handoffs = map[string]string{}
	}
	f.handoffs[token] = account
	f.expires = expires
	return nil
}
func (f *fakeStore) ConsumeHandoff(_ context.Context, token string, now time.Time) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id, ok := f.handoffs[token]
	if !ok || !now.Before(f.expires) {
		return "", ErrUnauthenticated
	}
	delete(f.handoffs, token)
	return id, nil
}
func (f *fakeStore) SuggestedGitHubApp(context.Context, string) (bool, error) { return false, nil }

type fakeGitHub struct{ fail bool }

func (f fakeGitHub) Exchange(context.Context, string, string, string, string) (string, error) {
	if f.fail {
		return "", errors.New("github offline")
	}
	return "secret-github-token", nil
}
func (f fakeGitHub) User(context.Context, string) (GitHubUser, error) {
	return GitHubUser{ID: 123, Login: "sam"}, nil
}
func newService(t *testing.T) (*Service, *fakeStore) {
	t.Helper()
	store := &fakeStore{}
	service, err := New(Config{ClientID: "client", ClientSecret: "secret", RedirectURI: "https://api.example.com/auth/github/callback", EncryptionKey: base64.StdEncoding.EncodeToString(make([]byte, 32)), FrontendOrigins: []string{"https://app.example.com", "https://branch-preview.vercel.app"}, AppEnvironment: "test", AdminLogins: []string{"sam"}}, store, fakeGitHub{})
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return time.Unix(1700000000, 0) }
	return service, store
}
func request(service *Service, method, path string, cookie *http.Cookie, origin string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, nil)
	if cookie != nil {
		r.AddCookie(cookie)
	}
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	w := httptest.NewRecorder()
	service.ServeHTTP(w, r)
	return w
}
func TestLoginCLIHandoffConfirmMeLogout(t *testing.T) {
	service, store := newService(t)
	target := "http://127.0.0.1:54321/callback?state=" + strings.Repeat("x", 43)
	initiate := request(service, "GET", "/auth/cli/github?return_to="+url.QueryEscape(target), nil, "")
	if initiate.Code != 302 {
		t.Fatalf("initiate %d: %s", initiate.Code, initiate.Body)
	}
	authorize, _ := url.Parse(initiate.Header().Get("Location"))
	if authorize.Query().Get("scope") != "read:user user:email repo read:org" {
		t.Fatal("scope changed")
	}
	callback := request(service, "GET", "/auth/github/callback?code=valid&state="+url.QueryEscape(authorize.Query().Get("state")), nil, "")
	if callback.Code != 302 {
		t.Fatalf("callback %d: %s", callback.Code, callback.Body)
	}
	redirect, _ := url.Parse(callback.Header().Get("Location"))
	if redirect.Host != "127.0.0.1:54321" || redirect.Query().Get("state") != strings.Repeat("x", 43) {
		t.Fatal("CLI nonce lost")
	}
	token := redirect.Query().Get("auth_token")
	if len(token) != 64 {
		t.Fatal("handoff missing")
	}
	plaintext, err := service.cipher.Decrypt(store.encrypted)
	if err != nil || plaintext != "secret-github-token" {
		t.Fatal("stored token not encrypted compatibly")
	}
	confirm := request(service, "GET", "/auth/confirm?token="+token, nil, "")
	if confirm.Code != 200 {
		t.Fatalf("confirm %d: %s", confirm.Code, confirm.Body)
	}
	cookies := confirm.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != "vapor-session" || cookies[0].Domain != "" || !cookies[0].Secure || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode {
		t.Fatal("unsafe session cookie")
	}
	if confirm.Header().Get("X-Context-Frontend-Origin") != "https://app.example.com" {
		t.Fatal("native origin header missing")
	}
	again := request(service, "GET", "/auth/confirm?token="+token, nil, "")
	if again.Code != 401 {
		t.Fatal("handoff replay succeeded")
	}
	me := request(service, "GET", "/auth/me", cookies[0], "")
	if me.Code != 200 {
		t.Fatalf("me %d", me.Code)
	}
	var user map[string]any
	if json.Unmarshal(me.Body.Bytes(), &user) != nil || user["is_admin"] != true || user["plan"] != "free" || user["can_manage_subscription"] != false || user["needs_project_selection"] != false {
		t.Fatal("unexpected user contract")
	}
	rejected := request(service, "POST", "/auth/logout", cookies[0], "https://evil.example")
	if rejected.Code != 403 {
		t.Fatal("cross-origin logout accepted")
	}
	logout := request(service, "POST", "/auth/logout", cookies[0], "https://app.example.com")
	if logout.Code != 204 {
		t.Fatalf("logout %d", logout.Code)
	}
	if request(service, "GET", "/auth/me", cookies[0], "").Code != 401 {
		t.Fatal("session survived logout")
	}
	if store.UpdateSession(context.Background(), cookies[0].Value, session.Payload{"accountId": store.account.ID}) != ErrUnauthenticated {
		t.Fatal("revoked session revived")
	}
}
func TestReturnToValidationAndPrompt(t *testing.T) {
	service, _ := newService(t)
	for _, target := range []string{"https://app.example.com.evil/", "https://app.example.com@evil.test/", "https://unrelated.vercel.app/", "http://127.0.0.1:1234/callback?state=" + strings.Repeat("x", 43), "javascript:alert(1)", "https://app.example.com/#fragment"} {
		w := request(service, "GET", "/auth/github?return_to="+url.QueryEscape(target), nil, "")
		if w.Code != 400 {
			t.Errorf("accepted browser target %s", target)
		}
	}
	for _, target := range []string{"https://app.example.com/dashboard", "https://branch-preview.vercel.app/"} {
		if request(service, "GET", "/auth/github?return_to="+url.QueryEscape(target), nil, "").Code != 302 {
			t.Errorf("rejected target %s", target)
		}
	}
	if request(service, "GET", "/auth/github?prompt=login", nil, "").Code != 400 {
		t.Fatal("unsupported prompt accepted")
	}
}
func TestCLIGrammarRejectsEncodedAndExtraParameters(t *testing.T) {
	valid := "http://127.0.0.1:54321/callback?state=" + strings.Repeat("a", 43)
	if _, err := ValidateCLIReturnTo(valid); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{strings.Replace(valid, "127.0.0.1", "localhost", 1), strings.Replace(valid, "54321", "054321", 1), strings.Replace(valid, "/callback", "/%63allback", 1), valid + "&extra=x", valid + "#x", strings.Replace(valid, "state=", "state=%61", 1), strings.Replace(valid, "http:", "https:", 1), "http://127.0.0.1:1/callback?state=short"} {
		if _, err := ValidateCLIReturnTo(target); err == nil {
			t.Errorf("accepted %s", target)
		}
	}
}
func TestSignedStateRejectsTamperExpiryAndWrongKey(t *testing.T) {
	service, _ := newService(t)
	state, err := signState(service.key, "https://app.example.com/", service.now())
	if err != nil {
		t.Fatal(err)
	}
	target, err := verifyState(service.key, state, service.now().Add(600*time.Second))
	if err != nil || target != "https://app.example.com/" {
		t.Fatal("valid state rejected")
	}
	if _, err = verifyState(service.key, state, service.now().Add(601*time.Second)); err == nil {
		t.Fatal("expired state accepted")
	}
	if _, err = verifyState([]byte("other"), state, service.now()); err == nil {
		t.Fatal("wrong key accepted")
	}
	if _, err = verifyState(service.key, state+"x", service.now()); err == nil {
		t.Fatal("tampered state accepted")
	}
	if request(service, "GET", "/auth/github/callback?code=valid&state=bad", nil, "").Code != 302 {
		t.Fatal("missing failure redirect")
	}
}
func TestConfirmRedirectValidationBeforeConsumption(t *testing.T) {
	service, store := newService(t)
	store.account = &Account{ID: "account"}
	store.IssueHandoff(context.Background(), "handoff", "account", service.now().Add(time.Minute))
	for _, path := range []string{"//evil.example", "/\\evil.example", "/%0Aevil"} {
		w := request(service, "GET", "/auth/confirm?token=handoff&redirect="+url.QueryEscape(path), nil, "")
		if path != "/%0Aevil" && w.Code != 400 {
			t.Errorf("accepted redirect %q", path)
		}
	}
}

type sqlFake struct {
	stmt   turso.Statement
	result *turso.Result
}

func (f *sqlFake) Execute(_ context.Context, stmt turso.Statement) (*turso.Result, error) {
	f.stmt = stmt
	return f.result, nil
}
func TestSQLSessionUpdateCannotRecreateMissingRow(t *testing.T) {
	db := &sqlFake{result: &turso.Result{AffectedRowCount: 0}}
	store := SQLStore{DB: db}
	if err := store.UpdateSession(context.Background(), "revoked", session.Payload{"accountId": "a"}); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("missing session did not fail")
	}
	if !strings.HasPrefix(db.stmt.SQL, "UPDATE app_sessions ") || strings.Contains(db.stmt.SQL, "INSERT") {
		t.Fatal("update can recreate session")
	}
}
func TestSQLHandoffIsAtomicDeleteReturning(t *testing.T) {
	db := &sqlFake{result: &turso.Result{Rows: [][]turso.Value{{turso.Text("account")}}}}
	id, err := (SQLStore{DB: db}).ConsumeHandoff(context.Background(), "one-time", time.Unix(123, 0))
	if err != nil || id != "account" {
		t.Fatal("consume failed")
	}
	if db.stmt.SQL != "DELETE FROM oauth_handoff_tokens WHERE token=? AND expires_at>? RETURNING account_id" {
		t.Fatal("handoff consumption not atomic")
	}
}
func TestAuthInfrastructureFailureIs503(t *testing.T) {
	service, store := newService(t)
	store.fail = true
	w := request(service, "GET", "/auth/me", &http.Cookie{Name: "vapor-session", Value: strings.Repeat("a", 64)}, "")
	if w.Code != 503 {
		t.Fatalf("database failure disguised as auth failure: %d", w.Code)
	}
}

func TestCallbackAcceptsSwiftMCPResumeOriginWithoutBroadeningBrowserLogin(t *testing.T) {
	service, _ := newService(t)
	target := "https://api.example.com/auth/mcp-oauth-resume?pending=one-time"
	state, err := signState(service.key, target, service.now())
	if err != nil {
		t.Fatal(err)
	}
	callback := request(service, "GET", "/auth/github/callback?code=valid&state="+url.QueryEscape(state), nil, "")
	if callback.Code != 302 {
		t.Fatalf("MCP OAuth resume callback rejected: %d %s", callback.Code, callback.Body)
	}
	resume, _ := url.Parse(callback.Header().Get("Location"))
	if resume.Query().Get("pending") != "one-time" || resume.Query().Get("auth_token") == "" {
		t.Fatal("MCP pending/handoff lost")
	}
	initiate := request(service, "GET", "/auth/github?return_to="+url.QueryEscape(target), nil, "")
	if initiate.Code != 400 {
		t.Fatal("MCP callback-only origin accepted by browser login")
	}
	hostile, _ := signState(service.key, "https://evil.example/auth/mcp-oauth-resume", service.now())
	if request(service, "GET", "/auth/github/callback?code=valid&state="+url.QueryEscape(hostile), nil, "").Code != 400 {
		t.Fatal("unknown signed callback origin accepted")
	}
}

func TestPreviewCanonicalOriginPrecedesProductionEnvironment(t *testing.T) {
	env := map[string]string{"VERCEL": "1", "VERCEL_ENV": "preview", "VERCEL_BRANCH_URL": "app-git-feature-team.vercel.app", "VERCEL_URL": "app-random-team.vercel.app", "FRONTEND_URL": "https://production.example.com"}
	origins := FrontendOriginsFromEnvironment(func(key string) string { return env[key] })
	if len(origins) != 3 || origins[0] != "https://app-git-feature-team.vercel.app" {
		t.Fatalf("incorrect preview canonical origin %v", origins)
	}
	service, store := newService(t)
	service.config.FrontendOrigins = origins
	store.account = &Account{ID: "account"}
	store.IssueHandoff(context.Background(), "preview", "account", service.now().Add(time.Minute))
	response := request(service, "GET", "/auth/confirm?token=preview&redirect=/dashboard", nil, "")
	if response.Code != 302 || response.Header().Get("Location") != "https://app-git-feature-team.vercel.app/dashboard" {
		t.Fatalf("preview redirected elsewhere: %d %s", response.Code, response.Header().Get("Location"))
	}
}
