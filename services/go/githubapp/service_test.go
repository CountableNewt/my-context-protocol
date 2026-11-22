package githubapp

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/CountableNewt/my-context-protocol/services/go/compiler"
	"github.com/CountableNewt/my-context-protocol/services/go/identity"
	"github.com/CountableNewt/my-context-protocol/services/go/internal/sqlitetest"
	"github.com/CountableNewt/my-context-protocol/services/go/migrations"
	"github.com/CountableNewt/my-context-protocol/services/go/security"
	"github.com/CountableNewt/my-context-protocol/services/go/session"
	"github.com/CountableNewt/my-context-protocol/services/go/syncservice"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
	"go.yaml.in/yaml/v3"
)

const seed = `INSERT INTO accounts(id,github_id,login) VALUES ('account',101,'owner'),('other',202,'other');INSERT INTO projects(id,account_id,name,slug) VALUES ('project','account','Project','project');`

func setup(t *testing.T) (*Service, *sqlitetest.DB) {
	t.Helper()
	db := sqlitetest.New(t, migrations.BaselineSQL+MigrationSQL+syncservice.MigrationSQL+seed)
	cipher, err := security.NewTokenCipher(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	token, _ := cipher.Encrypt("oauth")
	_, err = db.Execute(context.Background(), sql("UPDATE accounts SET github_token_encrypted=? WHERE id='account'", turso.Text(token)))
	if err != nil {
		t.Fatal(err)
	}
	store := identity.SQLStore{DB: db}
	if err = store.CreateSession(context.Background(), "session", session.Payload{"accountId": "account"}); err != nil {
		t.Fatal(err)
	}
	if err = store.CreateSession(context.Background(), "other-session", session.Payload{"accountId": "other"}); err != nil {
		t.Fatal(err)
	}
	service, err := New(Config{DB: db, Sessions: store, Cipher: cipher, Client: &Client{ClientID: "client", Key: key(t)}, Slug: "example-app", WebhookOrigin: "https://app.example", FrontendOrigins: []string{"https://app.example"}})
	if err != nil {
		t.Fatal(err)
	}
	return service, db
}
func request(service *Service, target, cookie string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", target, nil)
	r.AddCookie(&http.Cookie{Name: "vapor-session", Value: cookie})
	w := httptest.NewRecorder()
	service.ServeHTTP(w, r)
	return w
}
func intent(t *testing.T, s *Service) string {
	t.Helper()
	w := request(s, "https://app.example/auth/github/app/install?project_id=project&owner=owner&repo=repo", "session")
	if w.Code != 302 {
		t.Fatalf("install redirect %d %s", w.Code, w.Body)
	}
	u, _ := url.Parse(w.Header().Get("Location"))
	if u.Host != "github.com" || u.Path != "/apps/example-app/installations/new" {
		t.Fatal(u)
	}
	return u.Query().Get("state")
}
func TestInstallationIntentSessionBindingAndOneTimeCallback(t *testing.T) {
	s, db := setup(t)
	calls := 0
	s.config.Client.HTTP = &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Path != "/app/installations/7" {
			t.Fatal(r.URL)
		}
		return reply(200, `{"id":7,"account":{"id":101,"type":"User"}}`), nil
	})}
	state := intent(t, s)
	target := "https://app.example/auth/github/app/callback?installation_id=7&state=" + state
	w := request(s, target, "other-session")
	if w.Code != 400 || calls != 0 {
		t.Fatalf("wrong user callback %d calls %d", w.Code, calls)
	}
	w = request(s, target, "session")
	if w.Code != 302 || calls != 1 {
		t.Fatalf("valid callback %d %s", w.Code, w.Body)
	}
	u, _ := url.Parse(w.Header().Get("Location"))
	if u.Host != "app.example" || u.Query().Get("github_app_installed") != "1" || u.Query().Get("resume_repo") != "repo" {
		t.Fatal(u)
	}
	r, err := db.Execute(context.Background(), sql("SELECT github_app_installation_id FROM accounts WHERE id='account'"))
	if err != nil || col(r.Rows[0], 0) != "7" {
		t.Fatalf("installation not persisted %+v %v", r, err)
	}
	w = request(s, target, "session")
	if w.Code != 400 || calls != 1 {
		t.Fatal("replayed callback was accepted")
	}
}
func TestInstallationRejectsForeignOwnerAndExpiredState(t *testing.T) {
	s, db := setup(t)
	s.config.Client.HTTP = &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
		return reply(200, `{"id":7,"account":{"id":202,"type":"User"}}`), nil
	})}
	state := intent(t, s)
	w := request(s, "https://app.example/auth/github/app/callback?installation_id=7&state="+state, "session")
	if w.Code != 403 {
		t.Fatalf("foreign installation accepted %d", w.Code)
	}
	_, err := db.Execute(context.Background(), sql("UPDATE github_app_install_intents SET expires_at=0 WHERE id=?", turso.Text(state)))
	if err != nil {
		t.Fatal(err)
	}
	w = request(s, "https://app.example/auth/github/app/callback?installation_id=7&state="+state, "session")
	if w.Code != 400 {
		t.Fatal(w.Code)
	}
	w = request(s, "https://app.example/auth/github/app/install?project_id=project&return_to=https%3A%2F%2Fevil.example%2F", "session")
	if w.Code != 400 {
		t.Fatal("open redirect accepted")
	}
}
func TestConnectRepositoryRequiresAppAndScopesOwnership(t *testing.T) {
	s, db := setup(t)
	input := map[string]json.RawMessage{"owner": json.RawMessage(`"owner"`), "repo": json.RawMessage(`"repo"`), "branch": json.RawMessage(`"feature/nested"`)}
	_, err := s.ConnectRepository(context.Background(), "other", "project", input)
	public, ok := err.(*PublicError)
	if !ok || public.Status != 404 {
		t.Fatal(err)
	}
	_, err = s.ConnectRepository(context.Background(), "account", "project", input)
	public, ok = err.(*PublicError)
	if !ok || public.Status != 409 || public.Payload.(map[string]string)["reason"] != "github_app_install_required" {
		t.Fatal(err)
	}
	// OAuth-only fallback still creates a real signed repository webhook.
	s.config.Slug = ""
	s.config.Client.Key = nil
	calls := 0
	s.config.Client.HTTP = &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Header.Get("Authorization") != "Bearer oauth" {
			t.Fatal("credential mismatch")
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /repos/owner/repo":
			return reply(200, `{"name":"repo","default_branch":"main"}`), nil
		case "POST /repos/owner/repo/hooks":
			return reply(201, `{"id":88}`), nil
		default:
			t.Fatalf("unexpected API %s %s", r.Method, r.URL)
			return nil, nil
		}
	})}
	result, err := s.ConnectRepository(context.Background(), "account", "project", input)
	if err != nil || result["webhook_id"] != "88" || calls != 2 {
		t.Fatalf("%+v %v calls%d", result, err, calls)
	}
	r, err := db.Execute(context.Background(), sql("SELECT default_branch,webhook_id,length(webhook_secret) FROM repo_connections WHERE project_id='project'"))
	if err != nil || col(r.Rows[0], 0) != "feature/nested" || col(r.Rows[0], 1) != "88" || col(r.Rows[0], 2) != "64" {
		t.Fatalf("%+v %v", r, err)
	}
}
func TestMergeSidecarPreservesUnrelatedFieldsAndLimits(t *testing.T) {
	skill, _ := compiler.Parse("a/SKILL.md", []byte("---\nname: a\ndescription: Useful\nuse_when: writing\n---\nbody"))
	c, _ := compiler.Compile(skill, "owner/repo", "sha", nil)
	prior := []byte("version: 1\ncustom_root: retain\nskills:\n  unrelated:\n    metadata:\n      priority: 10\n  a:\n    vendor: retain\n    metadata:\n      custom: retain\n")
	merged, err := MergeSidecar(prior, c.Canonical, "resource")
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	yaml.Unmarshal(merged, &root)
	skills := root["skills"].(map[string]any)
	a := skills["a"].(map[string]any)
	if root["custom_root"] != "retain" || skills["unrelated"] == nil || a["vendor"] != "retain" || a["metadata"].(map[string]any)["custom"] != "retain" || a["base_checksum"] != skill.Hash {
		t.Fatal(string(merged))
	}
	if _, err = MergeSidecar([]byte("version: 2\nskills: {}"), c.Canonical, "resource"); err == nil {
		t.Fatal("unsupported version overwritten")
	}
	if _, err = MergeSidecar([]byte(strings.Repeat("x", (256<<10)+1)), c.Canonical, "resource"); err == nil {
		t.Fatal("oversized sidecar accepted")
	}
}
func TestWritebackOnlyChangesPolicyAndCreatesDraftPR(t *testing.T) {
	s, db := setup(t)
	ctx := context.Background()
	_, err := db.Execute(ctx, sql("INSERT INTO repo_connections(id,project_id,provider,repo_owner,repo_name,default_branch,auth_type) VALUES ('connection','project','github','owner','repo','main','oauth')"))
	if err != nil {
		t.Fatal(err)
	}
	store := syncservice.SQLStore{DB: db}
	connection, _ := store.Connection(ctx, "project")
	job, _, err := store.Claim(ctx, connection, "delivery", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	skill, _ := compiler.Parse("a/SKILL.md", []byte("---\nname: a\ndescription: Useful\nuse_when: writing\n---\nbody"))
	compiled, _ := compiler.Compile(skill, "owner/repo", "sha", nil)
	if err = store.Publish(ctx, connection, job, "sha", []compiler.Compiled{compiled}, compiler.Report{Valid: true}, time.Now()); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Execute(ctx, sql("SELECT id FROM compiled_skills WHERE release_id=?", turso.Text(job.ReleaseID)))
	if err != nil {
		t.Fatal(err)
	}
	compiledID := col(rows.Rows[0], 0)
	calls := 0
	s.config.Client.HTTP = &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
		calls++
		switch r.Method + " " + r.URL.Path {
		case "GET /repos/owner/repo/git/ref/heads/main":
			return reply(200, `{"object":{"sha":"base-sha"}}`), nil
		case "GET /repos/owner/repo/contents/.mycontext/skills.yaml":
			return reply(404, `{}`), nil
		case "POST /repos/owner/repo/git/refs":
			return reply(201, `{}`), nil
		case "PUT /repos/owner/repo/contents/.mycontext/skills.yaml":
			var payload map[string]any
			json.NewDecoder(r.Body).Decode(&payload)
			data, err := base64.StdEncoding.DecodeString(payload["content"].(string))
			if err != nil || !strings.Contains(string(data), "base_checksum: "+skill.Hash) || !strings.HasPrefix(payload["branch"].(string), "mcp/skill-metadata-a-") {
				t.Fatal(payload)
			}
			return reply(201, `{}`), nil
		case "POST /repos/owner/repo/pulls":
			var payload map[string]any
			json.NewDecoder(r.Body).Decode(&payload)
			if payload["draft"] != true || payload["base"] != "main" {
				t.Fatal(payload)
			}
			return reply(201, `{"html_url":"https://github.com/owner/repo/pull/42"}`), nil
		default:
			t.Fatalf("unexpected writeback API %s %s", r.Method, r.URL)
			return nil, nil
		}
	})}
	result, err := s.Writeback(ctx, "account", "project", job.ReleaseID, compiledID)
	if err != nil || result["source_path"] != sidecarPath || calls != 5 {
		t.Fatalf("%+v %v calls%d", result, err, calls)
	}
	_, err = s.Writeback(ctx, "other", "project", job.ReleaseID, compiledID)
	if err == nil || calls != 5 {
		t.Fatal("foreign user initiated writeback")
	}
}

