package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bluesky-social/indigo/atproto/atcrypto"
	"github.com/bluesky-social/indigo/atproto/auth/oauth"
)

func TestConfigurationCompatibilityAndPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "config.json")
	c, e := LoadConfiguration(path)
	if e != nil {
		t.Fatal(e)
	}
	c.Profiles["default"] = Profile{Session: "secret"}
	if e = c.Save(path); e != nil {
		t.Fatal(e)
	}
	loaded, e := LoadConfiguration(path)
	if e != nil || loaded.Profiles["default"].Session != "secret" {
		t.Fatalf("%+v %v", loaded, e)
	}
	for p, want := range map[string]os.FileMode{path: 0600, filepath.Dir(path): 0700} {
		info, e := os.Stat(p)
		if e != nil || info.Mode().Perm() != want {
			t.Fatalf("permissions %s: %v %v", p, info, e)
		}
	}
	for _, source := range []string{`{}`, `{"profiles":null}`} {
		os.WriteFile(path, []byte(source), 0600)
		c, e = LoadConfiguration(path)
		if e != nil || c.Profiles == nil {
			t.Fatalf("%+v %v", c, e)
		}
	}
}
func TestOriginAndPathValidation(t *testing.T) {
	for _, source := range []string{"https://example.com/", "http://127.0.0.1:8080", "http://[::1]:9000"} {
		if _, e := ValidateOrigin(source); e != nil {
			t.Fatal(e)
		}
	}
	for _, source := range []string{"http://example.com", "https://u:p@example.com", "https://example.com/path", "https://example.com?", "https://example.com#", "https://example.com\r\n"} {
		if _, e := ValidateOrigin(source); e == nil {
			t.Fatalf("accepted %q", source)
		}
	}
	for _, source := range []string{"https://example.com/%61pi", "https://example.com/api?x=1", "https://example.com/api#"} {
		if _, e := ValidateAPIBaseURL(source); e == nil {
			t.Fatalf("accepted %s", source)
		}
	}
	c := Client{Origin: "https://example.com/api"}
	for _, path := range []string{"/../secret", "/%2e%2e/secret", "/foo/%2fsecret", "/foo/%5csecret", "//evil.example", "/foo/%0a"} {
		if _, e := c.RequestURL(path, nil); e == nil {
			t.Fatalf("accepted %s", path)
		}
	}
	u, e := c.RequestURL("/projects", nil)
	if e != nil || u.String() != "https://example.com/api/projects" {
		t.Fatalf("%v %v", u, e)
	}
}
func TestFreshMethodBoundServiceTokens(t *testing.T) {
	ctx := context.Background()
	key, e := atcrypto.GeneratePrivateKeyP256()
	if e != nil {
		t.Fatal(e)
	}
	calls := 0
	aud := "did:web:example.com#myctx"
	method := "dev.mycontextprotocol.account.getAccount"
	pds := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/xrpc/com.atproto.server.getServiceAuth" || r.URL.Query().Get("aud") != aud || r.URL.Query().Get("lxm") != method || !strings.HasPrefix(r.Header.Get("Authorization"), "DPoP ") || r.Header.Get("DPoP") == "" {
			t.Errorf("invalid PDS request %s %v", r.URL, r.Header)
		}
		var exp int64
		fmt.Sscan(r.URL.Query().Get("exp"), &exp)
		if d := exp - time.Now().Unix(); d < 58 || d > 60 {
			t.Errorf("token lifetime %d", d)
		}
		fmt.Fprint(w, `{"token":"service-token"}`)
	}))
	defer pds.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer service-token" || r.Header.Get("Cookie") != "" {
			t.Errorf("invalid API credentials: %v", r.Header)
		}
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer api.Close()
	c := Client{OAuthHTTPClient: pds.Client(), Origin: api.URL, Session: "legacy-secret", OAuth: &OAuthCredential{CallbackURL: "http://127.0.0.1:12345/callback", Origin: api.URL, ServiceDID: aud, Data: oauth.ClientSessionData{AccountDID: "did:plc:abcdefghijklmnopqrstuvwx", SessionID: "state", HostURL: pds.URL, AccessToken: "pds-access", DPoPPrivateKeyMultibase: key.Multibase()}}}
	for range 2 {
		if _, _, e := c.Checked(ctx, "GET", "/xrpc/"+method, nil, nil); e != nil {
			t.Fatal(e)
		}
	}
	if calls != 2 {
		t.Fatalf("wanted fresh token each request, got %d", calls)
	}
	c.Origin = "https://other.example"
	if _, e := c.serviceToken(ctx, method); e == nil {
		t.Fatal("cross-origin credential accepted")
	}
}
func TestOAuthConfigUsesExactPermissions(t *testing.T) {
	c := oauthConfig("http://127.0.0.1:123/callback", "did:web:example.com#myctx")
	if c.Scopes[0] != "atproto" || len(c.Scopes) < 40 {
		t.Fatal(c.Scopes)
	}
	for _, scope := range c.Scopes[1:] {
		if strings.Contains(scope, "*") {
			t.Fatal(scope)
		}
	}
	b, e := json.Marshal(OAuthCredential{})
	if e != nil || !strings.Contains(string(b), "callback_url") {
		t.Fatal(string(b), e)
	}
}
func TestTransportNeverFollowsRedirects(t *testing.T) {
	hit := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit = true }))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 302) }))
	defer origin.Close()
	c := Client{Origin: origin.URL, Session: "secret"}
	_, r, e := c.Request(context.Background(), "GET", "/projects", nil, nil)
	if e != nil || r.StatusCode != 302 || hit {
		t.Fatalf("redirect followed %v %v %v", r, e, hit)
	}
}

