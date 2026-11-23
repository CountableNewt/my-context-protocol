package recordservice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/CountableNewt/my-context-protocol/services/go/atproto"
	"github.com/CountableNewt/my-context-protocol/services/go/domainrecords"
	"github.com/CountableNewt/my-context-protocol/services/go/internal/sqlitetest"
	"github.com/CountableNewt/my-context-protocol/services/go/migrations"
	"github.com/CountableNewt/my-context-protocol/services/go/providerhub"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
	cidlib "github.com/ipfs/go-cid"
	"github.com/multiformats/go-multihash"
)

const testOwner = "did:plc:aaaaaaaaaaaaaaaaaaaaaaaa"

type memoryPDS struct {
	mu        sync.Mutex
	records   map[string]atproto.Record
	fail      bool
	puts      int
	beforePut func()
}

func (m *memoryPDS) Get(_ context.Context, v atproto.Mutation) (atproto.Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.records[v.URI()]
	if !ok {
		return r, atproto.ErrNotFound
	}
	r.Value = append([]byte(nil), r.Value...)
	return r, nil
}
func (m *memoryPDS) Put(_ context.Context, v atproto.Mutation) (atproto.Reference, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail {
		return atproto.Reference{}, errors.New("PDS unavailable")
	}
	if m.beforePut != nil {
		run := m.beforePut
		m.beforePut = nil
		run()
	}
	old, exists := m.records[v.URI()]
	if v.ExpectedCID == nil && exists || v.ExpectedCID != nil && (!exists || old.CID != *v.ExpectedCID) {
		return atproto.Reference{}, atproto.ErrConflict
	}
	hash, _ := multihash.Sum(v.Record, multihash.SHA2_256, -1)
	ref := atproto.Reference{URI: v.URI(), CID: cidlib.NewCidV1(cidlib.DagCBOR, hash).String()}
	m.records[v.URI()] = atproto.Record{Reference: ref, Value: append([]byte(nil), v.Record...)}
	m.puts++
	return ref, nil
}

type sqlFixture struct {
	db      *sqlitetest.DB
	pds     *memoryPDS
	engine  domainrecords.Engine
	bridge  *SQLProjection
	service *Service
}