func TestSignedAppLifecycleClearsInstallationReferences(t *testing.T) {
	s, db := setup(t)
	ctx := context.Background()
	s.config.AppWebhookSecret = "app-secret"
	_, err := db.Execute(ctx, sql("UPDATE accounts SET github_app_installation_id=7 WHERE id='account'"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Execute(ctx, sql("INSERT INTO repo_connections(id,project_id,provider,repo_owner,repo_name,default_branch,auth_type,github_installation_id,webhook_id,webhook_secret) VALUES ('connection','project','github','owner','repo','main','oauth',7,'88','repo-secret')"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ event, body string }{{"installation", `{"action":"deleted","installation":{"id":7}}`}, {"github_app_authorization", `{"action":"revoked","account":{"id":101},"sender":{"id":999}}`}} {
		_, err = db.Execute(ctx, sql("UPDATE accounts SET github_app_installation_id=7 WHERE id='account'"))
		if err != nil {
			t.Fatal(err)
		}
		request := func(signature string) *httptest.ResponseRecorder {
			r := httptest.NewRequest("POST", "/webhooks/github-app", strings.NewReader(tc.body))
			r.Header.Set("X-GitHub-Event", tc.event)
			r.Header.Set("X-Hub-Signature-256", signature)
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			return w
		}
		if w := request("sha256=00"); w.Code != 401 {
			t.Fatal(w.Code)
		}
		if w := request(appSign("app-secret", []byte(tc.body))); w.Code != 200 {
			t.Fatalf("%s %d %s", tc.event, w.Code, w.Body)
		}
		r, err := db.Execute(ctx, sql("SELECT github_app_installation_id FROM accounts WHERE id='account'"))
		if err != nil || col(r.Rows[0], 0) != "" {
			t.Fatalf("lifecycle cleanup %+v %v", r, err)
		}
	}
}
func TestSignedAppPushDelegationPreservesRawBody(t *testing.T) {
	s, _ := setup(t)
	s.config.AppWebhookSecret = "secret"
	body := `{ "ref":"refs/heads/main" }`
	calls := 0
	s.config.PushWebhook = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		raw, _ := io.ReadAll(r.Body)
		if string(raw) != body || r.URL.Path != "/webhooks/github" {
			t.Fatal("delegation changed signed payload")
		}
		w.WriteHeader(202)
	})
	r := httptest.NewRequest("POST", "/webhooks/github-app", strings.NewReader(body))
	r.Header.Set("X-GitHub-Event", "push")
	r.Header.Set("X-Hub-Signature-256", appSign("secret", []byte(body)))
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 202 || calls != 1 {
		t.Fatal(w.Code, calls)
	}
}
func appSign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func TestInstallationOAuthStoresOnlyEncryptedMatchingUserToken(t *testing.T) {
	s, db := setup(t)
	s.config.Client.ClientSecret = "client-secret"
	s.config.Client.SetupCallback = "https://app.example/auth/github/app/callback"
	s.config.Client.HTTP = &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Host + r.URL.Path {
		case "api.github.com/app/installations/7":
			return reply(200, `{"id":7,"account":{"id":101,"type":"User"}}`), nil
		case "github.com/login/oauth/access_token":
			return reply(200, `{"access_token":"new-user-token"}`), nil
		case "api.github.com/user":
			return reply(200, `{"id":101}`), nil
		default:
			t.Fatalf("unexpected endpoint %s", r.URL)
			return nil, nil
		}
	})}
	state := intent(t, s)
	w := request(s, "https://app.example/auth/github/app/callback?installation_id=7&state="+state+"&code=code", "session")
	if w.Code != 302 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	r, err := db.Execute(context.Background(), sql("SELECT github_token_encrypted FROM accounts WHERE id='account'"))
	if err != nil {
		t.Fatal(err)
	}
	encrypted := col(r.Rows[0], 0)
	token, err := s.config.Cipher.Decrypt(encrypted)
	if err != nil || token != "new-user-token" || strings.Contains(encrypted, "new-user-token") {
		t.Fatal("OAuth token not encrypted correctly")
	}
}
func TestInstallation404ClearsStaleReferences(t *testing.T) {
	s, db := setup(t)
	_, err := db.Execute(context.Background(), sql("UPDATE accounts SET github_app_installation_id=7 WHERE id='account'"))
	if err != nil {
		t.Fatal(err)
	}
	s.config.Client.HTTP = &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) { return reply(404, `{"message":"Not Found"}`), nil })}
	if _, err = s.InstallationToken(context.Background(), "7", "oauth"); err == nil {
		t.Fatal("404 installation exchanged successfully")
	}
	r, err := db.Execute(context.Background(), sql("SELECT github_app_installation_id FROM accounts WHERE id='account'"))
	if err != nil || col(r.Rows[0], 0) != "" {
		t.Fatalf("stale installation persisted %+v %v", r, err)
	}
}

