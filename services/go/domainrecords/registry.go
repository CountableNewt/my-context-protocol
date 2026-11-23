// Package domainrecords stores durable product state in owner-controlled PDS
// records and rebuilds its SQL projections from confirmed manifest generations.
package domainrecords

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/CountableNewt/my-context-protocol/services/go/turso"
)

const Namespace = "dev.mycontextprotocol.data."

type Field struct{ Name, Type string }
type Definition struct {
	Table, Collection, Selector string
	Fields                      []Field
	Telemetry                   bool
}

func fields(text, integer, real, boolean string) []Field {
	out := []Field{}
	for kind, names := range map[string]string{"string": text, "integer": integer, "decimal": real, "boolean": boolean} {
		for _, name := range strings.Fields(names) {
			out = append(out, Field{name, kind})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Ordered parent-before-child for foreign-key-safe projection. Private tokens,
// permissions, verification proofs and source bodies are deliberately absent.
var definitions = []Definition{
	{"projects", Namespace + "project", "id=?", fields("name slug subdomain active_release_id custom_domain mcp_catalog_markdown_override", "", "created_at suspended_at", ""), false},
	{"repo_connections", Namespace + "repository", "project_id=?", fields("provider repo_owner repo_name default_branch auth_type", "", "created_at", ""), false},
	{"atproto_repo_bindings", Namespace + "repositoryIdentity", "connection_id IN(SELECT id FROM repo_connections WHERE project_id=?)", fields("repository_did owner_did actor_did branch", "", "", "is_private"), false},
	{"releases", Namespace + "release", "project_id=?", fields("commit_sha status error_summary", "skill_body_changes_count", "created_at", ""), false},
	{"project_runtime_settings", Namespace + "runtimeSettings", "project_id=?", fields("embedding_provider embedding_model provider_preferences_json", "telemetry_retention_days", "updated_at", "telemetry_enabled semantic_enabled feedback_issue_creation_enabled"), false},
	{"skill_packages", Namespace + "package", "release_id IN(SELECT id FROM releases WHERE project_id=?)", fields("release_id path name description hash validation_status", "", "created_at", ""), false},
	{"compiled_skills", Namespace + "frontmatter", "release_id IN(SELECT id FROM releases WHERE project_id=?)", fields("release_id skill_package_id path name summary exposure_type risk_level status skill_id kind scope activation_mode enforcement version source_checksum canonical_json clarification_json source_policy_json source_policy_base_checksum", "canonical_schema_version priority", "created_at", "repo_specific yaml_frontmatter_present clarification_required source_policy_stale"), false},
	{"capability_defs", Namespace + "capability", "compiled_skill_id IN(SELECT id FROM compiled_skills WHERE release_id IN(SELECT id FROM releases WHERE project_id=?))", fields("compiled_skill_id capability_name type schema_json side_effect_level", "", "created_at", ""), false},
	{"routing_rules", Namespace + "routingRule", "compiled_skill_id IN(SELECT id FROM compiled_skills WHERE release_id IN(SELECT id FROM releases WHERE project_id=?))", fields("compiled_skill_id use_when_json avoid_when_json failure_modes_json", "invoke_first", "created_at", ""), false},
	{"tools_index", Namespace + "tool", "skill_package_id IN(SELECT id FROM skill_packages WHERE release_id IN(SELECT id FROM releases WHERE project_id=?))", fields("skill_package_id tool_name schema_json handler_type", "", "created_at", ""), false},
	{"validation_reports", Namespace + "validation", "release_id IN(SELECT id FROM releases WHERE project_id=?)", fields("release_id report_json", "", "created_at", ""), false},
	{"skill_assignments", Namespace + "assignment", "project_id=?", fields("repo_connection_id skill_id scope activation_mode target_type target_id", "priority", "created_at", "required"), false},
	{"skill_runtime_overrides", Namespace + "runtimeOverride", "project_id=?", fields("repo_connection_id skill_id scope metadata_json source_checksum writeback_pr_url base_checksum", "", "created_at updated_at", "is_stale"), false},
	{"request_logs", Namespace + "requestLog", "project_id=?", fields("release_id client_id method status error_code mcp_capability_kind mcp_capability_key", "latency_ms", "timestamp", ""), true},
	{"skill_runtime_events", Namespace + "telemetry", "project_id=?", fields("trace_id event_type skill_id reason_code request_hash detail_json release_id skill_version source_checksum client_identity source", "", "score created_at", ""), true},
	{"skill_usage_reports", Namespace + "usageReport", "project_id=?", fields("client_identity report_id payload_hash", "recorded_count", "created_at", ""), true},
	{"skill_feedback", Namespace + "feedback", "project_id=?", fields("skill_id skill_version source_path source_revision category summary evidence suggested_change issue_draft_json external_issue_url", "", "created_at", ""), true},
}

func Registry() []Definition {
	out := append([]Definition(nil), definitions...)
	for i := range out {
		out[i].Fields = append([]Field(nil), out[i].Fields...)
	}
	return out
}
func definition(table string) (Definition, error) {
	for _, d := range definitions {
		if d.Table == table {
			return d, nil
		}
	}
	return Definition{}, errors.New("table is not public domain state")
}
func byCollection(collection string) (Definition, error) {
	for _, d := range definitions {
		if d.Collection == collection {
			return d, nil
		}
	}
	return Definition{}, errors.New("unknown public record collection")
}
func fieldValue(field Field, v turso.Value) (any, error) {
	if v.Type == "null" {
		return nil, nil
	}
	switch field.Type {
	case "string":
		if v.Type != "text" {
			return nil, fmt.Errorf("%s requires text", field.Name)
		}
		s, ok := v.Value.(string)
		if !ok {
			return nil, errors.New("invalid text cell")
		}
		if field.Name == "canonical_json" {
			var metadata map[string]json.RawMessage
			if json.Unmarshal([]byte(s), &metadata) != nil || metadata == nil {
				return nil, errors.New("invalid canonical metadata JSON")
			}
			delete(metadata, "instructions")
			raw, err := json.Marshal(metadata)
			if err != nil {
				return nil, err
			}
			s = string(raw)
		}
		return s, nil
	case "integer", "boolean":
		if v.Type != "integer" {
			return nil, fmt.Errorf("%s requires integer", field.Name)
		}
		n, err := strconv.ParseInt(fmt.Sprint(v.Value), 10, 64)
		if err != nil {
			return nil, err
		}
		if field.Type == "boolean" {
			if n != 0 && n != 1 {
				return nil, errors.New("invalid boolean")
			}
			return n == 1, nil
		}
		return json.Number(strconv.FormatInt(n, 10)), nil
	case "decimal":
		if v.Type != "float" && v.Type != "integer" {
			return nil, errors.New("invalid decimal cell")
		}
		s := fmt.Sprint(v.Value)
		number, err := strconv.ParseFloat(s, 64)
		if err != nil || s == "NaN" || strings.Contains(s, "Inf") {
			return nil, errors.New("invalid finite decimal")
		}
		return strconv.FormatFloat(number, 'g', -1, 64), nil
	}
	return nil, errors.New("unknown field type")
}

// Schemas returns direct-field record contracts for publication/code generation.
func Schemas() map[string][]byte {
	out := map[string][]byte{}
	for _, d := range definitions {
		props := map[string]any{"projectId": map[string]any{"type": "string"}, "legacyId": map[string]any{"type": "string"}}
		for _, f := range d.Fields {
			kind := f.Type
			if kind == "decimal" {
				kind = "string"
			}
			props[f.Name] = map[string]any{"type": kind}
		}
		mandatory := append([]string{"projectId", "legacyId"}, strings.Fields(required[d.Table])...)
		v := map[string]any{"lexicon": 1, "id": d.Collection, "defs": map[string]any{"main": map[string]any{"type": "record", "key": "any", "record": map[string]any{"type": "object", "required": mandatory, "properties": props}}}}
		out[d.Collection], _ = json.MarshalIndent(v, "", "  ")
	}
	strong := map[string]any{"type": "ref", "ref": "com.atproto.repo.strongRef"}
	for _, name := range []string{"manifest", "manifestShard"} {
		limit := 1000
		if name == "manifestShard" {
			limit = 100
		}
		props := map[string]any{"projectId": map[string]any{"type": "string"}, "digest": map[string]any{"type": "string"}, "refs": map[string]any{"type": "array", "maxLength": limit, "items": strong}}
		v := map[string]any{"lexicon": 1, "id": Namespace + name, "defs": map[string]any{"main": map[string]any{"type": "record", "key": "any", "record": map[string]any{"type": "object", "required": []string{"projectId", "digest", "refs"}, "properties": props}}}}
		out[Namespace+name], _ = json.MarshalIndent(v, "", "  ")
	}
	return out
}

func (d Definition) IDColumn() string {
	if d.Table == "atproto_repo_bindings" {
		return "connection_id"
	}
	return "id"
}
