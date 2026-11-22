package snapshotimport

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CountableNewt/my-context-protocol/services/go/internal/sqlitetest"
	"github.com/CountableNewt/my-context-protocol/services/go/migrations"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
)

const accountID = "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA"
const projectID = "BBBBBBBB-BBBB-4BBB-8BBB-BBBBBBBBBBBB"
const sourceFixture = `
import sqlite3,sys,json,hashlib
source=sqlite3.connect(sys.argv[1])
source.execute('PRAGMA foreign_keys=ON')
source.executescript(sys.stdin.read())
a='AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA'
p='BBBBBBBB-BBBB-4BBB-8BBB-BBBBBBBBBBBB'
r='CCCCCCCC-CCCC-4CCC-8CCC-CCCCCCCCCCCC'
pkg='DDDDDDDD-DDDD-4DDD-8DDD-DDDDDDDDDDDD'
c='EEEEEEEE-EEEE-4EEE-8EEE-EEEEEEEEEEEE'
source.execute('INSERT INTO _fluent_migrations(id,name,batch,created_at) VALUES(?,?,?,?)',('FLUENT-ID','App.CreateAccounts',47,1700000000.123456))
source.execute('INSERT INTO accounts(id,github_id,login,created_at,github_token_encrypted) VALUES(?,?,?,?,?)',(a,9007199254740993,'source-owner',1700000000.123456,'private encrypted token'))
source.execute('INSERT INTO projects(id,account_id,name,slug,subdomain,created_at) VALUES(?,?,?,?,?,?)',(p,a,'Private – Ω','private','private',1700000001.987654))
source.execute('INSERT INTO releases(id,project_id,commit_sha,status,created_at) VALUES(?,?,?,?,?)',(r,p,'commit','ready',1700000002.5))
source.execute('INSERT INTO skill_packages(id,release_id,path,name,validation_status) VALUES(?,?,?,?,?)',(pkg,r,'guide/SKILL.md','guide','valid'))
source.execute('INSERT INTO compiled_skills(id,release_id,skill_package_id,path,name,summary,exposure_type,risk_level,repo_specific,status,skill_body) VALUES(?,?,?,?,?,?,?,?,?,?,?)',(c,r,pkg,'guide/SKILL.md','guide','Useful','resource','low',0,'ready','# Private guide\nUnicode Ω & <quotes>'))
for ident,content in [('file-a',bytes(range(256))*256),('file-b',b'')]:
 source.execute('INSERT INTO skill_package_files(id,skill_package_id,path,content,content_type,byte_count,checksum) VALUES(?,?,?,?,?,?,?)',(ident,pkg,ident,content,'application/octet-stream',len(content),hashlib.sha256(content).hexdigest()))
source.execute('INSERT INTO app_sessions(id,session_key,payload,updated_at) VALUES(?,?,?,?)',('SESSION-ID','a'*64,json.dumps({'accountId':a,'private':'session data'}),1700000003.25))
source.commit()
source.close()
`