func TestRelativeInstallStaysOnActualAllowlistedPreviewHost(t *testing.T) {
	s, db := setup(t)
	s.config.FrontendOrigins = []string{"https://branch-preview.example", "https://deployment-immutable.example"}
	public := s.InstallRequired("project", "owner", "repo").(*PublicError)
	installer := public.Payload.(map[string]string)["install_url"]
	u, err := url.Parse(installer)
	if err != nil || u.IsAbs() || u.Host != "" || u.Path != "/api/auth/github/app/install" || u.Query().Get("return_to") != "/projects/project" {
		t.Fatalf("installer must remain origin-relative %q %v", installer, err)
	}
	// The browser is visiting the immutable deployment URL; the first configured
	// origin is only a branch alias and must not capture this installation return.
	target := "https://deployment-immutable.example/auth/github/app/install?" + u.RawQuery
	r := httptest.NewRequest("GET", target, nil)
	r.AddCookie(&http.Cookie{Name: "vapor-session", Value: "session"})
	r.Header.Set("X-Forwarded-Host", "attacker.example")
	r.Header.Set("X-Forwarded-Proto", "http")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 302 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	location, _ := url.Parse(w.Header().Get("Location"))
	state := location.Query().Get("state")
	rows, err := db.Execute(context.Background(), sql("SELECT return_to FROM github_app_install_intents WHERE id=?", turso.Text(state)))
	if err != nil || len(rows.Rows) != 1 || col(rows.Rows[0], 0) != "https://deployment-immutable.example/projects/project" {
		t.Fatalf("preview intent escaped browser origin %+v %v", rows, err)
	}
}
func TestRelativeInstallRejectsSpoofedHostsAndOtherProjectReturns(t *testing.T) {
	s, _ := setup(t)
	for _, tc := range []struct{ host, returnTo string }{{"attacker.example", "/projects/project"}, {"app.example", "//attacker.example/projects/project"}, {"app.example", "/projects/other"}, {"app.example", "/projects/project/extra"}, {"app.example", "/projects/%2fproject"}, {"app.example", "/projects/project#fragment"}, {"app.example", "https://attacker.example/projects/project"}, {"app.example", "/projects/project\\extra"}} {
		r := httptest.NewRequest("GET", "https://"+tc.host+"/auth/github/app/install?project_id=project&return_to="+url.QueryEscape(tc.returnTo), nil)
		r.AddCookie(&http.Cookie{Name: "vapor-session", Value: "session"})
		r.Header.Set("X-Forwarded-Host", "app.example")
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatalf("accepted host=%q return=%q status=%d", tc.host, tc.returnTo, w.Code)
		}
	}
}

