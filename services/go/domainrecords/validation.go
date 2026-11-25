package domainrecords

import (
	"errors"
	"fmt"
	"strings"
)

var required = map[string]string{
	"skill_usage_reports":   "client_identity report_id payload_hash recorded_count",
	"atproto_repo_bindings": "repository_did owner_did actor_did is_private branch",
	"projects":              "name slug", "repo_connections": "provider repo_owner repo_name default_branch auth_type", "releases": "commit_sha status skill_body_changes_count", "project_runtime_settings": "telemetry_enabled telemetry_retention_days semantic_enabled feedback_issue_creation_enabled", "skill_packages": "release_id path name validation_status", "compiled_skills": "release_id skill_package_id path name exposure_type risk_level repo_specific status yaml_frontmatter_present canonical_schema_version clarification_required source_policy_stale", "capability_defs": "compiled_skill_id capability_name type side_effect_level", "routing_rules": "compiled_skill_id", "tools_index": "skill_package_id tool_name handler_type", "validation_reports": "release_id report_json", "skill_assignments": "skill_id scope activation_mode required priority target_type target_id", "skill_runtime_overrides": "skill_id scope metadata_json is_stale", "request_logs": "method status", "skill_runtime_events": "trace_id event_type detail_json", "skill_feedback": "skill_id skill_version source_path category summary issue_draft_json",
}

func validateReview(r Review) error {
	entities := map[string]bool{}
	projectCount := 0
	for _, entry := range r.Entries {
		entities[entry.Table+"\x00"+entry.ID] = true
		if entry.Table == "projects" {
			if entry.ID != r.ProjectID {
				return errors.New("cross-project root")
			}
			projectCount++
		}
		for _, field := range strings.Fields(required[entry.Table]) {
			v, ok := entry.Values[field]
			if !ok || v.Type == "null" {
				return fmt.Errorf("%s.%s is required", entry.Table, field)
			}
		}
	}
	if projectCount != 1 {
		return errors.New("canonical snapshot requires exactly one project")
	}
	parents := map[string]string{"release_id": "releases", "active_release_id": "releases", "skill_package_id": "skill_packages", "compiled_skill_id": "compiled_skills", "repo_connection_id": "repo_connections"}
	for _, entry := range r.Entries {
		if entry.Table == "atproto_repo_bindings" {
			if !entities["repo_connections\x00"+entry.ID] || fmt.Sprint(entry.Values["actor_did"].Value) != r.OwnerDID {
				return errors.New("repository identity is not bound to this owner and connection")
			}
		}
		for field, table := range parents {
			v, ok := entry.Values[field]
			if !ok || v.Type == "null" {
				continue
			}
			id, ok := v.Value.(string)
			if !ok || !entities[table+"\x00"+id] {
				return fmt.Errorf("%s has missing or cross-project %s", entry.Table, field)
			}
		}
	}
	return nil
}

func hasTelemetry(entry Entry, d Definition) bool {
	if d.Telemetry {
		return true
	}
	if entry.Table == "project_runtime_settings" {
		if v, ok := entry.Values["telemetry_enabled"]; ok && fmt.Sprint(v.Value) == "1" {
			return true
		}
	}
	return false
}
