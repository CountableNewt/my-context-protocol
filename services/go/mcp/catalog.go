package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	skillruntime "github.com/CountableNewt/my-context-protocol/services/go/runtime"
	"sort"
	"strings"
)

var runtimeNames = []string{"resolve_context", "get_skill", "report_skill_feedback", "report_skill_usage"}
var aliases = []string{"mycontext_catalog", "discover_skills", "list_capabilities"}

func reserved(name string) bool { return contains(runtimeNames, name) || contains(aliases, name) }
func rich(version string) bool  { return version == "2025-06-18" || version == "2025-11-25" }
func supported(version string) bool {
	return contains([]string{"2024-11-05", "2025-03-26", "2025-06-18", "2025-11-25"}, version)
}
func stringSchema() map[string]any { return map[string]any{"type": "string"} }
func objectSchema(properties map[string]any, required ...string) map[string]any {
	value := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
	if len(required) > 0 {
		value["required"] = required
	}
	return value
}
func inputSchema(name string) map[string]any {
	switch name {
	case "resolve_context":
		context := map[string]any{}
		for _, key := range []string{"user", "organization", "workspace", "repository", "task"} {
			context[key] = stringSchema()
		}
		props := map[string]any{"request": stringSchema(), "event": stringSchema(), "context": objectSchema(context), "current_skill_ids": map[string]any{"type": "array", "items": stringSchema(), "uniqueItems": true}, "available_tools": map[string]any{"type": "array", "items": objectSchema(map[string]any{"server": stringSchema(), "name": stringSchema(), "description": stringSchema(), "inputSchema": stringSchema(), "provider": stringSchema()}, "server", "name")}}
		for key, value := range context {
			props[key] = value
		}
		return objectSchema(props, "request")
	case "get_skill":
		return objectSchema(map[string]any{"skill_id": stringSchema(), "version": stringSchema(), "path": stringSchema()}, "skill_id")
	case "report_skill_feedback":
		return objectSchema(map[string]any{"skill_id": stringSchema(), "version": stringSchema(), "category": map[string]any{"type": "string", "enum": []string{"missing_guidance", "ambiguous_instruction", "incorrect_instruction", "conflict", "missing_capability", "poor_discovery", "outdated_content", "other"}}, "summary": map[string]any{"type": "string", "maxLength": 2000}, "evidence": map[string]any{"type": "string", "maxLength": 8000}, "suggested_change": map[string]any{"type": "string", "maxLength": 8000}, "create_issue": map[string]any{"type": "boolean"}}, "skill_id", "version", "category", "summary", "evidence")
	case "report_skill_usage":
		return objectSchema(map[string]any{"report_id": map[string]any{"type": "string", "minLength": 1, "maxLength": 128}, "trace_id": map[string]any{"type": "string", "format": "uuid"}, "skills": map[string]any{"type": "array", "minItems": 1, "maxItems": 100, "items": objectSchema(map[string]any{"skill_id": stringSchema(), "version": stringSchema(), "release_id": map[string]any{"type": "string", "format": "uuid"}, "checksum": stringSchema(), "outcome": map[string]any{"type": "string", "enum": []string{"used", "skipped"}}, "skip_reason": map[string]any{"type": "string", "enum": []string{"not_relevant", "redundant", "instruction_conflict", "missing_capability", "task_changed", "other"}}}, "skill_id", "version", "outcome")}}, "report_id", "skills")
	}
	return objectSchema(map[string]any{})
}
func outputSchema(name string) map[string]any {
	switch name {
	case "report_skill_usage":
		return objectSchema(map[string]any{"status": map[string]any{"type": "string", "enum": []string{"recorded", "already_recorded", "collection_disabled"}}, "recorded_count": map[string]any{"type": "integer", "minimum": 0}}, "status", "recorded_count")
	case "get_skill":
		return map[string]any{"type": "object", "properties": map[string]any{"schemaVersion": map[string]any{"type": "integer"}, "kind": stringSchema(), "id": stringSchema(), "version": stringSchema(), "checksum": stringSchema(), "mediaType": stringSchema(), "resourceUri": stringSchema(), "source": map[string]any{"type": "object", "additionalProperties": true}}, "required": []string{"schemaVersion", "kind", "id", "version", "checksum", "mediaType", "resourceUri", "source"}, "additionalProperties": true}
	case "resolve_context":
		props := map[string]any{"schemaVersion": map[string]any{"type": "integer"}, "traceId": map[string]any{"type": "string", "format": "uuid"}, "eventCanonical": map[string]any{"type": "boolean"}}
		required := []string{"schemaVersion", "traceId"}
		for _, field := range []string{"activeSkills", "suggestedTaskSkills", "capabilityBindings", "conflicts", "nextActions", "resolutionTrace"} {
			props[field] = map[string]any{"type": "array", "items": map[string]any{"type": "object", "additionalProperties": true}}
			required = append(required, field)
		}
		for _, field := range []string{"missingRequirements", "missingContext"} {
			props[field] = map[string]any{"type": "array", "items": stringSchema()}
			required = append(required, field)
		}
		return objectSchema(props, required...)
	case "report_skill_feedback":
		return objectSchema(map[string]any{"schemaVersion": map[string]any{"type": "integer"}, "feedbackId": map[string]any{"type": "string", "format": "uuid"}, "effectStatus": map[string]any{"type": "string", "enum": []string{"draft"}}, "issueDraft": map[string]any{"type": "object", "additionalProperties": true}, "creationAuthorized": map[string]any{"type": "boolean"}, "message": stringSchema()}, "schemaVersion", "feedbackId", "effectStatus", "issueDraft", "creationAuthorized", "message")
	}
	return map[string]any{"type": "object"}
}
func descriptions() map[string]string {
	return map[string]string{"resolve_context": "Task bootstrap: returns ordered active and suggested skills, conflicts, provenance, capability bindings, and a resolution trace.", "get_skill": "Returns the complete versioned compiled skill document, including original Markdown and provenance.", "report_skill_usage": "When project telemetry is enabled, report one task-end batch of actual skill uses or consciously considered skips. Missing reports remain unknown. Retry with the same report_id; no reasoning transcripts or free-text explanations.", "report_skill_feedback": "Stores version-specific skill feedback and returns an issue draft; never implies an external side effect occurred."}
}
func findCompiled(snapshot skillruntime.Snapshot, id string) *skillruntime.Skill {
	for i := range snapshot.Skills {
		if snapshot.Skills[i].ID == id {
			return &snapshot.Skills[i]
		}
	}
	return nil
}
func mergedDescription(skill skillruntime.Skill) string {
	parts := []string{}
	if strings.TrimSpace(skill.Summary) != "" {
		parts = append(parts, strings.TrimSpace(skill.Summary))
	}
	for _, part := range []struct {
		label  string
		values []string
	}{{"When to use:", skill.Hints.UseWhen}, {"Avoid when:", skill.Hints.AvoidWhen}, {"Failure modes:", skill.Hints.FailureModes}} {
		if len(part.values) > 0 {
			parts = append(parts, part.label+"\n- "+strings.Join(part.values, "\n- "))
		}
	}
	if skill.Hints.InvokeFirst {
		parts = append(parts, "Invoke first: prefer calling this tool early in the session when relevant.")
	}
	value := strings.Join(parts, "\n\n")
	if len([]rune(value)) > 1800 {
		value = string([]rune(value)[:1799]) + "…"
	}
	return value
}
func tools(snapshot skillruntime.Snapshot, version string) []map[string]any {
	result := []map[string]any{}
	titles := map[string]string{"resolve_context": "Resolve project context", "get_skill": "Get complete skill", "report_skill_feedback": "Report skill feedback", "report_skill_usage": "Report skill usage"}
	for _, name := range runtimeNames {
		tool := map[string]any{"name": name, "description": descriptions()[name], "inputSchema": inputSchema(name)}
		if rich(version) {
			readOnly := name != "report_skill_feedback" && name != "report_skill_usage"
			tool["title"] = titles[name]
			tool["outputSchema"] = outputSchema(name)
			tool["annotations"] = map[string]any{"title": titles[name], "readOnlyHint": readOnly, "destructiveHint": false, "idempotentHint": readOnly || name == "report_skill_usage", "openWorldHint": false}
		}
		result = append(result, tool)
	}
	if snapshot.Settings.LegacyCompiledTools {
		legacy := []map[string]any{}
		for _, cap := range snapshot.Capabilities {
			if cap.Type != "tool" || reserved(cap.Name) {
				continue
			}
			skill := findCompiled(snapshot, cap.SkillID)
			if skill == nil {
				continue
			}
			schema := map[string]any{}
			if json.Unmarshal(cap.Schema, &schema) != nil || schema["type"] != "object" {
				schema = objectSchema(map[string]any{})
			}
			legacy = append(legacy, map[string]any{"name": cap.Name, "description": mergedDescription(*skill), "inputSchema": schema})
		}
		sort.SliceStable(legacy, func(i, j int) bool { return legacy[i]["name"].(string) < legacy[j]["name"].(string) })
		result = append(result, legacy...)
	}
	return result
}