func fixtureSQL(t *testing.T, compiled bool) *sqlFixture {
	t.Helper()
	db := sqlitetest.New(t, migrations.BaselineSQL+domainrecords.MigrationSQL+atproto.MigrationSQL+providerhub.MigrationSQL+MigrationSQL+`CREATE TABLE account_atproto_identities(did TEXT PRIMARY KEY,account_id TEXT);CREATE TABLE private_switch(id TEXT PRIMARY KEY,value INTEGER NOT NULL);CREATE TABLE mcp_catalog_revisions(project_id TEXT PRIMARY KEY,revision INTEGER NOT NULL DEFAULT 0);`)
	statements := []turso.Statement{
		{SQL: `INSERT INTO accounts(id,github_id,login) VALUES('account',1,'owner'),('other',2,'other')`},
		{SQL: `INSERT INTO account_atproto_identities(did,account_id) VALUES('` + testOwner + `','account')`},
		{SQL: `INSERT INTO projects(id,account_id,name,slug,subdomain,created_at) VALUES('project','account','Original','project','project',1234),('other-project','other','Other','other','other',1234)`},
		{SQL: `INSERT INTO repo_connections(id,project_id,provider,repo_owner,repo_name,default_branch,auth_type,webhook_secret) VALUES('repo','project','github','owner','repo','main','oauth','old-secret')`},
		{SQL: `INSERT INTO private_switch(id,value) VALUES('guard',1)`},
	}
	if compiled {
		statements = append(statements,
			turso.Statement{SQL: `INSERT INTO releases(id,project_id,commit_sha,status) VALUES('release','project','revision','ready')`},
			turso.Statement{SQL: `INSERT INTO skill_packages(id,release_id,path,name,validation_status) VALUES('package','release','SKILL.md','guide','valid')`},
			turso.Statement{SQL: `INSERT INTO compiled_skills(id,release_id,skill_package_id,path,name,exposure_type,risk_level,repo_specific,status,skill_body,canonical_json) VALUES('compiled','release','package','SKILL.md','guide','tool','low',0,'ready','private-source-body','{"instructions":"private-source-body","id":"guide"}')`},
		)
	}
	if _, err := db.Batch(context.Background(), statements); err != nil {
		t.Fatal(err)
	}
	pds := &memoryPDS{records: map[string]atproto.Record{}}
	engine := domainrecords.Engine{DB: db, Records: pds, Publisher: atproto.Publisher{Records: pds, Journal: atproto.SQLJournal{DB: db}}, WorkLimit: 100}
	review, err := engine.Inventory(context.Background(), "account", testOwner, "project")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = engine.Publish(context.Background(), "account", review, domainrecords.Consent{ReviewDigest: review.Digest, Public: true, Telemetry: true}); err != nil {
		t.Fatal(err)
	}
	service := &Service{DB: db}
	fixture := &sqlFixture{db: db, pds: pds, engine: engine, service: service}
	fixture.bridge = &SQLProjection{Raw: db, Records: service, runIntent: func(ctx context.Context, intent nativeIntent) error {
		engine := fixture.engine
		return executeNative(ctx, db, &engine, testOwner, intent)
	}}
	return fixture
}
func (f *sqlFixture) ctx(t *testing.T) context.Context {
	t.Helper()
	r, err := f.db.Execute(context.Background(), turso.Statement{SQL: `SELECT manifest_cid,review_digest FROM domain_canonical_projects WHERE project_id='project'`, WantRows: true})
	if err != nil {
		t.Fatal(err)
	}
	return context.WithValue(context.Background(), scopeKey{}, scope{progress: &scopeProgress{cid: cell(r.Rows[0][0]), digest: cell(r.Rows[0][1])}, service: f.service, account: "account", project: "project", owner: testOwner, cid: cell(r.Rows[0][0]), digest: cell(r.Rows[0][1]), telemetry: true})
}
func (f *sqlFixture) value(t *testing.T, query string) string {
	t.Helper()
	r, err := f.db.Execute(context.Background(), turso.Statement{SQL: query, WantRows: true})
	if err != nil {
		t.Fatal(err)
	}
	return cell(r.Rows[0][0])
}
func (f *sqlFixture) resume(t *testing.T) {
	t.Helper()
	for range 100 {
		intent, err := loadNative(context.Background(), f.db, "account", "project")
		if err != nil {
			t.Fatal(err)
		}
		if intent == nil {
			return
		}
		err = f.bridge.runIntent(context.Background(), *intent)
		if err == nil {
			continue
		}
		var pending *domainrecords.PendingError
		if !errors.As(err, &pending) {
			t.Fatal(err)
		}
	}
	t.Fatal("native intent did not finish within bounded retries")
}

func TestNativeSQLCanonicalFirstSecretsAndOriginalReturning(t *testing.T) {
	f := fixtureSQL(t, false)
	f.pds.fail = true
	command := turso.Statement{SQL: `UPDATE repo_connections SET repo_name=?,webhook_secret=? WHERE id=? RETURNING id AS connection`, Args: []turso.Value{turso.Text("renamed"), turso.Text("new-secret-never-public"), turso.Text("repo")}, WantRows: true}
	if _, err := f.bridge.Execute(f.ctx(t), command); err == nil {
		t.Fatal("PDS outage acknowledged")
	}
	if f.value(t, `SELECT repo_name FROM repo_connections WHERE id='repo'`) != "repo" || f.value(t, `SELECT webhook_secret FROM repo_connections WHERE id='repo'`) != "old-secret" {
		t.Fatal("rollback evaluation changed public or private state")
	}
	f.pds.fail = false
	f.resume(t)
	if f.value(t, `SELECT repo_name FROM repo_connections WHERE id='repo'`) != "renamed" || f.value(t, `SELECT webhook_secret FROM repo_connections WHERE id='repo'`) != "new-secret-never-public" {
		t.Fatal("native public/private projection did not commit together")
	}
	r, err := f.db.Execute(context.Background(), turso.Statement{SQL: `SELECT results_json,statements_json FROM domain_native_intents`, WantRows: true})
	if err != nil {
		t.Fatal(err)
	}
	var results []turso.Result
	if json.Unmarshal([]byte(cell(r.Rows[0][0])), &results) != nil || len(results) != 1 || *results[0].Columns[0].Name != "connection" || cell(results[0].Rows[0][0]) != "repo" {
		t.Fatal("original returning contract lost")
	}
	if cell(r.Rows[0][1]) != "[]" {
		t.Fatal("completed journal retained duplicate credential payload")
	}
	for _, record := range f.pds.records {
		if strings.Contains(string(record.Value), "new-secret") || strings.Contains(string(record.Value), "webhook_secret") {
			t.Fatal("private credential entered PDS record")
		}
	}
}

