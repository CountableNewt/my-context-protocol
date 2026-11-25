package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/CountableNewt/my-context-protocol/services/go/security"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
	"github.com/CountableNewt/my-context-protocol/services/go/xrpc"
)

func TestAgentXRPCRequiresActualTenantAndOwner(t *testing.T) {
	s, _, _ := fixture(t)
	owner := accountID
	api, err := s.XRPCHandler(xrpc.VerifierFunc(func(context.Context, *http.Request) (xrpc.Principal, error) {
		return xrpc.Principal{AccountID: owner, DID: "did:plc:owner", Scopes: map[string]bool{"rpc:" + xrpc.Namespace + "agent.getSkill": true, "rpc:" + xrpc.Namespace + "agent.listTools": true, "rpc:" + xrpc.Namespace + "agent.resolveContext": true, "rpc:" + xrpc.Namespace + "agent.reportUsage": true}}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	call := func(host, name, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "https://"+host+"/xrpc/"+xrpc.Namespace+name, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer service-auth")
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	w := call("tenant.mcp.example.com", "agent.getSkill", `{"skill_id":"test-skill"}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Apply guidance") {
		t.Fatalf("skill: %d %s", w.Code, w.Body)
	}
	w = call("tenant.mcp.example.com", "agent.listTools", `{}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "input_schema_json") {
		t.Fatalf("tools: %d %s", w.Code, w.Body)
	}
	w = call("tenant.mcp.example.com", "agent.resolveContext", `{"request":"Deployment guidance"}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"score":"`) {
		t.Fatalf("resolution: %d %s", w.Code, w.Body)
	}
	w = call("tenant.mcp.example.com", "agent.reportUsage", `{"report_id":"one","skills":[{"skill_id":"test-skill","version":"v1","outcome":"used"}]}`)
	if w.Code != 200 {
		t.Fatalf("usage: %d %s", w.Code, w.Body)
	}
	w = call("generic.vercel.app", "agent.getSkill", `{"skill_id":"test-skill"}`)
	if w.Code != 403 {
		t.Fatalf("generic host: %d %s", w.Code, w.Body)
	}
	owner = "other-account"
	w = call("tenant.mcp.example.com", "agent.getSkill", `{"skill_id":"test-skill"}`)
	if w.Code != 401 {
		t.Fatalf("wrong owner: %d %s", w.Code, w.Body)
	}
}

func TestAgentXRPCRejectsCookieAuthenticationFallback(t *testing.T) {
	s, _, _ := fixture(t)
	api, err := s.XRPCHandler(xrpc.VerifierFunc(func(context.Context, *http.Request) (xrpc.Principal, error) {
		t.Fatal("missing API credential reached verifier")
		return xrpc.Principal{}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "https://tenant.mcp.example.com/xrpc/"+xrpc.Namespace+"agent.listTools", strings.NewReader(`{}`))
	r.Header.Set("Content-Type", "application/json")
	r.AddCookie(&http.Cookie{Name: "vapor-session", Value: "untrusted"})
	w := httptest.NewRecorder()
	api.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatalf("cookie fallback: %d %s", w.Code, w.Body)
	}
}

func TestEveryAgentXRPCOperationRetainsOwnerAndTenantBoundaries(t *testing.T) {
	service, _, db := fixture(t)
	const foreignAccount = "99999999-9999-4999-8999-999999999999"
	if _, err := db.Batch(context.Background(), []turso.Statement{
		{SQL: "INSERT INTO accounts(id,github_id,login) VALUES(?,456,'foreign')", Args: []turso.Value{turso.Text(foreignAccount)}},
		{SQL: "UPDATE projects SET account_id=? WHERE id=?", Args: []turso.Value{turso.Text(foreignAccount), turso.Text(otherProjectID)}},
		{SQL: "INSERT INTO api_keys(id,project_id,key_prefix,key_hash,status) VALUES('foreign-key',?,'foreign',?,'active')", Args: []turso.Value{turso.Text(otherProjectID), turso.Text(security.HashToken("foreign-key"))}},
	}); err != nil {
		t.Fatal(err)
	}
	principal := xrpc.Principal{AccountID: foreignAccount, ProjectID: otherProjectID, DID: "did:plc:foreign", Scopes: map[string]bool{}}
	for _, op := range xrpc.Operations() {
		principal.Scopes[op.Scope()] = true
	}
	api, err := service.XRPCHandler(xrpc.VerifierFunc(func(context.Context, *http.Request) (xrpc.Principal, error) { return principal, nil }))
	if err != nil {
		t.Fatal(err)
	}
	covered := 0
	for _, op := range xrpc.Operations() {
		if op.Service != "mcp" {
			continue
		}
		covered++
		t.Run(op.NSID, func(t *testing.T) {
			for _, host := range []string{"tenant.mcp.example.com", "generic.vercel.app"} {
				request := httptest.NewRequest(op.Method, "https://"+host+"/xrpc/"+op.NSID, strings.NewReader("{}"))
				request.Header.Set("Authorization", "Bearer valid-foreign-credential")
				request.Header.Set("Content-Type", "application/json")
				writer := httptest.NewRecorder()
				api.ServeHTTP(writer, request)
				expected := 401
				if host == "generic.vercel.app" {
					expected = 403
				}
				if writer.Code != expected {
					t.Fatalf("XRPC %s: got%d want%d: %s", host, writer.Code, expected, writer.Body.String())
				}
				// Native MCP uses project-bound API keys rather than owner service JWTs.
				// Both transports must reject the foreign tenant before dispatch, though
				// credential mismatch is native403 and XRPC401 respectively.
				method := op.RESTPath
				params := map[string]any{}
				if op.RESTMethod == "MCP_TOOL" {
					method = "tools/call"
					params = map[string]any{"name": op.RESTPath, "arguments": map[string]any{}}
				}
				payload, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
				native := httptest.NewRequest("POST", "https://"+host+"/mcp", strings.NewReader(string(payload)))
				native.Header.Set("X-API-Key", "foreign-key")
				native.Header.Set("Content-Type", "application/json")
				native.Header.Set("Accept", "application/json, text/event-stream")
				denied := httptest.NewRecorder()
				service.ServeHTTP(denied, native)
				if denied.Code != 403 {
					t.Fatalf("native %s: got%d want403: %s", host, denied.Code, denied.Body.String())
				}
			}
		})
	}
	if covered == 0 {
		t.Fatal("agent operation coverage is empty")
	}
}
