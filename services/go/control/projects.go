package control

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/CountableNewt/my-context-protocol/services/go/identity"
	"github.com/CountableNewt/my-context-protocol/services/go/security"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
)

// Select one repository consistently for legacy projects with multiple connections.
// The join keeps dashboard listings to one query regardless of project count.
const projectSelect = `SELECT p.*,r.provider AS connection_provider,r.repo_owner AS connection_owner,r.repo_name AS connection_name,r.default_branch AS connection_branch
FROM projects p LEFT JOIN repo_connections r ON r.id=(SELECT first.id FROM repo_connections first WHERE first.project_id=p.id ORDER BY first.created_at ASC,first.id ASC LIMIT 1)`

func (s *Service) projectResponse(p row) row {
	var connection any
	if p["connection_provider"] != nil {
		connection = row{"provider": p["connection_provider"], "repo_owner": p["connection_owner"], "repo_name": p["connection_name"], "default_branch": p["connection_branch"]}
	}
	var mcp any
	host := ""
	if p["custom_domain_verified_at"] != nil {
		host = str(p["custom_domain"])
	}
	if host == "" && s.cfg.MCPBaseDomain != "" && str(p["subdomain"]) != "" {
		host = str(p["subdomain"]) + "." + s.cfg.MCPBaseDomain
	}
	if host != "" {
		mcp = "https://" + host + s.cfg.MCPPath
	}
	result := row{"id": p["id"], "account_id": p["account_id"], "name": p["name"], "slug": p["slug"], "subdomain": str(p["subdomain"]), "created_at": requiredDate(p["created_at"]), "custom_domain": p["custom_domain"], "custom_domain_verified_at": date(p["custom_domain_verified_at"]), "active_release_id": p["active_release_id"], "mcp_url": mcp, "mcp_oauth_enabled": s.cfg.MCPOAuthEnabled, "suspended_at": nil, "repo_connection": connection}
	if completed, ok := p["atproto_publication_completed"]; ok && s.cfg.Records != nil {
		result["atproto_publication_completed"] = completed
	}
	return result
}

var slugPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