func TestNativeOrderedInsertSelectGuardsAndPrivateCursor(t *testing.T) {
	f := fixtureSQL(t, false)
	commands := []turso.Statement{
		{SQL: `INSERT INTO releases(id,project_id,commit_sha,status) VALUES(?,?,'pending','pending')`, Args: []turso.Value{turso.Text("release-new"), turso.Text("project")}},
		{SQL: `UPDATE releases SET commit_sha='immutable',status='ready' WHERE id=? AND EXISTS(SELECT 1 FROM private_switch WHERE id='guard' AND value=1) RETURNING id`, Args: []turso.Value{turso.Text("release-new")}, WantRows: true},
		{SQL: `UPDATE private_switch SET value=2 WHERE id='guard'`},
	}
	results, err := f.bridge.Batch(f.ctx(t), commands)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 || cell(results[1].Rows[0][0]) != "release-new" || results[2].AffectedRowCount != 1 {
		t.Fatal("native ordered result shape changed")
	}
	if f.value(t, `SELECT status FROM releases WHERE id='release-new'`) != "ready" || f.value(t, `SELECT value FROM private_switch WHERE id='guard'`) != "2" {
		t.Fatal("dependent commands did not execute atomically")
	}
	// This is the actual GitHub INSERT SELECT owner/access pattern. It must
	// remain a no-op when a connection already exists, including RETURNING.
	result, err := f.bridge.Execute(f.ctx(t), turso.Statement{SQL: `INSERT INTO repo_connections(id,project_id,provider,repo_owner,repo_name,default_branch,auth_type) SELECT ?,?,'github',?,?,?,'oauth' WHERE EXISTS(SELECT 1 FROM projects WHERE id=? AND account_id=?) AND NOT EXISTS(SELECT 1 FROM repo_connections WHERE project_id=?) RETURNING id`, Args: []turso.Value{turso.Text("new-repo"), turso.Text("project"), turso.Text("owner"), turso.Text("repo"), turso.Text("main"), turso.Text("project"), turso.Text("account"), turso.Text("project")}, WantRows: true})
	if err != nil || len(result.Rows) != 0 || result.AffectedRowCount != 0 {
		t.Fatalf("guarded insert did not remain a no-op: %+v %v", result, err)
	}
}

func TestPendingRetainsGeneratedIdentityAndClock(t *testing.T) {
	f := fixtureSQL(t, false)
	f.engine.WorkLimit = 1
	_, err := f.bridge.Execute(f.ctx(t), turso.Statement{SQL: `INSERT INTO skill_assignments(id,project_id,skill_id,scope,activation_mode,required,priority,target_type,target_id,created_at) VALUES(?,?,?,?,?,?,?,?,?,?) RETURNING id`, Args: []turso.Value{turso.Text("generated-once"), turso.Text("project"), turso.Text("guide"), turso.Text("repository"), turso.Text("manual"), turso.Integer(0), turso.Integer(10), turso.Text("project"), turso.Text("project"), turso.Integer(123456)}, WantRows: true})
	var pending *domainrecords.PendingError
	if !errors.As(err, &pending) {
		t.Fatalf("expected bounded publication progress: %v", err)
	}
	if f.value(t, `SELECT COUNT(*) FROM skill_assignments`) != "0" {
		t.Fatal("pending canonical command projected early")
	}
	f.resume(t)
	if f.value(t, `SELECT id FROM skill_assignments`) != "generated-once" || f.value(t, `SELECT created_at FROM skill_assignments`) != "123456" {
		t.Fatal("retry regenerated identity or clock")
	}
}