func TestBoundInstallerUsesAllowlistedBFFBrowserOrigin(t *testing.T) {
	s, db := setup(t)
	s.config.FrontendOrigins = []string{"https://branch-preview.example", "https://deployment-immutable.example"}
	r := httptest.NewRequest("GET", "https://identity.internal/auth/github/app/install?project_id=project&return_to=%2Fprojects%2Fproject", nil)
	r.AddCookie(&http.Cookie{Name: "vapor-session", Value: "session"})
	r.Header.Set("X-Context-Frontend-Origin", "https://deployment-immutable.example")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 302 {
		t.Fatalf("binding host rejected trusted BFF origin %d %s", w.Code, w.Body)
	}
	location, _ := url.Parse(w.Header().Get("Location"))
	rows, err := db.Execute(context.Background(), sql("SELECT return_to FROM github_app_install_intents WHERE id=?", turso.Text(location.Query().Get("state"))))
	if err != nil || len(rows.Rows) != 1 || col(rows.Rows[0], 0) != "https://deployment-immutable.example/projects/project" {
		t.Fatalf("wrong origin persisted %+v %v", rows, err)
	}
}
func TestBFFOriginMetadataCannotAuthorizeOrIntroduceUntrustedReturns(t *testing.T) {
	s, _ := setup(t)
	for _, origin := range []string{"https://attacker.example", "https://app.example.attacker.example", "https://app.example/path", "https://app.example?query=1", "https://user@app.example", "https://app.example#fragment", "https://app.example,https://attacker.example", "http://app.example"} {
		r := httptest.NewRequest("GET", "https://identity.internal/auth/github/app/install?project_id=project&return_to=%2Fprojects%2Fproject", nil)
		r.AddCookie(&http.Cookie{Name: "vapor-session", Value: "session"})
		r.Header.Set("X-Context-Frontend-Origin", origin)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatalf("untrusted origin %q status%d", origin, w.Code)
		}
	}
	for _, cookie := range []string{"", "other-session"} {
		r := httptest.NewRequest("GET", "https://identity.internal/auth/github/app/install?project_id=project", nil)
		if cookie != "" {
			r.AddCookie(&http.Cookie{Name: "vapor-session", Value: cookie})
		}
		r.Header.Set("X-Context-Frontend-Origin", "https://app.example")
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != 401 && w.Code != 404 {
			t.Fatalf("origin header granted access cookie=%q status%d", cookie, w.Code)
		}
	}
}