func (s *Service) createProject(r *http.Request, account string) (any, int, error) {
	var body struct {
		Name string `json:"name"`
		Slug string `json:"slug"`
	}
	if e := decode(r, &body); e != nil {
		return nil, 0, e
	}
	name, slug := strings.TrimSpace(body.Name), strings.TrimSpace(body.Slug)
	if name == "" || utf8.RuneCountInString(name) > 256 {
		return nil, 0, bad("Name must contain 1–256 characters")
	}
	if len(slug) > 128 || !slugPattern.MatchString(slug) {
		return nil, 0, bad("Invalid slug")
	}
	existing, e := s.one(r.Context(), "SELECT id FROM projects WHERE account_id=? AND slug=?", account, slug)
	if e != nil {
		return nil, 0, e
	}
	if existing != nil {
		return nil, 0, apiError{409, "Slug already in use for this account"}
	}
	id, e := identity.UUID()
	if e != nil {
		return nil, 0, e
	}
	opaque, e := security.NewOpaqueToken()
	if e != nil {
		return nil, 0, e
	}
	subdomain := opaque[:16]
	now := s.now().Unix()
	_, e = s.exec(r.Context(), "INSERT INTO projects(id,account_id,name,slug,subdomain,created_at) VALUES(?,?,?,?,?,?)", id, account, name, slug, subdomain, now)
	if e != nil {
		return nil, 0, e
	}
	project := row{"id": id, "account_id": account, "name": name, "slug": slug, "subdomain": subdomain, "created_at": now}
	if s.cfg.Records != nil {
		project["atproto_publication_completed"] = false
	}
	return s.projectResponse(project), 200, nil
}
func (s *Service) keys(r *http.Request, p row, path []string) (any, int, error) {
	pid := str(p["id"])
	ctx := r.Context()
	if len(path) == 0 {
		if r.Method == "GET" {
			sql := "SELECT id,project_id,name,key_prefix,status,created_at,last_used_at FROM api_keys WHERE project_id=?"
			if r.URL.Query().Get("include_revoked") != "true" {
				sql += " AND status='active'"
			}
			rows, e := s.query(ctx, sql+" ORDER BY created_at ASC", pid)
			for _, v := range rows {
				v["created_at"] = requiredDate(v["created_at"])
				v["last_used_at"] = date(v["last_used_at"])
			}
			return rows, 200, e
		}
		if r.Method == "POST" {
			var body struct {
				Name *string `json:"name"`
			}
			if r.ContentLength != 0 {
				if e := decode(r, &body); e != nil {
					return nil, 0, e
				}
			}
			name, e := keyName(body.Name)
			if e != nil {
				return nil, 0, e
			}
			id, e := identity.UUID()
			if e != nil {
				return nil, 0, e
			}
			token, e := security.NewOpaqueToken()
			if e != nil {
				return nil, 0, e
			}
			key := "mcp_" + token
			prefix := key[:12]
			_, e = s.exec(ctx, "INSERT INTO api_keys(id,project_id,name,key_prefix,key_hash,status,created_at) VALUES(?,?,?,?,?,'active',?)", id, pid, name, prefix, security.HashToken(key), s.now().Unix())
			return row{"id": id, "key": key, "prefix": prefix, "name": name}, 200, e
		}
	}
	if len(path) != 1 {
		return nil, 0, missing("Not found")
	}
	kid, e := uuid(path[0])
	if e != nil {
		return nil, 0, bad("Invalid API key ID")
	}
	key, e := s.one(ctx, "SELECT id,project_id,name,key_prefix,status,created_at,last_used_at FROM api_keys WHERE id=? AND project_id=?", kid, pid)
	if e != nil {
		return nil, 0, e
	}
	if key == nil {
		return nil, 0, missing("API key not found")
	}
	if r.Method == "DELETE" {
		_, e := s.exec(ctx, "UPDATE api_keys SET status='revoked' WHERE id=? AND project_id=?", kid, pid)
		return nil, 204, e
	}
	if r.Method == "PATCH" {
		if str(key["status"]) != "active" {
			return nil, 0, apiError{409, "API key is revoked"}
		}
		var body struct {
			Name *string `json:"name"`
		}
		if e := decode(r, &body); e != nil {
			return nil, 0, e
		}
		if body.Name == nil {
			return nil, 0, bad("Name is required")
		}
		name, e := keyName(body.Name)
		if e != nil {
			return nil, 0, e
		}
		result, e := s.exec(ctx, "UPDATE api_keys SET name=? WHERE id=? AND project_id=? AND status='active'", name, kid, pid)
		if e != nil {
			return nil, 0, e
		}
		if result.AffectedRowCount != 1 {
			return nil, 0, apiError{409, "API key is revoked"}
		}
		key["name"] = name
		key["created_at"] = requiredDate(key["created_at"])
		key["last_used_at"] = date(key["last_used_at"])
		return key, 200, nil
	}
	return nil, 0, missing("Not found")
}
func keyName(v *string) (any, error) {
	if v == nil {
		return nil, nil
	}
	name := strings.TrimSpace(*v)
	if utf8.RuneCountInString(name) > 64 {
		return nil, bad("API key name must be 64 characters or fewer")
	}
	return optional(name), nil
}
func (s *Service) releases(r *http.Request, p row, path []string) (any, int, error) {
	ctx, pid := r.Context(), str(p["id"])
	if len(path) == 0 && r.Method == "GET" {
		rows, e := s.query(ctx, "SELECT * FROM releases WHERE project_id=? ORDER BY created_at DESC,id DESC", pid)
		if e != nil {
			return nil, 0, e
		}
		for _, v := range rows {
			skills, e := s.compiled(ctx, str(v["id"]))
			if e != nil {
				return nil, 0, e
			}
			blocking, warning := 0, 0
			for _, skill := range skills {
				tier := metadataTier(skill, true)
				if tier == "blocking" {
					blocking++
				}
				if tier == "warning" {
					warning++
				}
			}
			v["created_at"] = requiredDate(v["created_at"])
			v["is_active"] = v["id"] == p["active_release_id"]
			v["mcp_metadata_blocking_skills"] = blocking
			v["mcp_metadata_warning_skills"] = warning
		}
		return rows, 200, nil
	}
	if len(path) < 2 {
		return nil, 0, missing("Not found")
	}
	rid, e := uuid(path[0])
	if e != nil {
		return nil, 0, bad("Invalid release ID")
	}
	release, e := s.one(ctx, "SELECT * FROM releases WHERE id=? AND project_id=?", rid, pid)
	if e != nil {
		return nil, 0, e
	}
	if release == nil {
		return nil, 0, missing("Release not found")
	}
	if len(path) == 2 && path[1] == "activate" && r.Method == "POST" {
		// The predicate and readiness checks run inside the same write transaction as activation.
		result, e := s.db.Batch(ctx, []turso.Statement{{SQL: "UPDATE projects SET active_release_id=? WHERE id=? AND account_id=? AND EXISTS(SELECT 1 FROM releases WHERE id=? AND project_id=? AND status='ready') AND NOT EXISTS(SELECT 1 FROM compiled_skills WHERE release_id=? AND status<>'ready')", Args: values(rid, pid, p["account_id"], rid, pid, rid)}, turso.Statement{SQL: "INSERT INTO mcp_catalog_revisions(project_id,revision) SELECT ?,1 WHERE changes()>0 ON CONFLICT(project_id) DO UPDATE SET revision=revision+1", Args: values(pid)}})
		if e != nil {
			return nil, 0, e
		}
		if result[0].AffectedRowCount != 1 {
			return nil, 0, bad("Release and all compiled skills must be ready before activation")
		}
		return nil, 204, nil
	}
	if len(path) == 2 && path[1] == "validation" && r.Method == "GET" {
		record, e := s.one(ctx, "SELECT report_json FROM validation_reports WHERE release_id=? ORDER BY created_at DESC LIMIT 1", rid)
		if e != nil {
			return nil, 0, e
		}
		if record == nil {
			if release["status"] == "failed" {
				message := str(release["error_summary"])
				if message == "" {
					message = "Release failed; no structured validation report was stored."
				}
				return row{"is_valid": false, "errors": []row{{"path": "release", "message": message}}, "warnings": []any{}}, 200, nil
			}
			return row{"is_valid": true, "errors": []any{}, "warnings": []any{}}, 200, nil
		}
		var report map[string]any
		if json.Unmarshal([]byte(str(record["report_json"])), &report) != nil || report == nil {
			return row{"is_valid": false, "errors": []row{{"path": "report", "message": "Could not parse validation report JSON"}}, "warnings": []any{}}, 200, nil
		}
		return row{"is_valid": report["is_valid"] == true, "errors": validationEntries(report["errors"]), "warnings": validationEntries(report["warnings"])}, 200, nil
	}
	if path[1] == "compiled-skills" {
		if len(path) == 4 && path[3] == "writeback" && r.Method == "POST" && s.cfg.Writeback != nil {
			sid, e := uuid(path[2])
			if e != nil {
				return nil, 0, bad("Invalid compiled skill ID")
			}
			found, e := s.one(ctx, "SELECT id FROM compiled_skills WHERE id=? AND release_id=?", sid, rid)
			if e != nil {
				return nil, 0, e
			}
			if found == nil {
				return nil, 0, missing("Compiled skill not found")
			}
			v, e := s.cfg.Writeback(ctx, str(p["account_id"]), pid, rid, sid)
			return v, 200, e
		}

		if len(path) == 2 && r.Method == "GET" {
			v, e := s.compiled(ctx, rid)
			return v, 200, e
		}
		if len(path) == 3 && r.Method == "PATCH" {
			sid, e := uuid(path[2])
			if e != nil {
				return nil, 0, bad("Invalid compiled skill ID")
			}
			return s.patchCompiled(r, p, release, sid)
		}
	}
	return nil, 0, missing("Not found")
}
func revisionStatement(pid string) turso.Statement {
	return turso.Statement{SQL: "INSERT INTO mcp_catalog_revisions(project_id,revision) VALUES(?,1) ON CONFLICT(project_id) DO UPDATE SET revision=revision+1", Args: values(pid)}
}

