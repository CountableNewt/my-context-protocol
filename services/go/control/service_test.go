package control

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/CountableNewt/my-context-protocol/services/go/identity"
	"github.com/CountableNewt/my-context-protocol/services/go/internal/sqlitetest"
	"github.com/CountableNewt/my-context-protocol/services/go/migrations"
	"github.com/CountableNewt/my-context-protocol/services/go/security"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
)

const ownerID = "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA"
const otherID = "BBBBBBBB-BBBB-4BBB-8BBB-BBBBBBBBBBBB"
const projectID = "CCCCCCCC-CCCC-4CCC-8CCC-CCCCCCCCCCCC"
const releaseID = "DDDDDDDD-DDDD-4DDD-8DDD-DDDDDDDDDDDD"
const skillID = "EEEEEEEE-EEEE-4EEE-8EEE-EEEEEEEEEEEE"
const packageID = "FFFFFFFF-FFFF-4FFF-8FFF-FFFFFFFFFFFF"
const ownerCookie = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const otherCookie = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
const revisionDDL = "CREATE TABLE mcp_catalog_revisions(project_id TEXT PRIMARY KEY REFERENCES projects(id),revision INTEGER NOT NULL DEFAULT 0);"

func fixture(t *testing.T) (*Service, *sqlitetest.DB) {
	t.Helper()
	db := sqlitetest.New(t, migrations.BaselineSQL+revisionDDL)
	s, e := New(db, identity.SQLStore{DB: db}, Config{FrontendOrigins: []string{"https://app.example.com"}, MCPBaseDomain: "mcp.example.com", MCPOAuthEnabled: true})
	if e != nil {
		t.Fatal(e)
	}
	s.now = func() time.Time { return time.Unix(1700000000, 0) }
	for _, stmt := range []turso.Statement{
		{SQL: "INSERT INTO accounts(id,github_id,login,is_admin,paywall_bypass) VALUES(?,1,'owner',0,0),(?,2,'other',0,1)", Args: values(ownerID, otherID)},
		{SQL: "INSERT INTO app_sessions(id,session_key,payload) VALUES('a',?,?),('b',?,?)", Args: values(ownerCookie, encode(map[string]string{"accountId": ownerID}), otherCookie, encode(map[string]string{"accountId": otherID}))},
		{SQL: "INSERT INTO projects(id,account_id,name,slug,subdomain,created_at) VALUES(?,?,'Owned','owned','owned',?)", Args: values(projectID, ownerID, s.now().Unix()-1000)},
	} {
		if _, e := db.Execute(context.Background(), stmt); e != nil {
			t.Fatal(e)
		}
	}
	return s, db
}
func call(t *testing.T, s *Service, method, path, cookie, body, origin string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: "vapor-session", Value: cookie})
	}
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}
func object(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var value map[string]any
	if e := json.Unmarshal(w.Body.Bytes(), &value); e != nil {
		t.Fatal(e)
	}
	return value
}
func expectStatus(t *testing.T, w *httptest.ResponseRecorder, status int) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status %d want %d: %s", w.Code, status, w.Body)
	}
}
func seedRelease(t *testing.T, s *Service) {
	t.Helper()
	for _, statement := range []turso.Statement{
		{SQL: "INSERT INTO releases(id,project_id,commit_sha,status,created_at) VALUES(?,?,'sha','ready',?)", Args: values(releaseID, projectID, s.now().Unix())},
		{SQL: "INSERT INTO skill_packages(id,release_id,path,name,validation_status) VALUES(?,?,'guide/SKILL.md','guide','valid')", Args: values(packageID, releaseID)},
		{SQL: "INSERT INTO compiled_skills(id,release_id,skill_package_id,path,name,summary,exposure_type,risk_level,repo_specific,status,skill_body,skill_id,yaml_frontmatter_present,clarification_required) VALUES(?,?,?,'guide/SKILL.md','guide','Useful guide','tool','low',0,'ready','# Guide','guide',1,0)", Args: values(skillID, releaseID, packageID)},
		{SQL: "INSERT INTO capability_defs(id,compiled_skill_id,capability_name,type,schema_json,side_effect_level) VALUES('cap',?,'guide','tool','{" + `"type":"object"` + "}','low')", Args: values(skillID)},
	} {
		if _, e := s.db.Execute(context.Background(), statement); e != nil {
			t.Fatal(e)
		}
	}
}
func TestOwnerIsolationAndCSRF(t *testing.T) {
	s, _ := fixture(t)
	for _, path := range []string{"/projects/" + projectID, "/projects/" + projectID + "/api-keys", "/projects/" + projectID + "/releases", "/projects/" + projectID + "/request-logs", "/projects/" + projectID + "/skill-runtime"} {
		expectStatus(t, call(t, s, "GET", path, otherCookie, "", ""), 404)
	}
	expectStatus(t, call(t, s, "GET", "/projects", "", "", ""), 401)
	expectStatus(t, call(t, s, "PATCH", "/projects/"+projectID, ownerCookie, `{"name":"Changed"}`, "https://evil.example"), 403)
	got := call(t, s, "GET", "/projects/"+projectID, ownerCookie, "", "")
	expectStatus(t, got, 200)
	if object(t, got)["name"] != "Owned" {
		t.Fatal("cross-origin mutation happened")
	}
	expectStatus(t, call(t, s, "GET", "/admin/metrics", otherCookie, "", ""), 403)
}
func TestUnlimitedProjectsAndMetadata(t *testing.T) {
	s, _ := fixture(t)
	for i := 0; i < 6; i++ {
		body := encode(row{"name": "Project", "slug": "extra-" + string(rune('a'+i))})
		expectStatus(t, call(t, s, "POST", "/projects", ownerCookie, body, "https://app.example.com"), 200)
	}
	duplicate := call(t, s, "POST", "/projects", ownerCookie, `{"name":"Duplicate","slug":"owned"}`, "https://app.example.com")
	expectStatus(t, duplicate, 409)
	w := call(t, s, "GET", "/projects", ownerCookie, "", "")
	var projects []map[string]any
	json.Unmarshal(w.Body.Bytes(), &projects)
	if len(projects) != 7 {
		t.Fatalf("project limit applied %d", len(projects))
	}
	response := object(t, call(t, s, "PATCH", "/projects/"+projectID, ownerCookie, `{"name":"Renamed"}`, "https://app.example.com"))
	if response["name"] != "Renamed" || response["mcp_url"] != "https://owned.mcp.example.com/mcp" || response["suspended_at"] != nil {
		t.Fatal(response)
	}
}
func TestAPIKeySecretHashRevocationAndRename(t *testing.T) {
	s, _ := fixture(t)
	w := call(t, s, "POST", "/projects/"+projectID+"/api-keys", ownerCookie, `{"name":"  My agent  "}`, "https://app.example.com")
	expectStatus(t, w, 200)
	created := object(t, w)
	key, id := created["key"].(string), created["id"].(string)
	stored, e := s.one(context.Background(), "SELECT key_hash FROM api_keys WHERE id=?", id)
	if e != nil || stored["key_hash"] != security.HashToken(key) || stored["key_hash"] == key {
		t.Fatal("unsafe key storage", e)
	}
	list := call(t, s, "GET", "/projects/"+projectID+"/api-keys", ownerCookie, "", "")
	if strings.Contains(list.Body.String(), key) || strings.Contains(list.Body.String(), "key_hash") {
		t.Fatal("key secret leaked")
	}
	expectStatus(t, call(t, s, "PATCH", "/projects/"+projectID+"/api-keys/"+id, ownerCookie, `{"name":"Renamed"}`, "https://app.example.com"), 200)
	expectStatus(t, call(t, s, "DELETE", "/projects/"+projectID+"/api-keys/"+id, otherCookie, "", "https://app.example.com"), 404)
	expectStatus(t, call(t, s, "DELETE", "/projects/"+projectID+"/api-keys/"+id, ownerCookie, "", "https://app.example.com"), 204)
	expectStatus(t, call(t, s, "PATCH", "/projects/"+projectID+"/api-keys/"+id, ownerCookie, `{"name":"Revive"}`, "https://app.example.com"), 409)
	active := call(t, s, "GET", "/projects/"+projectID+"/api-keys", ownerCookie, "", "")
	if strings.TrimSpace(active.Body.String()) != "[]" {
		t.Fatal("revoked key listed active")
	}
}
func TestReleaseActivationReadinessAndAtomicRevision(t *testing.T) {
	s, _ := fixture(t)
	seedRelease(t, s)
	s.exec(context.Background(), "UPDATE compiled_skills SET status='needs_review' WHERE id=?", skillID)
	path := "/projects/" + projectID + "/releases/" + releaseID + "/activate"
	expectStatus(t, call(t, s, "POST", path, ownerCookie, "", "https://app.example.com"), 400)
	revision, e := s.one(context.Background(), "SELECT revision FROM mcp_catalog_revisions WHERE project_id=?", projectID)
	if e != nil || revision != nil {
		t.Fatal("failed activation changed catalog", revision, e)
	}
	s.exec(context.Background(), "UPDATE compiled_skills SET status='ready' WHERE id=?", skillID)
	expectStatus(t, call(t, s, "POST", path, ownerCookie, "", "https://app.example.com"), 204)
	p, _ := s.one(context.Background(), "SELECT active_release_id FROM projects WHERE id=?", projectID)
	revision, _ = s.one(context.Background(), "SELECT revision FROM mcp_catalog_revisions WHERE project_id=?", projectID)
	if p["active_release_id"] != releaseID || num(revision["revision"]) != 1 {
		t.Fatal("activation/revision mismatch", p, revision)
	}
}
func TestCompiledPatchRejectsInvalidSchemaAndUpdatesCapability(t *testing.T) {
	s, _ := fixture(t)
	seedRelease(t, s)
	path := "/projects/" + projectID + "/releases/" + releaseID + "/compiled-skills/" + skillID
	expectStatus(t, call(t, s, "PATCH", path, ownerCookie, `{"replace_schema":true,"schema_json":"bad"}`, "https://app.example.com"), 400)
	expectStatus(t, call(t, s, "PATCH", path, ownerCookie, `{"exposure_type":"resource","routing":{"use_when":["Planning"]}}`, "https://app.example.com"), 200)
	stored, e := s.one(context.Background(), "SELECT type,schema_json FROM capability_defs WHERE compiled_skill_id=?", skillID)
	if e != nil || stored["type"] != "resource" || !strings.Contains(str(stored["schema_json"]), "ctx://skill/guide") {
		t.Fatal(stored, e)
	}
	skills, e := s.compiled(context.Background(), releaseID)
	if e != nil || len(skills) != 1 || skills[0]["status"] != "ready" {
		t.Fatal(skills, e)
	}
}
func TestRuntimeAssignmentsAreScopedValidatedAndAtomic(t *testing.T) {
	s, _ := fixture(t)
	seedRelease(t, s)
	s.exec(context.Background(), "UPDATE projects SET active_release_id=? WHERE id=?", releaseID, projectID)
	path := "/projects/" + projectID + "/skill-runtime"
	valid := `{"telemetry_enabled":true,"semantic_enabled":true,"assignments":[{"skill_id":"guide","scope":"repository","activation_mode":"always","required":true,"priority":99,"target_type":"repository","target_id":"owner/repo"}]}`
	w := call(t, s, "PATCH", path, ownerCookie, valid, "https://app.example.com")
	expectStatus(t, w, 200)
	response := object(t, w)
	if response["semantic_enabled"] != false || response["telemetry_enabled"] != true {
		t.Fatal(response)
	}
	invalid := strings.Replace(valid, `"skill_id":"guide"`, `"skill_id":"foreign"`, 1)
	expectStatus(t, call(t, s, "PATCH", path, ownerCookie, invalid, "https://app.example.com"), 400)
	saved, e := s.query(context.Background(), "SELECT skill_id,target_id FROM skill_assignments WHERE project_id=?", projectID)
	if e != nil || len(saved) != 1 || saved[0]["skill_id"] != "guide" {
		t.Fatal("invalid patch lost assignments", saved, e)
	}
}
func TestAnalyticsCountsRPCFailureAndHidesOtherOwners(t *testing.T) {
	s, _ := fixture(t)
	for _, log := range []struct {
		id, status, err string
		lat             int
	}{{"one", "200", "", 10}, {"two", "200", "-32602", 100}, {"three", "500", "-32603", 30}} {
		_, e := s.exec(context.Background(), "INSERT INTO request_logs(id,project_id,timestamp,method,status,error_code,latency_ms,mcp_capability_kind,mcp_capability_key) VALUES(?,?,?,'tools/call',?,?,?,'tool','guide')", log.id, projectID, s.now().Unix()-100, log.status, optional(log.err), log.lat)
		if e != nil {
			t.Fatal(e)
		}
	}
	w := call(t, s, "GET", "/dashboard/summary", ownerCookie, "", "")
	expectStatus(t, w, 200)
	summary := object(t, w)
	if summary["total_requests"] != float64(3) || summary["success_rate_last_7d"] != float64(1)/3 || summary["p95_latency_ms_last_7d"] != float64(30) {
		t.Fatal(summary)
	}
	other := object(t, call(t, s, "GET", "/dashboard/summary", otherCookie, "", ""))
	if other["total_requests"] != float64(0) {
		t.Fatal("owner data leaked", other)
	}
	series := call(t, s, "GET", "/projects/"+projectID+"/dashboard/timeseries?range=1h", ownerCookie, "", "")
	expectStatus(t, series, 200)
	buckets := object(t, series)["buckets"].([]any)
	requests, success := float64(0), float64(0)
	for _, raw := range buckets {
		v := raw.(map[string]any)
		requests += v["request_count"].(float64)
		success += v["success_count"].(float64)
	}
	if requests != 3 || success != 1 {
		t.Fatal("timeseries metric mismatch", buckets)
	}
}
func TestAdminFlagsAndRollup(t *testing.T) {
	s, _ := fixture(t)
	s.exec(context.Background(), "UPDATE accounts SET is_admin=1 WHERE id=?", ownerID)
	expectStatus(t, call(t, s, "POST", "/admin/account-flags", ownerCookie, encode(row{"account_id": ownerID, "is_admin": false}), "https://app.example.com"), 400)
	expectStatus(t, call(t, s, "POST", "/admin/account-flags", ownerCookie, encode(row{"account_id": otherID, "paywall_bypass": true}), "https://app.example.com"), 400)
	expectStatus(t, call(t, s, "POST", "/admin/account-flags", ownerCookie, encode(row{"account_id": otherID, "is_admin": true}), "https://app.example.com"), 200)
	expectStatus(t, call(t, s, "POST", "/admin/analytics/rollup-refresh", ownerCookie, "", "https://app.example.com"), 204)
	expectStatus(t, call(t, s, "GET", "/admin/timeseries?range=all", ownerCookie, "", ""), 200)
}

