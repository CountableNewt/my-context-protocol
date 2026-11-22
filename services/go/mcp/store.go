package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/CountableNewt/my-context-protocol/services/go/identity"
	skillruntime "github.com/CountableNewt/my-context-protocol/services/go/runtime"
	"github.com/CountableNewt/my-context-protocol/services/go/security"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
	"strconv"
	"strings"
	"time"
)

var ErrCredential = errors.New("invalid credential")
var ErrLimit = errors.New("limit exceeded")

const MigrationSQL = `CREATE TABLE IF NOT EXISTS mcp_catalog_revisions(project_id TEXT PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,revision INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS mcp_resource_subscriptions(project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,subscriber_id TEXT NOT NULL,uri TEXT NOT NULL,created_at REAL NOT NULL,PRIMARY KEY(project_id,subscriber_id,uri));
CREATE TABLE IF NOT EXISTS mcp_rate_limits(bucket_key TEXT NOT NULL,window_start INTEGER NOT NULL,count INTEGER NOT NULL,PRIMARY KEY(bucket_key,window_start));`

type Project struct {
	Subdomain       string
	CustomDomain    string
	AccountID       string
	ID              string
	Name            string
	ReleaseID       string
	CatalogOverride string
}
type Principal struct {
	ID             string
	ClientIdentity string
	Kind           string
	Project        Project
}
type Observation struct {
	Skill   *skillruntime.Skill
	TraceID string
	Type    string
	Source  string
	Reason  string
}
type RequestLog struct {
	ProjectID      string
	ReleaseID      string
	ClientIdentity string
	Method         string
	LatencyMS      int64
	Status         int
	ErrorCode      int
	ErrorMessage   string
	CapabilityKind string
	CapabilityKey  string
}
type Store interface {
	HostProject(context.Context, string, string) (*Project, error)
	Authenticate(context.Context, string, bool, time.Time) (Principal, error)
	Snapshot(context.Context, string) (skillruntime.Snapshot, error)
	Files(context.Context, skillruntime.Skill) ([]skillruntime.File, error)
	File(context.Context, skillruntime.Skill, string) (skillruntime.File, error)
	Revision(context.Context, string) (int64, error)
	Subscriptions(context.Context, Principal) ([]string, error)
	Subscribe(context.Context, Principal, string, bool) (bool, error)
	RateLimit(context.Context, string, int, time.Time) (bool, error)
	Record(context.Context, string, string, []Observation) error
	Log(context.Context, RequestLog) error
	Feedback(context.Context, skillruntime.Snapshot, map[string]json.RawMessage) (any, error)
	Usage(context.Context, skillruntime.Snapshot, string, map[string]json.RawMessage) (any, error)
}
type Database interface {
	Execute(context.Context, turso.Statement) (*turso.Result, error)
	Batch(context.Context, []turso.Statement) ([]turso.Result, error)
}
type SQLStore struct{ DB Database }
type row map[string]turso.Value

func query(ctx context.Context, db Database, sql string, args ...turso.Value) ([]row, error) {
	r, err := db.Execute(ctx, turso.Statement{SQL: sql, Args: args, WantRows: true})
	if err != nil {
		return nil, err
	}
	result := make([]row, 0, len(r.Rows))
	for _, values := range r.Rows {
		if len(values) != len(r.Columns) {
			return nil, errors.New("invalid database result")
		}
		mapped := row{}
		for i, col := range r.Columns {
			if col.Name != nil {
				mapped[*col.Name] = values[i]
			}
		}
		result = append(result, mapped)
	}
	return result, nil
}
func text(r row, key string) string {
	value := r[key].Value
	if value == nil {
		return ""
	}
	s, _ := value.(string)
	return s
}
func number(r row, key string) int64 {
	v := r[key]
	if value, ok := v.Value.(string); ok {
		n, _ := strconv.ParseInt(value, 10, 64)
		return n
	}
	if value, ok := v.Value.(float64); ok {
		return int64(value)
	}
	return 0
}
func boolValue(r row, key string) bool { return number(r, key) != 0 }
func opt(s string) turso.Value {
	if s == "" {
		return turso.Null()
	}
	return turso.Text(s)
}
func project(r row) Project {
	return Project{AccountID: text(r, "account_id"), Subdomain: text(r, "subdomain"), CustomDomain: verifiedCustomDomain(r), ID: text(r, "project_id"), Name: text(r, "project_name"), ReleaseID: text(r, "active_release_id"), CatalogOverride: text(r, "mcp_catalog_markdown_override")}
}

const projectSelect = `p.subdomain,p.custom_domain,p.custom_domain_verified_at,p.account_id,p.id AS project_id,p.name AS project_name,p.active_release_id,p.mcp_catalog_markdown_override`