func TestCredentialsStayOnSelectedTransport(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Errorf("credential crossover on native exception: %v", r.Header)
		}
		fmt.Fprint(w, `{}`)
	}))
	defer server.Close()
	for _, c := range []Client{{Origin: server.URL, Token: "bearer", Session: "legacy"}, {Origin: server.URL, OAuth: &OAuthCredential{}, Session: "legacy"}} {
		if _, _, e := c.Request(context.Background(), "GET", "/health", nil, nil); e != nil {
			t.Fatal(e)
		}
	}
}

func TestDPoPNoncePersistsAndRevocation(t *testing.T) {
	key, e := atcrypto.GeneratePrivateKeyP256()
	if e != nil {
		t.Fatal(e)
	}
	calls := 0
	revoked := map[string]bool{}
	persisted := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/revoke" {
			if r.Header.Get("DPoP") == "" {
				t.Error("missing revocation DPoP")
			}
			r.ParseForm()
			revoked[r.Form.Get("token_type_hint")] = true
			return
		}
		calls++
		if calls == 1 {
			w.Header().Set("WWW-Authenticate", `DPoP error="use_dpop_nonce"`)
			w.Header().Set("DPoP-Nonce", "new-nonce")
			w.WriteHeader(401)
			return
		}
		if !persisted {
			t.Error("nonce was not persisted before retry")
		}
		fmt.Fprint(w, `{"token":"fresh"}`)
	}))
	defer server.Close()
	credential := &OAuthCredential{Origin: "https://api.example", ServiceDID: "did:web:api.example#myctx", CallbackURL: "http://127.0.0.1:123/callback", Data: oauth.ClientSessionData{AccountDID: "did:plc:abcdefghijklmnopqrstuvwx", SessionID: "state", HostURL: server.URL, AccessToken: "access", RefreshToken: "refresh", AuthServerURL: server.URL, AuthServerRevocationEndpoint: server.URL + "/revoke", DPoPPrivateKeyMultibase: key.Multibase()}}
	c := Client{Origin: credential.Origin, OAuth: credential, OAuthHTTPClient: server.Client(), PersistOAuth: func(updated *OAuthCredential) error {
		persisted = updated.Data.DPoPHostNonce == "new-nonce"
		return nil
	}}
	token, e := c.serviceToken(context.Background(), "dev.mycontextprotocol.account.getAccount")
	if e != nil || token != "fresh" || calls != 2 {
		t.Fatalf("%s %v %d", token, e, calls)
	}
	if e := c.RevokeOAuth(context.Background()); e != nil {
		t.Fatal(e)
	}
	if !revoked["access_token"] || !revoked["refresh_token"] {
		t.Fatal(revoked)
	}
}

func TestExplicitSessionOverridesSavedOAuth(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/auth/me" || r.Header.Get("Cookie") != "vapor-session=override" || r.Header.Get("Authorization") != "" {
			t.Errorf("wrong transport or credential: %s %v", r.URL, r.Header)
		}
		fmt.Fprint(w, `{"id":"account"}`)
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := Configuration{Profiles: map[string]Profile{"default": {URL: server.URL, OAuth: &OAuthCredential{Origin: server.URL, ServiceDID: "did:web:example.com#myctx", Data: oauth.ClientSessionData{AccountDID: "did:plc:abcdefghijklmnopqrstuvwx"}}}}}
	if e := cfg.Save(path); e != nil {
		t.Fatal(e)
	}
	var output, diagnostics strings.Builder
	code := Run(context.Background(), []string{"auth", "status", "--config", path}, map[string]string{"MYCTX_SESSION": "override"}, strings.NewReader(""), &output, &diagnostics, "test")
	if code != 0 {
		t.Fatalf("code %d: %s", code, diagnostics.String())
	}
}
func TestSessionImportDiscardsExplicitToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/auth/me" || r.Header.Get("Cookie") != "vapor-session=imported" || r.Header.Get("Authorization") != "" {
			t.Errorf("wrong import credentials %s %v", r.URL, r.Header)
		}
		fmt.Fprint(w, `{"id":"account"}`)
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "config.json")
	var output, diagnostics strings.Builder
	code := Run(context.Background(), []string{"auth", "login", "--session-stdin", "--config", path, "--url", server.URL}, map[string]string{"MYCTX_TOKEN": "must-not-be-used"}, strings.NewReader("imported\n"), &output, &diagnostics, "test")
	if code != 0 {
		t.Fatalf("%d %s", code, diagnostics.String())
	}
	cfg, e := LoadConfiguration(path)
	if e != nil || cfg.Profiles["default"].Session != "imported" || cfg.Profiles["default"].OAuth != nil {
		t.Fatalf("%+v %v", cfg, e)
	}
}