type resourceMeta struct {
	URI          string   `json:"uri"`
	MIME         string   `json:"mimeType"`
	UseWhen      []string `json:"use_when,omitempty"`
	AvoidWhen    []string `json:"avoid_when,omitempty"`
	FailureModes []string `json:"failure_modes,omitempty"`
	InvokeFirst  bool     `json:"invoke_first,omitempty"`
}

func resourceMetadata(cap skillruntime.Capability) (resourceMeta, bool) {
	var meta resourceMeta
	if json.Unmarshal(cap.Schema, &meta) != nil || meta.URI == "" {
		return meta, false
	}
	if meta.MIME == "" {
		meta.MIME = "text/markdown"
	}
	return meta, true
}
func resources(snapshot skillruntime.Snapshot) []map[string]any {
	result := []map[string]any{}
	for _, cap := range snapshot.Capabilities {
		if cap.Type != "resource" {
			continue
		}
		meta, ok := resourceMetadata(cap)
		skill := findCompiled(snapshot, cap.SkillID)
		if !ok || skill == nil {
			continue
		}
		entry := map[string]any{"uri": meta.URI, "name": skill.Name, "description": skill.Summary, "mimeType": meta.MIME}
		if len(meta.UseWhen) > 0 {
			entry["use_when"] = meta.UseWhen
		}
		if len(meta.AvoidWhen) > 0 {
			entry["avoid_when"] = meta.AvoidWhen
		}
		if len(meta.FailureModes) > 0 {
			entry["failure_modes"] = meta.FailureModes
		}
		if meta.InvokeFirst {
			entry["invoke_first"] = true
		}
		result = append(result, entry)
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i]["uri"].(string) < result[j]["uri"].(string) })
	return result
}
func prompts(snapshot skillruntime.Snapshot) []map[string]any {
	result := []map[string]any{}
	for _, cap := range snapshot.Capabilities {
		if cap.Type != "prompt" {
			continue
		}
		skill := findCompiled(snapshot, cap.SkillID)
		if skill != nil {
			result = append(result, map[string]any{"name": cap.Name, "description": mergedDescription(*skill)})
		}
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i]["name"].(string) < result[j]["name"].(string) })
	return result
}

