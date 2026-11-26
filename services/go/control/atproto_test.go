package control

import (
	"context"
	"testing"

	"github.com/CountableNewt/my-context-protocol/services/go/domainrecords"
	"github.com/CountableNewt/my-context-protocol/services/go/recordservice"
)

type recordController struct {
	scopes, engines, inspections, resolutions int
	account, project                          string
}

func (f *recordController) Scope(ctx context.Context, a, p string) (context.Context, error) {
	f.scopes++
	f.account = a
	f.project = p
	return ctx, nil
}
func (f *recordController) WithEngine(context.Context, string, func(context.Context, string, *domainrecords.Engine) error) error {
	f.engines++
	return domainrecords.ErrConsent
}
func (f *recordController) IsCanonical(context.Context, string, string) (bool, error) {
	return false, nil
}
func (f *recordController) Reconcile(context.Context, string, string) error { return nil }
func (f *recordController) Update(context.Context, string, string, []domainrecords.Change) error {
	return nil
}
func (f *recordController) InspectConflict(_ context.Context, a, p string) (*recordservice.ConflictReview, error) {
	f.inspections++
	f.account = a
	f.project = p
	return nil, nil
}
func (f *recordController) ResolveConflict(_ context.Context, a, p, i, c, action string) error {
	f.resolutions++
	f.account = a
	f.project = p
	return nil
}

func TestTangledRepositoryDiscoveryUsesAuthenticatedAccount(t *testing.T) {
	s, _ := fixture(t)
	accounts := []string{}
	cursors := []string{}
	s.cfg.ListTangledRepositories = func(_ context.Context, account, cursor string) (map[string]any, error) {
		accounts = append(accounts, account)
		cursors = append(cursors, cursor)
		return map[string]any{"provider": "tangled", "repositories": []any{}}, nil
	}
	expectStatus(t, call(t, s, "GET", "/repositories/tangled", "", "", ""), 401)
	expectStatus(t, call(t, s, "GET", "/repositories/tangled?account_id="+ownerID, otherCookie, "", ""), 200)
	expectStatus(t, call(t, s, "GET", "/repositories/tangled?cursor=next-page", ownerCookie, "", ""), 200)
	if len(cursors) != 2 || cursors[1] != "next-page" {
		t.Fatal("repository cursor not forwarded", cursors)
	}
	if len(accounts) != 2 || accounts[0] != otherID || accounts[1] != ownerID {
		t.Fatal("request supplied account overrode authenticated identity", accounts)
	}
}
func TestPublicationAndConflictControllerRequiresProjectOwner(t *testing.T) {
	s, _ := fixture(t)
	records := &recordController{}
	s.cfg.Records = records
	for _, path := range []string{"publication", "conflict"} {
		expectStatus(t, call(t, s, "GET", "/projects/"+projectID+"/atproto/"+path, otherCookie, "", ""), 404)
	}
	body := `{"intent_id":"intent","expected_current_cid":"cid","action":"discard"}`
	expectStatus(t, call(t, s, "POST", "/projects/"+projectID+"/atproto/conflict", otherCookie, body, "https://app.example.com"), 404)
	mustExec(t, s, "UPDATE accounts SET is_admin=1 WHERE id=?", otherID)
	expectStatus(t, call(t, s, "POST", "/projects/"+projectID+"/atproto/conflict", otherCookie, body, "https://app.example.com"), 404)
	if records.scopes+records.engines+records.inspections+records.resolutions != 0 {
		t.Fatal("foreign/admin account reached canonical owner API", records)
	}
	expectStatus(t, call(t, s, "GET", "/projects/"+projectID+"/atproto/publication", ownerCookie, "", ""), 409)
	expectStatus(t, call(t, s, "GET", "/projects/"+projectID+"/atproto/conflict", ownerCookie, "", ""), 200)
	expectStatus(t, call(t, s, "POST", "/projects/"+projectID+"/atproto/conflict", ownerCookie, body, "https://app.example.com"), 200)
	if records.engines != 1 || records.inspections != 1 || records.resolutions != 1 || records.account != ownerID || records.project != projectID {
		t.Fatal("owner mapping incorrect", records)
	}
}
func TestLegacySetActiveCannotBypassCanonicalSiblingScope(t *testing.T) {
	s, _ := fixture(t)
	s.cfg.Records = &recordController{}
	sibling := "ABABABAB-ABAB-4BAB-8BAB-ABABABABABAB"
	mustExec(t, s, "INSERT INTO projects(id,account_id,name,slug,subdomain,suspended_at) VALUES(?,?,'Sibling','sibling','sibling',123)", sibling, ownerID)
	expectStatus(t, call(t, s, "POST", "/projects/"+projectID+"/set-active", ownerCookie, "{}", "https://app.example.com"), 200)
	row, err := s.one(context.Background(), "SELECT suspended_at FROM projects WHERE id=?", sibling)
	if err != nil || num(row["suspended_at"]) != 123 {
		t.Fatal("legacy project changed sibling canonical state", row, err)
	}
}

