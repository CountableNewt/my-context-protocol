package recordservice

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/CountableNewt/my-context-protocol/services/go/repomigration"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
)

func canonicalMigration(t *testing.T, f *sqlFixture) (context.Context, []turso.Statement, func()) {
	t.Helper()
	ctx := f.ctx(t)
	if _, err := f.db.Execute(ctx, turso.Statement{SQL: `CREATE TABLE sync_project_locks(project_id TEXT PRIMARY KEY,job_id TEXT NOT NULL,lease_until INTEGER NOT NULL)`}); err != nil {
		t.Fatal(err)
	}
	source, err := repomigration.Parse(map[string]json.RawMessage{"migrate_from": json.RawMessage(`{"provider":"github","repo_owner":"owner","repo_name":"repo","default_branch":"main"}`)}, "tangled")
	if err != nil {
		t.Fatal(err)
	}
	release, err := repomigration.Acquire(ctx, f.bridge, "project", source)
	if err != nil {
		t.Fatal(err)
	}
	batch := []turso.Statement{
		repomigration.Guard("project", source),
		{SQL: `UPDATE repo_connections SET provider='tangled',repo_owner='did:plc:aaaaaaaaaaaaaaaaaaaaaaaa',repo_name='destination',auth_type='atproto',webhook_secret=NULL WHERE id='repo' RETURNING id`, WantRows: true},
		{SQL: `INSERT INTO atproto_repo_bindings(connection_id,repository_did,owner_did,actor_did,is_private,branch) VALUES('repo','did:plc:bbbbbbbbbbbbbbbbbbbbbbbb','did:plc:aaaaaaaaaaaaaaaaaaaaaaaa','did:plc:aaaaaaaaaaaaaaaaaaaaaaaa',0,'main')`},
		repomigration.ReleaseStatement("project", source),
	}
	return ctx, batch, release
}

func TestCanonicalRepositoryMigrationCommitsConnectionAndBindingTogether(t *testing.T) {
	f := fixtureSQL(t, false)
	ctx, batch, release := canonicalMigration(t, f)
	defer release()
	results, err := f.bridge.Batch(ctx, batch)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 4 || cell(results[1].Rows[0][0]) != "repo" {
		t.Fatalf("connection identity lost: %+v", results)
	}
	if f.value(t, `SELECT provider FROM repo_connections WHERE id='repo'`) != "tangled" || f.value(t, `SELECT repository_did FROM atproto_repo_bindings WHERE connection_id='repo'`) != "did:plc:bbbbbbbbbbbbbbbbbbbbbbbb" || f.value(t, `SELECT webhook_secret FROM repo_connections WHERE id='repo'`) != "" {
		t.Fatal("source connection, binding and credentials did not move together")
	}
	if f.value(t, `SELECT COUNT(*) FROM domain_native_intents WHERE status='complete'`) != "1" {
		t.Fatal("migration was not canonically confirmed")
	}
	if f.value(t, `SELECT COUNT(*) FROM sync_project_locks`) != "0" {
		t.Fatal("completed migration left its operational lease behind")
	}
}

func TestCanonicalRepositoryMigrationRejectsLeaseChangedDuringPublication(t *testing.T) {
	f := fixtureSQL(t, false)
	ctx, batch, release := canonicalMigration(t, f)
	defer release()
	f.pds.beforePut = func() {
		if _, err := f.db.Execute(context.Background(), turso.Statement{SQL: `UPDATE sync_project_locks SET job_id='new-sync-worker' WHERE project_id='project'`}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.bridge.Batch(ctx, batch); err == nil {
		t.Fatal("stale migration lease accepted")
	}
	if f.value(t, `SELECT provider FROM repo_connections WHERE id='repo'`) != "github" || f.value(t, `SELECT webhook_secret FROM repo_connections WHERE id='repo'`) != "old-secret" || f.value(t, `SELECT COUNT(*) FROM atproto_repo_bindings`) != "0" {
		t.Fatal("failed canonical guard partially replaced source")
	}
	if f.value(t, `SELECT status FROM domain_native_intents`) != "pending" {
		t.Fatal("failed projection completed the durable intent")
	}
	intent, err := loadNative(context.Background(), f.db, "account", "project")
	if err != nil || intent == nil {
		t.Fatalf("pending intent missing: %v", err)
	}
	if err = f.bridge.runIntent(context.Background(), *intent); err == nil {
		t.Fatal("replay bypassed changed lease guard")
	}
	if f.value(t, `SELECT provider FROM repo_connections WHERE id='repo'`) != "github" || f.value(t, `SELECT COUNT(*) FROM atproto_repo_bindings`) != "0" {
		t.Fatal("replay partially applied migration")
	}
}

func TestCanonicalRepositoryMigrationResumesAfterPDSFailureAndLeaseRelease(t *testing.T) {
	f := fixtureSQL(t, false)
	ctx, batch, release := canonicalMigration(t, f)
	f.pds.fail = true
	if _, err := f.bridge.Batch(ctx, batch); err == nil {
		t.Fatal("PDS outage acknowledged as a completed migration")
	}
	release()
	if f.value(t, `SELECT provider FROM repo_connections WHERE id='repo'`) != "github" || f.value(t, `SELECT COUNT(*) FROM atproto_repo_bindings`) != "0" || f.value(t, `SELECT COUNT(*) FROM sync_project_locks`) != "0" {
		t.Fatal("failed migration changed the source or leaked its execution lease")
	}
	f.pds.fail = false
	f.resume(t)
	if f.value(t, `SELECT provider FROM repo_connections WHERE id='repo'`) != "tangled" || f.value(t, `SELECT COUNT(*) FROM atproto_repo_bindings WHERE connection_id='repo'`) != "1" || f.value(t, `SELECT COUNT(*) FROM domain_native_intents WHERE status='pending'`) != "0" || f.value(t, `SELECT COUNT(*) FROM sync_project_locks`) != "0" {
		t.Fatal("durable migration failed to resume after releasing its original lease")
	}
}