func TestSharedOAuthCallbackResolvesCodeOnlyInstallationSecurely(t *testing.T) {
	s, db := setup(t)
	s.config.Client.ClientSecret = "client-secret"
	s.config.Client.SetupCallback = "https://app.example/auth/github/callback"
	calls := []string{}
	s.config.Client.HTTP = &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
		calls = append(calls, r.URL.Host+r.URL.Path)
		switch r.URL.Host + r.URL.Path {
		case "github.com/login/oauth/access_token":
			var input map[string]any
			json.NewDecoder(r.Body).Decode(&input)
			if input["redirect_uri"] != "https://app.example/auth/github/callback" {
				t.Fatal("install exchange used different callback", input)
			}
			return reply(200, `{"access_token":"installed-user-token"}`), nil
		case "api.github.com/user":
			return reply(200, `{"id":101}`), nil
		case "api.github.com/user/installations":
			if r.Header.Get("Authorization") != "Bearer installed-user-token" {
				t.Fatal("not using verified App user token")
			}
			return reply(200, `{"total_count":1,"installations":[{"id":7,"account":{"id":101,"type":"User"}}]}`), nil
		case "api.github.com/app/installations/7":
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ey") {
				t.Fatal("installation not independently JWT-verified")
			}
			return reply(200, `{"id":7,"account":{"id":101,"type":"User"}}`), nil
		default:
			t.Fatalf("unexpected endpoint %s", r.URL)
			return nil, nil
		}
	})}
	state := intent(t, s)
	target := "https://app.example/auth/github/callback?code=installation-code&state=" + state
	w := request(s, target, "session")
	if w.Code != 302 || len(calls) != 4 {
		t.Fatalf("code-only install %d %s calls%v", w.Code, w.Body, calls)
	}
	r, err := db.Execute(context.Background(), sql("SELECT github_app_installation_id,github_token_encrypted FROM accounts WHERE id='account'"))
	if err != nil || col(r.Rows[0], 0) != "7" {
		t.Fatalf("installation not saved %+v %v", r, err)
	}
	token, err := s.config.Cipher.Decrypt(col(r.Rows[0], 1))
	if err != nil || token != "installed-user-token" {
		t.Fatal("OAuth token not stored encrypted")
	}
	w = request(s, target, "session")
	if w.Code != 400 || len(calls) != 4 {
		t.Fatalf("callback replay accepted %d calls%v", w.Code, calls)
	}
}
func TestSharedInstallCallbackNeverUsesAnotherUserOrUnownedIntent(t *testing.T) {
	s, db := setup(t)
	s.config.Client.ClientSecret = "client-secret"
	s.config.Client.SetupCallback = "https://app.example/auth/github/callback"
	calls := 0
	s.config.Client.HTTP = &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Host == "github.com" {
			return reply(200, `{"access_token":"foreign-user-token"}`), nil
		}
		if r.URL.Path == "/user" {
			return reply(200, `{"id":202}`), nil
		}
		t.Fatal("unverified user reached installation API")
		return nil, nil
	})}
	state := intent(t, s)
	target := "https://app.example/auth/github/callback?code=code&state=" + state
	if w := request(s, target, "other-session"); w.Code != 400 || calls != 0 {
		t.Fatal("wrong session exchanged code", w.Code, calls)
	}
	if w := request(s, target, "session"); w.Code != 403 || calls != 2 {
		t.Fatal("foreign GitHub OAuth user attached", w.Code, calls)
	}
	r, err := db.Execute(context.Background(), sql("SELECT github_app_installation_id FROM accounts WHERE id='account'"))
	if err != nil || col(r.Rows[0], 0) != "" {
		t.Fatal("foreign installation persisted", r, err)
	}
	r, err = db.Execute(context.Background(), sql("SELECT id FROM github_app_install_intents WHERE id=?", turso.Text(state)))
	if err != nil || len(r.Rows) != 1 {
		t.Fatal("failed authorization consumed intent")
	}
}

