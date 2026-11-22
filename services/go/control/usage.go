package control

import (
	"net/http"
	"strconv"
	"strings"
)

var measurements = []string{"surfaced", "instructions_delivered", "supporting_file_read", "resolver_selected", "resolver_suggested", "resolver_excluded", "agent_reported_used", "agent_reported_skipped", "legacy_resolver_selected"}

func countColumns(prefix string) string {
	out := make([]string, len(measurements))
	for i, key := range measurements {
		event := key
		if key == "legacy_resolver_selected" {
			event = "skill_selected"
		}
		out[i] = "COALESCE(SUM(CASE WHEN " + prefix + "event_type='" + event + "' THEN 1 ELSE 0 END),0) AS " + key
	}
	return strings.Join(out, ",")
}
func extractCounts(v row) row {
	counts := row{}
	for _, key := range measurements {
		counts[key] = v[key]
		delete(v, key)
	}
	return counts
}
func (s *Service) usage(r *http.Request, p row) (any, int, error) {
	q, ctx := r.URL.Query(), r.Context()
	window := q.Get("window")
	if window == "" {
		window = "7d"
	}
	days := map[string]int64{"24h": 1, "7d": 7, "30d": 30}[window]
	if days == 0 {
		return nil, 0, bad("window must be 24h, 7d, or 30d")
	}
	page, size := 1, 25
	var e error
	if raw := q.Get("page"); raw != "" {
		page, e = strconv.Atoi(raw)
		if e != nil || page < 1 || page > 1000000 {
			return nil, 0, bad("Invalid pagination")
		}
	}
	if raw := q.Get("page_size"); raw != "" {
		size, e = strconv.Atoi(raw)
		if e != nil || size < 1 || size > 100 {
			return nil, 0, bad("Invalid pagination")
		}
	}
	sort := q.Get("sort")
	if sort == "" {
		sort = "activity"
	}
	direction := q.Get("direction")
	if direction == "" {
		direction = "desc"
	}
	if !member(sort, "activity", "skill_id", "surfaced", "instructions_delivered", "agent_reported_used", "agent_reported_skipped") || !member(direction, "asc", "desc") {
		return nil, 0, bad("Invalid sort")
	}
	skillID := q.Get("skill_id")
	if _, provided := q["skill_id"]; provided && (skillID == "" || len(skillID) > 256) {
		return nil, 0, bad("Invalid skill_id")
	}
	settings, e := s.one(ctx, "SELECT telemetry_enabled,telemetry_retention_days FROM project_runtime_settings WHERE project_id=?", p["id"])
	if e != nil {
		return nil, 0, e
	}
	retention := int64(30)
	enabled := false
	if settings != nil {
		retention = int64(num(settings["telemetry_retention_days"]))
		enabled = boolean(settings["telemetry_enabled"])
	}
	if retention < 1 {
		retention = 1
	}
	if days > retention {
		days = retention
	}
	cutoff, now := s.now().Unix()-days*86400, s.now().Unix()
	base := `WITH recent AS (SELECT * FROM skill_runtime_events WHERE project_id=? AND created_at>=? AND created_at<=? AND skill_id IS NOT NULL),current_skills AS (SELECT COALESCE(skill_id,name) AS skill_id,MAX(name) AS name FROM compiled_skills WHERE release_id=? AND status='ready' GROUP BY COALESCE(skill_id,name)),identities AS (SELECT skill_id FROM recent UNION SELECT skill_id FROM current_skills),filtered AS (SELECT skill_id FROM identities`
	args := []any{p["id"], cutoff, now, p["active_release_id"]}
	if skillID != "" {
		base += " WHERE skill_id=?"
		args = append(args, skillID)
	}
	base += ") "
	total, e := s.one(ctx, base+"SELECT COUNT(*) AS total FROM filtered", args...)
	if e != nil {
		return nil, 0, e
	}
	sortColumn, prefix := sort, ""
	if sort == "activity" {
		sortColumn = "last_activity"
		prefix = "CASE WHEN MAX(recent.created_at) IS NULL THEN 1 ELSE 0 END,"
	}
	if sort == "skill_id" {
		sortColumn = "filtered.skill_id"
	}
	sql := base + `SELECT filtered.skill_id,COALESCE(MAX(current_skills.name),filtered.skill_id) AS name,CASE WHEN MAX(current_skills.skill_id) IS NULL THEN 0 ELSE 1 END AS is_current,MAX(recent.created_at) AS last_activity,` + countColumns("recent.") + ` FROM filtered LEFT JOIN recent ON recent.skill_id=filtered.skill_id LEFT JOIN current_skills ON current_skills.skill_id=filtered.skill_id GROUP BY filtered.skill_id ORDER BY ` + prefix + sortColumn + " " + strings.ToUpper(direction) + ",filtered.skill_id ASC LIMIT ? OFFSET ?"
	skills, e := s.query(ctx, sql, append(args, size, (page-1)*size)...)
	if e != nil {
		return nil, 0, e
	}
	for _, skill := range skills {
		skill["counts"] = extractCounts(skill)
		skill["is_current"] = boolean(skill["is_current"])
		skill["last_activity"] = date(skill["last_activity"])
		skill["versions"] = []row{}
		skill["resolver_exclusion_reasons"] = []row{}
		skill["agent_skip_reasons"] = []row{}
	}
	if skillID != "" && len(skills) > 0 {
		args := []any{p["id"], skillID, cutoff, now}
		versions, e := s.query(ctx, "SELECT release_id,skill_version AS version,source_checksum AS checksum,"+countColumns("")+" FROM skill_runtime_events WHERE project_id=? AND skill_id=? AND created_at>=? AND created_at<=? GROUP BY release_id,skill_version,source_checksum ORDER BY MAX(created_at) DESC", args...)
		if e != nil {
			return nil, 0, e
		}
		for _, version := range versions {
			version["counts"] = extractCounts(version)
		}
		skills[0]["versions"] = versions
		reasons, e := s.query(ctx, "SELECT event_type,reason_code AS reason,COUNT(*) AS count FROM skill_runtime_events WHERE project_id=? AND skill_id=? AND created_at>=? AND created_at<=? AND event_type IN ('resolver_excluded','agent_reported_skipped') AND reason_code IS NOT NULL GROUP BY event_type,reason_code ORDER BY COUNT(*) DESC,reason_code ASC", args...)
		if e != nil {
			return nil, 0, e
		}
		excluded, skipped := []row{}, []row{}
		for _, reason := range reasons {
			kind := reason["event_type"]
			delete(reason, "event_type")
			if kind == "resolver_excluded" {
				excluded = append(excluded, reason)
			} else {
				skipped = append(skipped, reason)
			}
		}
		skills[0]["resolver_exclusion_reasons"] = excluded
		skills[0]["agent_skip_reasons"] = skipped
	}
	return row{"collection_enabled": enabled, "retention_days": retention, "requested_window": window, "effective_from": date(cutoff), "effective_to": date(now), "reporting_coverage": "partial", "historical_measurements": "legacy_resolver_only", "page": page, "page_size": size, "total": total["total"], "skills": skills}, 200, nil
}
