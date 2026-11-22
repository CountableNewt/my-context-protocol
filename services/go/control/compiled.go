package control

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/CountableNewt/my-context-protocol/services/go/identity"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
)

func encode(v any) string { b, _ := json.Marshal(v); return string(b) }
func defaultSchema(skill row) string {
	switch skill["exposure_type"] {
	case "resource":
		meta := row{"uri": "ctx://skill/" + uriComponent(str(skill["name"])), "mimeType": "text/markdown"}
		for _, key := range []string{"use_when", "avoid_when", "failure_modes"} {
			if a, ok := skill[key].([]any); ok && len(a) > 0 {
				meta[key] = a
			}
		}
		if boolean(skill["invoke_first"]) {
			meta["invoke_first"] = true
		}
		return encode(meta)
	case "prompt", "guidance":
		return `{"arguments":[]}`
	default:
		help := "Optional extra context or question for this skill."
		summary := strings.TrimSpace(str(skill["summary"]))
		if summary != "" {
			runes := []rune(summary)
			if len(runes) > 480 {
				summary = string(runes[:480]) + "…"
			}
			help = "Optional extra context. Skill summary: " + summary
		}
		return encode(row{"type": "object", "properties": row{"detail": row{"type": "string", "description": help}}, "additionalProperties": false})
	}
}
func uriComponent(raw string) string {
	var b strings.Builder
	for _, c := range []byte(raw) {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("-._~", rune(c)) {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}
func (s *Service) patchCompiled(r *http.Request, p, release row, sid string) (any, int, error) {
	ctx, pid, rid := r.Context(), str(p["id"]), str(release["id"])
	skills, e := s.compiled(ctx, rid)
	if e != nil {
		return nil, 0, e
	}
	var skill row
	for _, v := range skills {
		if v["id"] == sid {
			skill = v
			break
		}
	}
	if skill == nil {
		return nil, 0, missing("Compiled skill not found")
	}
	var body struct {
		Exposure *string `json:"exposure_type"`
		Risk     *string `json:"risk_level"`
		Status   *string `json:"status"`
		Summary  *string `json:"summary"`
		Body     *string `json:"skill_body"`
		Replace  bool    `json:"replace_schema"`
		Schema   *string `json:"schema_json"`
		Routing  *struct {
			Use      []string `json:"use_when"`
			Avoid    []string `json:"avoid_when"`
			Failures []string `json:"failure_modes"`
			First    bool     `json:"invoke_first"`
		} `json:"routing"`
		Runtime map[string]any `json:"runtime"`
	}
	if e := decode(r, &body); e != nil {
		return nil, 0, e
	}
	beforeBody, beforeSummary, beforeExposure := skill["skill_body"], skill["summary"], skill["exposure_type"]
	if body.Exposure != nil && member(*body.Exposure, "tool", "resource", "prompt") {
		skill["exposure_type"] = *body.Exposure
	}
	if body.Risk != nil && member(*body.Risk, "low", "medium", "high") {
		skill["risk_level"] = *body.Risk
	}
	if body.Summary != nil {
		skill["summary"] = optional(strings.TrimSpace(*body.Summary))
	}
	if body.Body != nil {
		if len(*body.Body) > 1<<20 {
			return nil, 0, bad("Skill body is too large")
		}
		if strings.TrimSpace(*body.Body) == "" {
			skill["skill_body"] = nil
		} else {
			skill["skill_body"] = *body.Body
		}
	}
	statements := []turso.Statement{}
	if body.Routing != nil {
		routing := body.Routing
		for _, list := range [][]string{routing.Use, routing.Avoid, routing.Failures} {
			if len(list) > 128 {
				return nil, 0, bad("Too many routing hints")
			}
			for _, v := range list {
				if utf8.RuneCountInString(v) > 4096 {
					return nil, 0, bad("Routing hint is too long")
				}
			}
		}
		skill["use_when"] = toAny(routing.Use)
		skill["avoid_when"] = toAny(routing.Avoid)
		skill["failure_modes"] = toAny(routing.Failures)
		skill["invoke_first"] = routing.First
		id, e := identity.UUID()
		if e != nil {
			return nil, 0, e
		}
		statements = append(statements, turso.Statement{SQL: "DELETE FROM routing_rules WHERE compiled_skill_id=?", Args: values(sid)}, turso.Statement{SQL: "INSERT INTO routing_rules(id,compiled_skill_id,use_when_json,avoid_when_json,failure_modes_json,invoke_first,created_at) VALUES(?,?,?,?,?,?,?)", Args: values(id, sid, encode(routing.Use), encode(routing.Avoid), encode(routing.Failures), routing.First, s.now().Unix())})
	}
	if body.Runtime != nil {
		if e := applyRuntime(skill, body.Runtime); e != nil {
			return nil, 0, e
		}
		var doc map[string]any
		json.Unmarshal([]byte(str(skill["canonical_json"])), &doc)
		if doc != nil {
			source, _ := doc["source"].(map[string]any)
			runtimeSkillID := str(doc["id"])
			id, e := identity.UUID()
			if e != nil {
				return nil, 0, e
			}
			existing, e := s.one(ctx, "SELECT id FROM skill_runtime_overrides WHERE project_id=? AND skill_id=? AND scope=? ORDER BY created_at,id LIMIT 1", pid, runtimeSkillID, skill["scope"])
			if e != nil {
				return nil, 0, e
			}
			if existing != nil {
				id = str(existing["id"])
			}
			statements = append(statements, turso.Statement{SQL: "DELETE FROM skill_runtime_overrides WHERE project_id=? AND skill_id=? AND id<>?", Args: values(pid, runtimeSkillID, id)})
			if existing != nil {
				statements = append(statements, turso.Statement{SQL: "UPDATE skill_runtime_overrides SET metadata_json=?,source_checksum=?,base_checksum=?,is_stale=0,updated_at=? WHERE id=? AND project_id=?", Args: values(encode(body.Runtime), source["checksum"], source["checksum"], s.now().Unix(), id, pid)})
			} else {
				statements = append(statements, turso.Statement{SQL: "INSERT INTO skill_runtime_overrides(id,project_id,skill_id,scope,metadata_json,source_checksum,base_checksum,is_stale,created_at,updated_at) VALUES(?,?,?,?,?,?,?,0,?,?)", Args: values(id, pid, runtimeSkillID, skill["scope"], encode(body.Runtime), source["checksum"], source["checksum"], s.now().Unix(), s.now().Unix())})
			}
		}
	}
	if body.Replace && body.Schema != nil {
		schema := strings.TrimSpace(*body.Schema)
		if _, valid := structuredJSON(schema); schema != "" && !valid {
			return nil, 0, bad("schema_json must be valid JSON")
		}
		if schema == "" {
			schema = defaultSchema(skill)
		}
		skill["schema_json"] = schema
	} else if beforeExposure != skill["exposure_type"] || beforeSummary != skill["summary"] || (body.Routing != nil && skill["exposure_type"] == "resource") || skill["schema_json"] == nil {
		skill["schema_json"] = defaultSchema(skill)
	}
	status := "ready"
	if strings.TrimSpace(str(skill["summary"])) == "" {
		status = "not_publishable"
	} else if skill["risk_level"] == "high" {
		status = "needs_review"
	}
	tier := metadataTier(skill, false)
	if tier == "blocking" {
		status = "not_publishable"
	} else if tier == "warning" && status != "not_publishable" {
		status = "needs_review"
	} else if tier == "ok" && body.Status != nil && member(*body.Status, "ready", "needs_review", "not_publishable") {
		status = *body.Status
	}
	skill["status"] = status
	if body.Body != nil && beforeBody != skill["skill_body"] {
		if skill["body_diff_unified"] != nil {
			statements = append(statements, turso.Statement{SQL: "UPDATE releases SET skill_body_changes_count=MAX(0,skill_body_changes_count-1) WHERE id=? AND project_id=?", Args: values(rid, pid)})
		}
		skill["body_diff_unified"] = nil
		skill["body_diff_prior_release_id"] = nil
	}
	capType := skill["exposure_type"]
	if capType == "guidance" {
		capType = "prompt"
	}
	statements = append(statements, turso.Statement{SQL: "UPDATE compiled_skills SET exposure_type=?,risk_level=?,status=?,summary=?,skill_body=?,body_diff_unified=?,body_diff_prior_release_id=?,canonical_json=?,kind=?,scope=?,activation_mode=?,enforcement=?,priority=?,version=?,clarification_required=?,clarification_json=? WHERE id=? AND release_id=?", Args: values(skill["exposure_type"], skill["risk_level"], skill["status"], skill["summary"], skill["skill_body"], skill["body_diff_unified"], skill["body_diff_prior_release_id"], skill["canonical_json"], skill["kind"], skill["scope"], skill["activation_mode"], skill["enforcement"], skill["priority"], skill["version"], boolean(skill["clarification_required"]), encode(skill["clarification_questions"]), sid, rid)}, turso.Statement{SQL: "UPDATE capability_defs SET type=?,schema_json=? WHERE compiled_skill_id=?", Args: values(capType, skill["schema_json"], sid)}, revisionStatement(pid))
	if _, e = s.db.Batch(ctx, statements); e != nil {
		return nil, 0, e
	}
	return skill, 200, nil
}
func toAny(in []string) []any {
	out := make([]any, len(in))
	for i, v := range in {
		out[i] = v
	}
	return out
}
func applyRuntime(skill row, patch map[string]any) error {
	for key, value := range patch {
		if value == nil {
			delete(patch, key)
		}
	}
	var doc map[string]any
	if json.Unmarshal([]byte(str(skill["canonical_json"])), &doc) != nil || doc == nil {
		// Validate typed input even when this legacy row has no canonical document.
		// Swift decodes the patch before deciding whether an override can be applied.
		copySkill := row{}
		for key, value := range skill {
			copySkill[key] = value
		}
		copySkill["canonical_json"] = "{}"
		return applyRuntime(copySkill, patch)
	}
	enums := map[string][]string{"kind": {"operating", "task", "tool-use", "reference"}, "scope": {"global", "organization", "workspace", "repository", "task"}, "enforcement": {"advisory", "required"}}
	for key, choices := range enums {
		if value, ok := patch[key]; ok {
			v, ok := value.(string)
			if !ok || !member(v, choices...) {
				return bad("Invalid runtime " + key)
			}
			doc[key] = v
			skill[key] = v
		}
	}
	if v, ok := patch["priority"]; ok {
		n, ok := v.(float64)
		if !ok || n != float64(int(n)) {
			return bad("Invalid runtime priority")
		}
		if n < 0 {
			n = 0
		}
		if n > 100 {
			n = 100
		}
		doc["priority"] = int(n)
		skill["priority"] = int(n)
	}
	for _, key := range []string{"version", "lifecycle"} {
		if v, ok := patch[key]; ok {
			value, ok := v.(string)
			if !ok {
				return bad("Invalid runtime " + key)
			}
			doc[key] = value
			if key == "version" {
				skill[key] = value
			}
		}
	}
	if v, ok := patch["activation"]; ok {
		activation, ok := v.(map[string]any)
		if !ok || !member(str(activation["mode"]), "always", "intent", "event", "explicit") {
			return bad("Invalid runtime activation")
		}
		for _, key := range []string{"intents", "events", "tags", "examples"} {
			if list, exists := activation[key]; exists {
				items, ok := list.([]any)
				if !ok {
					return bad("Invalid activation list")
				}
				for _, item := range items {
					if _, ok := item.(string); !ok {
						return bad("Invalid activation list")
					}
				}
			} else {
				activation[key] = []any{}
			}
		}
		doc["activation"] = activation
		skill["activation_mode"] = activation["mode"]
	}
	if v, ok := patch["conflicts_with"]; ok {
		list, ok := v.([]any)
		if !ok {
			return bad("Invalid conflicts_with")
		}
		for _, item := range list {
			if _, ok := item.(string); !ok {
				return bad("Invalid conflicts_with")
			}
		}
		doc["conflictsWith"] = list
	}
	if v, ok := patch["requires"]; ok {
		list, ok := v.([]any)
		if !ok {
			return bad("Invalid runtime requirements")
		}
		for _, item := range list {
			req, ok := item.(map[string]any)
			if !ok {
				return bad("Invalid runtime requirement")
			}
			if _, ok := req["capability"].(string); !ok {
				return bad("Invalid runtime requirement capability")
			}
			if _, ok := req["required"].(bool); !ok {
				return bad("Invalid runtime requirement flag")
			}
			fallback := req["on_missing"]
			if fallback == nil {
				fallback = req["onMissing"]
			}
			if !member(str(fallback), "fail_activation", "warn", "return_draft", "request_provider_selection", "continue_without_action") {
				return bad("Invalid missing capability fallback")
			}
			req["on_missing"] = fallback
			delete(req, "onMissing")
		}
		doc["requires"] = list
	}
	validation, _ := doc["validation"].(map[string]any)
	if validation != nil {
		fields, _ := validation["missingFields"].([]any)
		remaining := []any{}
		questions := []any{}
		oldQuestions, _ := skill["clarification_questions"].([]any)
		for _, field := range fields {
			if _, resolved := patch[str(field)]; !resolved {
				remaining = append(remaining, field)
				for _, q := range oldQuestions {
					if question, ok := q.(map[string]any); ok && question["field"] == field {
						questions = append(questions, q)
					}
				}
			}
		}
		validation["missingFields"] = remaining
		validation["clarificationRequired"] = len(remaining) > 0
		skill["clarification_required"] = len(remaining) > 0
		skill["clarification_questions"] = questions
	}
	for _, key := range []string{"kind", "scope", "enforcement", "priority", "version"} {
		if value, ok := doc[key]; ok {
			skill[key] = value
		}
	}
	if activation, ok := doc["activation"].(map[string]any); ok {
		skill["activation_mode"] = activation["mode"]
	}
	skill["canonical_json"] = encode(doc)
	return nil
}
