// Package repomigration validates explicit repository replacement intent and
// excludes sync workers while a verified destination replaces a connection.
package repomigration

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/CountableNewt/my-context-protocol/services/go/identity"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
)

type Source struct {
	leaseID          string
	publicationPlans bool

	Provider string `json:"provider"`
	Owner    string `json:"repo_owner"`
	Name     string `json:"repo_name"`
	Branch   string `json:"default_branch"`
}
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string   { return e.Message }
func (e *Error) HTTPStatus() int { return e.Status }

type Database interface {
	Execute(context.Context, turso.Statement) (*turso.Result, error)
	Batch(context.Context, []turso.Statement) ([]turso.Result, error)
}

func query(sql string, args ...turso.Value) turso.Statement {
	return turso.Statement{SQL: sql, Args: args, WantRows: true}
}
func text(row []turso.Value, i int) string {
	if i >= len(row) || row[i].Type == "null" {
		return ""
	}
	return fmt.Sprint(row[i].Value)
}
func Parse(input map[string]json.RawMessage, destination string) (*Source, error) {
	raw, ok := input["migrate_from"]
	if !ok {
		return nil, nil
	}
	var source Source
	if string(raw) == "null" || json.Unmarshal(raw, &source) != nil || source.Owner == "" || source.Name == "" || source.Branch == "" || (source.Provider != "github" && source.Provider != "tangled") || source.Provider == destination {
		return nil, &Error{400, "Migration requires the current repository and a different destination provider"}
	}
	return &source, nil
}

// Acquire shares the sync lease. A durable unfinished publication must complete
// first even if its execution lease expired, because its target remains resumable.
func Acquire(ctx context.Context, db Database, project string, source *Source) (func(), error) {
	id, err := identity.UUID()
	if err != nil {
		return nil, err
	}
	id = "migration:" + id
	now := time.Now().Unix()
	r, err := db.Execute(ctx, query("INSERT INTO sync_project_locks(project_id,job_id,lease_until) VALUES(?,?,?) ON CONFLICT(project_id) DO UPDATE SET job_id=excluded.job_id,lease_until=excluded.lease_until WHERE sync_project_locks.lease_until<=? RETURNING job_id", turso.Text(project), turso.Text(id), turso.Integer(now+120), turso.Integer(now)))
	if err != nil {
		return nil, err
	}
	if len(r.Rows) != 1 {
		return nil, &Error{409, "A sync is running. Finish it before migrating this project"}
	}
	release := func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		db.Execute(cleanup, query("DELETE FROM sync_project_locks WHERE project_id=? AND job_id=?", turso.Text(project), turso.Text(id)))
	}
	fail := func(err error) (func(), error) { release(); return nil, err }
	tables, err := db.Execute(ctx, query("SELECT name FROM sqlite_master WHERE type='table' AND name='sync_publication_plans'"))
	if err != nil {
		return fail(err)
	}
	if source != nil {
		source.publicationPlans = len(tables.Rows) > 0
	}
	if len(tables.Rows) > 0 {
		plans, e := db.Execute(ctx, query("SELECT job_id FROM sync_publication_plans WHERE project_id=? AND state IN ('staging','publishing')", turso.Text(project)))
		if e != nil {
			return fail(e)
		}
		if len(plans.Rows) > 0 {
			return fail(&Error{409, "Finish the pending sync publication before migrating this project"})
		}
	}
	if source == nil {
		return release, nil
	}
	current, err := db.Execute(ctx, query("SELECT provider,repo_owner,repo_name,default_branch FROM repo_connections WHERE project_id=?", turso.Text(project)))
	if err != nil {
		return fail(err)
	}
	if len(current.Rows) != 1 || text(current.Rows[0], 0) != source.Provider || text(current.Rows[0], 1) != source.Owner || text(current.Rows[0], 2) != source.Name || text(current.Rows[0], 3) != source.Branch {
		return fail(&Error{409, "Repository connection changed. Refresh before migrating"})
	}
	source.leaseID = id
	return release, nil
}

// Guard atomically reacquires the same migration lease when a durable canonical
// intent resumes after an interrupted request. A changed source, unfinished
// publication or another live worker deliberately violates NOT NULL, rolling
// back every statement in the migration batch.
func Guard(project string, source *Source) turso.Statement {
	pending := ""
	args := []turso.Value{turso.Text(project), turso.Text(project), turso.Text(source.Provider), turso.Text(source.Owner), turso.Text(source.Name), turso.Text(source.Branch), turso.Text(project)}
	if source.publicationPlans {
		pending = " AND NOT EXISTS(SELECT 1 FROM sync_publication_plans WHERE project_id=? AND state IN ('staging','publishing'))"
		args = append(args, turso.Text(project))
	}
	args = append(args, turso.Text(source.leaseID), turso.Text(source.leaseID))
	return query(`INSERT INTO sync_project_locks(project_id,job_id,lease_until)
VALUES(?,CASE WHEN EXISTS(SELECT 1 FROM repo_connections WHERE project_id=? AND provider=? AND repo_owner=? AND repo_name=? AND default_branch=?) AND (SELECT COUNT(*) FROM repo_connections WHERE project_id=?)=1`+pending+` THEN ? ELSE NULL END,unixepoch()+120)
ON CONFLICT(project_id) DO UPDATE SET job_id=CASE WHEN sync_project_locks.lease_until<=unixepoch() OR sync_project_locks.job_id=? THEN excluded.job_id ELSE NULL END,lease_until=excluded.lease_until`, args...)
}

// ReleaseStatement clears a replay-acquired lease in the same transaction as
// the successful swap; Scope can then finish a durable intent without another
// ConnectRepository call just to release its operational lease.
func ReleaseStatement(project string, source *Source) turso.Statement {
	return query("DELETE FROM sync_project_locks WHERE project_id=? AND job_id=?", turso.Text(project), turso.Text(source.leaseID))
}
