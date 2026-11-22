package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/CountableNewt/my-context-protocol/services/go/identity"
	skillruntime "github.com/CountableNewt/my-context-protocol/services/go/runtime"
	"github.com/CountableNewt/my-context-protocol/services/go/security"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
	"sort"
	"strings"
	"time"
)

type InputError struct {
	Message string
	Status  int
}

func (e *InputError) Error() string { return e.Message }
func invalid(message string) error  { return &InputError{Message: message, Status: 400} }
func rawString(args map[string]json.RawMessage, key string, maxLength int, required bool) (string, error) {
	raw, exists := args[key]
	if !exists {
		if required {
			return "", invalid(key + " is required")
		}
		return "", nil
	}
	if len(raw) == 0 || raw[0] != '"' {
		return "", invalid(key + " must be a string")
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return "", invalid(key + " must be a string")
	}
	value = strings.TrimSpace(value)
	if len([]rune(value)) > maxLength || required && value == "" {
		return "", invalid(key + " is missing or too long")
	}
	return value, nil
}
func (s SQLStore) Feedback(ctx context.Context, snapshot skillruntime.Snapshot, args map[string]json.RawMessage) (any, error) {
	id, err := rawString(args, "skill_id", 128, true)
	if err != nil {
		return nil, err
	}
	version, err := rawString(args, "version", 512, true)
	if err != nil {
		return nil, err
	}
	category, err := rawString(args, "category", 64, true)
	if err != nil {
		return nil, err
	}
	if !contains([]string{"missing_guidance", "ambiguous_instruction", "incorrect_instruction", "conflict", "missing_capability", "poor_discovery", "outdated_content", "other"}, category) {
		return nil, invalid("invalid feedback category")
	}
	summary, err := rawString(args, "summary", 2000, true)
	if err != nil {
		return nil, err
	}
	evidence, err := rawString(args, "evidence", 8000, true)
	if err != nil {
		return nil, err
	}
	suggestion, err := rawString(args, "suggested_change", 8000, false)
	if err != nil {
		return nil, err
	}
	create := false
	if raw, exists := args["create_issue"]; exists {
		if string(raw) != "true" && string(raw) != "false" {
			return nil, invalid("create_issue must be a boolean")
		}
		_ = json.Unmarshal(raw, &create)
	}
	skill, err := snapshot.FindSkill(id, version)
	if err != nil {
		return nil, invalid("The observed skill version is not active in this project")
	}
	doc := skill.Document
	draftSuggestion := suggestion
	if draftSuggestion == "" {
		draftSuggestion = "Not supplied"
	}
	draft := map[string]string{"title": "[Skill feedback] " + id + ": " + summary, "body": fmt.Sprintf("Skill: %s@%s\nSource: %s\nCategory: %s\n\nSummary: %s\n\nEvidence: %s\n\nSuggested change: %s", id, version, doc.Source.Path, category, summary, evidence, draftSuggestion)}
	encoded, _ := json.Marshal(draft)
	feedbackID, err := identity.UUID()
	if err != nil {
		return nil, err
	}
	revision := ""
	if doc.Source.Revision != nil {
		revision = *doc.Source.Revision
	}
	result, err := s.DB.Execute(ctx, turso.Statement{SQL: `INSERT INTO skill_feedback(id,project_id,skill_id,skill_version,source_path,source_revision,category,summary,evidence,suggested_change,issue_draft_json,created_at) SELECT ?,?,?,?,?,?,?,?,?,?,?,? WHERE EXISTS(SELECT 1 FROM projects WHERE id=? AND active_release_id=?) RETURNING id,COALESCE((SELECT feedback_issue_creation_enabled FROM project_runtime_settings WHERE project_id=?),0)`, Args: []turso.Value{turso.Text(feedbackID), turso.Text(snapshot.ProjectID), turso.Text(id), turso.Text(version), turso.Text(doc.Source.Path), opt(revision), turso.Text(category), turso.Text(summary), turso.Text(evidence), opt(suggestion), turso.Text(string(encoded)), turso.Integer(time.Now().Unix()), turso.Text(snapshot.ProjectID), turso.Text(snapshot.ReleaseID), turso.Text(snapshot.ProjectID)}, WantRows: true})
	if err != nil {
		return nil, err
	}
	if len(result.Rows) == 0 {
		return nil, invalid("The observed skill version is no longer active in this project")
	}
	authorized := false
	if create && len(result.Rows[0]) > 1 {
		authorized = number(row{"enabled": result.Rows[0][1]}, "enabled") == 1
	}
	message := "Feedback was stored and no external side effect occurred."
	if authorized {
		message = "Issue creation is authorized, but the harness must execute the returned draft through its bound issue.create tool."
	}
	return map[string]any{"schemaVersion": 1, "feedbackId": feedbackID, "effectStatus": "draft", "issueDraft": draft, "creationAuthorized": authorized, "message": message}, nil
}

