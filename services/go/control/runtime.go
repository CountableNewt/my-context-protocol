package control

import (
	"encoding/json"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/CountableNewt/my-context-protocol/services/go/identity"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
)

type assignment struct {
	SkillID    string `json:"skill_id"`
	Scope      string `json:"scope"`
	Activation string `json:"activation_mode"`
	Required   bool   `json:"required"`
	Priority   int    `json:"priority"`
	TargetType string `json:"target_type"`
	TargetID   string `json:"target_id"`
}

func (a *assignment) UnmarshalJSON(raw []byte) error {
	type plain assignment
	var value plain
	if e := json.Unmarshal(raw, &value); e != nil {
		return e
	}
	var fields map[string]json.RawMessage
	if e := json.Unmarshal(raw, &fields); e != nil {
		return e
	}
	for _, key := range []string{"skill_id", "scope", "activation_mode", "required", "priority"} {
		if value, ok := fields[key]; !ok || string(value) == "null" {
			return bad("Assignment field is required: " + key)
		}
	}
	*a = assignment(value)
	return nil
}

func member(s string, values ...string) bool {
	for _, v := range values {
		if s == v {
			return true
		}
	}
	return false
}
func (s *Service) runtime(r *http.Request, p row) (any, int, error) {
	if r.Method != "GET" && r.Method != "PATCH" {
		return nil, 0, missing("Not found")
	}
	ctx, pid := r.Context(), str(p["id"])
	id, e := identity.UUID()
	if e != nil {
		return nil, 0, e
	}
	_, e = s.exec(ctx, "INSERT INTO project_runtime_settings(id,project_id,telemetry_enabled,telemetry_retention_days,semantic_enabled,feedback_issue_creation_enabled,updated_at) VALUES(?,?,0,30,0,0,?) ON CONFLICT(project_id) DO NOTHING", id, pid, s.now().Unix())
	if e != nil {
		return nil, 0, e
	}
	settings, e := s.one(ctx, "SELECT * FROM project_runtime_settings WHERE project_id=?", pid)
	if e != nil {
		return nil, 0, e
	}
	if r.Method == "PATCH" {
		var body struct {
			Telemetry   *bool         `json:"telemetry_enabled"`
			Retention   *int          `json:"telemetry_retention_days"`
			Feedback    *bool         `json:"feedback_issue_creation_enabled"`
			Preferences *string       `json:"provider_preferences_json"`
			Assignments *[]assignment `json:"assignments"`
		}
		if e := decode(r, &body); e != nil {
			return nil, 0, e
		}
		if body.Telemetry != nil {
			settings["telemetry_enabled"] = *body.Telemetry
		}
		if body.Retention != nil {
			n := *body.Retention
			if n < 1 {
				n = 1
			}
			if n > 365 {
				n = 365
			}
			settings["telemetry_retention_days"] = int64(n)
		}
		if body.Feedback != nil {
			settings["feedback_issue_creation_enabled"] = *body.Feedback
		}
		if body.Preferences != nil {
			raw := *body.Preferences
			if _, valid := structuredJSON(raw); raw != "" && !valid {
				return nil, 0, bad("provider_preferences_json must be valid JSON")
			}
			settings["provider_preferences_json"] = optional(raw)
		}
		statements := []turso.Statement{{SQL: "UPDATE project_runtime_settings SET telemetry_enabled=?,telemetry_retention_days=?,semantic_enabled=0,embedding_provider=NULL,embedding_model=NULL,feedback_issue_creation_enabled=?,provider_preferences_json=?,updated_at=? WHERE project_id=?", Args: values(boolean(settings["telemetry_enabled"]), int(num(settings["telemetry_retention_days"])), boolean(settings["feedback_issue_creation_enabled"]), settings["provider_preferences_json"], s.now().Unix(), pid)}}
		if body.Assignments != nil {
			if len(*body.Assignments) > 100 {
				return nil, 0, bad("At most 100 assignments may be saved")
			}
			ready, e := s.query(ctx, "SELECT COALESCE(skill_id,name) AS skill_id FROM compiled_skills WHERE release_id=? AND status='ready'", p["active_release_id"])
			if e != nil {
				return nil, 0, e
			}
			active := map[string]bool{}
			for _, skill := range ready {
				active[str(skill["skill_id"])] = true
			}
			seen := map[string]bool{}
			statements = append(statements, turso.Statement{SQL: "DELETE FROM skill_assignments WHERE project_id=?", Args: values(pid)})
			for _, a := range *body.Assignments {
				a.SkillID = strings.TrimSpace(a.SkillID)
				a.TargetType = strings.TrimSpace(a.TargetType)
				a.TargetID = strings.TrimSpace(a.TargetID)
				if !active[a.SkillID] || utf8.RuneCountInString(a.SkillID) > 128 {
					return nil, 0, bad("Assignments must reference a ready skill in the active release")
				}
				if !member(a.Scope, "global", "organization", "workspace", "repository", "task") || !member(a.Activation, "always", "intent", "event", "explicit") || a.Priority < 0 || a.Priority > 100 {
					return nil, 0, bad("Invalid assignment scope, activation, or priority")
				}
				if a.TargetType != a.Scope || a.TargetID == "" || utf8.RuneCountInString(a.TargetID) > 512 || (a.Scope == "global" && !member(a.TargetID, "*", "global")) {
					return nil, 0, bad("Assignment target must match its scope and exact identity")
				}
				key := a.SkillID + "\x1f" + a.Scope + "\x1f" + a.TargetType + "\x1f" + a.TargetID
				if seen[key] {
					return nil, 0, bad("Duplicate assignment")
				}
				seen[key] = true
				aid, e := identity.UUID()
				if e != nil {
					return nil, 0, e
				}
				statements = append(statements, turso.Statement{SQL: "INSERT INTO skill_assignments(id,project_id,skill_id,scope,activation_mode,required,priority,target_type,target_id,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)", Args: values(aid, pid, a.SkillID, a.Scope, a.Activation, a.Required, a.Priority, a.TargetType, a.TargetID, s.now().Unix())})
			}
		}
		statements = append(statements, revisionStatement(pid))
		if _, e = s.db.Batch(ctx, statements); e != nil {
			return nil, 0, e
		}
	}
	assignments, e := s.query(ctx, "SELECT id,skill_id,scope,activation_mode,target_type,target_id,required,priority FROM skill_assignments WHERE project_id=? AND target_type=scope ORDER BY priority DESC,id ASC", pid)
	if e != nil {
		return nil, 0, e
	}
	for _, a := range assignments {
		a["required"] = boolean(a["required"])
	}
	retention := int64(num(settings["telemetry_retention_days"]))
	if retention < 1 {
		retention = 1
	}
	events, e := s.query(ctx, "SELECT id,trace_id,event_type,skill_id,reason_code,score,created_at FROM skill_runtime_events WHERE project_id=? AND created_at>=? AND created_at<=? ORDER BY created_at DESC,id DESC LIMIT 100", pid, s.now().Unix()-retention*86400, s.now().Unix())
	if e != nil {
		return nil, 0, e
	}
	for _, event := range events {
		event["created_at"] = date(event["created_at"])
	}
	return row{"telemetry_enabled": boolean(settings["telemetry_enabled"]), "telemetry_retention_days": retention, "semantic_enabled": false, "embedding_provider": nil, "embedding_model": nil, "feedback_issue_creation_enabled": boolean(settings["feedback_issue_creation_enabled"]), "provider_preferences_json": settings["provider_preferences_json"], "assignments": assignments, "recent_events": events}, 200, nil
}