func fixtureSnapshot(t *testing.T) (string, string, *Snapshot) {
	t.Helper()
	if _, e := exec.LookPath("python3"); e != nil {
		t.Skip("SQLite snapshot tests require python3")
	}
	dir := t.TempDir()
	source := filepath.Join(dir, "source.sqlite")
	cmd := exec.Command("python3", "-c", sourceFixture, source)
	cmd.Stdin = strings.NewReader(migrations.BaselineSQL)
	if raw, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("fixture creation failed: %v %s", e, raw)
	}
	artifact := filepath.Join(dir, "snapshot.jsonl")
	snapshot, e := Export(context.Background(), source, artifact)
	if e != nil {
		t.Fatal(e)
	}
	return source, artifact, snapshot
}
func scalar(t *testing.T, db Database, sql string, args ...turso.Value) turso.Value {
	t.Helper()
	r, e := db.Execute(context.Background(), turso.Statement{SQL: sql, Args: args, WantRows: true})
	if e != nil || len(r.Rows) != 1 || len(r.Rows[0]) != 1 {
		t.Fatal("scalar query failed", e, r)
	}
	return r.Rows[0][0]
}
func TestSQLiteSnapshotRoundTripPreservesTypedRowsAndFluentHistory(t *testing.T) {
	_, artifact, snapshot := fixtureSnapshot(t)
	if info, e := os.Stat(artifact); e != nil || info.Mode().Perm() != 0600 {
		t.Fatal("artifact is not private")
	}
	db := sqlitetest.New(t, "")
	report, e := Transfer(context.Background(), db, artifact, Options{Initialize: true, BatchRows: 1})
	if e != nil || !report.Verified || report.Status != "verified" || report.SnapshotID != snapshot.ID {
		t.Fatal(report, e)
	}
	if got := scalar(t, db, "SELECT github_id FROM accounts WHERE id=?", turso.Text(accountID)); got.Type != "integer" || got.Value != "9007199254740993" {
		t.Fatal("int64 lost precision", got)
	}
	if got := scalar(t, db, "SELECT created_at FROM accounts WHERE id=?", turso.Text(accountID)); got.Type != "float" || got.Value != float64(1700000000.123456) {
		t.Fatal("timestamp changed", got)
	}
	if got := scalar(t, db, "SELECT id FROM _fluent_migrations WHERE name='App.CreateAccounts'"); got.Value != "FLUENT-ID" {
		t.Fatal("Fluent migration history was replaced", got)
	}
	blob := scalar(t, db, "SELECT content FROM skill_package_files WHERE id='file-a'")
	decoded, e := base64.StdEncoding.DecodeString(*blob.Base64)
	if e != nil || len(decoded) != 65536 || decoded[0] != 0 || decoded[255] != 255 {
		t.Fatal("blob changed", e)
	}
	empty := scalar(t, db, "SELECT content FROM skill_package_files WHERE id='file-b'")
	if empty.Type != "blob" || empty.Base64 == nil || *empty.Base64 != "" {
		t.Fatal("empty blob became null", empty)
	}
	payload := scalar(t, db, "SELECT payload FROM app_sessions WHERE id='SESSION-ID'")
	if !strings.Contains(payload.Value.(string), accountID) {
		t.Fatal("session attribution lost")
	}
	if _, e := Verify(context.Background(), db, artifact); e != nil {
		t.Fatal(e)
	}
}
func TestImportRefusesPopulatedDestinationWithoutChangingIt(t *testing.T) {
	_, artifact, _ := fixtureSnapshot(t)
	db := sqlitetest.New(t, migrations.BaselineSQL+`INSERT INTO accounts(id,github_id,login) VALUES('KEEP',7,'keep');`)
	for _, options := range []Options{{Initialize: true}, {Resume: true}} {
		if _, e := Transfer(context.Background(), db, artifact, options); e == nil {
			t.Fatal("populated destination accepted")
		}
	}
	if got := scalar(t, db, "SELECT login FROM accounts WHERE id='KEEP'"); got.Value != "keep" {
		t.Fatal("existing data changed")
	}
}

type failingDataTransaction struct {
	Database
	seen      int
	ambiguous bool
}

