package oauth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/CountableNewt/my-context-protocol/services/go/identity"
	"github.com/CountableNewt/my-context-protocol/services/go/internal/sqlitetest"
	"github.com/CountableNewt/my-context-protocol/services/go/migrations"
	"github.com/CountableNewt/my-context-protocol/services/go/session"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
)

var schema = migrations.BaselineSQL + `
INSERT INTO accounts(id,github_id,login) VALUES ('OWNER',1,'owner'),('OTHER',2,'other');
INSERT INTO projects(id,account_id,name,slug,subdomain) VALUES ('PROJECT','OWNER','Project','project','project'),('OTHERPROJECT','OTHER','Other','other','other');
`

func setup(t *testing.T) (*Service, *sqlitetest.DB) {
	t.Helper()
	db := sqlitetest.New(t, schema)
	sessions := identity.SQLStore{DB: db}
	s, e := New(Config{Enabled: true, APIOrigin: "https://app.example", Resolve: func(r *http.Request) (*Project, error) {
		if r.Host == "other.example" {
			return &Project{"OTHERPROJECT", "OTHER", "Other", "https://other.example"}, nil
		}
		return &Project{"PROJECT", "OWNER", "<My Project>", "https://tenant.example"}, nil
	}, ProjectByID: func(context.Context, string) (*Project, error) {
		return &Project{"PROJECT", "OWNER", "Project", "https://tenant.example"}, nil
	}, GitHubLogin: func(target string) (string, error) {
		return "https://github.com/login?return_to=" + url.QueryEscape(target), nil
	}}, db, sessions)
	if e != nil {
		t.Fatal(e)
	}
	return s, db
}
func request(s *Service, method, path, body, cookie string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if method == "POST" {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: "vapor-session", Value: cookie})
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}
func register(t *testing.T, s *Service) string {
	t.Helper()
	r := httptest.NewRequest("POST", "https://tenant.example/register", strings.NewReader(`{"redirect_uris":["http://127.0.0.1:4321/callback"],"grant_types":["authorization_code","refresh_token"]}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 201 {
		t.Fatalf("register %d %s", w.Code, w.Body.String())
	}
	var out map[string]any
	if e := json.Unmarshal(w.Body.Bytes(), &out); e != nil {
		t.Fatal(e)
	}
	return out["client_id"].(string)
}
func pending(t *testing.T, s *Service, client string) string {
	t.Helper()
	digest := sha256.Sum256([]byte("verifier-for-this-test"))
	q := url.Values{"response_type": {"code"}, "client_id": {client}, "redirect_uri": {"http://127.0.0.1:4321/callback"}, "state": {"client-state"}, "code_challenge_method": {"S256"}, "code_challenge": {base64.RawURLEncoding.EncodeToString(digest[:])}, "resource": {"https://tenant.example/mcp"}}
	w := request(s, "GET", "https://tenant.example/authorize?"+q.Encode(), "", "")
	if w.Code != 303 {
		t.Fatalf("authorize %d %s", w.Code, w.Body.String())
	}
	u, _ := url.Parse(w.Header().Get("Location"))
	return u.Query().Get("pending")
}
func TestOAuthCodePKCEAndReplayAgainstSQLite(t *testing.T) {
	s, db := setup(t)
	client := register(t, s)
	id := pending(t, s, client)
	if id == "" {
		t.Fatal("missing pending")
	}
	sessions := identity.SQLStore{DB: db}
	if e := sessions.CreateSession(context.Background(), "session", session.Payload{"accountId": "OWNER"}); e != nil {
		t.Fatal(e)
	}
	w := request(s, "GET", "https://tenant.example/oauth/consent?pending="+id, "", "session")
	if w.Code != 200 {
		t.Fatalf("consent %d %s", w.Code, w.Body.String())
	}
	if w.Header().Get("Referrer-Policy") != "same-origin" {
		t.Fatal("consent must preserve same-origin browser form identity")
	}
	if strings.Contains(w.Body.String(), "<My Project>") {
		t.Fatal("project name was not escaped")
	}
	match := regexp.MustCompile(`name="consent_token" value="([^"]+)"`).FindStringSubmatch(w.Body.String())
	if len(match) != 2 {
		t.Fatal(w.Body.String())
	}
	body := url.Values{"pending": {id}, "decision": {"approve"}, "consent_token": {match[1]}}.Encode()
	// A browser previously serialized its Origin as null under no-referrer.
	// Keep rejecting opaque/foreign origins; fixing the form policy must not
	// weaken the allowlist or consume consent on a rejected request.
	for _, origin := range []string{"null", "https://other.example"} {
		r := httptest.NewRequest("POST", "https://tenant.example/oauth/consent", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", origin)
		blocked := httptest.NewRecorder()
		s.ServeHTTP(blocked, r)
		if blocked.Code != 403 {
			t.Fatal("non-tenant form origin accepted")
		}
	}
	r := httptest.NewRequest("POST", "https://tenant.example/oauth/consent", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", "https://tenant.example")
	approved := httptest.NewRecorder()
	s.ServeHTTP(approved, r)
	if approved.Code != 303 {
		t.Fatalf("approve %d %s", approved.Code, approved.Body.String())
	}
	u, _ := url.Parse(approved.Header().Get("Location"))
	code := u.Query().Get("code")
	if !strings.HasPrefix(code, "mcp_oac_") || u.Query().Get("state") != "client-state" {
		t.Fatal(u.String())
	}
	if replay := request(s, "POST", "https://tenant.example/oauth/consent", body, ""); replay.Code < 400 {
		t.Fatal("consent token replay accepted")
	}
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "client_id": {client}, "redirect_uri": {"http://127.0.0.1:4321/callback"}, "code_verifier": {"wrong"}, "resource": {"https://tenant.example/mcp"}}
	if bad := request(s, "POST", "https://tenant.example/token", form.Encode(), ""); bad.Code != 400 || !strings.Contains(bad.Body.String(), "PKCE") {
		t.Fatal(bad.Body.String())
	}
	form.Set("code_verifier", "verifier-for-this-test")
	ok := request(s, "POST", "https://tenant.example/token", form.Encode(), "")
	if ok.Code != 200 {
		t.Fatalf("token %d %s", ok.Code, ok.Body.String())
	}
	if !strings.Contains(ok.Body.String(), "mcp_oat_") {
		t.Fatal(ok.Body.String())
	}
	if ok.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("token response cacheable")
	}
	if replay := request(s, "POST", "https://tenant.example/token", form.Encode(), ""); replay.Code != 400 {
		t.Fatalf("code replay status %d", replay.Code)
	}
	rows, e := s.query(context.Background(), "SELECT token_hash,scope,project_id FROM mcp_oauth_access_tokens")
	if e != nil || len(rows) != 1 || strings.Contains(rows[0]["token_hash"], "mcp_oat_") || rows[0]["project_id"] != "PROJECT" {
		t.Fatal(rows, e)
	}
}
func TestOAuthClientAndResourceAreTenantBound(t *testing.T) {
	s, _ := setup(t)
	client := register(t, s)
	q := url.Values{"client_id": {client}, "redirect_uri": {"http://127.0.0.1:4321/callback"}, "state": {"nonce"}, "response_type": {"code"}, "code_challenge": {"challenge"}, "code_challenge_method": {"S256"}}
	if w := request(s, "GET", "https://other.example/authorize?"+q.Encode(), "", ""); w.Code != 400 || w.Header().Get("Location") != "" {
		t.Fatal("cross-project client accepted", w.Code)
	}
	q.Set("resource", "https://other.example/mcp")
	w := request(s, "GET", "https://tenant.example/authorize?"+q.Encode(), "", "")
	if w.Code != 303 || !strings.Contains(w.Header().Get("Location"), "invalid_target") {
		t.Fatal(w.Code, w.Header())
	}
	q.Set("redirect_uri", "https://evil.example/callback")
	w = request(s, "GET", "https://tenant.example/authorize?"+q.Encode(), "", "")
	if w.Code != 400 || w.Header().Get("Location") != "" {
		t.Fatal("unregistered callback received redirect")
	}
}
func TestOAuthConsentDoesNotAuthorizeOtherAccount(t *testing.T) {
	s, db := setup(t)
	client := register(t, s)
	id := pending(t, s, client)
	sessions := identity.SQLStore{DB: db}
	if e := sessions.CreateSession(context.Background(), "other", session.Payload{"accountId": "OTHER"}); e != nil {
		t.Fatal(e)
	}
	w := request(s, "GET", "https://tenant.example/oauth/consent?pending="+id, "", "other")
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
}
func TestRegistrationRejectsUnsafeMetadata(t *testing.T) {
	s, _ := setup(t)
	for _, body := range []string{`{"redirect_uris":["http://example.com/cb"]}`, `{"redirect_uris":["https://user:secret@example.com/cb"]}`, `{"redirect_uris":["https://example.com/cb#fragment"]}`, `{"redirect_uris":[]}`, `{"redirect_uris":["https://example.com/cb"],"grant_types":["client_credentials"]}`, `{"redirect_uris":["https://example.com/cb"],"token_endpoint_auth_method":"client_secret_post"}`} {
		r := httptest.NewRequest("POST", "https://tenant.example/register", strings.NewReader(body))
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatal(body, w.Code)
		}
	}
}
func TestDiscoveryAndDisabledOAuth(t *testing.T) {
	s, _ := setup(t)
	w := request(s, "GET", "https://tenant.example/.well-known/oauth-protected-resource/mcp", "", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "https://tenant.example/mcp") {
		t.Fatal(w.Code, w.Body.String())
	}
	s.config.Enabled = false
	if w = request(s, "POST", "https://tenant.example/register", "", ""); w.Code != 404 {
		t.Fatal(w.Code)
	}
}
func TestExpiredPendingAndUnknownConsentFailClosed(t *testing.T) {
	s, _ := setup(t)
	client := register(t, s)
	id := pending(t, s, client)
	s.now = func() time.Time { return time.Now().Add(time.Hour) }
	if w := request(s, "GET", "https://app.example/auth/github/mcp-oauth-start?pending="+id, "", ""); w.Code != 400 {
		t.Fatal(w.Code)
	}
}
func TestResourceComparison(t *testing.T) {
	if !resourceMatches("https://TENANT.example:443/mcp/?x=y", "https://tenant.example", "/mcp") {
		t.Fatal("normalization")
	}
	for _, v := range []string{"https://tenant.example.evil/mcp", "https://user@tenant.example/mcp", "https://tenant.example/mcp#x", "http://tenant.example/mcp"} {
		if resourceMatches(v, "https://tenant.example", "/mcp") {
			t.Fatal(v)
		}
	}
}
func TestCodeConsumptionBatchRollsBackOnTokenInsertFailure(t *testing.T) {
	s, db := setup(t)
	ctx := context.Background()
	_, e := db.Execute(ctx, turso.Statement{SQL: "INSERT INTO mcp_oauth_clients(id,public_client_id,project_id,status,is_confidential,redirect_uris_json,allowed_grants) VALUES ('CLIENT','PUBLIC','PROJECT','active',0,'[]','authorization_code')"})
	if e != nil {
		t.Fatal(e)
	}
	_, e = db.Batch(ctx, []turso.Statement{{SQL: "UPDATE projects SET account_id='OTHER' WHERE id='PROJECT'"}, {SQL: "INSERT INTO nonexistent_table VALUES (1)"}})
	if e == nil {
		t.Fatal("expected transaction failure")
	}
	if !s.owns(ctx, "OWNER", "PROJECT") {
		t.Fatal("failed transaction committed partial mutation")
	}
}

func TestOAuthCORSPreflightRequiresExactAllowedOrigin(t *testing.T) {
	s, _ := setup(t)
	s.config.AllowedOrigins = []string{"https://client.example"}
	for _, origin := range []string{"https://client.example", "https://client.example.evil"} {
		r := httptest.NewRequest("OPTIONS", "https://tenant.example/token", nil)
		r.Header.Set("Origin", origin)
		r.Header.Set("Access-Control-Request-Method", "POST")
		r.Header.Set("Access-Control-Request-Headers", "Authorization, Content-Type")
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if origin == "https://client.example" {
			if w.Code != 204 || w.Header().Get("Access-Control-Allow-Origin") != origin {
				t.Fatal(w.Code, w.Header())
			}
		} else if w.Code != 403 {
			t.Fatal("untrusted preflight accepted")
		}
	}
}
