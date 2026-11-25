package githubapp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/CountableNewt/my-context-protocol/services/go/xrpc"
)

func TestRepositoryXRPCUsesVerifiedAccountWithoutCookie(t *testing.T) {
	s, _ := setup(t)
	if _, err := s.config.DB.Execute(context.Background(), sql("INSERT INTO accounts(id,github_id,login,github_token_encrypted) SELECT 'XRPCOWNER',303,'owner',github_token_encrypted FROM accounts WHERE id='account'")); err != nil {
		t.Fatal(err)
	}
	calls := 0
	s.config.Client.HTTP = &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Host != "api.github.com" || r.Header.Get("Authorization") != "Bearer oauth" {
			t.Fatal("repository request lost account credentials")
		}
		return reply(200, `[{"full_name":"owner/private","name":"private","default_branch":"main","private":true,"owner":{"login":"owner"},"permissions":{"push":true}}]`), nil
	})}
	account := "XRPCOWNER"
	api, err := s.XRPCHandler(xrpc.VerifierFunc(func(context.Context, *http.Request) (xrpc.Principal, error) {
		return xrpc.Principal{AccountID: account, DID: "did:plc:owner", Scopes: map[string]bool{"rpc:" + xrpc.Namespace + "repository.listRepositories": true}}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/xrpc/"+xrpc.Namespace+"repository.listRepositories?provider=github", nil)
	r.Header.Set("Authorization", "Bearer verified-service-jwt")
	w := httptest.NewRecorder()
	api.ServeHTTP(w, r)
	if w.Code != 200 || calls != 1 {
		t.Fatalf("repository list: %d %s", w.Code, w.Body)
	}
	account = "MISSING"
	w = httptest.NewRecorder()
	api.ServeHTTP(w, r)
	if w.Code != 401 || calls != 1 {
		t.Fatalf("missing account reached GitHub: %d", w.Code)
	}
}
