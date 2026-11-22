package xrpc

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type verifierFunc func(context.Context, *http.Request) (Principal, error)

func (f verifierFunc) Verify(c context.Context, r *http.Request) (Principal, error) { return f(c, r) }

type invokerFunc func(context.Context, Principal, Operation, Input) (Result, error)

func (f invokerFunc) Invoke(c context.Context, p Principal, o Operation, i Input) (Result, error) {
	return f(c, p, o, i)
}

func TestTransportGuards(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, body, scope string
		admin                           bool
		limit                           int64
		status                          int
	}{
		{name: "query", method: "GET", path: "project.getProject?project_id=p", scope: "project.getProject", status: 200},
		{name: "procedure", method: "POST", path: "project.createProject", body: `{"name":"Project","slug":"project"}`, scope: "project.createProject", status: 200},
		{name: "unknown", method: "GET", path: "project.missing", scope: "project.missing", status: 404},
		{name: "path suffix", method: "GET", path: "project.getProject/extra", scope: "project.getProject", status: 404},
		{name: "encoded path", method: "GET", path: "project.%67etProject?project_id=p", scope: "project.getProject", status: 404},
		{name: "method mismatch", method: "PATCH", path: "project.updateProject", scope: "project.updateProject", status: 405},
		{name: "cookie alone", method: "GET", path: "project.listProjects", scope: "project.listProjects", status: 401},
		{name: "wildcard denied", method: "GET", path: "project.listProjects", scope: "*", status: 403},
		{name: "different scope", method: "GET", path: "project.listProjects", scope: "project.getProject", status: 403},
		{name: "admin required", method: "GET", path: "admin.getMetrics", scope: "admin.getMetrics", status: 403},
		{name: "admin allowed", method: "GET", path: "admin.getMetrics", scope: "admin.getMetrics", admin: true, status: 200},
		{name: "unknown query", method: "GET", path: "project.getProject?project_id=p&other=x", scope: "project.getProject", status: 400},
		{name: "missing field", method: "GET", path: "project.getProject", scope: "project.getProject", status: 400},
		{name: "duplicate query", method: "GET", path: "project.getProject?project_id=p&project_id=q", scope: "project.getProject", status: 400},
		{name: "invalid query", method: "GET", path: "project.getProject?project_id=%zz", scope: "project.getProject", status: 400},
		{name: "query body", method: "GET", path: "project.listProjects", body: `{}`, scope: "project.listProjects", status: 400},
		{name: "procedure query", method: "POST", path: "account.logout?extra=x", body: `{}`, scope: "account.logout", status: 400},
		{name: "unknown field", method: "POST", path: "account.logout", body: `{"extra":true}`, scope: "account.logout", status: 400},
		{name: "duplicate field", method: "POST", path: "project.createProject", body: `{"name":"a","name":"b","slug":"c"}`, scope: "project.createProject", status: 400},
		{name: "wrong scalar", method: "POST", path: "project.createProject", body: `{"name":1,"slug":"c"}`, scope: "project.createProject", status: 400},
		{name: "null scalar", method: "POST", path: "project.createProject", body: `{"name":null,"slug":"c"}`, scope: "project.createProject", status: 400},
		{name: "false boolean", method: "POST", path: "admin.updateAccountFlags", body: `{"account_id":"a","is_admin":false}`, scope: "admin.updateAccountFlags", admin: true, status: 200},
		{name: "null boolean", method: "POST", path: "admin.updateAccountFlags", body: `{"account_id":"a","is_admin":null}`, scope: "admin.updateAccountFlags", admin: true, status: 400},
		{name: "array", method: "POST", path: "account.logout", body: `[]`, scope: "account.logout", status: 400},
		{name: "trailing json", method: "POST", path: "account.logout", body: `{} {}`, scope: "account.logout", status: 400},
		{name: "malformed", method: "POST", path: "account.logout", body: `{`, scope: "account.logout", status: 400},
		{name: "large input", method: "POST", path: "project.createProject", body: `{"name":"abcdefgh","slug":"c"}`, scope: "project.createProject", limit: 8, status: 413},
		{name: "long name", method: "POST", path: "project.createProject", body: `{"name":"` + strings.Repeat("x", 257) + `","slug":"c"}`, scope: "project.createProject", status: 400},
		{name: "empty name", method: "POST", path: "project.createProject", body: `{"name":"","slug":"c"}`, scope: "project.createProject", status: 400},
		{name: "logout", method: "POST", path: "account.logout", body: `{}`, scope: "account.logout", status: 204},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			h, err := New(Config{MaxInputBytes: tc.limit, Verifier: verifierFunc(func(context.Context, *http.Request) (Principal, error) {
				return Principal{AccountID: "account", DID: "did:plc:owner", IsAdmin: tc.admin, Scopes: map[string]bool{"rpc:" + Namespace + tc.scope: true}}, nil
			}), Invoker: invokerFunc(func(_ context.Context, p Principal, o Operation, i Input) (Result, error) {
				calls++
				if p.AccountID != "account" {
					t.Fatal("lost principal")
				}
				if tc.name == "query" && i.Parameters["project_id"] != "p" {
					t.Fatal("lost input")
				}
				return Result{Value: map[string]any{"ok": true}}, nil
			})})
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest(tc.method, "https://service.test/xrpc/"+Namespace+tc.path, strings.NewReader(tc.body))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Cookie", "vapor-session=untrusted")
			if tc.name != "cookie alone" {
				r.Header.Set("Authorization", "DPoP proof-bound-token")
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("status=%d want=%d body=%s", w.Code, tc.status, w.Body.String())
			}
			if (tc.status < 300) != (calls == 1) {
				t.Fatalf("calls=%d status=%d", calls, w.Code)
			}
			if w.Header().Get("Cache-Control") != "private, no-store" {
				t.Fatal("missing no-store")
			}
		})
	}
}

