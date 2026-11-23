package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