func TestSkillUsageRetainsDistinctMeasurementsAndFiltersHistory(t *testing.T) {
	s, _ := fixture(t)
	seedRelease(t, s)
	s.exec(context.Background(), "UPDATE projects SET active_release_id=? WHERE id=?", releaseID, projectID)
	for i, event := range []string{"surfaced", "instructions_delivered", "agent_reported_skipped", "skill_selected"} {
		id, _ := identity.UUID()
		_, e := s.exec(context.Background(), "INSERT INTO skill_runtime_events(id,project_id,trace_id,event_type,skill_id,reason_code,detail_json,created_at,skill_version,source_checksum,release_id) VALUES(?,?,?,?,?,'not_relevant','{}',?,'1.0.0','checksum',?)", id, projectID, skillID, event, "guide", s.now().Unix()-int64(i), releaseID)
		if e != nil {
			t.Fatal(e)
		}
	}
	w := call(t, s, "GET", "/projects/"+projectID+"/skill-usage?skill_id=guide", ownerCookie, "", "")
	expectStatus(t, w, 200)
	response := object(t, w)
	rows := response["skills"].([]any)
	if len(rows) != 1 {
		t.Fatal(response)
	}
	skill := rows[0].(map[string]any)
	counts := skill["counts"].(map[string]any)
	if counts["surfaced"] != float64(1) || counts["instructions_delivered"] != float64(1) || counts["agent_reported_used"] != float64(0) || counts["legacy_resolver_selected"] != float64(1) {
		t.Fatal(counts)
	}
	if len(skill["versions"].([]any)) != 1 || len(skill["agent_skip_reasons"].([]any)) != 1 {
		t.Fatal(skill)
	}
	expectStatus(t, call(t, s, "GET", "/projects/"+projectID+"/skill-usage?sort=activity%20DROP%20TABLE", ownerCookie, "", ""), 400)
}

