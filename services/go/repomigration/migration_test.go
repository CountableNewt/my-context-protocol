package repomigration

import (
	"context"
	"github.com/CountableNewt/my-context-protocol/services/go/internal/sqlitetest"
	"github.com/CountableNewt/my-context-protocol/services/go/migrations"
	"github.com/CountableNewt/my-context-protocol/services/go/syncservice"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
	"testing"
	"time"
)

func fixture(t *testing.T) *sqlitetest.DB {
	return sqlitetest.New(t, migrations.BaselineSQL+syncservice.MigrationSQL+syncservice.PublicationMigrationSQL+`INSERT INTO accounts(id,github_id,login) VALUES('a',1,'owner'); INSERT INTO projects(id,account_id,name,slug) VALUES('p','a','Project','project'); INSERT INTO repo_connections(id,project_id,provider,repo_owner,repo_name,default_branch,auth_type) VALUES('c','p','github','owner','repo','main','oauth');`)
}
func TestMigrationInterlocksAndReplays(t *testing.T) {
	ctx := context.Background()
	db := fixture(t)
	source := &Source{Provider: "github", Owner: "owner", Name: "repo", Branch: "main"}
	release, err := Acquire(ctx, db, "p", source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(ctx, db, "p", source); err == nil {
		t.Fatal("concurrent migration accepted")
	}
	if _, err := db.Batch(ctx, []turso.Statement{Guard("p", source), query("UPDATE repo_connections SET provider='tangled' WHERE id='c'")}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Batch(ctx, []turso.Statement{Guard("p", source), query("UPDATE projects SET name='WRONG' WHERE id='p'")}); err == nil {
		t.Fatal("stale replay accepted")
	}
	release()
	row, _ := db.Execute(ctx, query("SELECT name FROM projects WHERE id='p'"))
	if text(row.Rows[0], 0) != "Project" {
		t.Fatal("stale batch was not rolled back")
	}
	if _, err := Acquire(ctx, db, "p", source); err == nil {
		t.Fatal("stale expected source accepted")
	}
}
func TestMigrationRejectsPendingAndRunningSync(t *testing.T) {
	ctx := context.Background()
	db := fixture(t)
	source := &Source{Provider: "github", Owner: "owner", Name: "repo", Branch: "main"}
	_, err := db.Execute(ctx, query("INSERT INTO sync_project_locks VALUES('p','sync',?)", turso.Integer(time.Now().Add(time.Minute).Unix())))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(ctx, db, "p", source); err == nil {
		t.Fatal("running sync accepted")
	}
	db.Execute(ctx, query("DELETE FROM sync_project_locks"))
	db.Execute(ctx, query("INSERT INTO sync_jobs(id,project_id,delivery_key,release_id,status,created_at,updated_at,lease_until) VALUES('j','p','k','r','running',0,0,0)"))
	_, err = db.Execute(ctx, query("INSERT INTO sync_publication_plans(project_id,job_id,release_id,prior_release_id,state,chunk_count,checksums_json,all_ready,report_valid,created_at,updated_at) VALUES('p','j','r','','publishing',1,'[]',1,1,0,0)"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(ctx, db, "p", source); err == nil {
		t.Fatal("resumable publication accepted")
	}
}

func TestGuardRejectsDifferentLeaseAndAdditionalConnections(t *testing.T) {
	for _, change := range []string{
		"UPDATE sync_project_locks SET job_id='migration:replacement' WHERE project_id='p'",

		"INSERT INTO repo_connections(id,project_id,provider,repo_owner,repo_name,default_branch,auth_type) VALUES('extra','p','github','owner','extra','main','oauth')",
	} {
		t.Run(change, func(t *testing.T) {
			ctx := context.Background()
			db := fixture(t)
			source := &Source{Provider: "github", Owner: "owner", Name: "repo", Branch: "main"}
			release, err := Acquire(ctx, db, "p", source)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			if _, err := db.Execute(ctx, query(change)); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Batch(ctx, []turso.Statement{Guard("p", source), query("UPDATE repo_connections SET provider='tangled' WHERE id='c'")}); err == nil {
				t.Fatal("changed migration guard accepted")
			}
			result, _ := db.Execute(ctx, query("SELECT provider FROM repo_connections WHERE id='c'"))
			if text(result.Rows[0], 0) != "github" {
				t.Fatal("guard did not rollback")
			}
		})
	}
}

func TestGuardReacquiresLeaseForDurableReplay(t *testing.T) {
	ctx := context.Background()
	db := fixture(t)
	source := &Source{Provider: "github", Owner: "owner", Name: "repo", Branch: "main"}
	release, err := Acquire(ctx, db, "p", source)
	if err != nil {
		t.Fatal(err)
	}
	release() // Native canonical intent may outlive the interrupted HTTP request.
	if _, err := db.Batch(ctx, []turso.Statement{Guard("p", source), query("UPDATE repo_connections SET provider='tangled' WHERE id='c'")}); err != nil {
		t.Fatal(err)
	}
	row, _ := db.Execute(ctx, query("SELECT job_id FROM sync_project_locks WHERE project_id='p'"))
	if text(row.Rows[0], 0) != source.leaseID {
		t.Fatal("replay did not reacquire original lease")
	}
}