func TestCanonicalSQLFailsClosedOnGuardDriftAndCrossProject(t *testing.T) {
	f := fixtureSQL(t, false)
	if _, err := f.bridge.Execute(f.ctx(t), turso.Statement{SQL: `UPDATE projects SET name='Crossed' WHERE id='other-project'`}); err == nil {
		t.Fatal("cross-project command accepted")
	}
	for _, query := range []string{`INSERT OR REPLACE INTO projects(id,name) VALUES('project','oops')`, `WITH moved AS(SELECT 1) UPDATE projects SET name='oops' WHERE id='project'`, `UPDATE projects SET name='oops'; DELETE FROM projects`} {
		if _, err := f.bridge.Execute(f.ctx(t), turso.Statement{SQL: query}); err == nil {
			t.Fatal("unsupported mutating shape bypassed bridge")
		}
	}
	f.pds.beforePut = func() {
		if _, err := f.db.Execute(context.Background(), turso.Statement{SQL: `UPDATE private_switch SET value=0 WHERE id='guard'`}); err != nil {
			t.Fatal(err)
		}
	}
	_, err := f.bridge.Batch(f.ctx(t), []turso.Statement{{SQL: `UPDATE projects SET name='Changed' WHERE id='project' AND EXISTS(SELECT 1 FROM private_switch WHERE id='guard' AND value=1)`}, {SQL: `UPDATE private_switch SET value=2 WHERE id='guard'`}})
	if err == nil {
		t.Fatal("changed native guard acknowledged")
	}
	if f.value(t, `SELECT name FROM projects WHERE id='project'`) != "Original" || f.value(t, `SELECT value FROM private_switch WHERE id='guard'`) != "0" {
		t.Fatal("guard failure partially committed native projection")
	}
	if f.value(t, `SELECT status FROM domain_native_intents`) != "pending" {
		t.Fatal("failed native projection was marked complete")
	}
}

func TestCanonicalSourceContentRemainsGitOwned(t *testing.T) {
	f := fixtureSQL(t, true)
	_, err := f.bridge.Execute(f.ctx(t), turso.Statement{SQL: `UPDATE compiled_skills SET summary='metadata',skill_body=? WHERE id='compiled'`, Args: []turso.Value{turso.Text("replacement")}})
	var source *SourceContentError
	if !errors.As(err, &source) || source.HTTPStatus() != 409 {
		t.Fatalf("source edit needs explicit conflict: %v", err)
	}
	_, err = f.bridge.Execute(f.ctx(t), turso.Statement{SQL: `UPDATE compiled_skills SET summary='metadata',skill_body=? WHERE id='compiled'`, Args: []turso.Value{turso.Text("private-source-body")}})
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range f.pds.records {
		if strings.Contains(string(record.Value), "private-source-body") {
			t.Fatal("source instructions exported")
		}
	}
}

func TestCanonicalNativeBatchPreservesHundredAssignmentsAndOutcomes(t *testing.T) {
	for _, kind := range []string{"assignments", "outcomes"} {
		t.Run(kind, func(t *testing.T) {
			f := fixtureSQL(t, false)
			commands := []turso.Statement{}
			if kind == "assignments" {
				commands = append(commands, turso.Statement{SQL: `DELETE FROM skill_assignments WHERE project_id='project'`})
			} else {
				commands = append(commands, turso.Statement{SQL: `INSERT INTO project_runtime_settings(id,project_id,telemetry_enabled,telemetry_retention_days,semantic_enabled,feedback_issue_creation_enabled,updated_at) VALUES('settings','project',1,30,0,0,1234)`})
			}
			for i := range 100 {
				id := fmt.Sprintf("%s-%03d", kind, i)
				if kind == "assignments" {
					commands = append(commands, turso.Statement{SQL: `INSERT INTO skill_assignments(id,project_id,skill_id,scope,activation_mode,required,priority,target_type,target_id,created_at) VALUES(?,'project',?,'repository','manual',0,10,'project','project',1234)`, Args: []turso.Value{turso.Text(id), turso.Text(fmt.Sprintf("guide-%03d", i))}})
				} else {
					commands = append(commands, turso.Statement{SQL: `INSERT INTO skill_runtime_events(id,project_id,trace_id,event_type,detail_json,created_at,client_identity,source) SELECT ?,'project','trace','usage','{}',1234,'client','user' WHERE EXISTS(SELECT 1 FROM project_runtime_settings WHERE project_id='project' AND telemetry_enabled=1)`, Args: []turso.Value{turso.Text(id)}})
				}
			}
			_, err := f.bridge.Batch(f.ctx(t), commands)
			var pending *domainrecords.PendingError
			if err != nil && !errors.As(err, &pending) {
				t.Fatal(err)
			}
			if err != nil {
				f.resume(t)
			}
			table := "skill_assignments"
			if kind == "outcomes" {
				table = "skill_runtime_events"
			}
			if got := f.value(t, "SELECT COUNT(*) FROM "+table); got != "100" {
				t.Fatalf("existing hundred-item limit regressed: %s", got)
			}
			if f.value(t, `SELECT COUNT(*) FROM domain_native_intents WHERE status='pending'`) != "0" {
				t.Fatal("successful batch left unresolved native intent")
			}
		})
	}
}