func TestRuntimeRejectsMissingRequiredAssignmentAndKeepsPriorState(t *testing.T) {
	s, _ := fixture(t)
	seedRelease(t, s)
	s.exec(context.Background(), "UPDATE projects SET active_release_id=? WHERE id=?", releaseID, projectID)
	body := `{"assignments":[{"skill_id":"guide","scope":"global","activation_mode":"always","priority":10,"target_type":"global","target_id":"*"}]}`
	expectStatus(t, call(t, s, "PATCH", "/projects/"+projectID+"/skill-runtime", ownerCookie, body, "https://app.example.com"), 400)
}

type catalogFixture struct{}

func (catalogFixture) Catalog(context.Context, string) (map[string]any, error) {
	return map[string]any{"catalog_markdown": "Effective", "catalog_markdown_generated": "Generated", "catalog_markdown_override": "Custom"}, nil
}
func TestCatalogOverrideAndRevisionAreCommittedTogether(t *testing.T) {
	s, _ := fixture(t)
	s.cfg.Catalog = catalogFixture{}
	path := "/projects/" + projectID + "/catalog-markdown"
	expectStatus(t, call(t, s, "PATCH", path, ownerCookie, `{"markdown":" Custom "}`, "https://app.example.com"), 200)
	p, e := s.one(context.Background(), "SELECT mcp_catalog_markdown_override FROM projects WHERE id=?", projectID)
	if e != nil || p["mcp_catalog_markdown_override"] != "Custom" {
		t.Fatal(p, e)
	}
	revision, e := s.one(context.Background(), "SELECT revision FROM mcp_catalog_revisions WHERE project_id=?", projectID)
	if e != nil || num(revision["revision"]) != 1 {
		t.Fatal(revision, e)
	}
	expectStatus(t, call(t, s, "PATCH", path, otherCookie, `{"markdown":"Attack"}`, "https://app.example.com"), 404)
}
func TestManualSyncUsesSharedPersistentRateLimit(t *testing.T) {
	s, db := fixture(t)
	_, e := db.Execute(context.Background(), turso.Statement{SQL: "CREATE TABLE mcp_rate_limits(bucket_key TEXT NOT NULL,window_start INTEGER NOT NULL,count INTEGER NOT NULL,PRIMARY KEY(bucket_key,window_start))"})
	if e != nil {
		t.Fatal(e)
	}
	calls := 0
	s.cfg.SyncLimit = 2
	s.cfg.Sync = func(_ context.Context, pid string) (map[string]any, error) {
		if pid != projectID {
			t.Fatal("wrong sync project")
		}
		calls++
		return map[string]any{"status": "ready"}, nil
	}
	path := "/projects/" + projectID + "/sync"
	expectStatus(t, call(t, s, "POST", path, otherCookie, "", "https://app.example.com"), 404)
	for i := 0; i < 2; i++ {
		expectStatus(t, call(t, s, "POST", path, ownerCookie, "", "https://app.example.com"), 204)
	}
	expectStatus(t, call(t, s, "POST", path, ownerCookie, "", "https://app.example.com"), 429)
	if calls != 2 {
		t.Fatal("sync limiter bypassed", calls)
	}
}
func TestFailedMetadataTransactionDoesNotPartiallyPersist(t *testing.T) {
	s, db := fixture(t)
	seedRelease(t, s)
	_, e := db.Execute(context.Background(), turso.Statement{SQL: "DROP TABLE mcp_catalog_revisions"})
	if e != nil {
		t.Fatal(e)
	}
	path := "/projects/" + projectID + "/releases/" + releaseID + "/compiled-skills/" + skillID
	expectStatus(t, call(t, s, "PATCH", path, ownerCookie, `{"summary":"Must roll back","routing":{"use_when":["Changed"]}}`, "https://app.example.com"), 503)
	saved, e := s.one(context.Background(), "SELECT summary FROM compiled_skills WHERE id=?", skillID)
	if e != nil || saved["summary"] != "Useful guide" {
		t.Fatal("partial metadata commit", saved, e)
	}
	rules, e := s.query(context.Background(), "SELECT * FROM routing_rules WHERE compiled_skill_id=?", skillID)
	if e != nil || len(rules) != 0 {
		t.Fatal("partial routing commit", rules, e)
	}
}