func TestInstallationOAuthCallbackCrossesAliasWithoutLosingIntentBinding(t *testing.T) {
	for _, withID := range []bool{false, true} {
		t.Run(fmt.Sprint("installation_id=", withID), func(t *testing.T) {
			s, db := setup(t)
			s.config.FrontendOrigins = []string{"https://app.example", "https://immutable-preview.example"}
			s.config.Client.ClientSecret = "client-secret"
			s.config.Client.SetupCallback = "https://app.example/auth/github/callback"
			calls := 0
			s.config.Client.HTTP = &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
				calls++
				switch r.URL.Host + r.URL.Path {
				case "github.com/login/oauth/access_token":
					var input map[string]any
					json.NewDecoder(r.Body).Decode(&input)
					if input["redirect_uri"] != "https://app.example/auth/github/callback" {
						t.Fatal("wrong callback exchange")
					}
					return reply(200, `{"access_token":"verified-install-user"}`), nil
				case "api.github.com/user":
					return reply(200, `{"id":101}`), nil
				case "api.github.com/user/installations":
					return reply(200, `{"installations":[{"id":7,"account":{"id":101,"type":"User"}}]}`), nil
				case "api.github.com/app/installations/7":
					return reply(200, `{"id":7,"account":{"id":101,"type":"User"}}`), nil
				default:
					t.Fatalf("unexpected API %s", r.URL)
					return nil, nil
				}
			})}
			begin := httptest.NewRequest("GET", "https://identity.internal/auth/github/app/install?project_id=project&return_to=%2Fprojects%2Fproject", nil)
			begin.Header.Set("X-Context-Frontend-Origin", "https://immutable-preview.example")
			begin.AddCookie(&http.Cookie{Name: "vapor-session", Value: "session"})
			started := httptest.NewRecorder()
			s.ServeHTTP(started, begin)
			if started.Code != 302 {
				t.Fatalf("begin %d %s", started.Code, started.Body)
			}
			location, _ := url.Parse(started.Header().Get("Location"))
			state := location.Query().Get("state")
			callback := "https://app.example/auth/github/callback?code=code&state=" + state
			if withID {
				callback += "&installation_id=7"
			}
			// Host-only cookie on immutable-preview.example is absent on app.example.
			w := httptest.NewRecorder()
			s.ServeHTTP(w, httptest.NewRequest("GET", callback, nil))
			if w.Code != 302 {
				t.Fatalf("callback %d %s", w.Code, w.Body)
			}
			target, _ := url.Parse(w.Header().Get("Location"))
			if target.Scheme != "https" || target.Host != "immutable-preview.example" || target.Path != "/projects/project" || target.Query().Get("github_app_installed") != "1" || w.Header().Get("Set-Cookie") != "" {
				t.Fatalf("unexpected callback return %s", target)
			}
			rows, err := db.Execute(context.Background(), sql("SELECT github_app_installation_id,github_token_encrypted FROM accounts WHERE id='account'"))
			if err != nil || col(rows.Rows[0], 0) != "7" {
				t.Fatalf("attachment %+v %v", rows, err)
			}
			token, err := s.config.Cipher.Decrypt(col(rows.Rows[0], 1))
			if err != nil || token != "verified-install-user" {
				t.Fatal("unverified token persisted")
			}
			before := calls
			replay := httptest.NewRecorder()
			s.ServeHTTP(replay, httptest.NewRequest("GET", callback, nil))
			if replay.Code != 400 || calls != before {
				t.Fatalf("callback replay accepted %d calls%d", replay.Code, calls)
			}
		})
	}
}
func TestCookieFreeInstallStillRejectsMissingCodeWrongIdentityAndPresentWrongSession(t *testing.T) {
	for _, mode := range []string{"missing-code", "wrong-user", "wrong-session", "invalid-session", "unknown-intent", "expired-intent", "wrong-installation-owner"} {
		t.Run(mode, func(t *testing.T) {
			s, db := setup(t)
			s.config.Client.ClientSecret = "client-secret"
			s.config.Client.SetupCallback = "https://app.example/auth/github/callback"
			calls := 0
			s.config.Client.HTTP = &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
				calls++
				switch r.URL.Host + r.URL.Path {
				case "github.com/login/oauth/access_token":
					return reply(200, `{"access_token":"candidate-user"}`), nil
				case "api.github.com/user":
					if mode == "wrong-user" {
						return reply(200, `{"id":202}`), nil
					}
					return reply(200, `{"id":101}`), nil
				case "api.github.com/app/installations/7":
					return reply(200, `{"id":7,"account":{"id":202,"type":"User"}}`), nil
				default:
					t.Fatalf("unexpected API %s", r.URL)
					return nil, nil
				}
			})}
			state := intent(t, s)
			if mode == "unknown-intent" {
				state = "11111111-1111-4111-8111-111111111111"
			}
			if mode == "expired-intent" {
				if _, err := db.Execute(context.Background(), sql("UPDATE github_app_install_intents SET expires_at=0 WHERE id=?", turso.Text(state))); err != nil {
					t.Fatal(err)
				}
			}
			target := "https://app.example/auth/github/callback?state=" + state + "&installation_id=7"
			if mode != "missing-code" {
				target += "&code=code"
			}
			r := httptest.NewRequest("GET", target, nil)
			if mode == "wrong-session" {
				r.AddCookie(&http.Cookie{Name: "vapor-session", Value: "other-session"})
			}
			if mode == "invalid-session" {
				r.AddCookie(&http.Cookie{Name: "vapor-session", Value: "missing-session"})
			}
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			if w.Code != 400 && w.Code != 401 && w.Code != 403 {
				t.Fatalf("unsafe callback mode%s status%d", mode, w.Code)
			}
			if mode != "wrong-user" && mode != "wrong-installation-owner" && calls != 0 {
				t.Fatalf("invalid state/session exchanged code mode%s calls%d", mode, calls)
			}
			rows, err := db.Execute(context.Background(), sql("SELECT github_app_installation_id FROM accounts WHERE id='account'"))
			if err != nil || col(rows.Rows[0], 0) != "" {
				t.Fatal("rejected callback mutated account")
			}
		})
	}
}