type cursorPayload struct {
	Scope  string `json:"scope"`
	Offset int    `json:"offset"`
}

func page(items []map[string]any, cursor, scope string) ([]map[string]any, string, error) {
	offset := 0
	if cursor != "" {
		if len(cursor) > 1024 {
			return nil, "", errors.New("Invalid pagination cursor")
		}
		raw, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil {
			return nil, "", errors.New("Invalid pagination cursor")
		}
		var value cursorPayload
		if json.Unmarshal(raw, &value) != nil || value.Scope != scope || value.Offset < 0 || value.Offset >= len(items) {
			return nil, "", errors.New("Invalid pagination cursor")
		}
		offset = value.Offset
	}
	end := min(len(items), offset+50)
	next := ""
	if end < len(items) {
		raw, _ := json.Marshal(cursorPayload{scope, end})
		next = base64.RawURLEncoding.EncodeToString(raw)
	}
	return items[offset:end], next, nil
}
func generatedMarkdown(snapshot skillruntime.Snapshot) string {
	lines := []string{"# MCP catalog", "", "Synthetic tool `mycontext_catalog` lists this overview; compiled tools and prompts use each skill’s package slug as its MCP name.", ""}
	if snapshot.ReleaseID == "" {
		return "# MCP catalog\n\n**No active release.**\n\nConnect a Git repository, sync, then activate a ready release in the dashboard."
	}
	for _, section := range []struct{ title, kind string }{{"Tools", "tool"}, {"Resources", "resource"}, {"Prompts", "prompt"}} {
		lines = append(lines, "## "+section.title)
		count := 0
		for _, cap := range snapshot.Capabilities {
			if cap.Type != section.kind {
				continue
			}
			skill := findCompiled(snapshot, cap.SkillID)
			if skill == nil {
				continue
			}
			if section.kind == "resource" {
				meta, ok := resourceMetadata(cap)
				if !ok {
					continue
				}
				lines = append(lines, fmt.Sprintf("- **%s** — URI `%s`", skill.Name, meta.URI))
				if skill.Summary != "" {
					lines = append(lines, "  - "+skill.Summary)
				}
			} else {
				summary := mergedDescription(*skill)
				if summary == "" {
					summary = "(no summary)"
				}
				lines = append(lines, fmt.Sprintf("- **`%s`** — %s", cap.Name, strings.ReplaceAll(summary, "\n", " ")))
			}
			count++
		}
		if count == 0 {
			lines = append(lines, "_None._")
		}
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

// Catalog adapts the same active snapshot to the dashboard API contract.
func (s *Service) Catalog(ctx context.Context, projectID string) (map[string]any, error) {
	snapshot, err := s.store.Snapshot(ctx, projectID)
	if err != nil {
		return nil, err
	}
	generated := generatedMarkdown(snapshot)
	effective := generated
	var override any
	if strings.TrimSpace(snapshot.CatalogOverride) != "" {
		effective = strings.TrimSpace(snapshot.CatalogOverride)
		override = snapshot.CatalogOverride
	}
	dashboardTools := []map[string]any{}
	for _, tool := range tools(snapshot, "2025-11-25") {
		schema, _ := json.Marshal(tool["inputSchema"])
		dashboardTools = append(dashboardTools, map[string]any{"name": tool["name"], "description": tool["description"], "input_schema_json": string(schema)})
	}
	dashboardResources := resources(snapshot)
	for _, r := range dashboardResources {
		r["mime_type"] = r["mimeType"]
		delete(r, "mimeType")
	}
	releaseStatus := snapshot.ReleaseStatus
	mcpURL := ""
	host := snapshot.CustomDomain
	if host == "" && snapshot.Subdomain != "" && s.config.BaseDomain != "" {
		host = snapshot.Subdomain + "." + s.config.BaseDomain
	}
	if host != "" {
		mcpURL = "https://" + host + s.config.Path
	}
	return map[string]any{"release_id": nilIfEmpty(snapshot.ReleaseID), "release_status": nilIfEmpty(releaseStatus), "mcp_url": nilIfEmpty(mcpURL), "mcp_oauth_enabled": s.config.OAuthEnabled, "catalog_markdown": effective, "catalog_markdown_generated": generated, "catalog_markdown_override": override, "tools": dashboardTools, "resources": dashboardResources, "prompts": prompts(snapshot)}, nil
}
func nilIfEmpty(value string) any {
	if value == "" {
		return nil
	}
	return value
}