type callCountingDB struct {
	Database
	execute, batches int
}

func (d *callCountingDB) Execute(ctx context.Context, s turso.Statement) (*turso.Result, error) {
	d.execute++
	return d.Database.Execute(ctx, s)
}
func (d *callCountingDB) Batch(ctx context.Context, s []turso.Statement) ([]turso.Result, error) {
	d.batches++
	return d.Database.Batch(ctx, s)
}
func TestScopedReadAndPrivateExecutePreserveNativeCallShape(t *testing.T) {
	f := fixtureSQL(t, false)
	raw := &callCountingDB{Database: f.db}
	f.bridge.Raw = raw
	result, err := f.bridge.Execute(f.ctx(t), turso.Statement{SQL: `SELECT 9223372036854775807 AS precise`, WantRows: true})
	if err != nil {
		t.Fatal(err)
	}
	if raw.execute != 1 || raw.batches != 0 || len(result.Rows) != 1 || cell(result.Rows[0][0]) != "9223372036854775807" {
		t.Fatal("read was wrapped or lost its integer type")
	}
	if _, err = f.bridge.Execute(f.ctx(t), turso.Statement{SQL: `UPDATE private_switch SET value=3 WHERE id='guard'`}); err != nil {
		t.Fatal(err)
	}
	if raw.execute != 2 || raw.batches != 0 {
		t.Fatal("private execute changed transactional call shape")
	}
}

func TestPrivateEffectGuardsPreserveNativeChangesFunction(t *testing.T) {
	for _, count := range []int{0, 1, 2} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			f := fixtureSQL(t, false)
			if count == 2 {
				if _, err := f.db.Execute(context.Background(), turso.Statement{SQL: "INSERT INTO private_switch(id,value) VALUES('guard2',1)"}); err != nil {
					t.Fatal(err)
				}
			}
			where := "value=1"
			if count == 0 {
				where = "value=9"
			}
			_, err := f.bridge.Batch(f.ctx(t), []turso.Statement{
				{SQL: "UPDATE private_switch SET value=2 WHERE " + where},
				{SQL: "INSERT INTO private_switch(id,value) SELECT 'proof',changes() WHERE changes()=?", Args: []turso.Value{turso.Integer(int64(count))}},
				{SQL: "UPDATE projects SET name='Expected' WHERE id='project' AND EXISTS(SELECT 1 FROM private_switch WHERE id='proof')"},
			})
			if err != nil {
				t.Fatal(err)
			}
			row, err := f.db.Execute(context.Background(), turso.Statement{SQL: "SELECT name FROM projects WHERE id='project'", WantRows: true})
			if err != nil || cell(row.Rows[0][0]) != "Expected" {
				t.Fatal("guard changed native changes() semantics", row, err)
			}
		})
	}
}