func TestInstalledRepositoryUsesExistingAppWebhookWithoutHookAdministration(t *testing.T) {
	s, db := setup(t)
	s.config.AppWebhookSecret = "existing-app-secret"
	if _, err := db.Execute(context.Background(), sql("UPDATE accounts SET github_app_installation_id=7 WHERE id='account'")); err != nil {
		t.Fatal(err)
	}
	calls := map[string]int{}
	s.config.Client.HTTP = &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
		route := r.Method + " " + r.URL.Path
		calls[route]++
		switch route {
		case "POST /app/installations/7/access_tokens":
			return reply(201, `{"token":"installation-token","expires_at":"2099-01-01T00:00:00Z"}`), nil
		case "GET /repos/owner/repo":
			if r.Header.Get("Authorization") != "Bearer installation-token" {
				t.Fatal("repository access did not use scoped installation token")
			}
			return reply(200, `{"name":"repo","default_branch":"main"}`), nil
		case "GET /app":
			return reply(200, `{"events":["push","installation"]}`), nil
		case "GET /app/hook/config":
			return reply(200, `{"url":"https://app.example/webhooks/github","content_type":"json","insecure_ssl":"0"}`), nil
		default:
			t.Fatalf("unexpected GitHub request: %s", route)
			return nil, nil
		}
	})}
	input := map[string]json.RawMessage{"owner": json.RawMessage(`"owner"`), "repo": json.RawMessage(`"repo"`), "branch": json.RawMessage(`"feature/nested"`)}
	result, err := s.ConnectRepository(context.Background(), "account", "project", input)
	if err != nil || result["webhook_id"] != nil || len(calls) != 4 {
		t.Fatal("App connection should reuse registered webhook", err)
	}
	rows, err := db.Execute(context.Background(), sql("SELECT github_installation_id,webhook_id,webhook_secret,default_branch FROM repo_connections WHERE project_id='project'"))
	if err != nil || len(rows.Rows) != 1 || col(rows.Rows[0], 0) != "7" || rows.Rows[0][1].Type != "null" || rows.Rows[0][2].Type != "null" || col(rows.Rows[0], 3) != "feature/nested" {
		t.Fatal("App connection did not persist scoped installation without repository hook", err)
	}
	// Missing local verification secret fails closed before saving a connection.
	s.config.AppWebhookSecret = ""
	if _, err = s.ConnectRepository(context.Background(), "account", "project", input); err == nil {
		t.Fatal("missing App signature secret accepted")
	}
}

