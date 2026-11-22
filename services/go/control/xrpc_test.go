package control

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/CountableNewt/my-context-protocol/services/go/xrpc"
)

type apiVerifier func(context.Context, *http.Request) (xrpc.Principal, error)

func (f apiVerifier) Verify(ctx context.Context, r *http.Request) (xrpc.Principal, error) {
	return f(ctx, r)
}

func xrpcFixture(t *testing.T, principal *xrpc.Principal) *Service {
	t.Helper()
	base, _ := fixture(t)
	cfg := base.cfg
	cfg.XRPCVerifier = apiVerifier(func(context.Context, *http.Request) (xrpc.Principal, error) { return *principal, nil })
	s, err := New(base.db, base.auth, cfg)
	if err != nil {
		t.Fatal(err)
	}
	s.now = base.now
	return s
}
func apiCall(t *testing.T, s *Service, method, name, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, "/xrpc/"+xrpc.Namespace+name, strings.NewReader(body))
	r.Header.Set("Authorization", "DPoP verified-token")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}
func principalFor(account string, admin bool, names ...string) xrpc.Principal {
	p := xrpc.Principal{AccountID: account, DID: "did:plc:owner", IsAdmin: admin, Scopes: map[string]bool{}}
	for _, name := range names {
		p.Scopes["rpc:"+xrpc.Namespace+name] = true
	}
	return p
}

func TestXRPCProjectOwnershipAndViews(t *testing.T) {
	p := principalFor(ownerID, false, "project.getProject", "project.listProjects", "project.updateProject", "project.createProject")
	s := xrpcFixture(t, &p)
	got := apiCall(t, s, "GET", "project.getProject?project_id="+projectID, "")
	expectStatus(t, got, 200)
	view := object(t, got)
	if view["name"] != "Owned" {
		t.Fatal(view)
	}
	for _, name := range []string{"custom_domain", "custom_domain_verified_at", "active_release_id", "suspended_at"} {
		if _, ok := view[name]; ok {
			t.Fatalf("nullable property present: %s", name)
		}
	}
	list := apiCall(t, s, "GET", "project.listProjects", "")
	expectStatus(t, list, 200)
	if projects, ok := object(t, list)["projects"].([]any); !ok || len(projects) != 1 {
		t.Fatal("missing projects envelope")
	}
	p.AccountID = otherID
	expectStatus(t, apiCall(t, s, "GET", "project.getProject?project_id="+projectID, ""), 404)
	expectStatus(t, apiCall(t, s, "POST", "project.updateProject", `{"project_id":"`+projectID+`","name":"Stolen"}`), 404)
	p.AccountID = ownerID
	expectStatus(t, apiCall(t, s, "POST", "project.updateProject", `{"project_id":"`+projectID+`","name":"Updated"}`), 200)
	if object(t, apiCall(t, s, "GET", "project.getProject?project_id="+projectID, ""))["name"] != "Updated" {
		t.Fatal("shared application mutation failed")
	}
	expectStatus(t, apiCall(t, s, "POST", "project.createProject", `{"name":"Duplicate","slug":"owned"}`), 409)
	expectStatus(t, apiCall(t, s, "GET", "project.getProject?project_id=bad/path", ""), 400)
}

func TestXRPCRechecksCurrentAdminAndAccount(t *testing.T) {
	p := principalFor(ownerID, true, "admin.getMetrics", "admin.updateAccountFlags")
	s := xrpcFixture(t, &p)
	// A stale credential claiming admin never overrides the current account row.
	expectStatus(t, apiCall(t, s, "GET", "admin.getMetrics", ""), 403)
	if _, err := s.exec(context.Background(), "UPDATE accounts SET is_admin=1 WHERE id=?", ownerID); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, apiCall(t, s, "GET", "admin.getMetrics", ""), 200)
	expectStatus(t, apiCall(t, s, "POST", "admin.updateAccountFlags", `{"account_id":"`+otherID+`","is_admin":true}`), 200)
	if _, err := s.exec(context.Background(), "UPDATE accounts SET is_admin=0 WHERE id=?", ownerID); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, apiCall(t, s, "GET", "admin.getMetrics", ""), 403)
	p.AccountID = "11111111-1111-4111-8111-111111111111"
	expectStatus(t, apiCall(t, s, "GET", "admin.getMetrics", ""), 401)
}

func TestXRPCDoesNotRelaxRESTAuthenticationOrCSRF(t *testing.T) {
	p := principalFor(ownerID, false, "project.updateProject", "account.logout")
	s := xrpcFixture(t, &p)
	body := `{"project_id":"` + projectID + `","name":"API update"}`
	expectStatus(t, apiCall(t, s, "POST", "project.updateProject", body), 200)
	expectStatus(t, call(t, s, "PATCH", "/projects/"+projectID, ownerCookie, `{"name":"CSRF"}`, ""), 403)
	expectStatus(t, call(t, s, "PATCH", "/projects/"+projectID, ownerCookie, `{"name":"CSRF"}`, "https://evil.example"), 403)
	expectStatus(t, call(t, s, "PATCH", "/projects/"+projectID, "", `{"name":"Anonymous"}`, "https://app.example.com"), 401)
	// Cookie authentication cannot enter the optional XRPC adapter.
	expectStatus(t, call(t, s, "POST", "/xrpc/"+xrpc.Namespace+"project.updateProject", ownerCookie, body, "https://app.example.com"), 401)
	p.Scopes = map[string]bool{"rpc:" + xrpc.Namespace + "project.*": true}
	expectStatus(t, apiCall(t, s, "POST", "project.updateProject", body), 403)
	p = principalFor(ownerID, false, "account.logout")
	expectStatus(t, apiCall(t, s, "POST", "account.logout", `{}`), 404)
}

func TestXRPCDisabledUnlessExplicitlyConfigured(t *testing.T) {
	s, _ := fixture(t)
	expectStatus(t, apiCall(t, s, "GET", "project.listProjects", ""), 404)
}

func TestXRPCContextCannotCrossServices(t *testing.T) {
	p := principalFor(ownerID, false, "project.listProjects")
	first := xrpcFixture(t, &p)
	second := xrpcFixture(t, &p)
	ctx := context.WithValue(context.Background(), xrpcContextKey{}, xrpcAuthorization{service: first, accountID: ownerID})
	r := httptest.NewRequest("POST", "/projects", strings.NewReader(`{"name":"Attack","slug":"attack"}`)).WithContext(ctx)
	if second.validOrigin(r) {
		t.Fatal("accepted authorization from a different service")
	}
	if _, err := second.account(r); err == nil {
		t.Fatal("accepted identity from a different service")
	}
}

func TestXRPCVerifierFailureCannotUseCookie(t *testing.T) {
	base, _ := fixture(t)
	cfg := base.cfg
	cfg.XRPCVerifier = apiVerifier(func(context.Context, *http.Request) (xrpc.Principal, error) {
		return xrpc.Principal{}, errors.New("expired")
	})
	s, err := New(base.db, base.auth, cfg)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/xrpc/"+xrpc.Namespace+"project.listProjects", nil)
	r.Header.Set("Authorization", "Bearer expired")
	r.AddCookie(&http.Cookie{Name: "vapor-session", Value: ownerCookie})
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	expectStatus(t, w, 401)
}
