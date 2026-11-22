package control

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/CountableNewt/my-context-protocol/services/go/githubapp"
	"github.com/CountableNewt/my-context-protocol/services/go/mcp"
	"github.com/CountableNewt/my-context-protocol/services/go/syncservice"
)

const cronSecret = "test-maintenance-secret-0123456789"

func maintenanceFixture(t *testing.T) *Service {
	t.Helper()
	s, _ := fixture(t)
	s.cfg.CronSecret = cronSecret
	for _, ddl := range strings.Split(mcp.MigrationSQL+syncservice.MigrationSQL+githubapp.MigrationSQL+MaintenanceSQL, ";") {
		if strings.TrimSpace(ddl) != "" {
			mustExec(t, s, ddl)
		}
	}
	return s
}
func mustExec(t *testing.T, s *Service, sql string, args ...any) {
	t.Helper()
	if _, e := s.exec(context.Background(), sql, args...); e != nil {
		t.Fatal(e)
	}
}
func maintenanceCall(s *Service, method, authorization, cookie string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "/internal/maintenance", nil)
	if authorization != "" {
		r.Header.Set("Authorization", authorization)
	}
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: "vapor-session", Value: cookie})
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}
func tableCount(t *testing.T, s *Service, table string) int {
	t.Helper()
	v, e := s.one(context.Background(), "SELECT COUNT(*) AS count FROM "+table)
	if e != nil {
		t.Fatal(e)
	}
	return int(num(v["count"]))
}
func TestMaintenanceRequiresExactBearerWithoutSession(t *testing.T) {
	s := maintenanceFixture(t)
	for _, header := range []string{"", "Bearer wrong", "bearer " + cronSecret, "Bearer " + cronSecret + " ", "Bearer " + cronSecret + ",other"} {
		expectStatus(t, maintenanceCall(s, "GET", header, ownerCookie), 401)
	}
	expectStatus(t, maintenanceCall(s, "POST", "Bearer "+cronSecret, ""), 405)
	w := maintenanceCall(s, "GET", "Bearer "+cronSecret, "")
	expectStatus(t, w, 200)
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("maintenance response cacheable")
	}
	s.cfg.CronSecret = ""
	expectStatus(t, maintenanceCall(s, "GET", "Bearer ", ownerCookie), 503)
}
func TestMaintenanceCleansExpiredStateAndPreservesLivePrincipals(t *testing.T) {
	s := maintenanceFixture(t)
	now := s.now().Unix()
	second := "11111111-1111-4111-8111-111111111111"
	mustExec(t, s, "INSERT INTO projects(id,account_id,name,slug,subdomain) VALUES(?,?,'Other','other','other')", second, otherID)
	for _, v := range []struct{ id, status string }{{"live-key", "active"}, {"revoked-key", "revoked"}} {
		mustExec(t, s, "INSERT INTO api_keys(id,project_id,key_prefix,key_hash,status) VALUES(?,?,'mcp_prefix',?,?)", v.id, projectID, v.id, v.status)
	}
	mustExec(t, s, "INSERT INTO mcp_oauth_clients(id,public_client_id,is_confidential,redirect_uris_json,allowed_grants,status,project_id) VALUES('client','client',0,'[]','authorization_code','active',?),('disabled-client','disabled-client',0,'[]','authorization_code','revoked',?)", projectID, projectID)
	for _, v := range []struct {
		id, client string
		expiry     int64
		revoked    any
	}{{"live-token", "client", now + 1000, nil}, {"expired-token", "client", now, nil}, {"revoked-token", "client", now + 1000, now - 1}, {"disabled-token", "disabled-client", now + 1000, nil}} {
		mustExec(t, s, "INSERT INTO mcp_oauth_access_tokens(id,token_hash,project_id,mcp_oauth_client_id,subject_type,scope,expires_at,revoked_at) VALUES(?,?,?,?,'service','mcp:invoke',?,?)", v.id, v.id, projectID, v.client, v.expiry, v.revoked)
	}
	for _, id := range []string{"live-key", "revoked-key", "live-token", "expired-token", "revoked-token", "disabled-token", "removed-token"} {
		mustExec(t, s, "INSERT INTO mcp_resource_subscriptions(project_id,subscriber_id,uri,created_at) VALUES(?,?,'ctx://skill/guide',?)", projectID, id, now-100)
	}
	for _, expiry := range []int64{now, now + 1000} {
		suffix := "expired"
		if expiry > now {
			suffix = "live"
		}
		mustExec(t, s, "INSERT INTO mcp_oauth_pending_authorizations(id,project_id,mcp_oauth_client_id,redirect_uri,state,scope,code_challenge,code_challenge_method,expires_at) VALUES(?,?,'client','https://client.example/callback','state','mcp:invoke','challenge','S256',?)", suffix, projectID, expiry)
		mustExec(t, s, "INSERT INTO mcp_oauth_authorization_codes(id,code_hash,project_id,mcp_oauth_client_id,account_id,redirect_uri,scope,code_challenge,code_challenge_method,expires_at) VALUES(?,?,?,'client',?,'https://client.example/callback','mcp:invoke','challenge','S256',?)", suffix, suffix, projectID, ownerID, expiry)
		mustExec(t, s, "INSERT INTO oauth_handoff_tokens(id,token,account_id,expires_at) VALUES(?,?,?,?)", suffix, suffix, ownerID, expiry)
		mustExec(t, s, "INSERT INTO github_app_install_intents(id,project_id,account_id,expires_at) VALUES(?,?,?,?)", suffix, projectID, ownerID, expiry)
	}
	mustExec(t, s, "INSERT INTO mcp_rate_limits(bucket_key,window_start,count) VALUES('expired',?,9),('active',?,99)", now-2*86400, now/60*60)
	mustExec(t, s, "INSERT INTO github_operation_locks(project_id,operation_id,lease_until) VALUES(?,'old',?),(?,'live',?)", projectID, now-600, second, now+600)
	mustExec(t, s, "INSERT INTO releases(id,project_id,commit_sha,status) VALUES('pending-release',?,'pending','pending')", projectID)
	mustExec(t, s, "INSERT INTO sync_project_locks(project_id,job_id,lease_until) VALUES(?,'stale-running',?),(?,'live-running',?)", projectID, now-600, second, now+600)
	for _, v := range []struct {
		id, status     string
		updated, lease int64
		release        string
	}{{"stale-running", "running", now - 1000, now - 600, "pending-release"}, {"live-running", "running", now, now + 600, "live-release"}, {"terminal-old", "ready", now - 40*86400, now - 40*86400, "old-release"}, {"terminal-recent", "failed", now - 86400, now - 86400, "recent-release"}} {
		mustExec(t, s, "INSERT INTO sync_jobs(id,project_id,delivery_key,release_id,status,created_at,updated_at,lease_until) VALUES(?,?,?,?,?,?,?,?)", v.id, projectID, v.id, v.release, v.status, v.updated, v.updated, v.lease)
	}
	w := maintenanceCall(s, "GET", "Bearer "+cronSecret, "")
	expectStatus(t, w, 200)
	response := object(t, w)
	if response["more_work"] != false || response["rollup_refreshed"] != true {
		t.Fatal(response)
	}
	for _, table := range []string{"mcp_oauth_pending_authorizations", "mcp_oauth_authorization_codes", "oauth_handoff_tokens", "github_app_install_intents", "mcp_rate_limits", "github_operation_locks", "sync_project_locks"} {
		if tableCount(t, s, table) != 1 {
			t.Fatal("live/expired cleanup mismatch", table)
		}
	}
	quota, e := s.one(context.Background(), "SELECT count FROM mcp_rate_limits WHERE bucket_key='active'")
	if e != nil || num(quota["count"]) != 99 {
		t.Fatal("live quota counter reset", quota, e)
	}
	if tableCount(t, s, "mcp_resource_subscriptions") != 2 {
		t.Fatal("live credential subscriptions lost or revoked retained")
	}
	if tableCount(t, s, "mcp_oauth_access_tokens") != 2 {
		t.Fatal("live tokens removed")
	}
	if tableCount(t, s, "sync_jobs") != 3 {
		t.Fatal("terminal job retention mismatch")
	}
	stale, e := s.one(context.Background(), "SELECT status,error_code FROM sync_jobs WHERE id='stale-running'")
	if e != nil || stale["status"] != "failed" || stale["error_code"] != "lease_expired" {
		t.Fatal(stale, e)
	}
	release, e := s.one(context.Background(), "SELECT status FROM releases WHERE id='pending-release'")
	if e != nil || release["status"] != "failed" {
		t.Fatal(release, e)
	}
}
func TestMaintenanceTelemetryRetentionIncludesDisabledInactiveProjects(t *testing.T) {
	s := maintenanceFixture(t)
	now := s.now().Unix()
	second := "11111111-1111-4111-8111-111111111111"
	mustExec(t, s, "INSERT INTO projects(id,account_id,name,slug) VALUES(?,?,'Other','other')", second, otherID)
	mustExec(t, s, "INSERT INTO project_runtime_settings(id,project_id,telemetry_enabled,telemetry_retention_days) VALUES('disabled',?,0,1)", projectID)
	for _, v := range []struct {
		id, project string
		age         int64
	}{{"owner-old", projectID, 2 * 86400}, {"owner-live", projectID, 1000}, {"default-old", second, 31 * 86400}, {"default-live", second, 10 * 86400}} {
		mustExec(t, s, "INSERT INTO skill_runtime_events(id,project_id,trace_id,event_type,detail_json,created_at) VALUES(?,?,'trace','surfaced','{}',?)", v.id, v.project, now-v.age)
		mustExec(t, s, "INSERT INTO skill_usage_reports(id,project_id,client_identity,report_id,payload_hash,recorded_count,created_at) VALUES(?,?,'client',?,'digest',1,?)", v.id, v.project, v.id, now-v.age)
	}
	expectStatus(t, maintenanceCall(s, "GET", "Bearer "+cronSecret, ""), 200)
	for _, table := range []string{"skill_runtime_events", "skill_usage_reports"} {
		if tableCount(t, s, table) != 2 {
			t.Fatal("retention mismatch", table)
		}
		rows, e := s.query(context.Background(), "SELECT id FROM "+table+" ORDER BY id")
		if e != nil || rows[0]["id"] != "default-live" || rows[1]["id"] != "owner-live" {
			t.Fatal(rows, e)
		}
	}
}
func TestMaintenanceDeleteCapPreservesQuotaWindowsAndReportsBacklog(t *testing.T) {
	s := maintenanceFixture(t)
	s.cfg.MaintenanceRows = 2
	s.cfg.SyncWindowSeconds = 200000
	now := s.now().Unix()
	for i := 0; i < 5; i++ {
		mustExec(t, s, "INSERT INTO oauth_handoff_tokens(id,token,account_id,expires_at) VALUES(?,?,?,?)", string(rune('a'+i)), string(rune('a'+i)), ownerID, now-100)
	}
	mustExec(t, s, "INSERT INTO mcp_rate_limits(bucket_key,window_start,count) VALUES('long-active',?,7)", now-100000)
	w := maintenanceCall(s, "GET", "Bearer "+cronSecret, "")
	expectStatus(t, w, 200)
	response := object(t, w)
	if tableCount(t, s, "oauth_handoff_tokens") != 3 || response["more_work"] != true {
		t.Fatal(response)
	}
	if tableCount(t, s, "mcp_rate_limits") != 1 {
		t.Fatal("long active safety quota reset")
	}
}
func TestMaintenanceRollupDefersOverBudgetWithoutPartialWrites(t *testing.T) {
	s := maintenanceFixture(t)
	s.cfg.MaintenanceRollupRows = 1
	now := s.now().Unix()
	hour := now / 3600 * 3600
	mustExec(t, s, "INSERT INTO admin_analytics_hourly(id,hour_start,request_count,success_count,latency_sum_ms,latency_count) VALUES('existing',?,99,99,0,0)", hour)
	for _, id := range []string{"one", "two"} {
		mustExec(t, s, "INSERT INTO request_logs(id,project_id,timestamp,method,status) VALUES(?,?,?,'tools/call','200')", id, projectID, now-10)
	}
	w := maintenanceCall(s, "GET", "Bearer "+cronSecret, "")
	expectStatus(t, w, 200)
	response := object(t, w)
	if response["rollup_deferred"] != true || response["rollup_refreshed"] != false || response["more_work"] != true {
		t.Fatal(response)
	}
	rollup, e := s.one(context.Background(), "SELECT id,request_count FROM admin_analytics_hourly")
	if e != nil || rollup["id"] != "existing" || num(rollup["request_count"]) != 99 {
		t.Fatal("partial aggregate written", rollup, e)
	}
}
func TestMaintenanceRollupRefreshKeepsHistoricAggregates(t *testing.T) {
	s := maintenanceFixture(t)
	now := s.now().Unix()
	mustExec(t, s, "INSERT INTO admin_analytics_hourly(id,hour_start,request_count,success_count,latency_sum_ms,latency_count) VALUES('historic',?,7,6,700,7)", now-10*86400)
	for _, v := range []struct {
		id, code string
		latency  int
	}{{"one", "", 50}, {"two", "-32602", 150}} {
		mustExec(t, s, "INSERT INTO request_logs(id,project_id,timestamp,method,status,error_code,latency_ms) VALUES(?,?,?,'tools/call','200',?,?)", v.id, projectID, now-10, optional(v.code), v.latency)
	}
	expectStatus(t, maintenanceCall(s, "GET", "Bearer "+cronSecret, ""), 200)
	if tableCount(t, s, "admin_analytics_hourly") != 2 {
		t.Fatal("history lost or recent aggregate missing")
	}
	recent, e := s.one(context.Background(), "SELECT request_count,success_count,latency_sum_ms FROM admin_analytics_hourly WHERE id<>'historic'")
	if e != nil || num(recent["request_count"]) != 2 || num(recent["success_count"]) != 1 || num(recent["latency_sum_ms"]) != 200 {
		t.Fatal(recent, e)
	}
}
func TestMaintenanceCleanupTransactionRollsBackOnSchemaFailure(t *testing.T) {
	s := maintenanceFixture(t)
	mustExec(t, s, "INSERT INTO oauth_handoff_tokens(id,token,account_id,expires_at) VALUES('old','old',?,?)", ownerID, s.now().Unix()-100)
	mustExec(t, s, "DROP TABLE sync_jobs")
	expectStatus(t, maintenanceCall(s, "GET", "Bearer "+cronSecret, ""), 503)
	if tableCount(t, s, "oauth_handoff_tokens") != 1 {
		t.Fatal("cleanup partially committed")
	}
}