const compiledSelect = `SELECT c.id,c.release_id,c.skill_package_id,c.path,c.name,c.summary,c.skill_body,c.yaml_frontmatter_present,c.exposure_type,c.risk_level,c.repo_specific,c.status,c.canonical_schema_version,c.skill_id,c.kind,c.scope,c.activation_mode,c.enforcement,c.priority,c.version,c.source_checksum,c.canonical_json,c.clarification_required,c.clarification_json,c.body_diff_unified,c.body_diff_prior_release_id,(SELECT schema_json FROM capability_defs WHERE compiled_skill_id=c.id AND schema_json IS NOT NULL AND schema_json<>'' ORDER BY created_at,id LIMIT 1) AS schema_json,(SELECT use_when_json FROM routing_rules WHERE compiled_skill_id=c.id LIMIT 1) AS use_when_json,(SELECT avoid_when_json FROM routing_rules WHERE compiled_skill_id=c.id LIMIT 1) AS avoid_when_json,(SELECT failure_modes_json FROM routing_rules WHERE compiled_skill_id=c.id LIMIT 1) AS failure_modes_json,(SELECT invoke_first FROM routing_rules WHERE compiled_skill_id=c.id LIMIT 1) AS invoke_first FROM compiled_skills c WHERE c.release_id=? ORDER BY c.path,c.id`