func TestRequestScopeAdvancesOnlyItsOwnConfirmedPublications(t *testing.T) {
	f := fixtureSQL(t, false)
	ctx := f.ctx(t)
	for _, name := range []string{"First", "Second"} {
		if _, err := f.bridge.Execute(ctx, turso.Statement{SQL: "UPDATE projects SET name=? WHERE id='project'", Args: []turso.Value{turso.Text(name)}}); err != nil {
			t.Fatal("sequential request mutation failed", err)
		}
	}
	if f.value(t, "SELECT name FROM projects WHERE id='project'") != "Second" {
		t.Fatal("missing own second mutation")
	}
	if _, err := f.engine.Update(context.Background(), "account", testOwner, "project", []domainrecords.Change{{Table: "projects", ID: "project", Values: map[string]turso.Value{"name": turso.Text("External")}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.bridge.Execute(ctx, turso.Statement{SQL: "UPDATE projects SET name='Overwrite' WHERE id='project'"}); !errors.Is(err, domainrecords.ErrChanged) {
		t.Fatal("silently rebased external mutation", err)
	}
}
func TestConcurrentWritesShareRequestScopeSafely(t *testing.T) {
	f := fixtureSQL(t, false)
	ctx := f.ctx(t)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, name := range []string{"One", "Two"} {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			_, err := f.bridge.Execute(ctx, turso.Statement{SQL: "UPDATE projects SET name=? WHERE id='project'", Args: []turso.Value{turso.Text(name)}})
			errs <- err
		}(name)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestOwnCommittedMutationSucceedsWhenLaterWriterWinsBeforeReturn(t *testing.T) {
	f := fixtureSQL(t, false)
	ctx := f.ctx(t)
	run := f.bridge.runIntent
	f.bridge.runIntent = func(ctx context.Context, intent nativeIntent) error {
		if err := run(ctx, intent); err != nil {
			return err
		}
		_, err := f.engine.Update(context.Background(), "account", testOwner, "project", []domainrecords.Change{{Table: "projects", ID: "project", Values: map[string]turso.Value{"name": turso.Text("Later writer")}}})
		return err
	}
	if _, err := f.bridge.Execute(ctx, turso.Statement{SQL: "UPDATE projects SET name='Own committed command' WHERE id='project'"}); err != nil {
		t.Fatal("already committed operation falsely failed", err)
	}
	if _, err := f.bridge.Execute(ctx, turso.Statement{SQL: "UPDATE projects SET name='Silent rebase' WHERE id='project'"}); !errors.Is(err, domainrecords.ErrChanged) {
		t.Fatal("adopted later generation", err)
	}
}

func TestNativeUnchangedPublicStateCompletesPrivateAndNoopCommands(t *testing.T) {
	for _, command := range []string{
		"UPDATE projects SET custom_domain_verified_at=123,custom_domain_verification_token='proof' WHERE id='project' RETURNING id",
		"UPDATE repo_connections SET github_installation_id=42,token_encrypted='private-new' WHERE id='repo' RETURNING id",
		"UPDATE projects SET name=name WHERE id='project' RETURNING id",
	} {
		t.Run(command, func(t *testing.T) {
			f := fixtureSQL(t, false)
			before := f.pds.puts
			results, err := f.bridge.Execute(f.ctx(t), turso.Statement{SQL: command, WantRows: true})
			if err != nil {
				t.Fatal(err)
			}
			if results == nil || len(results.Rows) != 1 {
				t.Fatal("native RETURNING result lost")
			}
			if f.pds.puts != before {
				t.Fatal("unchanged public records written to PDS")
			}
			if f.value(t, "SELECT COUNT(*) FROM domain_native_intents WHERE status='complete'") != "1" {
				t.Fatal("native command did not produce complete receipt")
			}
			if f.value(t, "SELECT COUNT(*) FROM domain_native_intents WHERE status='pending'") != "0" {
				t.Fatal("no-op native intent left pending")
			}
			if strings.Contains(command, "custom_domain_verified_at") && f.value(t, "SELECT custom_domain_verification_token FROM projects WHERE id='project'") != "proof" {
				t.Fatal("private domain proof update lost")
			}
			if strings.Contains(command, "github_installation_id") && f.value(t, "SELECT token_encrypted FROM repo_connections WHERE id='repo'") != "private-new" {
				t.Fatal("private credential update lost")
			}
		})
	}
}