type usageItem struct {
	SkillID    string  `json:"skill_id"`
	Version    string  `json:"version"`
	ReleaseID  *string `json:"release_id,omitempty"`
	Checksum   *string `json:"checksum,omitempty"`
	Outcome    string  `json:"outcome"`
	SkipReason *string `json:"skip_reason,omitempty"`
}
type usageInput struct {
	ReportID string      `json:"report_id"`
	TraceID  *string     `json:"trace_id,omitempty"`
	Skills   []usageItem `json:"skills"`
}

func validUUID(raw string) bool {
	if len(raw) != 36 {
		return false
	}
	for i, c := range raw {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
		} else if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}
func usageSortKey(i usageItem) string {
	release, checksum, reason := "", "", ""
	if i.ReleaseID != nil {
		release = *i.ReleaseID
	}
	if i.Checksum != nil {
		checksum = *i.Checksum
	}
	if i.SkipReason != nil {
		reason = *i.SkipReason
	}
	return strings.Join([]string{i.SkillID, i.Version, release, checksum, i.Outcome, reason}, "\x00")
}
func swiftSortedJSON(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var obj any
	if err = json.Unmarshal(raw, &obj); err != nil {
		return nil, err
	}
	var output bytes.Buffer
	enc := json.NewEncoder(&output)
	enc.SetEscapeHTML(false)
	if err = enc.Encode(obj); err != nil {
		return nil, err
	}
	return []byte(strings.ReplaceAll(strings.TrimSuffix(output.String(), "\n"), "/", "\\/")), nil
}
func (s SQLStore) Usage(ctx context.Context, snapshot skillruntime.Snapshot, client string, args map[string]json.RawMessage) (any, error) {
	encoded, _ := json.Marshal(args)
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var input usageInput
	if decoder.Decode(&input) != nil || strings.TrimSpace(input.ReportID) == "" || len(input.ReportID) > 128 || len(input.Skills) < 1 || len(input.Skills) > 100 {
		return nil, invalid("Expected report_id, optional trace_id, and 1–100 skill outcomes; explanatory notes are not accepted")
	}
	if input.TraceID != nil {
		if !validUUID(*input.TraceID) {
			return nil, invalid("invalid trace_id")
		}
		*input.TraceID = strings.ToUpper(*input.TraceID)
	}
	for i := range input.Skills {
		item := &input.Skills[i]
		if strings.TrimSpace(item.SkillID) == "" || len(item.SkillID) > 128 || strings.TrimSpace(item.Version) == "" || len(item.Version) > 512 || !contains([]string{"used", "skipped"}, item.Outcome) {
			return nil, invalid("invalid skill outcome")
		}
		if item.Checksum != nil && (strings.TrimSpace(*item.Checksum) == "" || len(*item.Checksum) > 128) {
			return nil, invalid("invalid checksum")
		}
		if item.ReleaseID != nil {
			if !validUUID(*item.ReleaseID) {
				return nil, invalid("invalid release_id")
			}
			*item.ReleaseID = strings.ToUpper(*item.ReleaseID)
		}
		if item.Outcome == "skipped" {
			if item.SkipReason == nil || !contains([]string{"not_relevant", "redundant", "instruction_conflict", "missing_capability", "task_changed", "other"}, *item.SkipReason) {
				return nil, invalid("skipped outcomes require a categorized skip_reason")
			}
		} else if item.SkipReason != nil {
			return nil, invalid("used outcomes cannot contain skip_reason")
		}
	}
	sort.Slice(input.Skills, func(i, j int) bool { return usageSortKey(input.Skills[i]) < usageSortKey(input.Skills[j]) })
	digestJSON, err := swiftSortedJSON(input)
	if err != nil {
		return nil, err
	}
	digest := security.HashToken(string(digestJSON))
	reportKey := security.HashToken(input.ReportID)
	settings, err := query(ctx, s.DB, "SELECT telemetry_enabled,telemetry_retention_days FROM project_runtime_settings WHERE project_id=?", turso.Text(snapshot.ProjectID))
	if err != nil {
		return nil, err
	}
	if len(settings) == 0 || !boolValue(settings[0], "telemetry_enabled") {
		return map[string]any{"status": "collection_disabled", "recorded_count": 0}, nil
	}
	now := time.Now()
	retention := max(int64(1), number(settings[0], "telemetry_retention_days"))
	cutoff := now.Unix() - retention*86400
	existing, err := query(ctx, s.DB, "SELECT payload_hash,recorded_count FROM skill_usage_reports WHERE project_id=? AND client_identity=? AND report_id=? AND created_at>=?", turso.Text(snapshot.ProjectID), turso.Text(client), turso.Text(reportKey), turso.Integer(cutoff))
	if err != nil {
		return nil, err
	}
	if len(existing) > 0 {
		return duplicateUsage(existing[0], digest)
	}
	if input.TraceID != nil {
		trace, err := query(ctx, s.DB, "SELECT 1 FROM skill_runtime_events WHERE project_id=? AND client_identity=? AND trace_id=? AND event_type='resolution_trace' AND created_at>=? AND created_at<=? LIMIT 1", turso.Text(snapshot.ProjectID), turso.Text(client), turso.Text(*input.TraceID), turso.Integer(cutoff), turso.Integer(now.Unix()))
		if err != nil {
			return nil, err
		}
		if len(trace) == 0 {
			return nil, invalid("Resolution trace is unavailable for this project and client")
		}
	}
	observations := []Observation{}
	seen := map[string]bool{}
	for _, item := range input.Skills {
		sql := `SELECT c.release_id,c.skill_id,c.version,c.source_checksum FROM compiled_skills c JOIN releases r ON r.id=c.release_id WHERE r.project_id=? AND c.skill_id=? AND c.version=? AND c.status='ready'`
		binds := []turso.Value{turso.Text(snapshot.ProjectID), turso.Text(item.SkillID), turso.Text(item.Version)}
		if item.ReleaseID != nil {
			sql += " AND c.release_id=?"
			binds = append(binds, turso.Text(*item.ReleaseID))
		}
		if item.Checksum != nil {
			sql += " AND c.source_checksum=?"
			binds = append(binds, turso.Text(*item.Checksum))
		}
		sql += " ORDER BY c.created_at DESC,c.id LIMIT 5001"
		candidates, err := query(ctx, s.DB, sql, binds...)
		if err != nil {
			return nil, err
		}
		if len(candidates) == 0 || len(candidates) > 5000 {
			return nil, invalid("Skill version is not present in this project's releases")
		}
		if input.TraceID != nil && item.ReleaseID == nil {
			traced, err := query(ctx, s.DB, "SELECT DISTINCT release_id FROM skill_runtime_events WHERE project_id=? AND client_identity=? AND trace_id=? AND skill_id=? AND skill_version=? AND created_at>=?", turso.Text(snapshot.ProjectID), turso.Text(client), turso.Text(*input.TraceID), turso.Text(item.SkillID), turso.Text(item.Version), turso.Integer(cutoff))
			if err != nil {
				return nil, err
			}
			allowed := map[string]bool{}
			for _, r := range traced {
				allowed[text(r, "release_id")] = true
			}
			filtered := []row{}
			for _, r := range candidates {
				if allowed[text(r, "release_id")] {
					filtered = append(filtered, r)
				}
			}
			if len(filtered) > 0 {
				candidates = filtered
			}
		}
		checksum := text(candidates[0], "source_checksum")
		for _, candidate := range candidates {
			if text(candidate, "source_checksum") != checksum {
				return nil, invalid("Skill version is ambiguous; provide release_id or checksum")
			}
		}
		chosen := candidates[0]
		release := text(chosen, "release_id")
		identityKey := strings.Join([]string{item.SkillID, item.Version, release, checksum}, "\x00")
		if seen[identityKey] {
			return nil, invalid("Report each resolved skill version and release only once")
		}
		seen[identityKey] = true
		eventType := "agent_reported_used"
		reason := ""
		if item.Outcome == "skipped" {
			eventType = "agent_reported_skipped"
			reason = *item.SkipReason
		}
		trace := ""
		if input.TraceID != nil {
			trace = *input.TraceID
		}
		observations = append(observations, Observation{Skill: &skillruntime.Skill{SkillID: item.SkillID, Version: item.Version, ReleaseID: release, Checksum: checksum}, TraceID: trace, Type: eventType, Source: "report_skill_usage", Reason: reason})
	}
	reservation, err := identity.UUID()
	if err != nil {
		return nil, err
	}
	statements := []turso.Statement{{SQL: "DELETE FROM skill_usage_reports WHERE project_id=? AND client_identity=? AND report_id=? AND created_at<?", Args: []turso.Value{turso.Text(snapshot.ProjectID), turso.Text(client), turso.Text(reportKey), turso.Integer(cutoff)}}, {SQL: `INSERT INTO skill_usage_reports(id,project_id,client_identity,report_id,payload_hash,recorded_count,created_at) SELECT ?,?,?,?,?,?,? WHERE EXISTS(SELECT 1 FROM project_runtime_settings WHERE project_id=? AND telemetry_enabled=1) ON CONFLICT(project_id,client_identity,report_id) DO NOTHING`, Args: []turso.Value{turso.Text(reservation), turso.Text(snapshot.ProjectID), turso.Text(client), turso.Text(reportKey), turso.Text(digest), turso.Integer(int64(len(observations))), turso.Integer(now.Unix()), turso.Text(snapshot.ProjectID)}}}
	for _, o := range observations {
		id, err := identity.UUID()
		if err != nil {
			return nil, err
		}
		trace := o.TraceID
		if trace == "" {
			trace, err = identity.UUID()
			if err != nil {
				return nil, err
			}
		}
		statements = append(statements, turso.Statement{SQL: `INSERT INTO skill_runtime_events(id,project_id,trace_id,event_type,skill_id,reason_code,detail_json,release_id,skill_version,source_checksum,client_identity,source,created_at) SELECT ?,?,?,?,?,?,'{}',?,?,?,?,?,? WHERE EXISTS(SELECT 1 FROM skill_usage_reports WHERE id=?)`, Args: []turso.Value{turso.Text(id), turso.Text(snapshot.ProjectID), turso.Text(trace), turso.Text(o.Type), turso.Text(o.Skill.SkillID), opt(o.Reason), turso.Text(o.Skill.ReleaseID), turso.Text(o.Skill.Version), opt(o.Skill.Checksum), turso.Text(client), turso.Text(o.Source), turso.Integer(now.Unix()), turso.Text(reservation)}})
	}
	statements = append(statements, turso.Statement{SQL: "SELECT id,payload_hash,recorded_count FROM skill_usage_reports WHERE project_id=? AND client_identity=? AND report_id=?", Args: []turso.Value{turso.Text(snapshot.ProjectID), turso.Text(client), turso.Text(reportKey)}, WantRows: true})
	results, err := s.DB.Batch(ctx, statements)
	if err != nil {
		return nil, err
	}
	last := results[len(results)-1]
	if len(last.Rows) == 0 {
		return map[string]any{"status": "collection_disabled", "recorded_count": 0}, nil
	}
	if len(last.Rows[0]) != 3 {
		return nil, errors.New("invalid usage report result")
	}
	record := row{"id": last.Rows[0][0], "payload_hash": last.Rows[0][1], "recorded_count": last.Rows[0][2]}
	if text(record, "id") != reservation {
		return duplicateUsage(record, digest)
	}
	return map[string]any{"status": "recorded", "recorded_count": len(observations)}, nil
}
func duplicateUsage(existing row, digest string) (any, error) {
	if text(existing, "payload_hash") != digest {
		return nil, &InputError{Status: 409, Message: "Report ID was already used for different skill outcomes"}
	}
	return map[string]any{"status": "already_recorded", "recorded_count": number(existing, "recorded_count")}, nil
}