func TestAppWebhookRegistrationMustAlreadyHavePushAndVerifiedLocalTarget(t *testing.T) {
	s, _ := setup(t)
	for _, tc := range []struct {
		name, events, target, contentType, tls string
		valid                                  bool
	}{
		{"legacy route", "push", "https://app.example/webhooks/github", "json", "0", true},
		{"App route", "push", "https://app.example/webhooks/github-app", "json", "0", true},
		{"missing push", "issues", "https://app.example/webhooks/github", "json", "0", false},
		{"foreign target", "push", "https://other.example/webhooks/github", "json", "0", false},
		{"unhandled path", "push", "https://app.example/unhandled", "json", "0", false},
		{"insecure TLS", "push", "https://app.example/webhooks/github", "json", "1", false},
		{"form encoding", "push", "https://app.example/webhooks/github", "form", "0", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s.config.Client.HTTP = &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
				if r.Method != "GET" || !strings.Contains(r.Header.Get("Authorization"), ".") {
					t.Fatal("registration must be checked read-only with App JWT")
				}
				if r.URL.Path == "/app" {
					return reply(200, fmt.Sprintf(`{"events":[%q]}`, tc.events)), nil
				}
				if r.URL.Path == "/app/hook/config" {
					return reply(200, fmt.Sprintf(`{"url":%q,"content_type":%q,"insecure_ssl":%q}`, tc.target, tc.contentType, tc.tls)), nil
				}
				t.Fatal("unexpected registration request")
				return nil, nil
			})}
			err := s.config.Client.VerifyAppWebhook(context.Background(), "https://app.example")
			if (err == nil) != tc.valid {
				t.Fatal("incorrect App webhook registration acceptance", err)
			}
		})
	}
}

func TestConnectRepositoryCannotReplaceOtherProvider(t *testing.T) {
	service, db := setup(t)
	_, err := db.Execute(context.Background(), sql("INSERT INTO repo_connections(id,project_id,provider,repo_owner,repo_name,default_branch,auth_type) VALUES ('connection','project','tangled','owner','repo','main','oauth')"))
	if err != nil {
		t.Fatal(err)
	}
	service.config.Client.HTTP = &http.Client{Transport: transport(func(*http.Request) (*http.Response, error) {
		t.Fatal("contacted GitHub for native repository replacement")
		return nil, fmt.Errorf("unexpected request")
	})}
	_, err = service.ConnectRepository(context.Background(), "account", "project", map[string]json.RawMessage{"owner": json.RawMessage(`"owner"`), "repo": json.RawMessage(`"repo"`)})
	public, ok := err.(*PublicError)
	if !ok || public.Status != 409 {
		t.Fatalf("native repository replacement accepted: %v", err)
	}
	result, err := db.Execute(context.Background(), sql("SELECT provider FROM repo_connections WHERE id='connection'"))
	if err != nil || col(result.Rows[0], 0) != "tangled" {
		t.Fatalf("provider changed: %+v %v", result, err)
	}
}
