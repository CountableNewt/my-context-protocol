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
	"testing"
	"time"

	"github.com/CountableNewt/my-context-protocol/services/go/internal/sqlitetest"
	"github.com/CountableNewt/my-context-protocol/services/go/migrations"
	"github.com/CountableNewt/my-context-protocol/services/go/security"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
	"github.com/bluesky-social/indigo/atproto/auth/oauth"
	"github.com/bluesky-social/indigo/atproto/syntax"
)

type fakeATProtoOAuth struct {
	callbackCalls int
	state         string
}

func (f *fakeATProtoOAuth) StartAuth(context.Context, string) (string, string, error) {
	f.state = "sdk-state"
	return "https://auth.example.com/authorize?request_uri=opaque", f.state, nil
}
func (f *fakeATProtoOAuth) ProcessCallback(context.Context, url.Values) (*oauth.ClientSessionData, error) {
	f.callbackCalls++
	return &oauth.ClientSessionData{AccountDID: "did:plc:aaaaaaaaaaaaaaaaaaaaaaaa", HostURL: "https://pds.example.com", SessionID: "sdk-session"}, nil
}
func (f *fakeATProtoOAuth) Logout(context.Context, syntax.DID, string) error { return nil }

func atprotoFixture(t *testing.T) (*Service, *ATProtoSQLStore, *fakeATProtoOAuth) {
	t.Helper()
	// The explicit account migration is exercised by migration package tests.
	schema := strings.Replace(migrations.BaselineSQL, `"github_id" INTEGER NOT NULL`, `"github_id" INTEGER`, 1)
	db := sqlitetest.New(t, schema+ATProtoMigrationSQL)
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	cipher, err := security.NewTokenCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	store := &ATProtoSQLStore{DB: db, Cipher: cipher}
	client := &fakeATProtoOAuth{}
	s, err := New(Config{ClientID: "github", ClientSecret: "secret", RedirectURI: "https://app.example.com/auth/github/callback", EncryptionKey: key, FrontendOrigins: []string{"https://app.example.com"}, AdminLogins: []string{"admin"}, ATProto: &ATProtoConfig{Client: client, Accounts: store, Handle: func(context.Context, syntax.DID) (string, error) { return "admin", nil }}}, SQLStore{DB: db}, fakeGitHub{})
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return time.Unix(1700000000, 0) }
	return s, store, client
}

func atRequest(s *Service, method, path, body, origin string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}
func cookieNamed(t *testing.T, w *httptest.ResponseRecorder, name string) *http.Cookie {
	t.Helper()
	for _, c := range w.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("missing %s cookie: %s", name, w.Body)
	return nil
}

func TestATProtoBrowserBoundLoginAndReplay(t *testing.T) {
	s, store, client := atprotoFixture(t)
	start := atRequest(s, "GET", "/auth/atproto?handle=alice.example.com&return_to=https://app.example.com/account", "", "")
	if start.Code != 302 {
		t.Fatalf("start %d %s", start.Code, start.Body)
	}
	cookie := cookieNamed(t, start, "atproto-login")
	if !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteLaxMode {
		t.Fatal("unsafe login cookie")
	}
	missing := atRequest(s, "GET", "/auth/atproto/callback?state=sdk-state&code=code&iss=https://auth.example.com", "", "")
	if missing.Code != 400 || client.callbackCalls != 0 {
		t.Fatal("missing browser state reached token exchange")
	}
	wrong := atRequest(s, "GET", "/auth/atproto/callback?state=sdk-state", "", "", &http.Cookie{Name: "atproto-login", Value: strings.Repeat("x", 64)})
	if wrong.Code != 400 || client.callbackCalls != 0 {
		t.Fatal("cross-browser state reached token exchange")
	}
	complete := atRequest(s, "GET", "/auth/atproto/callback?state=sdk-state&code=code&iss=https://auth.example.com", "", "", cookie)
	if complete.Code != 302 {
		t.Fatalf("callback %d %s", complete.Code, complete.Body)
	}
	location, _ := url.Parse(complete.Header().Get("Location"))
	if location.Host != "app.example.com" || location.Query().Get("auth_token") == "" {
		t.Fatal("missing safe handoff")
	}
	session := cookieNamed(t, complete, "vapor-session")
	me := atRequest(s, "GET", "/auth/me", "", "", session)
	var user map[string]any
	if json.Unmarshal(me.Body.Bytes(), &user) != nil || user["atproto_did"] != "did:plc:aaaaaaaaaaaaaaaaaaaaaaaa" || user["github_connected"] != false || user["is_admin"] != false {
		t.Fatalf("invalid ATProto account: %s", me.Body)
	}
	ident, err := store.ATProtoIdentity(context.Background(), user["id"].(string))
	if err != nil || ident.SessionID != "sdk-session" {
		t.Fatalf("linked OAuth session missing: %v", err)
	}
	replay := atRequest(s, "GET", "/auth/atproto/callback?state=sdk-state", "", "", cookie)
	if replay.Code != 400 || client.callbackCalls != 1 {
		t.Fatal("state replay reached exchange")
	}
}

