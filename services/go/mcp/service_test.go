package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CountableNewt/my-context-protocol/services/go/internal/sqlitetest"
	"github.com/CountableNewt/my-context-protocol/services/go/migrations"
	skillruntime "github.com/CountableNewt/my-context-protocol/services/go/runtime"
	"github.com/CountableNewt/my-context-protocol/services/go/security"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
)

const accountID = "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA"
const projectID = "BBBBBBBB-BBBB-4BBB-8BBB-BBBBBBBBBBBB"
const otherProjectID = "CCCCCCCC-CCCC-4CCC-8CCC-CCCCCCCCCCCC"
const releaseID = "DDDDDDDD-DDDD-4DDD-8DDD-DDDDDDDDDDDD"
const keyID = "EEEEEEEE-EEEE-4EEE-8EEE-EEEEEEEEEEEE"
const packageID = "FFFFFFFF-FFFF-4FFF-8FFF-FFFFFFFFFFFF"
const skillRowID = "11111111-1111-4111-8111-111111111111"

func fixture(t *testing.T) (*Service, SQLStore, *sqlitetest.DB) {
	t.Helper()
	db := sqlitetest.New(t, migrations.BaselineSQL+MigrationSQL)
	document := skillruntime.Document{SchemaVersion: 1, ID: "test-skill", Name: "Test Skill", Description: "Deployment guidance", Kind: "operating", Scope: "global", Activation: skillruntime.Activation{Mode: "always", Intents: []string{}, Events: []string{}, Tags: []string{}, Examples: []string{}}, Enforcement: "required", Requires: []skillruntime.Requirement{}, ConflictsWith: []string{}, Instructions: "# Test\nApply guidance.", Source: skillruntime.Source{Path: "test-skill/SKILL.md", Checksum: security.HashToken("body")}, Version: "v1", Validation: skillruntime.Validation{MissingFields: []string{}, Warnings: []string{}}}
	canonical, _ := json.Marshal(document)
	statements := []turso.Statement{
		{SQL: `INSERT INTO accounts(id,github_id,login) VALUES(?,123,'sam')`, Args: []turso.Value{turso.Text(accountID)}},
		{SQL: `INSERT INTO projects(id,account_id,name,slug,subdomain,active_release_id) VALUES(?,?,'Test','test','tenant',?)`, Args: []turso.Value{turso.Text(projectID), turso.Text(accountID), turso.Text(releaseID)}},
		{SQL: `INSERT INTO projects(id,account_id,name,slug,subdomain) VALUES(?,?,'Other','other','other')`, Args: []turso.Value{turso.Text(otherProjectID), turso.Text(accountID)}},
		{SQL: `INSERT INTO releases(id,project_id,commit_sha,status) VALUES(?,?,'commit','ready')`, Args: []turso.Value{turso.Text(releaseID), turso.Text(projectID)}},
		{SQL: `INSERT INTO api_keys(id,project_id,key_prefix,key_hash,status) VALUES(?,?,'test',?,'active')`, Args: []turso.Value{turso.Text(keyID), turso.Text(projectID), turso.Text(security.HashToken("test-key"))}},
		{SQL: `INSERT INTO skill_packages(id,release_id,path,name,validation_status) VALUES(?,?,'test-skill','Test Skill','ready')`, Args: []turso.Value{turso.Text(packageID), turso.Text(releaseID)}},
		{SQL: `INSERT INTO compiled_skills(id,release_id,skill_package_id,path,name,summary,skill_body,exposure_type,risk_level,repo_specific,status,skill_id,version,source_checksum,canonical_json,clarification_required) VALUES(?,?,?,'test-skill/SKILL.md','Test Skill','Deployment guidance','# Test body','tool','low',0,'ready','test-skill','v1',?,?,0)`, Args: []turso.Value{turso.Text(skillRowID), turso.Text(releaseID), turso.Text(packageID), turso.Text(document.Source.Checksum), turso.Text(string(canonical))}},
		{SQL: `INSERT INTO capability_defs(id,compiled_skill_id,capability_name,type,schema_json,side_effect_level) VALUES('cap-tool',?,'test-skill','tool','{"type":"object","properties":{"detail":{"type":"string"}},"additionalProperties":false}','none')`, Args: []turso.Value{turso.Text(skillRowID)}},
		{SQL: `INSERT INTO capability_defs(id,compiled_skill_id,capability_name,type,schema_json,side_effect_level) VALUES('cap-resource',?,'test-skill','resource','{"uri":"ctx://skill/test-skill","mimeType":"text/markdown","use_when":["Deployment"]}','none')`, Args: []turso.Value{turso.Text(skillRowID)}},
		{SQL: `INSERT INTO capability_defs(id,compiled_skill_id,capability_name,type,schema_json,side_effect_level) VALUES('cap-prompt',?,'test-skill','prompt','{}','none')`, Args: []turso.Value{turso.Text(skillRowID)}},
		{SQL: `INSERT INTO routing_rules(id,compiled_skill_id,use_when_json) VALUES('rule',?,'["Deployment"]')`, Args: []turso.Value{turso.Text(skillRowID)}},
		{SQL: `INSERT INTO skill_package_files(id,skill_package_id,path,content,content_type,byte_count,checksum) VALUES('file',?,'references/a.txt',?,'text/plain',4,?)`, Args: []turso.Value{turso.Text(packageID), turso.Blob([]byte("file")), turso.Text(security.HashToken("file"))}},
		{SQL: `INSERT INTO project_runtime_settings(id,project_id,telemetry_enabled,telemetry_retention_days,semantic_enabled,feedback_issue_creation_enabled,provider_preferences_json) VALUES('settings',?,1,30,0,1,'{"legacy_compiled_tools_enabled":true}')`, Args: []turso.Value{turso.Text(projectID)}},
		{SQL: `INSERT INTO mcp_catalog_revisions(project_id,revision) VALUES(?,1)`, Args: []turso.Value{turso.Text(projectID)}},
	}
	if _, err := db.Batch(context.Background(), statements); err != nil {
		t.Fatal(err)
	}
	store := SQLStore{DB: db}
	service, err := New(Config{Path: "/mcp", BaseDomain: "mcp.example.com", RequireTenantHost: true, AllowedOrigins: []string{"https://app.example.com"}, FrontendOrigin: "https://app.example.com", OAuthEnabled: true}, store)
	if err != nil {
		t.Fatal(err)
	}
	return service, store, db
}
func invoke(t *testing.T, service *Service, method string, params string, version string) *httptest.ResponseRecorder {
	t.Helper()
	if params == "" {
		params = "{}"
	}
	request := httptest.NewRequest("POST", "https://tenant.mcp.example.com/mcp", strings.NewReader(fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":%q,"params":%s}`, method, params)))
	request.Header.Set("X-API-Key", "test-key")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	if version != "" {
		request.Header.Set("MCP-Protocol-Version", version)
	}
	response := httptest.NewRecorder()
	service.ServeHTTP(response, request)
	return response
}
func decoded(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &value); err != nil {
		t.Fatalf("decode status %d: %s", w.Code, w.Body)
	}
	return value
}
func result(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	return decoded(t, w)["result"].(map[string]any)
}
func TestTenantCredentialBindingAndChallenge(t *testing.T) {
	service, _, db := fixture(t)
	request := httptest.NewRequest("POST", "https://other.mcp.example.com/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
	request.Header.Set("Authorization", "Bearer test-key")
	request.Header.Set("X-Forwarded-Host", "tenant.mcp.example.com")
	response := httptest.NewRecorder()
	service.ServeHTTP(response, request)
	if response.Code != 403 {
		t.Fatalf("tenant/forwarded-host bypass: %d %s", response.Code, response.Body)
	}
	request = httptest.NewRequest("POST", "https://tenant.mcp.example.com/mcp", strings.NewReader(`{}`))
	response = httptest.NewRecorder()
	service.ServeHTTP(response, request)
	if response.Code != 401 || !strings.Contains(response.Header().Get("WWW-Authenticate"), "resource_metadata") {
		t.Fatal("OAuth challenge missing")
	}
	if _, err := db.Execute(context.Background(), turso.Statement{SQL: "UPDATE api_keys SET status='revoked' WHERE id=?", Args: []turso.Value{turso.Text(keyID)}}); err != nil {
		t.Fatal(err)
	}
	if invoke(t, service, "initialize", "{}", "").Code != 401 {
		t.Fatal("revoked API key accepted")
	}
}
func TestJSONRPCHeadersNotificationsAndRichVersionCompatibility(t *testing.T) {
	service, _, _ := fixture(t)
	for _, version := range []string{"", "2024-11-05", "2025-03-26", "2025-06-18", "2025-11-25"} {
		list := result(t, invoke(t, service, "tools/list", "{}", version))
		items := list["tools"].([]any)
		if len(items) != 5 {
			t.Fatalf("runtime/legacy tool list count %d", len(items))
		}
		first := items[0].(map[string]any)
		_, hasOutput := first["outputSchema"]
		if hasOutput != rich(version) {
			t.Fatalf("rich schema leaked to %s", version)
		}
		get := result(t, invoke(t, service, "tools/call", `{"name":"get_skill","arguments":{"skill_id":"test-skill"}}`, version))
		_, hasStructured := get["structuredContent"]
		if hasStructured != rich(version) {
			t.Fatalf("rich result mismatch %s", version)
		}
	}
	if invoke(t, service, "initialize", "{}", "2099-01-01").Code != 400 {
		t.Fatal("unsupported header version accepted")
	}
	initialize := result(t, invoke(t, service, "initialize", `{"protocolVersion":"unknown"}`, ""))
	if initialize["protocolVersion"] != "2025-11-25" {
		t.Fatal("initialize negotiation changed")
	}
	for _, body := range []string{`not json`, `[]`, `{"jsonrpc":"1.0","id":1,"method":"initialize"}`, `{"jsonrpc":"2.0","id":true,"method":"initialize"}`, `{"jsonrpc":"2.0","id":null,"method":"initialize"}`, `{"jsonrpc":"2.0","method":"initialize"}`, `{"jsonrpc":"2.0","id":1,"method":"notifications/initialized"}`} {
		req := httptest.NewRequest("POST", "https://tenant.mcp.example.com/mcp", strings.NewReader(body))
		req.Header.Set("X-API-Key", "test-key")
		w := httptest.NewRecorder()
		service.ServeHTTP(w, req)
		if w.Code != 400 {
			t.Errorf("malformed envelope accepted %s status %d", body, w.Code)
		}
	}
	req := httptest.NewRequest("POST", "https://tenant.mcp.example.com/mcp", strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
	req.Header.Set("X-API-Key", "test-key")
	w := httptest.NewRecorder()
	service.ServeHTTP(w, req)
	if w.Code != 202 || w.Body.Len() != 0 {
		t.Fatal("notification must have no response payload")
	}
}
func TestResourcePromptAndSafeFiles(t *testing.T) {
	service, _, _ := fixture(t)
	read := result(t, invoke(t, service, "resources/read", `{"uri":"ctx://skill/test-skill?version=v1"}`, ""))
	contents := read["contents"].([]any)
	if contents[0].(map[string]any)["text"] != "# Test\nApply guidance." {
		t.Fatal("canonical instructions not returned")
	}
	file := result(t, invoke(t, service, "resources/read", `{"uri":"ctx://skill/test-skill/file/references/a.txt?version=v1"}`, ""))
	if file["contents"].([]any)[0].(map[string]any)["text"] != "file" {
		t.Fatal("package file mismatch")
	}
	if invoke(t, service, "resources/read", `{"uri":"ctx://skill/test-skill/file/%2e%2e/secret"}`, "").Code != 400 {
		t.Fatal("traversal accepted")
	}
	prompt := result(t, invoke(t, service, "prompts/get", `{"name":"test-skill","arguments":{"context":"deployment"}}`, ""))
	message := prompt["messages"].([]any)[0].(map[string]any)["content"].(map[string]any)["text"].(string)
	if !strings.Contains(message, `context: "deployment"`) || !strings.Contains(message, "# Test body") {
		t.Fatal("prompt context/body lost")
	}
	wrong := result(t, invoke(t, service, "tools/call", `{"name":"get_skill","arguments":{"skill_id":"test-skill","version":"wrong"}}`, "2025-11-25"))
	if wrong["isError"] != true || wrong["structuredContent"] == nil {
		t.Fatal("unknown version returned empty success or lost rich error")
	}
}
func TestCrossReleasePaginationAndReservedRuntimeNames(t *testing.T) {
	service, _, db := fixture(t)
	for i := 0; i < 53; i++ {
		if _, err := db.Execute(context.Background(), turso.Statement{SQL: "INSERT INTO capability_defs(id,compiled_skill_id,capability_name,type,schema_json,side_effect_level) VALUES(?,?,?,'tool','{}','none')", Args: []turso.Value{turso.Text(fmt.Sprint("extra", i)), turso.Text(skillRowID), turso.Text(fmt.Sprintf("tool-%02d", i))}}); err != nil {
			t.Fatal(err)
		}
	}
	first := result(t, invoke(t, service, "tools/list", "{}", ""))
	cursor := first["nextCursor"].(string)
	if len(first["tools"].([]any)) != 50 {
		t.Fatal("page size changed")
	}
	second := result(t, invoke(t, service, "tools/list", fmt.Sprintf(`{"cursor":%q}`, cursor), ""))
	if len(second["tools"].([]any)) != 8 {
		t.Fatal("second page incorrect")
	}
	if invoke(t, service, "resources/list", fmt.Sprintf(`{"cursor":%q}`, cursor), "").Code != 400 {
		t.Fatal("cross-list cursor accepted")
	}
	if _, err := db.Execute(context.Background(), turso.Statement{SQL: "UPDATE projects SET active_release_id=NULL WHERE id=?", Args: []turso.Value{turso.Text(projectID)}}); err != nil {
		t.Fatal(err)
	}
	if invoke(t, service, "tools/list", fmt.Sprintf(`{"cursor":%q}`, cursor), "").Code != 400 {
		t.Fatal("stale release cursor accepted")
	}
}
func TestDurableSubscriptionAndRateLimit(t *testing.T) {
	_, store, db := fixture(t)
	p, err := store.Authenticate(context.Background(), "test-key", true, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Execute(context.Background(), turso.Statement{SQL: `WITH RECURSIVE n(x) AS(VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<256) INSERT INTO mcp_resource_subscriptions(project_id,subscriber_id,uri,created_at) SELECT ?,?,'ctx://item/'||x,1 FROM n`, Args: []turso.Value{turso.Text(projectID), turso.Text(keyID)}})
	if err != nil {
		t.Fatal(err)
	}
	if allowed, err := store.Subscribe(context.Background(), p, "ctx://item/new", true); err != nil || allowed {
		t.Fatal("subscription limit not enforced")
	}
	if allowed, err := store.Subscribe(context.Background(), p, "ctx://item/1", true); err != nil || !allowed {
		t.Fatal("existing subscription not idempotent")
	}
	for i := 0; i < 3; i++ {
		allowed, err := (SQLStore{DB: db}).RateLimit(context.Background(), "ip", 2, time.Unix(120, 0))
		if err != nil || allowed != (i < 2) {
			t.Fatalf("durable limit %d: %t %v", i, allowed, err)
		}
	}
	allowed, err := store.RateLimit(context.Background(), "ip", 2, time.Unix(180, 0))
	if err != nil || !allowed {
		t.Fatal("fixed window not reset")
	}
}
func TestUsageReportingIsAtomicIdempotentAndConsentGated(t *testing.T) {
	service, store, db := fixture(t)
	snapshot, err := store.Snapshot(context.Background(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	args := map[string]json.RawMessage{}
	_ = json.Unmarshal([]byte(`{"report_id":"report","skills":[{"skill_id":"test-skill","version":"v1","outcome":"used"}]}`), &args)
	var wg sync.WaitGroup
	errorsCh := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := store.Usage(context.Background(), snapshot, "apikey:"+keyID, args)
			errorsCh <- err
		}()
	}
	wg.Wait()
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	rows, err := query(context.Background(), db, "SELECT COUNT(*) AS count FROM skill_runtime_events WHERE event_type='agent_reported_used'")
	if err != nil || number(rows[0], "count") != 1 {
		t.Fatalf("duplicate usage events: %+v %v", rows, err)
	}
	duplicate, err := store.Usage(context.Background(), snapshot, "apikey:"+keyID, args)
	if err != nil || duplicate.(map[string]any)["status"] != "already_recorded" {
		t.Fatal("retry not idempotent")
	}
	changed := map[string]json.RawMessage{}
	_ = json.Unmarshal([]byte(`{"report_id":"report","skills":[{"skill_id":"test-skill","version":"v1","outcome":"skipped","skip_reason":"other"}]}`), &changed)
	if _, err = store.Usage(context.Background(), snapshot, "apikey:"+keyID, changed); err == nil {
		t.Fatal("same report ID accepted different payload")
	}
	if _, err = db.Execute(context.Background(), turso.Statement{SQL: "UPDATE project_runtime_settings SET telemetry_enabled=0 WHERE project_id=?", Args: []turso.Value{turso.Text(projectID)}}); err != nil {
		t.Fatal(err)
	}
	disabled := result(t, invoke(t, service, "tools/call", `{"name":"report_skill_usage","arguments":{"report_id":"new","skills":[{"skill_id":"test-skill","version":"v1","outcome":"used"}]}}`, "2025-11-25"))
	if disabled["structuredContent"].(map[string]any)["status"] != "collection_disabled" {
		t.Fatal("disabled consent ignored")
	}
}
func TestFeedbackPersistsDraftWithoutClaimingExternalAction(t *testing.T) {
	service, _, db := fixture(t)
	feedback := result(t, invoke(t, service, "tools/call", `{"name":"report_skill_feedback","arguments":{"skill_id":"test-skill","version":"v1","category":"incorrect_instruction","summary":"wrong command","evidence":"reproduction","create_issue":true}}`, "2025-11-25"))
	structured := feedback["structuredContent"].(map[string]any)
	if structured["effectStatus"] != "draft" || structured["creationAuthorized"] != true || !strings.Contains(structured["message"].(string), "harness must execute") {
		t.Fatal("feedback claimed external action")
	}
	rows, err := query(context.Background(), db, "SELECT COUNT(*) AS count FROM skill_feedback WHERE project_id=?", turso.Text(projectID))
	if err != nil || number(rows[0], "count") != 1 {
		t.Fatal("feedback not persisted")
	}
}

type revisionStore struct {
	Store
	calls int
}

func (s *revisionStore) Revision(context.Context, string) (int64, error) {
	s.calls++
	if s.calls < 3 {
		return 1, nil
	}
	return 2, nil
}
func TestSSEPollsDurableRevisionAndReconnectsWithinBound(t *testing.T) {
	service, store, _ := fixture(t)
	service.store = &revisionStore{Store: store}
	service.config.StreamDuration = 15 * time.Millisecond
	service.config.PollInterval = time.Millisecond
	req := httptest.NewRequest("GET", "https://tenant.mcp.example.com/mcp", nil)
	req.Header.Set("X-API-Key", "test-key")
	req.Header.Set("Accept", "text/event-stream")
	w := httptest.NewRecorder()
	service.ServeHTTP(w, req)
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("SSE failed %d %s", w.Code, w.Body)
	}
	for _, expected := range []string{"retry: 3000", "notifications/tools/list_changed", "notifications/resources/list_changed", "notifications/prompts/list_changed", ":reconnect"} {
		if !strings.Contains(w.Body.String(), expected) {
			t.Errorf("missing SSE %s", expected)
		}
	}
}

func TestOAuthCredentialExpiryRevocationScopeAndClientBinding(t *testing.T) {
	_, store, db := fixture(t)
	ctx := context.Background()
	now := time.Now()
	statements := []turso.Statement{{SQL: `INSERT INTO mcp_oauth_clients(id,public_client_id,is_confidential,redirect_uris_json,allowed_grants,status,project_id) VALUES('oauth-client','public',0,'[]','authorization_code','active',?)`, Args: []turso.Value{turso.Text(projectID)}}, {SQL: `INSERT INTO mcp_oauth_access_tokens(id,token_hash,project_id,mcp_oauth_client_id,account_id,subject_type,scope,expires_at) VALUES('oauth-token',?,?,'oauth-client',?,'user','mcp:invoke',?)`, Args: []turso.Value{turso.Text(security.HashToken("mcp_oat_test")), turso.Text(projectID), turso.Text(accountID), turso.Integer(now.Unix() + 60)}}}
	if _, err := db.Batch(ctx, statements); err != nil {
		t.Fatal(err)
	}
	principal, err := store.Authenticate(ctx, "mcp_oat_test", true, now)
	if err != nil || principal.Project.ID != projectID || principal.Kind != "oauth_user" || principal.ClientIdentity != "oauth:oauth-client:user:"+accountID {
		t.Fatalf("OAuth principal incorrect %+v %v", principal, err)
	}
	if _, err = store.Authenticate(ctx, "mcp_oat_test", false, now); !errors.Is(err, ErrCredential) {
		t.Fatal("OAuth token accepted when disabled")
	}
	if _, err = store.Authenticate(ctx, "mcp_oat_test", true, now.Add(time.Minute)); !errors.Is(err, ErrCredential) {
		t.Fatal("expired token accepted")
	}
	for _, sql := range []string{"UPDATE mcp_oauth_access_tokens SET scope='other' WHERE id='oauth-token'", "UPDATE mcp_oauth_access_tokens SET scope='mcp:invoke',revoked_at=1 WHERE id='oauth-token'", "UPDATE mcp_oauth_access_tokens SET revoked_at=NULL WHERE id='oauth-token';"} {
		if _, err = db.Execute(ctx, turso.Statement{SQL: sql}); err != nil {
			t.Fatal(err)
		}
		_, err = store.Authenticate(ctx, "mcp_oat_test", true, now)
		if strings.Contains(sql, "revoked_at=NULL") {
			if err != nil {
				t.Fatal(err)
			}
		} else if !errors.Is(err, ErrCredential) {
			t.Fatal("invalid token accepted")
		}
	}
	if _, err = db.Execute(ctx, turso.Statement{SQL: "UPDATE mcp_oauth_clients SET status='revoked' WHERE id='oauth-client'"}); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Authenticate(ctx, "mcp_oat_test", true, now); !errors.Is(err, ErrCredential) {
		t.Fatal("revoked OAuth client still authenticates")
	}
}
func TestCatalogDashboardAdapterAndFileIntegrity(t *testing.T) {
	service, _, db := fixture(t)
	catalog, err := service.Catalog(context.Background(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	if catalog["release_id"] != releaseID || catalog["release_status"] != "ready" || catalog["mcp_url"] != "https://tenant.mcp.example.com/mcp" {
		t.Fatalf("wrong dashboard catalog metadata %+v", catalog)
	}
	if len(catalog["tools"].([]map[string]any)) != 5 || !strings.Contains(catalog["catalog_markdown_generated"].(string), "Test Skill") {
		t.Fatal("dashboard catalog missing actual capabilities")
	}
	if _, err = db.Execute(context.Background(), turso.Statement{SQL: "UPDATE skill_package_files SET content=? WHERE id='file'", Args: []turso.Value{turso.Blob([]byte("evil"))}}); err != nil {
		t.Fatal(err)
	}
	if invoke(t, service, "resources/read", `{"uri":"ctx://skill/test-skill/file/references/a.txt"}`, "").Code != 400 {
		t.Fatal("corrupt package bytes served")
	}
}
func TestOriginAndMediaValidation(t *testing.T) {
	service, _, _ := fixture(t)
	for _, header := range []struct{ key, value string }{{"Origin", "https://evil.example.com"}, {"Accept", "application/json"}, {"Content-Type", "text/plain"}} {
		req := httptest.NewRequest("POST", "https://tenant.mcp.example.com/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
		req.Header.Set("X-API-Key", "test-key")
		req.Header.Set(header.key, header.value)
		w := httptest.NewRecorder()
		service.ServeHTTP(w, req)
		if w.Code < 400 {
			t.Errorf("invalid %s accepted", header.key)
		}
	}
}
func TestVercelClientIPHeaderTrustIsExplicit(t *testing.T) {
	service, _, _ := fixture(t)
	service.config.RateLimitPerMinute = 1
	service.config.TrustVercelClientIP = true
	for i, ip := range []string{"198.51.100.1", "198.51.100.2", "198.51.100.1"} {
		req := httptest.NewRequest("GET", "https://tenant.mcp.example.com/mcp/ping", nil)
		req.Header.Set("X-Vercel-Forwarded-For", ip)
		w := httptest.NewRecorder()
		service.ServeHTTP(w, req)
		if i < 2 && w.Code != 200 || i == 2 && w.Code != 429 {
			t.Fatalf("Vercel client IP bucket %s status %d", ip, w.Code)
		}
	}
	service.config.TrustVercelClientIP = false
	for _, ip := range []string{"198.51.100.3", "198.51.100.4"} {
		req := httptest.NewRequest("GET", "https://tenant.mcp.example.com/mcp/ping", nil)
		req.Header.Set("X-Vercel-Forwarded-For", ip)
		w := httptest.NewRecorder()
		service.ServeHTTP(w, req)
		if ip == "198.51.100.4" && w.Code != 429 {
			t.Fatal("untrusted header changed client IP bucket")
		}
	}
}

func TestAllowedBrowserMCPPreflightAndExposedChallenge(t *testing.T) {
	service, _, _ := fixture(t)
	for _, origin := range []string{"https://app.example.com", "https://evil.example.com"} {
		req := httptest.NewRequest("OPTIONS", "https://tenant.mcp.example.com/mcp", nil)
		req.Header.Set("Origin", origin)
		req.Header.Set("Access-Control-Request-Method", "POST")
		w := httptest.NewRecorder()
		service.ServeHTTP(w, req)
		if origin == "https://app.example.com" {
			if w.Code != 204 || w.Header().Get("Access-Control-Allow-Origin") != origin || !strings.Contains(w.Header().Get("Access-Control-Allow-Headers"), "Authorization") {
				t.Fatal("allowed browser cannot preflight MCP")
			}
		} else if w.Code != 403 || w.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Fatal("untrusted browser origin allowed")
		}
	}
	req := httptest.NewRequest("POST", "https://tenant.mcp.example.com/mcp", strings.NewReader(`{}`))
	req.Header.Set("Origin", "https://app.example.com")
	w := httptest.NewRecorder()
	service.ServeHTTP(w, req)
	if w.Code != 401 || !strings.Contains(w.Header().Get("Access-Control-Expose-Headers"), "WWW-Authenticate") {
		t.Fatal("browser cannot read OAuth challenge")
	}
}

func TestOAuthAndMCPShareIngressQuota(t *testing.T) {
	service, _, _ := fixture(t)
	service.config.RateLimitPerMinute = 1
	register := httptest.NewRequest("POST", "https://tenant.mcp.example.com/register", nil)
	allowed, err := service.AllowIngress(register)
	if err != nil || !allowed {
		t.Fatal("first OAuth ingress denied")
	}
	ping := httptest.NewRequest("GET", "https://tenant.mcp.example.com/mcp/ping", nil)
	w := httptest.NewRecorder()
	service.ServeHTTP(w, ping)
	if w.Code != 429 {
		t.Fatal("OAuth and MCP use separate quota buckets")
	}
}
func TestConfiguredMCPPathIsNormalized(t *testing.T) {
	_, store, _ := fixture(t)
	for _, raw := range []string{"custom/mcp/", "/custom//mcp", "/custom/mcp"} {
		service, err := New(Config{Path: raw}, store)
		if err != nil || service.Path() != "/custom/mcp" {
			t.Fatalf("path %q not normalized: %v", raw, err)
		}
	}
	for _, raw := range []string{"/../mcp", "/mcp?query=1", "/mcp\\path", "/mcp/%2e%2e"} {
		if _, err := New(Config{Path: raw}, store); err == nil {
			t.Errorf("unsafe configured path %q accepted", raw)
		}
	}
}

func TestLegacyCatalogLimitDoesNotReportUndeliveredSkills(t *testing.T) {
	service, store, _ := fixture(t)
	snapshot, err := store.Snapshot(context.Background(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	original := snapshot.Skills[0]
	for i := 0; i < 3; i++ {
		skill := original
		document := *original.Document
		document.ID = fmt.Sprint("extra-", i)
		skill.SkillID = document.ID
		skill.Document = &document
		snapshot.Skills = append(snapshot.Skills, skill)
	}
	args := map[string]json.RawMessage{}
	_ = json.Unmarshal([]byte(`{"mode":"route","task":"deployment","limit":1}`), &args)
	output, code, message, observations := service.callTool(context.Background(), snapshot, Principal{Project: Project{ID: projectID}}, rpcParams{Name: "mycontext_catalog", Arguments: args}, "2025-11-25")
	if code != 0 {
		t.Fatal(message)
	}
	resolution := output.(map[string]any)["structuredContent"].(skillruntime.Resolution)
	if len(resolution.ActiveSkills) != 1 {
		t.Fatal("legacy catalog limit ignored")
	}
	delivered, surfaced := 0, 0
	for _, observation := range observations {
		if observation.Type == "instructions_delivered" {
			delivered++
		}
		if observation.Type == "surfaced" {
			surfaced++
		}
	}
	if delivered != 1 || surfaced != 1 {
		t.Fatalf("reported delivery beyond returned catalog: %d %d", delivered, surfaced)
	}
}
