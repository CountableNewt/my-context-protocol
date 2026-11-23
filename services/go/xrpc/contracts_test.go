package xrpc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNestedContractsRejectUnpublishedAndUntypedTelemetry(t *testing.T) {
	for _, tc := range []struct{ method, body string }{
		{"agent.reportUsage", `{"report_id":"report","skills":[{"skill_id":"skill","version":"1","outcome":"used","reasoning":"private transcript"}]}`},
		{"agent.reportUsage", `{"report_id":"report","skills":[{"skill_id":"skill","version":"1","outcome":"used","outcome":"skipped"}]}`},
		{"runtime.updateSettings", `{"project_id":"p","assignments":[{"skill_id":"s","scope":"task","activation_mode":"always","required":"true","priority":1,"target_type":"task","target_id":"t"}]}`},
		{"runtime.updateSettings", `{"project_id":"p","telemetry_retention_days":2.5}`},
		{"release.updateCompiledSkill", `{"project_id":"p","release_id":"r","skill_id":"s","runtime":{"priority":9223372036854775808}}`},
	} {
		t.Run(tc.method+tc.body, func(t *testing.T) {
			h, _ := New(Config{Verifier: verifierFunc(func(context.Context, *http.Request) (Principal, error) {
				return Principal{AccountID: "a", DID: "did:plc:a", Scopes: map[string]bool{"rpc:" + Namespace + tc.method: true}}, nil
			}), Invoker: invokerFunc(func(context.Context, Principal, Operation, Input) (Result, error) {
				t.Fatal("invalid contract reached business logic")
				return Result{}, nil
			})})
			r := httptest.NewRequest("POST", "/xrpc/"+Namespace+tc.method, strings.NewReader(tc.body))
			r.Header.Set("Authorization", "DPoP token")
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 400 {
				t.Fatalf("status=%d body=%s", w.Code, w.Body)
			}
		})
	}
}

func TestOutputViewsPreserveDecimalsAndExcludeNullAndUnknownFields(t *testing.T) {
	op, _ := Lookup(Namespace + "analytics.getAccountTimeseries")
	value := map[string]any{"range_key": "24h", "range_start": "start", "range_end": "end", "project_id": nil, "buckets": []any{map[string]any{"label": "now", "start": "start", "end": "end", "request_count": 3, "success_count": 2, "avg_latency_ms": 1.25, "secret": "never"}}, "private_token": "never"}
	normalized, err := NormalizeOutput(op, value)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(normalized)
	if strings.Contains(string(raw), "never") || strings.Contains(string(raw), "null") || !strings.Contains(string(raw), `"avg_latency_ms":"1.25"`) {
		t.Fatalf("invalid XRPC view: %s", raw)
	}
	if value["private_token"] != "never" {
		t.Fatal("application value was mutated")
	}
}

func TestEveryInventoriedOperationHasTransportMetadata(t *testing.T) {
	if len(Operations()) < 55 {
		t.Fatalf("operation coverage=%d", len(Operations()))
	}
	for _, op := range Operations() {
		if op.Service == "" || op.inputSchema == nil || !op.NoOutput && op.outputSchema == nil {
			t.Fatalf("incomplete %s", op.NSID)
		}
	}
}

func TestPreflightAllowsExplicitAPICredentialsWithoutCookieCredentials(t *testing.T) {
	h, _ := New(Config{Verifier: verifierFunc(func(context.Context, *http.Request) (Principal, error) {
		t.Fatal("preflight authenticated")
		return Principal{}, nil
	}), Invoker: invokerFunc(func(context.Context, Principal, Operation, Input) (Result, error) {
		t.Fatal("preflight invoked")
		return Result{}, nil
	})})
	r := httptest.NewRequest("OPTIONS", "/xrpc/"+Namespace+"project.listProjects", nil)
	r.Header.Set("Origin", "https://third-party.example")
	r.Header.Set("Access-Control-Request-Method", "GET")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 204 || w.Header().Get("Access-Control-Allow-Origin") != "*" || w.Header().Get("Access-Control-Allow-Credentials") != "" {
		t.Fatalf("unsafe preflight: %d %v", w.Code, w.Header())
	}
	r.Header.Set("Access-Control-Request-Method", "POST")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 405 {
		t.Fatal("wrong preflight method accepted")
	}
}

func TestPendingPublicationRetainsAcceptedStatusAndTypedResult(t *testing.T) {
	h, _ := New(Config{Verifier: verifierFunc(func(context.Context, *http.Request) (Principal, error) {
		return Principal{AccountID: "a", DID: "did:plc:a", Scopes: map[string]bool{"rpc:" + Namespace + "publication.publishReview": true}}, nil
	}), Invoker: invokerFunc(func(context.Context, Principal, Operation, Input) (Result, error) {
		return Result{Status: 202, Value: map[string]any{"status": "pending", "digest": "review", "completed": 0, "total": 3}}, nil
	})})
	r := httptest.NewRequest("POST", "/xrpc/"+Namespace+"publication.publishReview", strings.NewReader(`{"project_id":"project","review_digest":"review","public":true,"telemetry":false}`))
	r.Header.Set("Authorization", "Bearer method-token")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 202 || !strings.Contains(w.Body.String(), `"status":"pending"`) {
		t.Fatalf("pending response: %d %s", w.Code, w.Body)
	}
}