func TestATProtoCLIUsesExistingLoopbackGrammar(t *testing.T) {
	s, _, _ := atprotoFixture(t)
	for _, target := range []string{"https://evil.example/callback", "http://127.0.0.1:9/callback?state=short"} {
		w := atRequest(s, "GET", "/auth/cli/atproto?handle=alice.example.com&return_to="+url.QueryEscape(target), "", "")
		if w.Code != 400 {
			t.Fatal("unsafe CLI target accepted")
		}
	}
	target := "http://127.0.0.1:54321/callback?state=" + strings.Repeat("x", 43)
	start := atRequest(s, "GET", "/auth/cli/atproto?handle=alice.example.com&return_to="+url.QueryEscape(target), "", "")
	if start.Code != 302 {
		t.Fatal(start.Body.String())
	}
	callback := atRequest(s, "GET", "/auth/atproto/callback?state=sdk-state", "", "", cookieNamed(t, start, "atproto-login"))
	location, _ := url.Parse(callback.Header().Get("Location"))
	if callback.Code != 302 || location.Host != "127.0.0.1:54321" || location.Query().Get("state") != strings.Repeat("x", 43) {
		t.Fatal("CLI handoff changed nonce or target")
	}
}

func TestATProtoStoreDoesNotMergeAccountsOrOverwriteLinks(t *testing.T) {
	s, store, _ := atprotoFixture(t)
	ctx := context.Background()
	github, err := s.store.UpsertAccount(ctx, GitHubUser{ID: 42, Login: "admin"}, "encrypted", false)
	if err != nil {
		t.Fatal(err)
	}
	ident := ATProtoIdentity{DID: "did:plc:aaaaaaaaaaaaaaaaaaaaaaaa", Handle: "admin", PDSURL: "https://pds.example.com", SessionID: "secret-session"}
	native, err := store.UpsertATProtoAccount(ctx, ident, "")
	if err != nil {
		t.Fatal(err)
	}
	if native.ID == github.ID || native.GitHubID != 0 {
		t.Fatal("accounts merged by mutable login")
	}
	if _, err = store.UpsertATProtoAccount(ctx, ident, github.ID); !errors.Is(err, ErrIdentityConflict) {
		t.Fatalf("linked DID was reassigned: %v", err)
	}
	ident.DID = "did:plc:bbbbbbbbbbbbbbbbbbbbbbbb"
	linked, err := store.UpsertATProtoAccount(ctx, ident, github.ID)
	if err != nil || linked.ID != github.ID {
		t.Fatalf("explicit link failed: %v", err)
	}
	row, err := store.DB.Execute(ctx, atStatement("SELECT session_id_encrypted FROM account_atproto_identities WHERE account_id=?", turso.Text(github.ID)))
	if err != nil {
		t.Fatal(err)
	}
	if row.Rows[0][0].Value == ident.SessionID {
		t.Fatal("OAuth session ID stored unencrypted")
	}
}