func (db *failingDataTransaction) Batch(ctx context.Context, statements []turso.Statement) ([]turso.Result, error) {
	for _, s := range statements {
		if strings.HasPrefix(s.SQL, `INSERT INTO "skill_package_files"`) {
			db.seen++
			if db.seen == 2 {
				if db.ambiguous {
					_, e := db.Database.Batch(ctx, statements)
					if e != nil {
						return nil, e
					}
					return nil, errors.New("simulated lost response after commit")
				}
				copyStatements := append([]turso.Statement{}, statements...)
				copyStatements = append(copyStatements, turso.Statement{SQL: "INSERT INTO nonexistent_failure_target VALUES(1)"})
				return db.Database.Batch(ctx, copyStatements)
			}
		}
	}
	return db.Database.Batch(ctx, statements)
}
func TestImportChunkFailureAndAmbiguousCommitResumeWithoutDuplicates(t *testing.T) {
	for _, ambiguous := range []bool{false, true} {
		name := "sql_rollback"
		if ambiguous {
			name = "lost_commit_response"
		}
		t.Run(name, func(t *testing.T) {
			_, artifact, _ := fixtureSnapshot(t)
			db := sqlitetest.New(t, "")
			failing := &failingDataTransaction{Database: db, ambiguous: ambiguous}
			if _, e := Transfer(context.Background(), failing, artifact, Options{Initialize: true, BatchRows: 1}); e == nil {
				t.Fatal("failure not injected")
			}
			expected := "1"
			if ambiguous {
				expected = "2"
			}
			if got := scalar(t, db, "SELECT next_row FROM go_snapshot_import_progress WHERE table_name='skill_package_files'"); got.Value != expected {
				t.Fatal("data/progress transaction diverged", got)
			}
			if got := scalar(t, db, "SELECT COUNT(*) FROM skill_package_files"); got.Value != expected {
				t.Fatal("row count diverged", got)
			}
			if got := scalar(t, db, "SELECT status FROM go_snapshot_import"); got.Value != "loading" {
				t.Fatal("partial data published")
			}
			report, e := Transfer(context.Background(), db, artifact, Options{Resume: true, BatchRows: 1})
			if e != nil || !report.Verified || report.Status != "verified" {
				t.Fatal(report, e)
			}
		})
	}
}
func mutateSource(t *testing.T, path, sql string) {
	t.Helper()
	cmd := exec.Command("python3", "-c", "import sqlite3,sys; d=sqlite3.connect(sys.argv[1]);d.execute(sys.argv[2]);d.commit();d.close()", path, sql)
	if raw, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("fixture mutation failed %v %s", e, raw)
	}
}
func TestResumeRejectsDifferentSnapshotAndModifiedStagedRows(t *testing.T) {
	source, artifact, _ := fixtureSnapshot(t)
	db := sqlitetest.New(t, "")
	if _, e := Transfer(context.Background(), &failingDataTransaction{Database: db}, artifact, Options{Initialize: true, BatchRows: 1}); e == nil {
		t.Fatal("failure not injected")
	}
	mutateSource(t, source, "UPDATE accounts SET login='changed'")
	different := filepath.Join(filepath.Dir(source), "different.jsonl")
	if _, e := Export(context.Background(), source, different); e != nil {
		t.Fatal(e)
	}
	if _, e := Transfer(context.Background(), db, different, Options{Resume: true}); e == nil {
		t.Fatal("different snapshot resumed")
	}
	if _, e := db.Execute(context.Background(), turso.Statement{SQL: "UPDATE accounts SET login='tampered'"}); e != nil {
		t.Fatal(e)
	}
	if _, e := Transfer(context.Background(), db, artifact, Options{Resume: true}); e == nil {
		t.Fatal("tampered staged prefix accepted")
	}
	if got := scalar(t, db, "SELECT COUNT(*) FROM skill_package_files"); got.Value != "1" {
		t.Fatal("resume wrote after failed prefix check")
	}
}
func TestExportRejectsSchemaDriftAndNeverReplacesArtifacts(t *testing.T) {
	source, artifact, _ := fixtureSnapshot(t)
	before, e := os.ReadFile(artifact)
	if e != nil {
		t.Fatal(e)
	}
	if _, e := Export(context.Background(), source, artifact); e == nil {
		t.Fatal("existing artifact overwritten")
	}
	after, _ := os.ReadFile(artifact)
	if string(before) != string(after) {
		t.Fatal("artifact changed")
	}
	mutateSource(t, source, "DROP INDEX skill_usage_project_time")
	output := filepath.Join(filepath.Dir(source), "invalid.jsonl")
	if _, e := Export(context.Background(), source, output); e == nil {
		t.Fatal("schema drift exported")
	}
	if _, e := os.Stat(output); !os.IsNotExist(e) {
		t.Fatal("partial artifact published")
	}
}
func TestSnapshotCompletionAndTypedIntValidationFailBeforeDestinationMutation(t *testing.T) {
	_, artifact, _ := fixtureSnapshot(t)
	raw, e := os.ReadFile(artifact)
	if e != nil {
		t.Fatal(e)
	}
	incomplete := strings.Replace(string(raw), `{"complete":true}`, "", 1)
	path := filepath.Join(filepath.Dir(artifact), "incomplete.jsonl")
	os.WriteFile(path, []byte(incomplete), 0600)
	if _, e := Read(path); e == nil {
		t.Fatal("incomplete snapshot accepted")
	}
	var value turso.Value
	if e := json.Unmarshal([]byte(`{"type":"integer","value":"9223372036854775808"}`), &value); e != nil {
		t.Fatal(e)
	}
	if validateValue(&value) == nil {
		t.Fatal("out-of-range int accepted")
	}
}
func TestVerifiedImportDetectsLaterDataChangesWithoutMutating(t *testing.T) {
	_, artifact, _ := fixtureSnapshot(t)
	db := sqlitetest.New(t, "")
	if _, e := Transfer(context.Background(), db, artifact, Options{Initialize: true}); e != nil {
		t.Fatal(e)
	}
	if _, e := db.Execute(context.Background(), turso.Statement{SQL: "UPDATE accounts SET login='changed-after-verification'"}); e != nil {
		t.Fatal(e)
	}
	if _, e := Verify(context.Background(), db, artifact); e == nil {
		t.Fatal("changed data passed verification")
	}
	if got := scalar(t, db, "SELECT login FROM accounts"); got.Value != "changed-after-verification" {
		t.Fatal("read-only verifier changed data")
	}
}