func (s SQLStore) HostProject(ctx context.Context, host, base string) (*Project, error) {
	rows, err := query(ctx, s.DB, "SELECT "+projectSelect+" FROM projects p WHERE p.custom_domain=? AND p.custom_domain_verified_at IS NOT NULL LIMIT 1", turso.Text(host))
	if err != nil {
		return nil, err
	}
	if len(rows) > 0 {
		p := project(rows[0])
		return &p, nil
	}
	if base != "" && strings.HasSuffix(host, "."+base) {
		prefix := strings.TrimSuffix(host, "."+base)
		if prefix != "" && !strings.Contains(prefix, ".") {
			rows, err = query(ctx, s.DB, "SELECT "+projectSelect+" FROM projects p WHERE p.subdomain=? LIMIT 1", turso.Text(prefix))
			if err != nil {
				return nil, err
			}
			if len(rows) > 0 {
				p := project(rows[0])
				return &p, nil
			}
		}
	}
	return nil, nil
}
func (s SQLStore) Authenticate(ctx context.Context, raw string, oauth bool, now time.Time) (Principal, error) {
	hash := security.HashToken(raw)
	rows, err := query(ctx, s.DB, "SELECT k.id AS credential_id,"+projectSelect+" FROM api_keys k JOIN projects p ON p.id=k.project_id WHERE k.key_hash=? AND k.status='active' LIMIT 1", turso.Text(hash))
	if err != nil {
		return Principal{}, err
	}
	if len(rows) > 0 {
		r := rows[0]
		id := text(r, "credential_id")
		touched, touchErr := s.DB.Execute(ctx, turso.Statement{SQL: "UPDATE api_keys SET last_used_at=? WHERE id=? AND status='active'", Args: []turso.Value{turso.Integer(now.Unix()), turso.Text(id)}})
		if touchErr != nil {
			return Principal{}, touchErr
		}
		if touched.AffectedRowCount != 1 {
			return Principal{}, ErrCredential
		}
		return Principal{ID: id, ClientIdentity: "apikey:" + id, Kind: "api_key", Project: project(r)}, nil
	}
	if !oauth {
		return Principal{}, ErrCredential
	}
	rows, err = query(ctx, s.DB, "SELECT t.id AS credential_id,t.mcp_oauth_client_id,t.subject_type,t.account_id,t.scope,"+projectSelect+" FROM mcp_oauth_access_tokens t JOIN projects p ON p.id=t.project_id JOIN mcp_oauth_clients c ON c.id=t.mcp_oauth_client_id WHERE c.status='active' AND t.token_hash=? AND t.revoked_at IS NULL AND t.expires_at>? LIMIT 1", turso.Text(hash), turso.Integer(now.Unix()))
	if err != nil {
		return Principal{}, err
	}
	if len(rows) == 0 {
		return Principal{}, ErrCredential
	}
	r := rows[0]
	if !contains(strings.Fields(text(r, "scope")), "mcp:invoke") {
		return Principal{}, ErrCredential
	}
	subject := "service"
	kind := "oauth_service"
	if text(r, "subject_type") != "service" {
		if text(r, "subject_type") != "user" || text(r, "account_id") == "" {
			return Principal{}, ErrCredential
		}
		subject = "user:" + text(r, "account_id")
		kind = "oauth_user"
	}
	return Principal{ID: text(r, "credential_id"), ClientIdentity: "oauth:" + text(r, "mcp_oauth_client_id") + ":" + subject, Kind: kind, Project: project(r)}, nil
}
func (s SQLStore) Snapshot(ctx context.Context, id string) (skillruntime.Snapshot, error) {
	rows, err := query(ctx, s.DB, "SELECT "+projectSelect+" FROM projects p WHERE p.id=?", turso.Text(id))
	if err != nil {
		return skillruntime.Snapshot{}, err
	}
	if len(rows) != 1 {
		return skillruntime.Snapshot{}, errors.New("project not found")
	}
	p := project(rows[0])
	snapshot := skillruntime.Snapshot{ProjectID: p.ID, Subdomain: p.Subdomain, CustomDomain: p.CustomDomain, ProjectName: p.Name, ReleaseID: p.ReleaseID, CatalogOverride: p.CatalogOverride, Skills: []skillruntime.Skill{}, Capabilities: []skillruntime.Capability{}, Assignments: []skillruntime.Assignment{}, Settings: skillruntime.Settings{TelemetryRetentionDays: 30}}
	settings, err := query(ctx, s.DB, "SELECT telemetry_enabled,telemetry_retention_days,feedback_issue_creation_enabled,provider_preferences_json FROM project_runtime_settings WHERE project_id=?", turso.Text(id))
	if err != nil {
		return snapshot, err
	}
	if len(settings) > 0 {
		r := settings[0]
		snapshot.Settings.TelemetryEnabled = boolValue(r, "telemetry_enabled")
		snapshot.Settings.TelemetryRetentionDays = int(number(r, "telemetry_retention_days"))
		snapshot.Settings.FeedbackIssueCreationEnabled = boolValue(r, "feedback_issue_creation_enabled")
		var prefs map[string]any
		_ = json.Unmarshal([]byte(text(r, "provider_preferences_json")), &prefs)
		snapshot.Settings.LegacyCompiledTools = prefs["legacy_compiled_tools_enabled"] == true
	}
	assignments, err := query(ctx, s.DB, "SELECT skill_id,scope,activation_mode,target_type,target_id,required,priority FROM skill_assignments WHERE project_id=? ORDER BY id LIMIT 5001", turso.Text(id))
	if err != nil {
		return snapshot, err
	}
	if len(assignments) > 5000 {
		return snapshot, ErrLimit
	}
	for _, r := range assignments {
		snapshot.Assignments = append(snapshot.Assignments, skillruntime.Assignment{SkillID: text(r, "skill_id"), Scope: text(r, "scope"), ActivationMode: text(r, "activation_mode"), TargetType: text(r, "target_type"), TargetID: text(r, "target_id"), Required: boolValue(r, "required"), Priority: int(number(r, "priority"))})
	}
	if p.ReleaseID == "" {
		return snapshot, nil
	}
	releaseRows, err := query(ctx, s.DB, "SELECT status FROM releases WHERE id=? AND project_id=?", turso.Text(p.ReleaseID), turso.Text(p.ID))
	if err != nil {
		return snapshot, err
	}
	if len(releaseRows) != 1 {
		return snapshot, errors.New("active release does not belong to project")
	}
	snapshot.ReleaseStatus = text(releaseRows[0], "status")
	skills, err := query(ctx, s.DB, "SELECT id,release_id,skill_package_id,name,path,summary,skill_body,skill_id,version,source_checksum,canonical_json FROM compiled_skills WHERE release_id=? AND status='ready' ORDER BY id LIMIT 5001", turso.Text(p.ReleaseID))
	if err != nil {
		return snapshot, err
	}
	if len(skills) > 5000 {
		return snapshot, ErrLimit
	}
	for _, r := range skills {
		document, _ := skillruntime.ParseDocument(text(r, "canonical_json"))
		snapshot.Skills = append(snapshot.Skills, skillruntime.Skill{ID: text(r, "id"), ReleaseID: text(r, "release_id"), PackageID: text(r, "skill_package_id"), Name: text(r, "name"), Path: text(r, "path"), Summary: text(r, "summary"), Body: text(r, "skill_body"), SkillID: text(r, "skill_id"), Version: text(r, "version"), Checksum: text(r, "source_checksum"), Document: document})
	}
	rules, err := query(ctx, s.DB, "SELECT r.compiled_skill_id,r.use_when_json,r.avoid_when_json,r.failure_modes_json,r.invoke_first FROM routing_rules r JOIN compiled_skills c ON c.id=r.compiled_skill_id WHERE c.release_id=? AND c.status='ready' ORDER BY r.id LIMIT 5001", turso.Text(p.ReleaseID))
	if err != nil {
		return snapshot, err
	}
	for i := range snapshot.Skills {
		for _, r := range rules {
			if snapshot.Skills[i].ID == text(r, "compiled_skill_id") {
				hints := skillruntime.Hints{InvokeFirst: boolValue(r, "invoke_first")}
				_ = json.Unmarshal([]byte(text(r, "use_when_json")), &hints.UseWhen)
				_ = json.Unmarshal([]byte(text(r, "avoid_when_json")), &hints.AvoidWhen)
				_ = json.Unmarshal([]byte(text(r, "failure_modes_json")), &hints.FailureModes)
				snapshot.Skills[i].Hints = hints
				break
			}
		}
	}
	caps, err := query(ctx, s.DB, "SELECT d.capability_name,d.type,d.schema_json,d.compiled_skill_id FROM capability_defs d JOIN compiled_skills c ON c.id=d.compiled_skill_id WHERE c.release_id=? AND c.status='ready' ORDER BY d.capability_name,d.type LIMIT 15001", turso.Text(p.ReleaseID))
	if err != nil {
		return snapshot, err
	}
	if len(caps) > 15000 {
		return snapshot, ErrLimit
	}
	for _, r := range caps {
		snapshot.Capabilities = append(snapshot.Capabilities, skillruntime.Capability{Name: text(r, "capability_name"), Type: text(r, "type"), Schema: json.RawMessage(text(r, "schema_json")), SkillID: text(r, "compiled_skill_id")})
	}
	return snapshot, nil
}
func (s SQLStore) Files(ctx context.Context, skill skillruntime.Skill) ([]skillruntime.File, error) {
	rows, err := query(ctx, s.DB, "SELECT path,checksum,content_type,byte_count FROM skill_package_files WHERE skill_package_id=? ORDER BY path LIMIT 129", turso.Text(skill.PackageID))
	if err != nil {
		return nil, err
	}
	if len(rows) > 128 {
		return nil, ErrLimit
	}
	files := make([]skillruntime.File, 0, len(rows))
	for _, r := range rows {
		mime := text(r, "content_type")
		if mime == "" {
			mime = "application/octet-stream"
		}
		files = append(files, skillruntime.File{Path: text(r, "path"), Checksum: text(r, "checksum"), MediaType: mime, ByteCount: int(number(r, "byte_count")), ResourceURI: skillruntime.ResourceURI(skill.SkillID, text(r, "path"), skill.Version)})
	}
	return files, nil
}
func (s SQLStore) File(ctx context.Context, skill skillruntime.Skill, path string) (skillruntime.File, error) {
	safe, err := skillruntime.SafePath(path)
	if err != nil {
		return skillruntime.File{}, err
	}
	rows, err := query(ctx, s.DB, "SELECT path,checksum,content_type,byte_count,content FROM skill_package_files WHERE skill_package_id=? AND path=? LIMIT 1", turso.Text(skill.PackageID), turso.Text(safe))
	if err != nil {
		return skillruntime.File{}, err
	}
	if len(rows) == 0 {
		return skillruntime.File{}, errors.New("Package file not found")
	}
	r := rows[0]
	v := r["content"]
	var content []byte
	if v.Type == "blob" && v.Base64 != nil {
		content, err = base64.StdEncoding.DecodeString(*v.Base64)
	} else {
		err = errors.New("invalid package content")
	}
	if err != nil {
		return skillruntime.File{}, err
	}
	if len(content) > 256*1024 || len(content) != int(number(r, "byte_count")) {
		return skillruntime.File{}, ErrLimit
	}
	if security.HashToken(string(content)) != text(r, "checksum") {
		return skillruntime.File{}, errors.New("package checksum mismatch")
	}
	mime := text(r, "content_type")
	if mime == "" {
		mime = "application/octet-stream"
	}
	return skillruntime.File{Path: safe, Checksum: text(r, "checksum"), MediaType: mime, ByteCount: len(content), Content: content, ResourceURI: skillruntime.ResourceURI(skill.SkillID, safe, skill.Version)}, nil
}
func (s SQLStore) Revision(ctx context.Context, project string) (int64, error) {
	rows, err := query(ctx, s.DB, "SELECT revision FROM mcp_catalog_revisions WHERE project_id=?", turso.Text(project))
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}
	return number(rows[0], "revision"), nil
}
func (s SQLStore) Subscribe(ctx context.Context, p Principal, uri string, add bool) (bool, error) {
	if len(uri) > 2048 {
		return false, nil
	}
	if !add {
		_, err := s.DB.Execute(ctx, turso.Statement{SQL: "DELETE FROM mcp_resource_subscriptions WHERE project_id=? AND subscriber_id=? AND uri=?", Args: []turso.Value{turso.Text(p.Project.ID), turso.Text(p.ID), turso.Text(uri)}})
		return err == nil, err
	}
	r, err := s.DB.Execute(ctx, turso.Statement{SQL: `INSERT INTO mcp_resource_subscriptions(project_id,subscriber_id,uri,created_at) SELECT ?,?,?,? WHERE (SELECT COUNT(*) FROM mcp_resource_subscriptions WHERE project_id=? AND subscriber_id=?)<256 ON CONFLICT(project_id,subscriber_id,uri) DO UPDATE SET created_at=excluded.created_at RETURNING uri`, Args: []turso.Value{turso.Text(p.Project.ID), turso.Text(p.ID), turso.Text(uri), turso.Integer(time.Now().Unix()), turso.Text(p.Project.ID), turso.Text(p.ID)}, WantRows: true})
	if err != nil {
		return false, err
	}
	if len(r.Rows) > 0 {
		return true, nil
	}
	existing, err := query(ctx, s.DB, "SELECT 1 FROM mcp_resource_subscriptions WHERE project_id=? AND subscriber_id=? AND uri=?", turso.Text(p.Project.ID), turso.Text(p.ID), turso.Text(uri))
	return len(existing) > 0, err
}
func (s SQLStore) RateLimit(ctx context.Context, bucket string, limit int, now time.Time) (bool, error) {
	window := now.Unix() / 60 * 60
	r, err := s.DB.Execute(ctx, turso.Statement{SQL: `INSERT INTO mcp_rate_limits(bucket_key,window_start,count) VALUES(?,?,1) ON CONFLICT(bucket_key,window_start) DO UPDATE SET count=count+1 WHERE count<? RETURNING count`, Args: []turso.Value{turso.Text(security.HashToken(bucket)), turso.Integer(window), turso.Integer(int64(limit))}, WantRows: true})
	if err != nil {
		return false, err
	}
	return len(r.Rows) > 0, nil
}
func (s SQLStore) Record(ctx context.Context, project, client string, events []Observation) error {
	if len(events) == 0 {
		return nil
	}
	for offset := 0; offset < len(events); offset += 120 {
		end := min(len(events), offset+120)
		statements := make([]turso.Statement, 0, end-offset)
		for _, event := range events[offset:end] {
			id, err := identity.UUID()
			if err != nil {
				return err
			}
			trace := event.TraceID
			if trace == "" {
				trace, err = identity.UUID()
				if err != nil {
					return err
				}
			}
			skillID, release, version, checksum := "", "", "", ""
			if event.Skill != nil {
				skillID = event.Skill.SkillID
				release = event.Skill.ReleaseID
				version = event.Skill.Version
				checksum = event.Skill.Checksum
			}
			statements = append(statements, turso.Statement{SQL: `INSERT INTO skill_runtime_events(id,project_id,trace_id,event_type,skill_id,reason_code,detail_json,release_id,skill_version,source_checksum,client_identity,source,created_at) SELECT ?,?,?,?,?,?,'{}',?,?,?,?,?,? WHERE EXISTS(SELECT 1 FROM project_runtime_settings WHERE project_id=? AND telemetry_enabled=1)`, Args: []turso.Value{turso.Text(id), turso.Text(project), turso.Text(trace), turso.Text(event.Type), opt(skillID), opt(event.Reason), opt(release), opt(version), opt(checksum), turso.Text(client), turso.Text(event.Source), turso.Integer(time.Now().Unix()), turso.Text(project)}})
		}
		if _, err := s.DB.Batch(ctx, statements); err != nil {
			return err
		}
	}
	return nil
}
func (s SQLStore) Log(ctx context.Context, event RequestLog) error {
	id, err := identity.UUID()
	if err != nil {
		return err
	}
	code := ""
	if event.ErrorCode != 0 {
		code = strconv.Itoa(event.ErrorCode)
	}
	_, err = s.DB.Execute(ctx, turso.Statement{SQL: `INSERT INTO request_logs(id,project_id,release_id,timestamp,client_id,method,latency_ms,status,error_code,error_message,mcp_capability_kind,mcp_capability_key) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, Args: []turso.Value{turso.Text(id), turso.Text(event.ProjectID), opt(event.ReleaseID), turso.Integer(time.Now().Unix()), turso.Text(event.ClientIdentity), turso.Text(event.Method), turso.Integer(event.LatencyMS), turso.Text(strconv.Itoa(event.Status)), opt(code), opt(event.ErrorMessage), opt(event.CapabilityKind), opt(event.CapabilityKey)}})
	return err
}
func contains(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}
func assertRows(rows []row) error {
	if len(rows) != 1 {
		return fmt.Errorf("unexpected row count")
	}
	return nil
}

func (s SQLStore) ProjectByID(ctx context.Context, id string) (*Project, error) {
	rows, err := query(ctx, s.DB, "SELECT "+projectSelect+" FROM projects p WHERE p.id=?", turso.Text(id))
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	p := project(rows[0])
	return &p, nil
}

func verifiedCustomDomain(r row) string {
	value, ok := r["custom_domain_verified_at"]
	if !ok || value.Type == "null" {
		return ""
	}
	return text(r, "custom_domain")
}

func (s SQLStore) Subscriptions(ctx context.Context, p Principal) ([]string, error) {
	rows, err := query(ctx, s.DB, "SELECT uri FROM mcp_resource_subscriptions WHERE project_id=? AND subscriber_id=? ORDER BY uri LIMIT 256", turso.Text(p.Project.ID), turso.Text(p.ID))
	if err != nil {
		return nil, err
	}
	uris := make([]string, 0, len(rows))
	for _, r := range rows {
		uris = append(uris, text(r, "uri"))
	}
	return uris, nil
}
