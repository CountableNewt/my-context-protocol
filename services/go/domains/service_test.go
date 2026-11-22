package domains

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/CountableNewt/my-context-protocol/services/go/internal/sqlitetest"
	"github.com/CountableNewt/my-context-protocol/services/go/migrations"
)

type dns struct{ token string }

func (d *dns) LookupTXT(context.Context, string) ([]string, error) { return []string{d.token}, nil }
func fixture(t *testing.T) (*Service, *bool, *bool, *dns) {
	t.Helper()
	db := sqlitetest.New(t, migrations.BaselineSQL+`INSERT INTO accounts(id,github_id,login) VALUES ('OWNER',1,'owner'); INSERT INTO projects(id,account_id,name,slug) VALUES ('PROJECT','OWNER','Project','project'),('OTHER','OWNER','Other','other');`)
	exists, ready := false, false
	resolver := &dns{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" || r.URL.Query().Get("teamId") != "team" {
			t.Error("missing scoped auth")
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(r.URL.Path, "/v6/domains/") {
			json.NewEncoder(w).Encode(map[string]any{"misconfigured": !ready, "recommendedCNAME": []map[string]any{{"rank": 1, "value": "project.vercel-dns.test"}}})
			return
		}
		if r.Method == "POST" && r.URL.Path == "/v10/projects/project/domains" {
			exists = true
			json.NewEncoder(w).Encode(domain{Name: "mcp.example.com", ProjectID: "project"})
			return
		}
		if !exists {
			w.WriteHeader(404)
			return
		}
		json.NewEncoder(w).Encode(domain{Name: "mcp.example.com", ProjectID: "project", Verified: ready})
	}))
	t.Cleanup(server.Close)
	s, e := New(db, Config{Token: "test-token", ProjectID: "project", TeamID: "team", ReservedDomains: []string{"platform.example"}})
	if e != nil {
		t.Fatal(e)
	}
	s.api = server.URL
	s.resolver = resolver
	return s, &exists, &ready, resolver
}
func TestDomainOwnershipAndRoutingAgainstSQLite(t *testing.T) {
	s, _, ready, d := fixture(t)
	ctx := context.Background()
	created, e := s.Set(ctx, "PROJECT", "MCP.Example.com.")
	if e != nil {
		t.Fatal(e)
	}
	if created["verified"] != false || created["verification_record_name"] != "_mcp-verify.mcp.example.com" {
		t.Fatal(created)
	}
	token := created["verification_token"].(string)
	if token == "" {
		t.Fatal("no ownership token")
	}
	if _, e = s.Verify(ctx, "PROJECT"); e == nil {
		t.Fatal("missing TXT accepted")
	}
	d.token = token
	if _, e = s.Verify(ctx, "PROJECT"); e == nil {
		t.Fatal("missing routing accepted")
	}
	*ready = true
	verified, e := s.Verify(ctx, "PROJECT")
	if e != nil {
		t.Fatal(e)
	}
	if verified["verified"] != true || verified["verification_token"] != nil {
		t.Fatal(verified)
	}
	if verified["certificate_status"] == "issued" {
		t.Fatal("claimed TLS without certificate evidence")
	}
	*ready = false
	status, e := s.Get(ctx, "PROJECT")
	if e != nil || status["verified"] != false {
		t.Fatal("persisted flag hid routing regression", status, e)
	}
}
func TestDomainCannotBeClaimedByAnotherProject(t *testing.T) {
	s, _, _, _ := fixture(t)
	if _, e := s.Set(context.Background(), "PROJECT", "mcp.example.com"); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Set(context.Background(), "OTHER", "mcp.example.com"); e == nil {
		t.Fatal("duplicate domain claim accepted")
	}
}
func TestInvalidHostsRejectedBeforeProvisioning(t *testing.T) {
	s, exists, _, _ := fixture(t)
	for _, raw := range []string{"127.0.0.1", "https://mcp.example.com", "localhost", "*.example.com", "a..example.com", "-a.example.com", "a_.example.com", "x.platform.example", "branch.vercel.app", "host.example.com/path"} {
		if _, e := s.Set(context.Background(), "PROJECT", raw); e == nil {
			t.Fatal(raw)
		}
	}
	if *exists {
		t.Fatal("invalid host reached provider")
	}
}
func TestProviderFailureDoesNotSaveHostname(t *testing.T) {
	s, _, _, _ := fixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
		w.Write([]byte("secret provider body"))
	}))
	defer server.Close()
	s.api = server.URL
	_, e := s.Set(context.Background(), "PROJECT", "mcp.example.com")
	if e == nil || strings.Contains(e.Error(), "secret provider body") {
		t.Fatal(e)
	}
	p, e := s.load(context.Background(), "PROJECT")
	if e != nil || p.host != "" {
		t.Fatal(p, e)
	}
}
func TestProviderRedirectDoesNotForwardCredentials(t *testing.T) {
	s, _, _, _ := fixture(t)
	called := false
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 302) }))
	defer redirect.Close()
	s.api = redirect.URL
	if _, e := s.Set(context.Background(), "PROJECT", "mcp.example.com"); e == nil || called {
		t.Fatal("followed credential-bearing redirect")
	}
}

func TestUnexpectedCreatedAssociationIsNotPersisted(t *testing.T) {
	s, _, _, _ := fixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			w.WriteHeader(404)
			return
		}
		json.NewEncoder(w).Encode(domain{Name: "mcp.example.com", ProjectID: "different-project"})
	}))
	defer server.Close()
	s.api = server.URL
	if _, e := s.Set(context.Background(), "PROJECT", "mcp.example.com"); e == nil {
		t.Fatal("unexpected association accepted")
	}
	p, e := s.load(context.Background(), "PROJECT")
	if e != nil || p.host != "" {
		t.Fatal("saved unexpected association", p, e)
	}
}