func (s *Service) compiled(ctx context.Context, rid string) ([]row, error) {
	rows, e := s.query(ctx, compiledSelect, rid)
	if e != nil {
		return nil, e
	}
	for _, v := range rows {
		for _, key := range []string{"yaml_frontmatter_present", "repo_specific", "clarification_required", "invoke_first"} {
			v[key] = boolean(v[key])
		}
		v["clarification_questions"] = jsonArray(v["clarification_json"])
		delete(v, "clarification_json")
		for _, key := range []string{"use_when", "avoid_when", "failure_modes"} {
			v[key] = jsonArray(v[key+"_json"])
			delete(v, key+"_json")
		}
	}
	return rows, nil
}
func jsonArray(raw any) []any {
	var list []any
	if json.Unmarshal([]byte(str(raw)), &list) != nil || list == nil {
		return []any{}
	}
	return list
}
func metadataTier(skill row, includeStatus bool) string {
	if includeStatus && skill["status"] == "not_publishable" {
		return "blocking"
	}
	schema := strings.TrimSpace(str(skill["schema_json"]))
	var metadata any
	if schema != "" {
		var ok bool
		metadata, ok = structuredJSON(schema)
		if !ok {
			return "blocking"
		}
	}
	if skill["exposure_type"] == "resource" {
		obj, ok := metadata.(map[string]any)
		if !ok || strings.TrimSpace(str(obj["uri"])) == "" {
			return "blocking"
		}
	}
	if !boolean(skill["yaml_frontmatter_present"]) || strings.TrimSpace(str(skill["skill_body"])) == "" {
		return "warning"
	}
	if skill["exposure_type"] == "resource" {
		count := 0
		for _, key := range []string{"use_when", "avoid_when", "failure_modes"} {
			if list, ok := skill[key].([]any); ok {
				count += len(list)
			}
		}
		if count == 0 {
			return "warning"
		}
	}
	if includeStatus && skill["status"] != "ready" {
		return "warning"
	}
	return "ok"
}
func (s *Service) logs(r *http.Request, pid string) (any, int, error) {
	limit, offset := 50, 0
	var e error
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, e = strconv.Atoi(raw)
		if e != nil || limit < 1 {
			return nil, 0, bad("Invalid limit")
		}
	}
	if limit > 200 {
		limit = 200
	}
	if raw := r.URL.Query().Get("offset"); raw != "" {
		offset, e = strconv.Atoi(raw)
		if e != nil || offset < 0 {
			return nil, 0, bad("Invalid offset")
		}
	}
	rows, e := s.query(r.Context(), `SELECT l.id,l.project_id,l.release_id,l.timestamp,CASE WHEN l.client_id LIKE 'apikey:%' THEN COALESCE((SELECT COALESCE(NULLIF(TRIM(k.name),''),k.key_prefix) FROM api_keys k WHERE k.project_id=l.project_id AND k.id=SUBSTR(l.client_id,8)),'API key (removed)') ELSE l.client_id END AS client_id,l.method,l.latency_ms,CAST(l.status AS INTEGER) AS status,l.error_code,l.error_message FROM request_logs l WHERE l.project_id=? ORDER BY l.timestamp DESC,l.id DESC LIMIT ? OFFSET ?`, pid, limit, offset)
	for _, v := range rows {
		v["timestamp"] = requiredDate(v["timestamp"])
	}
	return rows, 200, e
}

func validationEntries(raw any) []row {
	out := []row{}
	list, _ := raw.([]any)
	for _, item := range list {
		source, ok := item.(map[string]any)
		if !ok {
			continue
		}
		path, ok := source["path"].(string)
		if !ok {
			continue
		}
		v := row{"path": path, "message": str(source["message"])}
		for _, key := range []string{"code", "summary", "fix_hint"} {
			if text, ok := source[key].(string); ok {
				v[key] = text
			}
		}
		if line, ok := source["line"].(float64); ok {
			v["line"] = int(line)
		}
		out = append(out, v)
	}
	return out
}
