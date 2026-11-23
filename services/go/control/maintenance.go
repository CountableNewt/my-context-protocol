package control

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/CountableNewt/my-context-protocol/services/go/turso"
)

// MaintenanceSQL adds range indexes used by bounded daily cleanup and rollups.
// Apply through the controlled migration command, never during service startup.
const MaintenanceSQL = `
CREATE INDEX IF NOT EXISTS idx_maintenance_request_logs_time ON request_logs(timestamp,id);
CREATE INDEX IF NOT EXISTS idx_maintenance_rate_window ON mcp_rate_limits(window_start);
CREATE INDEX IF NOT EXISTS idx_maintenance_subscriptions_created ON mcp_resource_subscriptions(created_at);
CREATE INDEX IF NOT EXISTS idx_maintenance_oauth_pending_expiry ON mcp_oauth_pending_authorizations(expires_at);
CREATE INDEX IF NOT EXISTS idx_maintenance_oauth_codes_expiry ON mcp_oauth_authorization_codes(expires_at);
CREATE INDEX IF NOT EXISTS idx_maintenance_oauth_tokens_expiry ON mcp_oauth_access_tokens(expires_at);
CREATE INDEX IF NOT EXISTS idx_maintenance_oauth_tokens_revoked ON mcp_oauth_access_tokens(revoked_at) WHERE revoked_at IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_maintenance_handoffs_expiry ON oauth_handoff_tokens(expires_at);
CREATE INDEX IF NOT EXISTS idx_maintenance_install_intents_expiry ON github_app_install_intents(expires_at);
CREATE INDEX IF NOT EXISTS idx_maintenance_github_operation_lease ON github_operation_locks(lease_until);
CREATE INDEX IF NOT EXISTS idx_maintenance_sync_lock_lease ON sync_project_locks(lease_until);
CREATE INDEX IF NOT EXISTS idx_maintenance_sync_job_lease ON sync_jobs(status,lease_until,id);
CREATE INDEX IF NOT EXISTS idx_maintenance_sync_job_updated ON sync_jobs(updated_at,status);
CREATE INDEX IF NOT EXISTS idx_maintenance_skill_events_time ON skill_runtime_events(created_at);
CREATE INDEX IF NOT EXISTS idx_maintenance_skill_reports_time ON skill_usage_reports(created_at);
`