type failCheckpointCreation struct {
	Database
	failed bool
}

func (db *failCheckpointCreation) Batch(ctx context.Context, statements []turso.Statement) ([]turso.Result, error) {
	if !db.failed && len(statements) > 0 && statements[0].SQL == runDDL {
		db.failed = true
		copyStatements := append([]turso.Statement{}, statements...)
		copyStatements = append(copyStatements, turso.Statement{SQL: "INSERT INTO nonexistent_failure_target VALUES(1)"})
		return db.Database.Batch(ctx, copyStatements)
	}
	return db.Database.Batch(ctx, statements)
}
func TestResumeRecoversEmptyBaselineAfterCheckpointSetupFailure(t *testing.T) {
	_, artifact, _ := fixtureSnapshot(t)
	db := sqlitetest.New(t, "")
	if _, e := Transfer(context.Background(), &failCheckpointCreation{Database: db}, artifact, Options{Initialize: true}); e == nil {
		t.Fatal("failure not injected")
	}
	if got := scalar(t, db, "SELECT COUNT(*) FROM accounts"); got.Value != "0" {
		t.Fatal("data was imported before checkpoint")
	}
	report, e := Transfer(context.Background(), db, artifact, Options{Resume: true})
	if e != nil || !report.Verified {
		t.Fatal(report, e)
	}
}
func TestResumeRejectsInitializedButUnownedData(t *testing.T) {
	_, artifact, _ := fixtureSnapshot(t)
	db := sqlitetest.New(t, "")
	if e := migrations.Apply(context.Background(), db, migrations.Options{Initialize: true}); e != nil {
		t.Fatal(e)
	}
	if _, e := db.Execute(context.Background(), turso.Statement{SQL: "INSERT INTO accounts(id,github_id,login) VALUES('KEEP',7,'keep')"}); e != nil {
		t.Fatal(e)
	}
	if _, e := Transfer(context.Background(), db, artifact, Options{Resume: true}); e == nil {
		t.Fatal("unowned data accepted as staged import")
	}
	if got := scalar(t, db, "SELECT login FROM accounts WHERE id='KEEP'"); got.Value != "keep" {
		t.Fatal("unowned data changed")
	}
}
func TestExportRejectsBrokenSourceForeignKeys(t *testing.T) {
	source, _, _ := fixtureSnapshot(t)
	mutateSource(t, source, "UPDATE projects SET account_id='missing-parent'")
	output := filepath.Join(filepath.Dir(source), "broken-fk.jsonl")
	if _, e := Export(context.Background(), source, output); e == nil {
		t.Fatal("broken source foreign key accepted")
	}
	if _, e := os.Stat(output); !os.IsNotExist(e) {
		t.Fatal("broken source artifact published")
	}
}
func TestInvalidArtifactNeverInitializesDestination(t *testing.T) {
	_, artifact, _ := fixtureSnapshot(t)
	raw, e := os.ReadFile(artifact)
	if e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(filepath.Dir(artifact), "incomplete-target.jsonl")
	os.WriteFile(path, []byte(strings.Replace(string(raw), `{"complete":true}`, "", 1)), 0600)
	db := sqlitetest.New(t, "")
	if _, e := Transfer(context.Background(), db, path, Options{Initialize: true}); e == nil {
		t.Fatal("incomplete snapshot loaded")
	}
	if got := scalar(t, db, "SELECT COUNT(*) FROM sqlite_schema WHERE type='table'"); got.Value != "0" {
		t.Fatal("invalid artifact initialized destination")
	}
}