func TestErrors(t *testing.T) {
	for _, tc := range []struct {
		verifyErr, error error
		status           int
		retry            string
	}{
		{verifyErr: errors.New("credential-secret"), status: 401},
		{error: errors.New("database-password"), status: 503},
		{error: &Error{Status: 403, Code: "Forbidden", Message: "Project ownership required"}, status: 403},
		{error: &Error{Status: 429, Code: "RateLimitExceeded", Message: "Slow down", RetryAfterSeconds: 30}, status: 429, retry: "30"},
		{error: &Error{Status: 200, Code: "Forbidden", Message: "invalid pairing"}, status: 503},
	} {
		h, _ := New(Config{Verifier: verifierFunc(func(context.Context, *http.Request) (Principal, error) {
			return Principal{AccountID: "account", DID: "did:plc:owner", Scopes: map[string]bool{"rpc:" + Namespace + "project.listProjects": true}}, tc.verifyErr
		}), Invoker: invokerFunc(func(context.Context, Principal, Operation, Input) (Result, error) {
			if tc.verifyErr != nil {
				t.Fatal("called after verification failure")
			}
			return Result{}, tc.error
		})})
		r := httptest.NewRequest("GET", "/xrpc/"+Namespace+"project.listProjects", nil)
		r.Header.Set("Authorization", "Bearer token")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.status || w.Header().Get("Retry-After") != tc.retry || strings.Contains(w.Body.String(), "password") || strings.Contains(w.Body.String(), "secret") {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
	}
}

func TestRegistryMatchesDrafts(t *testing.T) {
	for _, o := range Operations() {
		raw, err := os.ReadFile(filepath.Join("..", "..", "..", "lexicons", o.NSID+".json"))
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Defs map[string]struct {
				Type       string
				Parameters struct {
					Required   []string
					Properties map[string]struct {
						Type      string
						MinLength int
						MaxLength int
					}
				}
				Input struct {
					Schema struct {
						Required   []string
						Properties map[string]struct {
							Type      string
							MinLength int
							MaxLength int
						}
					}
				}
				Output any
			}
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		main := doc.Defs["main"]
		props, required := main.Parameters.Properties, main.Parameters.Required
		if o.Method == "POST" {
			if main.Type != "procedure" {
				t.Fatal("not procedure")
			}
			props, required = main.Input.Schema.Properties, main.Input.Schema.Required
		} else if main.Type != "query" {
			t.Fatal("not query")
		}
		if len(o.Fields) != len(props) {
			t.Fatalf("field count: %s", o.NSID)
		}
		for _, field := range o.Fields {
			p, ok := props[field.Name]
			if !ok || field.Boolean != (p.Type == "boolean") || field.MinLength != p.MinLength || field.MaxLength != p.MaxLength {
				t.Fatalf("field mismatch %s.%s", o.NSID, field.Name)
			}
			want := false
			for _, name := range required {
				want = want || name == field.Name
			}
			if field.Required != want {
				t.Fatal("required mismatch")
			}
		}
		if o.NoOutput != (main.Output == nil) {
			t.Fatalf("output mismatch: %s", o.NSID)
		}
	}
	copied := Operations()
	copied[1].Fields[0].Name = "corrupted"
	if Operations()[1].Fields[0].Name != "project_id" {
		t.Fatal("registry mutated")
	}
}

func TestConfigurationAndCredentialBoundaries(t *testing.T) {
	verifier := verifierFunc(func(context.Context, *http.Request) (Principal, error) { return Principal{}, nil })
	invoker := invokerFunc(func(context.Context, Principal, Operation, Input) (Result, error) {
		t.Fatal("unexpected invocation")
		return Result{}, nil
	})
	for _, config := range []Config{{}, {Verifier: verifier}, {Invoker: invoker}, {Verifier: verifier, Invoker: invoker, MaxInputBytes: -1}, {Verifier: verifier, Invoker: invoker, MaxInputBytes: 3 << 20}} {
		if _, err := New(config); err == nil {
			t.Fatal("accepted unsafe configuration")
		}
	}
	h, _ := New(Config{Verifier: verifier, Invoker: invoker})
	r := httptest.NewRequest("GET", "/xrpc/"+Namespace+"project.listProjects", nil)
	r.Header.Set("Authorization", "Bearer token")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatalf("empty verified identity: %d", w.Code)
	}
}

func TestInputMediaAndQueryLimit(t *testing.T) {
	h, _ := New(Config{Verifier: verifierFunc(func(context.Context, *http.Request) (Principal, error) {
		return Principal{AccountID: "a", DID: "did:plc:a", Scopes: map[string]bool{"rpc:" + Namespace + "project.getProject": true, "rpc:" + Namespace + "account.logout": true}}, nil
	}), Invoker: invokerFunc(func(context.Context, Principal, Operation, Input) (Result, error) {
		t.Fatal("invalid input invoked")
		return Result{}, nil
	})})
	for _, tc := range []struct {
		method, path, contentType, body string
		status                          int
	}{
		{"POST", "account.logout", "text/plain", "{}", 400},
		{"GET", "project.getProject?project_id=" + strings.Repeat("p", 8193), "", "", 413},
	} {
		r := httptest.NewRequest(tc.method, "/xrpc/"+Namespace+tc.path, strings.NewReader(tc.body))
		r.Header.Set("Authorization", "Bearer token")
		r.Header.Set("Content-Type", tc.contentType)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
	}
}