func TestSyncContinuationBypassesNewOperationQuotaAndPreservesResponses(t *testing.T) {
	p := principalFor(ownerID, false, "repository.syncRepository")
	s := xrpcFixture(t, &p)
	mustExec(t, s, "CREATE TABLE mcp_rate_limits(bucket_key TEXT,window_start INTEGER,count INTEGER,PRIMARY KEY(bucket_key,window_start))")
	s.cfg.SyncLimit = 1
	s.cfg.SyncWindowSeconds = 3600
	continuation := false
	status := "ready"
	calls := 0
	s.cfg.SyncContinuation = func(context.Context, string) (bool, error) { return continuation, nil }
	s.cfg.Sync = func(_ context.Context, project string) (map[string]any, error) {
		calls++
		if project != projectID {
			t.Fatal(project)
		}
		return map[string]any{"job_id": "job", "project_id": projectID, "status": status, "release_id": releaseID}, nil
	}
	path := "/projects/" + projectID + "/sync"
	expectStatus(t, call(t, s, "POST", path, ownerCookie, "{}", "https://app.example.com"), 204)
	expectStatus(t, call(t, s, "POST", path, ownerCookie, "{}", "https://app.example.com"), 429)
	continuation = true
	status = "publishing"
	expectStatus(t, call(t, s, "POST", path, ownerCookie, "{}", "https://app.example.com"), 202)
	expectStatus(t, apiCall(t, s, "POST", "repository.syncRepository", `{"project_id":"`+projectID+`"}`), 202)
	status = "ready"
	got := apiCall(t, s, "POST", "repository.syncRepository", `{"project_id":"`+projectID+`"}`)
	expectStatus(t, got, 200)
	if object(t, got)["status"] != "ready" {
		t.Fatal("XRPC discarded sync result", got.Body.String())
	}
	p.AccountID = otherID
	expectStatus(t, apiCall(t, s, "POST", "repository.syncRepository", `{"project_id":"`+projectID+`"}`), 404)
	if calls != 4 {
		t.Fatal("foreign/quota-denied requests invoked sync", calls)
	}
}

func TestXRPCConflictContractsPreserveOwnerIsolationForAdmins(t *testing.T) {
	p := principalFor(ownerID, false, "publication.getConflict", "publication.resolveConflict", "publication.getReview")
	s := xrpcFixture(t, &p)
	records := &recordController{}
	s.cfg.Records = records
	expectStatus(t, apiCall(t, s, "GET", "publication.getConflict?project_id="+projectID, ""), 200)
	expectStatus(t, apiCall(t, s, "POST", "publication.resolveConflict", `{"project_id":"`+projectID+`","intent_id":"intent","expected_current_cid":"cid","action":"discard"}`), 200)
	p.AccountID = otherID
	p.IsAdmin = true
	mustExec(t, s, "UPDATE accounts SET is_admin=1 WHERE id=?", otherID)
	expectStatus(t, apiCall(t, s, "GET", "publication.getConflict?project_id="+projectID, ""), 404)
	expectStatus(t, apiCall(t, s, "GET", "publication.getReview?project_id="+projectID, ""), 404)
	expectStatus(t, apiCall(t, s, "POST", "publication.resolveConflict", `{"project_id":"`+projectID+`","intent_id":"intent","expected_current_cid":"cid","action":"discard"}`), 404)
	if records.inspections != 1 || records.resolutions != 1 || records.engines != 0 {
		t.Fatal("admin escaped project ownership", records)
	}
}

type repairController struct {
	recordController
	auditError error
	reconciles int
}

func (f *repairController) Scope(ctx context.Context, a, p string) (context.Context, error) {
	f.scopes++
	return ctx, domainrecords.ErrAuditInvalid
}
func (f *repairController) WithEngine(context.Context, string, func(context.Context, string, *domainrecords.Engine) error) error {
	f.engines++
	return f.auditError
}
func (f *repairController) Reconcile(context.Context, string, string) error {
	f.reconciles++
	return nil
}
func TestExplicitRepairAuditsBeforeNormalReadinessAndNativeResume(t *testing.T) {
	s, _ := fixture(t)
	records := &repairController{auditError: &domainrecords.PendingError{}}
	s.cfg.Records = records
	path := "/projects/" + projectID + "/atproto/reconcile"
	expectStatus(t, call(t, s, "POST", path, ownerCookie, "{}", "https://app.example.com"), 202)
	if records.scopes != 0 || records.reconciles != 0 || records.engines != 1 {
		t.Fatal("pending repair bypassed audit", records)
	}
	records.auditError = nil
	expectStatus(t, call(t, s, "POST", path, ownerCookie, "{}", "https://app.example.com"), 200)
	if records.scopes != 0 || records.reconciles != 1 || records.engines != 2 {
		t.Fatal("validated repair did not resume projection", records)
	}
}
