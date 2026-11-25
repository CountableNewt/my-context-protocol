package control

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	expectStatus(t, apiCall(t, s, "POST", "account.logout", `{"session_id":"id"}`), 404)
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

func TestEveryProjectXRPCOperationRetainsOwnerIsolation(t *testing.T) {
	p := principalFor(otherID, false)
	for _, op := range xrpc.Operations() {
		p.Scopes[op.Scope()] = true
	}
	s := xrpcFixture(t, &p)
	for _, op := range xrpc.Operations() {
		if op.Service != "control" || !strings.Contains(op.RESTPath, "{project_id}") {
			continue
		}
		t.Run(op.NSID, func(t *testing.T) {
			body := map[string]any{}
			query := url.Values{}
			for _, field := range op.Fields {
				if !field.Required {
					continue
				}
				value := "value"
				switch field.Name {
				case "project_id":
					value = projectID
				case "release_id":
					value = releaseID
				case "skill_id", "key_id":
					value = skillID
				}
				if op.Method == "GET" {
					query.Set(field.Name, value)
				} else if field.Boolean {
					body[field.Name] = true
				} else {
					body[field.Name] = value
				}
			}
			method := strings.TrimPrefix(op.NSID, xrpc.Namespace)
			if len(query) > 0 {
				method += "?" + query.Encode()
			}
			raw, _ := json.Marshal(body)
			input := string(raw)
			if op.Method == "GET" {
				input = ""
			}
			xrpcDenied := apiCall(t, s, op.Method, method, input)
			expectStatus(t, xrpcDenied, 404)
			restPath, restBody := nativeOperationRequest(op, query, body)
			restDenied := call(t, s, op.RESTMethod, restPath, otherCookie, restBody, "https://app.example.com")
			expectStatus(t, restDenied, xrpcDenied.Code)
			if object(t, restDenied)["error"] != object(t, xrpcDenied)["message"] {
				t.Fatal("transport owner denials differ", restDenied.Body.String(), xrpcDenied.Body.String())
			}
		})
	}
}

func TestXRPCKeyManagementAndTypedQueryParameters(t *testing.T) {
	p := principalFor(ownerID, false, "security.listKeys", "security.createKey", "security.updateKey", "security.revokeKey", "analytics.getAccountTimeseries")
	s := xrpcFixture(t, &p)
	w := apiCall(t, s, "POST", "security.createKey", `{"project_id":"`+projectID+`","name":"Agent"}`)
	expectStatus(t, w, 200)
	key := object(t, w)
	id := key["id"].(string)
	expectStatus(t, apiCall(t, s, "POST", "security.updateKey", `{"project_id":"`+projectID+`","key_id":"`+id+`","name":"Updated"}`), 200)
	w = apiCall(t, s, "GET", "security.listKeys?project_id="+projectID+"&include_revoked=true", "")
	expectStatus(t, w, 200)
	if !strings.Contains(w.Body.String(), "Updated") {
		t.Fatal(w.Body.String())
	}
	expectStatus(t, apiCall(t, s, "POST", "security.revokeKey", `{"project_id":"`+projectID+`","key_id":"`+id+`"}`), 204)
	expectStatus(t, apiCall(t, s, "GET", "security.listKeys?project_id="+projectID+"&include_revoked=not-a-boolean", ""), 400)
	expectStatus(t, apiCall(t, s, "GET", "analytics.getAccountTimeseries?range=24h", ""), 200)
}

// nativeOperationRequest maps only routing and transport encoding. It does not
// reproduce application validation, authorization, or product behavior.
func nativeOperationRequest(op xrpc.Operation, query url.Values, body map[string]any) (string, string) {
	path := op.RESTPath
	nativeBody := map[string]any{}
	for k, v := range body {
		nativeBody[k] = v
	}
	nativeQuery := url.Values{}
	for k, values := range query {
		nativeQuery[k] = append([]string(nil), values...)
	}
	for _, field := range []string{"project_id", "release_id", "skill_id", "key_id", "account_id"} {
		placeholder := "{" + field + "}"
		if !strings.Contains(path, placeholder) {
			continue
		}
		value := nativeQuery.Get(field)
		if v, ok := nativeBody[field].(string); ok {
			value = v
		}
		if value == "" {
			value = projectID
		}
		path = strings.ReplaceAll(path, placeholder, url.PathEscape(value))
		delete(nativeBody, field)
		nativeQuery.Del(field)
	}
	if len(nativeQuery) > 0 {
		path += "?" + nativeQuery.Encode()
	}
	raw, _ := json.Marshal(nativeBody)
	return path, string(raw)
}

func TestEveryAdminControlOperationRejectsCurrentNonAdminAcrossTransports(t *testing.T) {
	// A stale credential still claiming admin must be denied by the shared
	// application's current-account check, rather than only the XRPC scope gate.
	p := principalFor(ownerID, true)
	for _, op := range xrpc.Operations() {
		p.Scopes[op.Scope()] = true
	}
	s := xrpcFixture(t, &p)
	tested := 0
	for _, op := range xrpc.Operations() {
		if op.Service != "control" || !op.Admin {
			continue
		}
		tested++
		t.Run(op.NSID, func(t *testing.T) {
			body := map[string]any{}
			query := url.Values{}
			for _, field := range op.Fields {
				if !field.Required {
					continue
				}
				value := any("value")
				if strings.HasSuffix(field.Name, "_id") {
					value = otherID
				}
				if field.Boolean {
					value = true
				}
				if op.Method == "GET" {
					query.Set(field.Name, value.(string))
				} else {
					body[field.Name] = value
				}
			}
			method := strings.TrimPrefix(op.NSID, xrpc.Namespace)
			if len(query) > 0 {
				method += "?" + query.Encode()
			}
			raw, _ := json.Marshal(body)
			input := string(raw)
			if op.Method == "GET" {
				input = ""
			}
			got := apiCall(t, s, op.Method, method, input)
			expectStatus(t, got, 403)
			path, nativeBody := nativeOperationRequest(op, query, body)
			expectStatus(t, call(t, s, op.RESTMethod, path, ownerCookie, nativeBody, "https://app.example.com"), got.Code)
		})
	}
	if tested == 0 {
		t.Fatal("admin operation coverage is empty")
	}
}

func TestEveryControlOperationRequiresAuthenticationAcrossTransports(t *testing.T) {
	p := xrpc.Principal{}
	s := xrpcFixture(t, &p)
	tested := 0
	for _, op := range xrpc.Operations() {
		if op.Service != "control" {
			continue
		}
		tested++
		t.Run(op.NSID, func(t *testing.T) {
			input := "{}"
			if op.Method == "GET" {
				input = ""
			}
			got := apiCall(t, s, op.Method, strings.TrimPrefix(op.NSID, xrpc.Namespace), input)
			expectStatus(t, got, 401)
			path, body := nativeOperationRequest(op, url.Values{}, map[string]any{})
			expectStatus(t, call(t, s, op.RESTMethod, path, "", body, "https://app.example.com"), got.Code)
		})
	}
	if tested == 0 {
		t.Fatal("control operation coverage is empty")
	}
}