// maintenance is a bounded, idempotent request. It never starts a background
// goroutine or assumes that a function instance will remain alive after return.
func (s *Service) maintenance(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		s.maintenanceJSON(w, 405, row{"error": "Method not allowed"})
		return
	}
	if len(s.cfg.CronSecret) < 16 || strings.ContainsAny(s.cfg.CronSecret, "\r\n") {
		s.maintenanceJSON(w, 503, row{"error": "Maintenance is not configured"})
		return
	}
	expected := sha256.Sum256([]byte("Bearer " + s.cfg.CronSecret))
	received := sha256.Sum256([]byte(r.Header.Get("Authorization")))
	if subtle.ConstantTimeCompare(expected[:], received[:]) != 1 || len(r.Header.Values("Authorization")) != 1 {
		w.Header().Set("WWW-Authenticate", "Bearer")
		s.maintenanceJSON(w, 401, row{"error": "Unauthorized"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	result, e := s.runMaintenance(ctx)
	if e != nil {
		s.maintenanceJSON(w, 503, row{"error": "Maintenance did not complete; retry is safe"})
		return
	}
	s.maintenanceJSON(w, 200, result)
}
func (s *Service) maintenanceJSON(w http.ResponseWriter, status int, value row) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(value)
}

type cleanup struct {
	name, sql string
	args      []any
}

func (s *Service) runMaintenance(ctx context.Context) (row, error) {
	now := s.now().Unix()
	limit := s.cfg.MaintenanceRows
	if limit < 1 {
		limit = 250
	}
	if limit > 1000 {
		limit = 1000
	}
	// Keep at least one full day of quota windows. A longer configured sync window
	// is retained in full, so cleanup can never reset a live safety budget.
	quotaRetention := int64(86400)
	if s.cfg.SyncWindowSeconds > quotaRetention {
		quotaRetention = s.cfg.SyncWindowSeconds
	}
	leaseCutoff := now - 300
	categories := []cleanup{
		{"rate_limits", `DELETE FROM mcp_rate_limits WHERE rowid IN (SELECT rowid FROM mcp_rate_limits WHERE window_start<=? ORDER BY window_start,rowid LIMIT ?)`, []any{now - quotaRetention, limit}},
		{"resource_subscriptions", `DELETE FROM mcp_resource_subscriptions WHERE rowid IN (SELECT s.rowid FROM mcp_resource_subscriptions s WHERE NOT EXISTS(SELECT 1 FROM api_keys k WHERE k.id=s.subscriber_id AND k.project_id=s.project_id AND k.status='active') AND NOT EXISTS(SELECT 1 FROM mcp_oauth_access_tokens t JOIN mcp_oauth_clients c ON c.id=t.mcp_oauth_client_id WHERE t.id=s.subscriber_id AND t.project_id=s.project_id AND t.revoked_at IS NULL AND t.expires_at>? AND c.status='active') ORDER BY s.created_at,s.rowid LIMIT ?)`, []any{now, limit}},
		{"oauth_pending", `DELETE FROM mcp_oauth_pending_authorizations WHERE rowid IN (SELECT rowid FROM mcp_oauth_pending_authorizations WHERE expires_at<=? ORDER BY expires_at,rowid LIMIT ?)`, []any{now, limit}},
		{"oauth_codes", `DELETE FROM mcp_oauth_authorization_codes WHERE rowid IN (SELECT rowid FROM mcp_oauth_authorization_codes WHERE expires_at<=? ORDER BY expires_at,rowid LIMIT ?)`, []any{now, limit}},
		{"oauth_handoffs", `DELETE FROM oauth_handoff_tokens WHERE rowid IN (SELECT rowid FROM oauth_handoff_tokens WHERE expires_at<=? ORDER BY expires_at,rowid LIMIT ?)`, []any{now, limit}},
		{"oauth_tokens", `DELETE FROM mcp_oauth_access_tokens WHERE rowid IN (SELECT rowid FROM mcp_oauth_access_tokens WHERE expires_at<=? OR revoked_at IS NOT NULL ORDER BY expires_at,rowid LIMIT ?)`, []any{now, limit}},
		{"github_install_intents", `DELETE FROM github_app_install_intents WHERE rowid IN (SELECT rowid FROM github_app_install_intents WHERE expires_at<=? ORDER BY expires_at,rowid LIMIT ?)`, []any{now, limit}},
		{"github_operation_locks", `DELETE FROM github_operation_locks WHERE rowid IN (SELECT rowid FROM github_operation_locks WHERE lease_until<=? ORDER BY lease_until,rowid LIMIT ?)`, []any{leaseCutoff, limit}},
		{"sync_locks", `DELETE FROM sync_project_locks WHERE rowid IN (SELECT rowid FROM sync_project_locks WHERE lease_until<=? ORDER BY lease_until,rowid LIMIT ?)`, []any{leaseCutoff, limit}},
		{"failed_sync_releases", `UPDATE releases SET status='failed',error_summary='Sync lease expired' WHERE status='pending' AND id IN (SELECT release_id FROM sync_jobs WHERE status='running' AND lease_until<=? ORDER BY lease_until,id LIMIT ?)`, []any{leaseCutoff, limit}},
		{"failed_sync_jobs", `UPDATE sync_jobs SET status='failed',error_code='lease_expired',updated_at=? WHERE id IN (SELECT id FROM sync_jobs WHERE status='running' AND lease_until<=? ORDER BY lease_until,id LIMIT ?)`, []any{now, leaseCutoff, limit}},
		{"expired_sync_jobs", `DELETE FROM sync_jobs WHERE rowid IN (SELECT rowid FROM sync_jobs WHERE status IN ('ready','failed') AND updated_at<? ORDER BY updated_at,rowid LIMIT ?)`, []any{now - 30*86400, limit}},
		// Consent may be disabled and a project may be inactive: neither condition
		// exempts its old records from the configured retention cutoff.
		{"skill_runtime_events", `DELETE FROM skill_runtime_events WHERE rowid IN (SELECT e.rowid FROM skill_runtime_events e WHERE e.created_at < ? - 86400 * MAX(1,COALESCE((SELECT s.telemetry_retention_days FROM project_runtime_settings s WHERE s.project_id=e.project_id),30)) ORDER BY e.created_at,e.rowid LIMIT ?)`, []any{now, limit}},
		{"skill_usage_reports", `DELETE FROM skill_usage_reports WHERE rowid IN (SELECT e.rowid FROM skill_usage_reports e WHERE e.created_at < ? - 86400 * MAX(1,COALESCE((SELECT s.telemetry_retention_days FROM project_runtime_settings s WHERE s.project_id=e.project_id),30)) ORDER BY e.created_at,e.rowid LIMIT ?)`, []any{now, limit}},
	}
	if s.cfg.CanonicalMaintenance {
		// Global operational cleanup must not mutate owner-record projections or
		// remove the leases/jobs needed to resume interrupted publication.
		protectedJob := "NOT EXISTS(SELECT 1 FROM sync_publication_plans p WHERE p.job_id=sync_jobs.id AND p.state IN ('staging','publishing')) AND NOT EXISTS(SELECT 1 FROM domain_native_intents n WHERE n.project_id=sync_jobs.project_id AND n.status='pending')"
		for i := range categories {
			c := &categories[i]
			switch c.name {
			case "skill_runtime_events", "skill_usage_reports":
				c.sql = strings.Replace(c.sql, "WHERE e.created_at", "WHERE NOT EXISTS(SELECT 1 FROM domain_canonical_projects p WHERE p.project_id=e.project_id) AND e.created_at", 1)
			case "failed_sync_releases":
				c.sql = strings.Replace(c.sql, "WHERE status='pending'", "WHERE NOT EXISTS(SELECT 1 FROM domain_canonical_projects p WHERE p.project_id=releases.project_id) AND status='pending'", 1)
				c.sql = strings.Replace(c.sql, "WHERE status='running'", "WHERE "+protectedJob+" AND status='running'", 1)
			case "failed_sync_jobs", "expired_sync_jobs":
				c.sql = strings.Replace(c.sql, "SELECT id FROM sync_jobs WHERE", "SELECT id FROM sync_jobs WHERE "+protectedJob+" AND", 1)
				c.sql = strings.Replace(c.sql, "SELECT rowid FROM sync_jobs WHERE", "SELECT rowid FROM sync_jobs WHERE "+protectedJob+" AND", 1)
			case "sync_locks":
				c.sql = strings.Replace(c.sql, "WHERE lease_until<=?", "WHERE NOT EXISTS(SELECT 1 FROM sync_publication_plans p WHERE p.project_id=sync_project_locks.project_id AND p.state IN ('staging','publishing')) AND NOT EXISTS(SELECT 1 FROM domain_native_intents n WHERE n.project_id=sync_project_locks.project_id AND n.status='pending') AND lease_until<=?", 1)
			}
		}
	}
	statements := make([]turso.Statement, len(categories))
	for i, c := range categories {
		statements[i] = turso.Statement{SQL: c.sql, Args: values(c.args...)}
	}
	results, e := s.db.Batch(ctx, statements)
	if e != nil {
		return nil, e
	}
	if len(results) != len(categories) {
		return nil, errors.New("incomplete maintenance result")
	}
	counts := row{}
	more := false
	for i, result := range results {
		counts[categories[i].name] = result.AffectedRowCount
		if result.AffectedRowCount >= int64(limit) {
			more = true
		}
	}
	refreshed, deferred, e := s.refreshRecentRollup(ctx, now)
	if e != nil {
		return nil, e
	}
	extra := map[string]any{}
	if s.cfg.ExtraMaintenance != nil {
		extra, e = s.cfg.ExtraMaintenance(ctx)
		if e != nil {
			return nil, e
		}
	}
	return row{"canonical": extra, "completed_at": date(now), "batch_limit": limit, "counts": counts, "more_work": more || deferred, "rollup_refreshed": refreshed, "rollup_deferred": deferred}, nil
}

// A daily Hobby cron refreshes the overlapping latest 48 hours. Historic hourly
// aggregates remain untouched. Large windows are deferred explicitly rather than
// writing inaccurate aggregates from a truncated sample.
func (s *Service) refreshRecentRollup(ctx context.Context, now int64) (bool, bool, error) {
	start := (now/3600 - 48) * 3600
	cap := s.cfg.MaintenanceRollupRows
	if cap < 1 {
		cap = 50000
	}
	if cap > 100000 {
		cap = 100000
	}
	countSQL := "SELECT COUNT(*) AS count FROM (SELECT 1 FROM request_logs WHERE timestamp>=? AND timestamp<=? LIMIT ?)"
	results, e := s.db.Batch(ctx, []turso.Statement{
		{SQL: countSQL, Args: values(start, now, cap+1), WantRows: true},
		{SQL: "DELETE FROM admin_analytics_hourly WHERE hour_start>=? AND hour_start<=? AND ?>=(" + countSQL + ")", Args: values(start, now, cap, start, now, cap+1)},
		{SQL: `INSERT INTO admin_analytics_hourly(id,hour_start,request_count,success_count,latency_sum_ms,latency_count,updated_at) SELECT upper(hex(randomblob(4))||'-'||hex(randomblob(2))||'-'||hex(randomblob(2))||'-'||hex(randomblob(2))||'-'||hex(randomblob(6))),CAST(l.timestamp/3600 AS INTEGER)*3600,COUNT(*),SUM(` + successful + `),COALESCE(SUM(l.latency_ms),0),COUNT(l.latency_ms),? FROM (SELECT timestamp,status,error_code,latency_ms FROM request_logs WHERE timestamp>=? AND timestamp<=? ORDER BY timestamp,id LIMIT ?) l WHERE ?>=(` + countSQL + `) GROUP BY CAST(l.timestamp/3600 AS INTEGER)`, Args: values(now, start, now, cap+1, cap, start, now, cap+1)},
	})
	if e != nil {
		return false, false, e
	}
	if len(results) != 3 || len(results[0].Rows) != 1 || len(results[0].Rows[0]) != 1 {
		return false, false, errors.New("missing maintenance rollup count")
	}
	if num(results[0].Rows[0][0].Value) > float64(cap) {
		return false, true, nil
	}
	return true, false, nil
}
