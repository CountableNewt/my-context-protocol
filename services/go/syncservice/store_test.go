package syncservice

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/CountableNewt/my-context-protocol/services/go/compiler"
	"github.com/CountableNewt/my-context-protocol/services/go/internal/sqlitetest"
	"github.com/CountableNewt/my-context-protocol/services/go/migrations"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
)

const seedSQL = `
INSERT INTO accounts(id,github_id,login) VALUES ('account',1,'owner');
INSERT INTO projects(id,account_id,name,slug) VALUES ('project','account','Project','project');
INSERT INTO repo_connections(id,project_id,provider,repo_owner,repo_name,default_branch,auth_type,webhook_secret) VALUES ('connection','project','github','owner','repo','main','oauth','secret');
`

func TestSQLClaimIdempotencyLeaseAndPublication(t *testing.T) {
	ctx := context.Background()
	db := sqlitetest.New(t, migrations.BaselineSQL+MigrationSQL+seedSQL)
	store := SQLStore{DB: db}
	connection, err := store.Connection(ctx, "project")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	job, claimed, err := store.Claim(ctx, connection, "delivery", now)
	if err != nil || !claimed {
		t.Fatalf("%+v %v %v", job, claimed, err)
	}
	duplicate, claimed, err := store.Claim(ctx, connection, "delivery", now)
	if err != nil || claimed || duplicate.ID != job.ID {
		t.Fatalf("duplicate %+v %v %v", duplicate, claimed, err)
	}
	_, claimed, err = store.Claim(ctx, connection, "other-delivery", now)
	if !errors.Is(err, ErrBusy) || claimed {
		t.Fatalf("parallel job accepted %v %v", claimed, err)
	}
	skill, _ := compiler.Parse("a/SKILL.md", []byte("---\nname: a\ndescription: useful\nuse_when: writing\n---\nbody"))
	compiled, _ := compiler.Compile(skill, "owner/repo", strings.Repeat("a", 40), nil)
	compiled.Files = []compiler.PackageFile{{Path: "helper.txt", ContentType: "text/plain", Content: []byte("helper"), Checksum: compiler.Hash([]byte("helper"))}}
	if err = store.Publish(ctx, connection, job, strings.Repeat("a", 40), []compiler.Compiled{compiled}, compiler.Report{Valid: true}, now); err != nil {
		t.Fatal(err)
	}
	r, err := db.Execute(ctx, stmt("SELECT p.active_release_id,r.revision,j.status,length(f.content) FROM projects p JOIN mcp_catalog_revisions r ON r.project_id=p.id JOIN sync_jobs j ON j.release_id=p.active_release_id JOIN skill_packages sp ON sp.release_id=p.active_release_id JOIN skill_package_files f ON f.skill_package_id=sp.id"))
	if err != nil || len(r.Rows) != 1 {
		t.Fatalf("%+v %v", r, err)
	}
	if value(r.Rows[0], 0) != job.ReleaseID || value(r.Rows[0], 1) != "1" || value(r.Rows[0], 2) != "ready" || value(r.Rows[0], 3) != "6" {
		t.Fatal(r.Rows)
	}
	duplicate, claimed, err = store.Claim(ctx, connection, "delivery", now)
	if err != nil || claimed || duplicate.Status != "ready" {
		t.Fatalf("completed duplicate %+v %v %v", duplicate, claimed, err)
	}
}
func TestSQLExpiredLeaseRetryAndStaleWorker(t *testing.T) {
	ctx := context.Background()
	db := sqlitetest.New(t, migrations.BaselineSQL+MigrationSQL+seedSQL)
	store := SQLStore{DB: db}
	connection, _ := store.Connection(ctx, "project")
	now := time.Now().UTC()
	old, _, err := store.Claim(ctx, connection, "delivery", now)
	if err != nil {
		t.Fatal(err)
	}
	retry, claimed, err := store.Claim(ctx, connection, "delivery", now.Add(121*time.Second))
	if err != nil || !claimed || retry.ID == old.ID || retry.ReleaseID == old.ReleaseID {
		t.Fatalf("retry %+v old %+v claimed %v err %v", retry, old, claimed, err)
	}
	if err = store.Fail(ctx, old, "late_failure", now.Add(122*time.Second)); err != nil {
		t.Fatal(err)
	}
	r, err := db.Execute(ctx, stmt("SELECT job_id FROM sync_project_locks WHERE project_id='project'"))
	if err != nil || len(r.Rows) != 1 || value(r.Rows[0], 0) != retry.ID {
		t.Fatalf("stale worker released current lease: %+v %v", r, err)
	}
}
func TestSQLFailedPublicationCannotChangeActiveRelease(t *testing.T) {
	ctx := context.Background()
	db := sqlitetest.New(t, migrations.BaselineSQL+MigrationSQL+seedSQL)
	store := SQLStore{DB: db}
	c, _ := store.Connection(ctx, "project")
	now := time.Now()
	job, _, err := store.Claim(ctx, c, "delivery", now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Execute(ctx, turso.Statement{SQL: "INSERT INTO validation_reports(id,release_id,report_json) VALUES ('duplicate',?,'{}')", Args: []turso.Value{turso.Text(job.ReleaseID)}})
	if err != nil {
		t.Fatal(err)
	}
	// Force the final transaction to fail after staging a ready compiled skill.
	_, err = db.Execute(ctx, turso.Statement{SQL: "CREATE UNIQUE INDEX one_validation_per_release ON validation_reports(release_id)"})
	if err != nil {
		t.Fatal(err)
	}
	skill, _ := compiler.Parse("a/SKILL.md", []byte("---\nname: a\ndescription: useful\nuse_when: writing\n---\nbody"))
	compiled, _ := compiler.Compile(skill, "repo", "rev", nil)
	err = store.Publish(ctx, c, job, "rev", []compiler.Compiled{compiled}, compiler.Report{Valid: true}, now)
	if err == nil {
		t.Fatal("accepted failed final transaction")
	}
	r, err := db.Execute(ctx, stmt("SELECT active_release_id FROM projects WHERE id='project'"))
	if err != nil || value(r.Rows[0], 0) != "" {
		t.Fatalf("catalog mutated on failed publication: %+v %v", r, err)
	}
}

func TestSQLCarryForwardPreservesDashboardMetadataAndBodyDiff(t *testing.T) {
	ctx := context.Background()
	db := sqlitetest.New(t, migrations.BaselineSQL+MigrationSQL+seedSQL)
	store := SQLStore{DB: db}
	now := time.Now()
	c, _ := store.Connection(ctx, "project")
	job, _, err := store.Claim(ctx, c, "first", now)
	if err != nil {
		t.Fatal(err)
	}
	skill, _ := compiler.Parse("a/SKILL.md", []byte("---\nname: a\ndescription: useful\nuse_when: writing\n---\nold body"))
	compiled, _ := compiler.Compile(skill, "owner/repo", "old", nil)
	if err = store.Publish(ctx, c, job, "old", []compiler.Compiled{compiled}, compiler.Report{Valid: true}, now); err != nil {
		t.Fatal(err)
	}
	_, err = db.Execute(ctx, stmt("UPDATE compiled_skills SET summary='Dashboard summary',exposure_type='prompt',risk_level='medium' WHERE release_id=?", turso.Text(job.ReleaseID)))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Execute(ctx, stmt("UPDATE capability_defs SET type='prompt',schema_json='{\"arguments\":[]}' WHERE compiled_skill_id IN (SELECT id FROM compiled_skills WHERE release_id=?)", turso.Text(job.ReleaseID)))
	if err != nil {
		t.Fatal(err)
	}
	c, _ = store.Connection(ctx, "project")
	next, _, err := store.Claim(ctx, c, "next", now)
	if err != nil {
		t.Fatal(err)
	}
	skill, _ = compiler.Parse("a/SKILL.md", []byte("---\nname: a\ndescription: source description\nuse_when: changed\n---\nnew body"))
	compiled, _ = compiler.Compile(skill, "owner/repo", "new", nil)
	if err = store.Publish(ctx, c, next, "new", []compiler.Compiled{compiled}, compiler.Report{Valid: true}, now); err != nil {
		t.Fatal(err)
	}
	r, err := db.Execute(ctx, stmt("SELECT summary,exposure_type,risk_level,skill_body,body_diff_unified,body_diff_prior_release_id FROM compiled_skills WHERE release_id=?", turso.Text(next.ReleaseID)))
	if err != nil || len(r.Rows) != 1 {
		t.Fatalf("%+v %v", r, err)
	}
	row := r.Rows[0]
	if value(row, 0) != "Dashboard summary" || value(row, 1) != "prompt" || value(row, 2) != "medium" || value(row, 3) != "new body" || !strings.Contains(value(row, 4), "-old body\n+new body") || value(row, 5) != job.ReleaseID {
		t.Fatal(row)
	}
	r, err = db.Execute(ctx, stmt("SELECT revision FROM mcp_catalog_revisions WHERE project_id='project'"))
	if err != nil || value(r.Rows[0], 0) != "2" {
		t.Fatalf("catalog revision %+v %v", r, err)
	}
}

func TestSQLRuntimeOverrideSelectionAndStaleChecksums(t *testing.T) {
	ctx := context.Background()
	db := sqlitetest.New(t, migrations.BaselineSQL+MigrationSQL+seedSQL)
	store := SQLStore{DB: db}
	c, _ := store.Connection(ctx, "project")
	skill, _ := compiler.Parse("a/SKILL.md", []byte("---\nname: a\ndescription: useful\n---\nbody"))
	rows := []struct {
		id, repo, scope, patch, checksum string
		updated                          int64
	}{{"global", "", "task", `{"priority":1}`, skill.Hash, 100}, {"scoped", "connection", "repository", `{"priority":70}`, skill.Hash, 50}, {"preferred", "connection", "task", `{"priority":80}`, skill.Hash, 20}}
	for _, r := range rows {
		_, err := db.Execute(ctx, stmt("INSERT INTO skill_runtime_overrides(id,project_id,repo_connection_id,skill_id,scope,metadata_json,base_checksum,updated_at) VALUES (?,'project',?,'a',?,?,?,?)", turso.Text(r.id), nullableString(r.repo), turso.Text(r.scope), turso.Text(r.patch), turso.Text(r.checksum), turso.Integer(r.updated)))
		if err != nil {
			t.Fatal(err)
		}
	}
	patch, err := store.RuntimeOverride(ctx, c, skill, "task")
	if err != nil || patch["priority"] != float64(80) {
		t.Fatalf("preferred override %+v %v", patch, err)
	}
	patch, err = store.RuntimeOverride(ctx, c, skill, "repository")
	if err != nil || patch["priority"] != float64(70) {
		t.Fatalf("repository override %+v %v", patch, err)
	}
	_, err = db.Execute(ctx, stmt("UPDATE skill_runtime_overrides SET base_checksum='old' WHERE id='preferred'"))
	if err != nil {
		t.Fatal(err)
	}
	patch, err = store.RuntimeOverride(ctx, c, skill, "task")
	if err != nil || patch != nil {
		t.Fatalf("stale patch applied %+v %v", patch, err)
	}
	r, err := db.Execute(ctx, stmt("SELECT is_stale FROM skill_runtime_overrides WHERE id='preferred'"))
	if err != nil || value(r.Rows[0], 0) != "1" {
		t.Fatalf("stale flag %+v %v", r, err)
	}
}

func TestSQLConnectionsPreserveProviderAndFilterGitHubWebhookTargets(t *testing.T) {
	ctx := context.Background()
	db := sqlitetest.New(t, migrations.BaselineSQL+MigrationSQL+seedSQL)
	if _, err := db.Execute(ctx, stmt("UPDATE repo_connections SET provider='tangled' WHERE id='connection'")); err != nil {
		t.Fatal(err)
	}
	store := SQLStore{DB: db}
	c, err := store.Connection(ctx, "project")
	if err != nil || c.Provider != "tangled" {
		t.Fatalf("provider lost: %+v %v", c, err)
	}
	targets, err := store.Connections(ctx, "owner", "repo")
	if err != nil || len(targets) != 0 {
		t.Fatalf("native connection selected by GitHub webhook: %+v %v", targets, err)
	}
}
