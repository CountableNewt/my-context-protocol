package migrations_test

import (
	"context"
	"strings"
	"testing"

	"github.com/CountableNewt/my-context-protocol/services/go/internal/sqlitetest"
	"github.com/CountableNewt/my-context-protocol/services/go/migrations"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
)

func TestControlledSetupAdoptionAndChecksum(t *testing.T) {
	ctx := context.Background()
	db := sqlitetest.New(t, "")
	if e := migrations.Apply(ctx, db, migrations.Options{}); e == nil {
		t.Fatal("implicitly initialized database")
	}
	if e := migrations.Apply(ctx, db, migrations.Options{Initialize: true}); e != nil {
		t.Fatal(e)
	}
	addition := migrations.Migration{Version: "002_test", SQL: "CREATE TABLE test_addition(id INTEGER PRIMARY KEY);"}
	if e := migrations.Apply(ctx, db, migrations.Options{Additions: []migrations.Migration{addition}}); e != nil {
		t.Fatal(e)
	}
	if e := migrations.Apply(ctx, db, migrations.Options{Additions: []migrations.Migration{addition}}); e != nil {
		t.Fatal(e)
	}
	addition.SQL = "CREATE TABLE altered(id TEXT);"
	if e := migrations.Apply(ctx, db, migrations.Options{Additions: []migrations.Migration{addition}}); e == nil {
		t.Fatal("changed migration checksum accepted")
	}
}
func TestAdoptLegacyNeverReplaysOrDropsData(t *testing.T) {
	ctx := context.Background()
	db := sqlitetest.New(t, migrations.BaselineSQL+`INSERT INTO accounts(id,github_id,login) VALUES ('KEEP',123,'owner');`)
	if e := migrations.Apply(ctx, db, migrations.Options{}); e == nil {
		t.Fatal("implicit adoption")
	}
	if e := migrations.Apply(ctx, db, migrations.Options{AdoptLegacy: true}); e != nil {
		t.Fatal(e)
	}
	r, e := db.Execute(ctx, turso.Statement{SQL: "SELECT login FROM accounts WHERE id='KEEP'", WantRows: true})
	if e != nil || len(r.Rows) != 1 || r.Rows[0][0].Value != "owner" {
		t.Fatal("lost imported row", r, e)
	}
}
func TestAdoptLegacyRejectsMissingIndex(t *testing.T) {
	ctx := context.Background()
	db := sqlitetest.New(t, migrations.BaselineSQL)
	if _, e := db.Execute(ctx, turso.Statement{SQL: "DROP INDEX skill_usage_project_time"}); e != nil {
		t.Fatal(e)
	}
	if e := migrations.Apply(ctx, db, migrations.Options{AdoptLegacy: true}); e == nil {
		t.Fatal("incomplete schema adopted")
	}
}
func TestMigrationRollbackIsAtomic(t *testing.T) {
	ctx := context.Background()
	db := sqlitetest.New(t, "")
	if e := migrations.Apply(ctx, db, migrations.Options{Initialize: true}); e != nil {
		t.Fatal(e)
	}
	e := migrations.Apply(ctx, db, migrations.Options{Additions: []migrations.Migration{{Version: "002_fail", SQL: "CREATE TABLE partial(id INTEGER); INSERT INTO nonexistent VALUES(1);"}}})
	if e == nil {
		t.Fatal("accepted failed migration")
	}
	r, e := db.Execute(ctx, turso.Statement{SQL: "SELECT name FROM sqlite_schema WHERE name='partial'", WantRows: true})
	if e != nil || len(r.Rows) != 0 {
		t.Fatal("partial schema committed")
	}
}
func TestSQLSplitterHandlesQuotedDelimitersAndComments(t *testing.T) {
	statements, e := migrations.SplitSQL("-- comment; ignored\nCREATE TABLE [semi;colon](value TEXT DEFAULT 'a;''b'); /* ; */ INSERT INTO [semi;colon] VALUES ('--hi;');")
	if e != nil || len(statements) != 2 || !strings.Contains(statements[0], "a;''b") {
		t.Fatal(statements, e)
	}
	for _, s := range []string{"SELECT 'unfinished", "/* unfinished"} {
		if _, e := migrations.SplitSQL(s); e == nil {
			t.Fatal(s)
		}
	}
}

