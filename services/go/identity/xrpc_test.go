package identity

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/CountableNewt/my-context-protocol/services/go/internal/sqlitetest"
	"github.com/CountableNewt/my-context-protocol/services/go/migrations"
	"github.com/CountableNewt/my-context-protocol/services/go/session"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
	"github.com/CountableNewt/my-context-protocol/services/go/xrpc"
)

func TestAccountXRPCAndOwnedSessionRevocation(t *testing.T) {
	ctx := context.Background()
	owner := "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA"
	other := "BBBBBBBB-BBBB-4BBB-8BBB-BBBBBBBBBBBB"
	db := sqlitetest.New(t, migrations.BaselineSQL)
	if _, err := db.Execute(ctx, turso.Statement{SQL: "INSERT INTO accounts(id,github_id,login) VALUES(?,1,'owner'),(?,2,'other')", Args: []turso.Value{turso.Text(owner), turso.Text(other)}}); err != nil {
		t.Fatal(err)
	}
	store := SQLStore{DB: db}
	for _, item := range []struct{ key, id string }{{strings.Repeat("a", 64), owner}, {strings.Repeat("b", 64), other}} {
		if err := store.CreateSession(ctx, item.key, session.Payload{"accountId": item.id}); err != nil {
			t.Fatal(err)
		}
	}
	ownID, err := store.SessionIdentifier(ctx, strings.Repeat("a", 64), owner)
	if err != nil || ownID == "" {
		t.Fatalf("session id: %s %v", ownID, err)
	}
	otherID, _ := store.SessionIdentifier(ctx, strings.Repeat("b", 64), other)
	service, err := New(Config{ClientID: "client", ClientSecret: "secret", RedirectURI: "https://app.example/auth/github/callback", EncryptionKey: base64.StdEncoding.EncodeToString(make([]byte, 32)), FrontendOrigins: []string{"https://app.example"}}, store, fakeGitHub{})
	if err != nil {
		t.Fatal(err)
	}
	api, err := service.XRPCHandler(xrpc.VerifierFunc(func(context.Context, *http.Request) (xrpc.Principal, error) {
		return xrpc.Principal{AccountID: owner, DID: "did:plc:owner", Scopes: map[string]bool{"rpc:" + xrpc.Namespace + "account.getAccount": true, "rpc:" + xrpc.Namespace + "account.logout": true}}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, name, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/xrpc/"+xrpc.Namespace+name, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer service-auth")
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	w := call("GET", "account.getAccount", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), owner) || strings.Contains(w.Body.String(), "cookie") {
		t.Fatalf("account: %d %s", w.Code, w.Body)
	}
	if w = call("POST", "account.logout", `{"session_id":"`+otherID+`"}`); w.Code != 404 {
		t.Fatalf("cross-owner session: %d %s", w.Code, w.Body)
	}
	if w = call("POST", "account.logout", `{"session_id":"`+ownID+`"}`); w.Code != 204 {
		t.Fatalf("own session: %d %s", w.Code, w.Body)
	}
	if _, err := store.ReadSession(ctx, strings.Repeat("a", 64)); err == nil {
		t.Fatal("session still active")
	}
	if _, err := store.ReadSession(ctx, strings.Repeat("b", 64)); err != nil {
		t.Fatal("unrelated session revoked")
	}
	r := httptest.NewRequest("GET", "/auth/me", nil)
	r.AddCookie(&http.Cookie{Name: "vapor-session", Value: strings.Repeat("b", 64)})
	w = httptest.NewRecorder()
	service.ServeHTTP(w, r)
	var user map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &user); err != nil {
		t.Fatal(err)
	}
	if user["session_id"] != otherID {
		t.Fatalf("native session identifier missing: %s", w.Body)
	}
}