func TestATProtoLinkRequiresOriginAndSameSession(t *testing.T) {
	s, _, client := atprotoFixture(t)
	ctx := context.Background()
	github, err := s.store.UpsertAccount(ctx, GitHubUser{ID: 42, Login: "owner"}, "encrypted", false)
	if err != nil {
		t.Fatal(err)
	}
	key := strings.Repeat("a", 64)
	if err = s.store.CreateSession(ctx, key, map[string]string{"accountId": github.ID}); err != nil {
		t.Fatal(err)
	}
	session := &http.Cookie{Name: "vapor-session", Value: key}
	body := `{"handle":"alice.example.com","return_to":"https://app.example.com/account"}`
	for _, origin := range []string{"", "https://evil.example"} {
		if w := atRequest(s, "POST", "/auth/atproto/link", body, origin, session); w.Code != 403 {
			t.Fatal("link accepted missing/wrong origin")
		}
	}
	start := atRequest(s, "POST", "/auth/atproto/link", body, "https://app.example.com", session)
	if start.Code != 200 {
		t.Fatalf("start link %d %s", start.Code, start.Body)
	}
	login := cookieNamed(t, start, "atproto-login")
	changed := atRequest(s, "GET", "/auth/atproto/callback?state=sdk-state", "", "", login)
	if changed.Code != 403 || client.callbackCalls != 0 {
		t.Fatal("link succeeded after account session disappeared")
	}
}

func TestATProtoAccountCanLinkGitHubWithoutMerging(t *testing.T) {
	s, store, _ := atprotoFixture(t)
	ctx := context.Background()
	native, err := store.UpsertATProtoAccount(ctx, ATProtoIdentity{DID: "did:plc:aaaaaaaaaaaaaaaaaaaaaaaa", Handle: "native.example.com", PDSURL: "https://pds.example.com", SessionID: "session"}, "")
	if err != nil {
		t.Fatal(err)
	}
	key := strings.Repeat("b", 64)
	if err = s.store.CreateSession(ctx, key, map[string]string{"accountId": native.ID}); err != nil {
		t.Fatal(err)
	}
	session := &http.Cookie{Name: "vapor-session", Value: key}
	if w := atRequest(s, "POST", "/auth/github/link", `{}`, "https://evil.example", session); w.Code != 403 {
		t.Fatal("GitHub link accepted cross-origin")
	}
	start := atRequest(s, "POST", "/auth/github/link", `{"return_to":"https://app.example.com/account"}`, "https://app.example.com", session)
	if start.Code != 200 {
		t.Fatalf("link start %d %s", start.Code, start.Body)
	}
	var response map[string]string
	json.Unmarshal(start.Body.Bytes(), &response)
	authorize, _ := url.Parse(response["authorization_url"])
	state := authorize.Query().Get("state")
	callback := "/auth/github/callback?state=" + url.QueryEscape(state) + "&code=valid"
	login := cookieNamed(t, start, "atproto-login")
	complete := atRequest(s, "GET", callback, "", "", session, login)
	if complete.Code != 302 {
		t.Fatalf("link callback %d %s", complete.Code, complete.Body)
	}
	got, err := s.store.Account(ctx, native.ID)
	if err != nil || got.GitHubID != 123 || got.Login != "sam" {
		t.Fatalf("GitHub not linked to original account: %v %+v", err, got)
	}
	if w := atRequest(s, "GET", callback, "", "", session, login); w.Code != 400 {
		t.Fatal("GitHub link state replay accepted")
	}
	other, err := store.UpsertATProtoAccount(ctx, ATProtoIdentity{DID: "did:plc:bbbbbbbbbbbbbbbbbbbbbbbb", PDSURL: "https://pds.example.com", SessionID: "other"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.LinkGitHubAccount(ctx, other.ID, GitHubUser{ID: 123, Login: "sam"}, "encrypted"); !errors.Is(err, ErrIdentityConflict) {
		t.Fatalf("GitHub linked account reassigned: %v", err)
	}
}