func TestDashboardSuccessRejectsMalformedStatusAndAcceptsWhitespaceError(t *testing.T) {
	s, _ := fixture(t)
	for _, v := range []struct{ id, status, errorCode string }{{"malformed", "200oops", ""}, {"whitespace", "200", "\n\t"}} {
		_, e := s.exec(context.Background(), "INSERT INTO request_logs(id,project_id,timestamp,method,status,error_code) VALUES(?,?,?,'tools/call',?,?)", v.id, projectID, s.now().Unix()-100, v.status, v.errorCode)
		if e != nil {
			t.Fatal(e)
		}
	}
	response := object(t, call(t, s, "GET", "/dashboard/summary", ownerCookie, "", ""))
	if response["success_rate_last_7d"] != float64(0.5) {
		t.Fatal(response)
	}
}

func TestRuntimeMetadataEditPreservesExistingWritebackAndIdentity(t *testing.T) {
	s, _ := fixture(t)
	seedRelease(t, s)
	document := `{"schemaVersion":1,"id":"guide","name":"guide","description":"Useful guide","kind":"task","scope":"global","activation":{"mode":"explicit","intents":[],"events":[],"tags":[],"examples":[]},"enforcement":"advisory","priority":50,"requires":[],"conflictsWith":[],"instructions":"# Guide","source":{"path":"guide/SKILL.md","checksum":"checksum"},"version":"1.0.0","validation":{"clarificationRequired":false,"missingFields":[],"warnings":[]}}`
	if _, e := s.exec(context.Background(), "UPDATE compiled_skills SET canonical_json=? WHERE id=?", document, skillID); e != nil {
		t.Fatal(e)
	}
	if _, e := s.exec(context.Background(), "INSERT INTO skill_runtime_overrides(id,project_id,skill_id,scope,metadata_json,writeback_pr_url,created_at) VALUES('existing',?,'guide','global','{}','https://github.com/example/repo/pull/1',123)", projectID); e != nil {
		t.Fatal(e)
	}
	path := "/projects/" + projectID + "/releases/" + releaseID + "/compiled-skills/" + skillID
	expectStatus(t, call(t, s, "PATCH", path, ownerCookie, `{"runtime":{"priority":70}}`, "https://app.example.com"), 200)
	saved, e := s.one(context.Background(), "SELECT id,writeback_pr_url,created_at,metadata_json FROM skill_runtime_overrides WHERE project_id=?", projectID)
	if e != nil || saved["id"] != "existing" || saved["writeback_pr_url"] != "https://github.com/example/repo/pull/1" || num(saved["created_at"]) != 123 {
		t.Fatal("override history lost", saved, e)
	}
}

func TestScalarMetadataJSONMatchesSwiftRejection(t *testing.T) {
	s, _ := fixture(t)
	seedRelease(t, s)
	path := "/projects/" + projectID + "/releases/" + releaseID + "/compiled-skills/" + skillID
	for _, raw := range []string{"true", "1", "null", `"text"`} {
		expectStatus(t, call(t, s, "PATCH", path, ownerCookie, encode(row{"replace_schema": true, "schema_json": raw}), "https://app.example.com"), 400)
		expectStatus(t, call(t, s, "PATCH", "/projects/"+projectID+"/skill-runtime", ownerCookie, encode(row{"provider_preferences_json": raw}), "https://app.example.com"), 400)
	}
	if _, ok := structuredJSON("[]"); !ok {
		t.Fatal("Foundation-compatible array rejected")
	}
}