const registryDDL = "CREATE TABLE go_schema_migrations(version TEXT PRIMARY KEY,checksum TEXT NOT NULL,applied_at REAL NOT NULL);"

// Inject an actual SQLite error after all supplied statements, rather than
// returning a fabricated Go error before the transaction executes.
type failTransaction struct {
	migrations.Database
	fail bool
}

func (db *failTransaction) Batch(ctx context.Context, statements []turso.Statement) ([]turso.Result, error) {
	if db.fail {
		db.fail = false
		copyStatements := append([]turso.Statement{}, statements...)
		copyStatements = append(copyStatements, turso.Statement{SQL: "INSERT INTO nonexistent_failure_target VALUES(1)"})
		return db.Database.Batch(ctx, copyStatements)
	}
	return db.Database.Batch(ctx, statements)
}
func schemaObjectCount(t *testing.T, db migrations.Database) int {
	t.Helper()
	r, e := db.Execute(context.Background(), turso.Statement{SQL: "SELECT name FROM sqlite_schema WHERE type IN ('table','index') AND name NOT LIKE 'sqlite_%'", WantRows: true})
	if e != nil {
		t.Fatal(e)
	}
	return len(r.Rows)
}
func registryCount(t *testing.T, db migrations.Database) int {
	t.Helper()
	r, e := db.Execute(context.Background(), turso.Statement{SQL: "SELECT version FROM go_schema_migrations", WantRows: true})
	if e != nil {
		t.Fatal(e)
	}
	return len(r.Rows)
}
func keptAccount(t *testing.T, db migrations.Database) {
	t.Helper()
	r, e := db.Execute(context.Background(), turso.Statement{SQL: "SELECT login FROM accounts WHERE id='KEEP'", WantRows: true})
	if e != nil || len(r.Rows) != 1 || r.Rows[0][0].Value != "owner" {
		t.Fatal("imported data changed", r, e)
	}
}
func TestInitialSetupFailureRollsBackRegistryAndRetries(t *testing.T) {
	ctx := context.Background()
	db := sqlitetest.New(t, "")
	failing := &failTransaction{Database: db, fail: true}
	if e := migrations.Apply(ctx, failing, migrations.Options{Initialize: true}); e == nil {
		t.Fatal("injected SQL failure accepted")
	}
	if n := schemaObjectCount(t, db); n != 0 {
		t.Fatalf("initial registry or baseline escaped rollback: %d objects", n)
	}
	if e := migrations.Apply(ctx, db, migrations.Options{Initialize: true}); e != nil {
		t.Fatal("retry failed", e)
	}
	if registryCount(t, db) != 1 {
		t.Fatal("baseline not recorded exactly once")
	}
}
func TestInitialAdoptionFailureLeavesImportedDataAndNoRegistry(t *testing.T) {
	ctx := context.Background()
	db := sqlitetest.New(t, migrations.BaselineSQL+`INSERT INTO accounts(id,github_id,login) VALUES('KEEP',123,'owner');`)
	before := schemaObjectCount(t, db)
	failing := &failTransaction{Database: db, fail: true}
	if e := migrations.Apply(ctx, failing, migrations.Options{AdoptLegacy: true}); e == nil {
		t.Fatal("injected SQL failure accepted")
	}
	if schemaObjectCount(t, db) != before {
		t.Fatal("failed adoption left registry behind")
	}
	keptAccount(t, db)
	if e := migrations.Apply(ctx, db, migrations.Options{AdoptLegacy: true}); e != nil {
		t.Fatal("adoption retry failed", e)
	}
	keptAccount(t, db)
}
func TestExistingEmptyRegistryRequiresExplicitInitialize(t *testing.T) {
	ctx := context.Background()
	db := sqlitetest.New(t, registryDDL)
	for _, options := range []migrations.Options{{}, {AdoptLegacy: true}} {
		if e := migrations.Apply(ctx, db, options); e == nil {
			t.Fatal("empty registry bypassed explicit initialization")
		}
	}
	if schemaObjectCount(t, db) != 1 || registryCount(t, db) != 0 {
		t.Fatal("unauthorized initialization changed schema")
	}
	failing := &failTransaction{Database: db, fail: true}
	if e := migrations.Apply(ctx, failing, migrations.Options{Initialize: true}); e == nil {
		t.Fatal("injected SQL failure accepted")
	}
	if schemaObjectCount(t, db) != 1 || registryCount(t, db) != 0 {
		t.Fatal("existing empty registry recovery partially committed")
	}
	if e := migrations.Apply(ctx, db, migrations.Options{Initialize: true}); e != nil {
		t.Fatal(e)
	}
	if registryCount(t, db) != 1 {
		t.Fatal("baseline record missing")
	}
}
func TestExistingEmptyRegistryRequiresVerifiedAdoptionWithoutBaselineReplay(t *testing.T) {
	ctx := context.Background()
	db := sqlitetest.New(t, migrations.BaselineSQL+registryDDL+`INSERT INTO accounts(id,github_id,login) VALUES('KEEP',123,'owner');`)
	for _, options := range []migrations.Options{{}, {Initialize: true}} {
		if e := migrations.Apply(ctx, db, options); e == nil {
			t.Fatal("imported data adopted without explicit legacy mode")
		}
	}
	keptAccount(t, db)
	if registryCount(t, db) != 0 {
		t.Fatal("baseline recorded without adoption")
	}
	failing := &failTransaction{Database: db, fail: true}
	if e := migrations.Apply(ctx, failing, migrations.Options{AdoptLegacy: true}); e == nil {
		t.Fatal("injected adoption failure accepted")
	}
	keptAccount(t, db)
	if registryCount(t, db) != 0 {
		t.Fatal("failed adoption recorded baseline")
	}
	if e := migrations.Apply(ctx, db, migrations.Options{AdoptLegacy: true}); e != nil {
		t.Fatal("verified adoption replayed baseline instead of recording it", e)
	}
	keptAccount(t, db)
	if registryCount(t, db) != 1 {
		t.Fatal("baseline record missing")
	}
}
func TestEmptyRegistryAdoptionStillVerifiesEntireSchema(t *testing.T) {
	ctx := context.Background()
	db := sqlitetest.New(t, migrations.BaselineSQL+registryDDL)
	if _, e := db.Execute(ctx, turso.Statement{SQL: "DROP INDEX skill_usage_project_time"}); e != nil {
		t.Fatal(e)
	}
	if e := migrations.Apply(ctx, db, migrations.Options{AdoptLegacy: true}); e == nil {
		t.Fatal("empty registry bypassed schema verification")
	}
	if registryCount(t, db) != 0 {
		t.Fatal("incomplete baseline recorded")
	}
}
func TestNonemptyRegistryWithoutBaselineCannotReplay(t *testing.T) {
	ctx := context.Background()
	db := sqlitetest.New(t, registryDDL+`INSERT INTO go_schema_migrations(version,checksum,applied_at) VALUES('002_unknown','unknown',1);`)
	if e := migrations.Apply(ctx, db, migrations.Options{Initialize: true}); e == nil {
		t.Fatal("missing baseline authorized DDL replay")
	}
	if schemaObjectCount(t, db) != 1 || registryCount(t, db) != 1 {
		t.Fatal("foreign migration history changed")
	}
}
