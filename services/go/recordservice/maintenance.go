package recordservice

import (
	"context"
	"time"

	"github.com/CountableNewt/my-context-protocol/services/go/domainrecords"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
)

// Maintenance performs bounded owner-authorized retention and record collection.
// Failed/reconnect-required owners are deferred independently; no worker survives
// this request. Oldest indexed projects are visited first to avoid starvation.
func (s *Service) Maintenance(ctx context.Context, projectLimit, rowLimit int, now time.Time) (map[string]any, error) {
	if projectLimit < 1 {
		projectLimit = 2
	}
	if projectLimit > 10 {
		projectLimit = 10
	}
	if rowLimit < 1 {
		rowLimit = 25
	}
	if rowLimit > 50 {
		rowLimit = 50
	}
	rows, err := s.DB.Execute(ctx, turso.Statement{SQL: "SELECT p.account_id,p.project_id FROM domain_canonical_projects p LEFT JOIN domain_maintenance_visits v ON v.project_id=p.project_id ORDER BY COALESCE(v.last_attempt,0),p.project_id LIMIT ?", Args: []turso.Value{turso.Integer(int64(projectLimit))}, WantRows: true})
	if err != nil {
		return nil, err
	}
	completed, deferred, deleted := 0, 0, 0
	more := false
	for _, row := range rows.Rows {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if len(row) != 2 {
			continue
		}
		account, project := cell(row[0]), cell(row[1])
		_, err := s.DB.Execute(ctx, turso.Statement{SQL: "INSERT INTO domain_maintenance_visits(project_id,last_attempt) VALUES(?,?) ON CONFLICT(project_id) DO UPDATE SET last_attempt=excluded.last_attempt", Args: []turso.Value{turso.Text(project), turso.Integer(now.Unix())}})
		if err != nil {
			return nil, err
		}
		err = s.maintainProject(ctx, account, project, rowLimit, now, &deleted, &more)
		if err != nil {
			deferred++
		} else {
			completed++
		}
	}
	return map[string]any{"projects_completed": completed, "projects_deferred": deferred, "records_collected": deleted, "more_work": more || len(rows.Rows) >= projectLimit || deferred > 0}, nil
}
func (s *Service) maintainProject(ctx context.Context, account, project string, limit int, now time.Time, deleted *int, more *bool) error {
	if err := s.WithEngine(ctx, account, func(ctx context.Context, owner string, e *domainrecords.Engine) error {
		return e.Audit(ctx, account, owner, project)
	}); err != nil {
		return err
	}
	scoped, err := s.Scope(ctx, account, project)
	if err != nil {
		return err
	}
	bridge := &SQLProjection{Raw: s.DB, Records: s}
	statements := retentionStatements(project, limit, now)
	if _, err = bridge.Batch(scoped, statements); err != nil {
		return err
	}
	return s.WithEngine(ctx, account, func(ctx context.Context, owner string, e *domainrecords.Engine) error {
		report, err := e.Collect(ctx, account, owner, project, limit)
		*deleted += report.Deleted
		*more = *more || report.More
		if err != nil {
			return err
		}
		_, err = s.DB.Execute(ctx, turso.Statement{SQL: "UPDATE domain_canonical_projects SET updated_at=? WHERE account_id=? AND project_id=?", Args: []turso.Value{turso.Integer(now.Unix()), turso.Text(account), turso.Text(project)}})
		return err
	})
}

func retentionStatements(project string, limit int, now time.Time) []turso.Statement {
	statements := []turso.Statement{}
	for _, table := range []string{"skill_runtime_events", "skill_usage_reports"} {
		statements = append(statements, turso.Statement{SQL: "DELETE FROM " + table + " WHERE id IN(SELECT e.id FROM " + table + " e WHERE e.project_id=? AND e.created_at < ? - 86400 * MAX(1,COALESCE((SELECT telemetry_retention_days FROM project_runtime_settings WHERE project_id=e.project_id),30)) ORDER BY e.created_at,e.id LIMIT ?)", Args: []turso.Value{turso.Text(project), turso.Integer(now.Unix()), turso.Integer(int64(limit))}})
	}
	return statements
}
