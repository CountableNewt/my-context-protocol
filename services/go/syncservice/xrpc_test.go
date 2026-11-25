package syncservice

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/CountableNewt/my-context-protocol/services/go/internal/sqlitetest"
	"github.com/CountableNewt/my-context-protocol/services/go/migrations"
	"github.com/CountableNewt/my-context-protocol/services/go/xrpc"
)

func TestSyncJobXRPCChecksProjectOwnership(t *testing.T) {
	db := sqlitetest.New(t, migrations.BaselineSQL+MigrationSQL+seedSQL)
	store := SQLStore{DB: db}
	connection, err := store.Connection(context.Background(), "project")
	if err != nil {
		t.Fatal(err)
	}
	job, _, err := store.Claim(context.Background(), connection, "delivery", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(Config{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	owner := "account"
	api, err := s.XRPCHandler(xrpc.VerifierFunc(func(context.Context, *http.Request) (xrpc.Principal, error) {
		return xrpc.Principal{AccountID: owner, DID: "did:plc:owner", Scopes: map[string]bool{"rpc:" + xrpc.Namespace + "repository.getSyncJob": true}}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/xrpc/"+xrpc.Namespace+"repository.getSyncJob?job_id="+job.ID, nil)
	r.Header.Set("Authorization", "Bearer service-auth")
	w := httptest.NewRecorder()
	api.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("owned job: %d %s", w.Code, w.Body)
	}
	owner = "other"
	w = httptest.NewRecorder()
	api.ServeHTTP(w, r)
	if w.Code != 404 {
		t.Fatalf("cross-owner job: %d %s", w.Code, w.Body)
	}
}
