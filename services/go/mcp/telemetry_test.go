package mcp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/CountableNewt/my-context-protocol/services/go/domainrecords"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
	"github.com/CountableNewt/my-context-protocol/services/go/xrpc"
)

type telemetryFailureStore struct {
	Store
	recordErr, logErr error
	records, logs     int
	beforeLog         func()
}

func (s *telemetryFailureStore) CanonicalTelemetry(context.Context) bool { return true }
func (s *telemetryFailureStore) Record(context.Context, string, string, []Observation) error {
	s.records++
	return s.recordErr
}
func (s *telemetryFailureStore) Log(context.Context, RequestLog) error {
	s.logs++
	if s.beforeLog != nil {
		s.beforeLog()
	}
	return s.logErr
}
func TestNativeTelemetryFailurePreservesCommittedFeedbackAndPrecedesResponse(t *testing.T) {
	service, base, db := fixture(t)
	store := &telemetryFailureStore{Store: base, logErr: errors.New("PDS unavailable")}
	service.store = store
	writer := httptest.NewRecorder()
	store.beforeLog = func() {
		if writer.Body.Len() != 0 {
			t.Fatal("response written before telemetry persistence")
		}
	}
	request := httptest.NewRequest("POST", "https://tenant.mcp.example.com/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"report_skill_feedback","arguments":{"skill_id":"test-skill","version":"v1","category":"incorrect_instruction","summary":"wrong command","evidence":"reproduction"}}}`))
	request.Header.Set("X-API-Key", "test-key")
	request.Header.Set("MCP-Protocol-Version", "2025-11-25")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	service.ServeHTTP(writer, request)
	if writer.Code != 200 || writer.Header().Get("X-Myctx-Telemetry-Publication") != "failed" {
		t.Fatal(writer.Code, writer.Header(), writer.Body.String())
	}
	if result(t, writer)["structuredContent"].(map[string]any)["effectStatus"] != "draft" {
		t.Fatal("committed feedback reported failed")
	}
	rows, err := query(context.Background(), db, "SELECT COUNT(*) AS count FROM skill_feedback")
	if err != nil || number(rows[0], "count") != 1 {
		t.Fatal("feedback missing or duplicated", err)
	}
}
func TestNativeAndXRPCTelemetryPendingPreservePrimarySuccess(t *testing.T) {
	service, base, _ := fixture(t)
	store := &telemetryFailureStore{Store: base, recordErr: domainrecords.ErrPending}
	service.store = store
	native := invoke(t, service, "tools/call", `{"name":"resolve_context","arguments":{"request":"Deployment guidance"}}`, "2025-11-25")
	if native.Code != 200 || native.Header().Get("X-Myctx-Telemetry-Publication") != "pending" {
		t.Fatal(native.Code, native.Header(), native.Body.String())
	}
	api, err := service.XRPCHandler(xrpc.VerifierFunc(func(context.Context, *http.Request) (xrpc.Principal, error) {
		return xrpc.Principal{AccountID: accountID, DID: "did:plc:owner", Scopes: map[string]bool{"rpc:" + xrpc.Namespace + "agent.resolveContext": true}}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("POST", "https://tenant.mcp.example.com/xrpc/"+xrpc.Namespace+"agent.resolveContext", strings.NewReader(`{"request":"Deployment guidance"}`))
	request.Header.Set("Authorization", "Bearer verified")
	request.Header.Set("Content-Type", "application/json")
	writer := httptest.NewRecorder()
	api.ServeHTTP(writer, request)
	if writer.Code != 200 || writer.Header().Get("X-Myctx-Telemetry-Publication") != "pending" {
		t.Fatal(writer.Code, writer.Header(), writer.Body.String())
	}
	if store.records != 2 || store.logs != 0 {
		t.Fatal("pending observations followed by dependent log mutation", store.records, store.logs)
	}
}

type noConsentDB struct{ calls int }

func (d *noConsentDB) Execute(context.Context, turso.Statement) (*turso.Result, error) {
	d.calls++
	return nil, errors.New("unexpected SQL")
}
func (d *noConsentDB) Batch(context.Context, []turso.Statement) ([]turso.Result, error) {
	d.calls++
	return nil, errors.New("unexpected SQL")
}
func (d *noConsentDB) TelemetryPublicationAllowed(context.Context) bool { return false }
func TestCanonicalTelemetryWithoutPublicConsentDoesNotWrite(t *testing.T) {
	db := &noConsentDB{}
	store := SQLStore{DB: db}
	if err := store.Log(context.Background(), RequestLog{ProjectID: "project"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Record(context.Background(), "project", "client", []Observation{{Type: "surfaced"}}); err != nil {
		t.Fatal(err)
	}
	if db.calls != 0 {
		t.Fatal("published without renewed consent")
	}
}
