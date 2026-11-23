package snapshotimport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CountableNewt/my-context-protocol/services/go/internal/sqlitetest"
	"github.com/CountableNewt/my-context-protocol/services/go/migrations"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
)

func TestPostCutoverWritesSurviveRemoteExportAndOfflineRestore(t *testing.T) {
	_, artifact, original := fixtureSnapshot(t)
	ctx := context.Background()
	db := sqlitetest.New(t, "")
	if _, e := Transfer(ctx, db, artifact, Options{Initialize: true}); e != nil {
		t.Fatal(e)
	}
	// Legacy rollback remains supported only before the explicit account schema cutover.
	legacyAdditions := []migrations.Migration{}
	for _, migration := range additiveMigrations() {
		if migration.Version != migrations.AccountIdentityVersion {
			legacyAdditions = append(legacyAdditions, migration)
		}
	}
	if e := migrations.Apply(ctx, db, migrations.Options{Additions: legacyAdditions}); e != nil {
		t.Fatal(e)
	}
	// Application data and historical Fluent records survive, while Go-only quota
	// state deliberately does not become part of a Swift rollback database.
	if _, e := db.Batch(ctx, []turso.Statement{
		{SQL: "UPDATE projects SET name='Post-cutover Ω edit' WHERE id=?", Args: []turso.Value{turso.Text(projectID)}},
		{SQL: "UPDATE accounts SET rowid=-9223372036854775808 WHERE id=?", Args: []turso.Value{turso.Text(accountID)}},
		{SQL: "INSERT INTO mcp_rate_limits(bucket_key,window_start,count) VALUES('local',1,2)"},
	}); e != nil {
		t.Fatal(e)
	}
	dir := t.TempDir()
	rollback := filepath.Join(dir, "rollback.jsonl")
	snapshot, e := ExportRemote(ctx, db, rollback, RemoteExportOptions{WritersPaused: true})
	if e != nil {
		t.Fatal(e)
	}
	if snapshot.ID == original.ID {
		t.Fatal("post-cutover edit missing")
	}
	restored := filepath.Join(dir, "rollback.sqlite")
	result, e := RestoreLocal(ctx, rollback, restored)
	if e != nil {
		t.Fatal(e)
	}
	if result.ID != snapshot.ID {
		t.Fatal("restore manifest changed")
	}
	checked, e := Export(ctx, restored, filepath.Join(dir, "check.jsonl"))
	if e != nil || checked.ID != snapshot.ID {
		t.Fatal("standalone rollback changed typed values", e)
	}
	for _, path := range []string{rollback, restored} {
		info, e := os.Stat(path)
		if e != nil || info.Mode().Perm() != 0600 {
			t.Fatal("rollback output not private")
		}
	}
	for _, table := range checked.Tables {
		if strings.HasPrefix(table.Name, "go_") || table.Name == "mcp_rate_limits" {
			t.Fatal("Go transient state leaked")
		}
	}
	// fixture includes >2^53 int, REAL timestamps, empty/nonempty BLOBs, Unicode,
	// sessions and Fluent history. Equality of every typed digest checks all of it.
	if _, e = RestoreLocal(ctx, rollback, restored); e == nil {
		t.Fatal("existing database overwritten")
	}
	if _, e = ExportRemote(ctx, db, rollback, RemoteExportOptions{WritersPaused: true}); e == nil {
		t.Fatal("existing snapshot overwritten")
	}
}

type noAccessDB struct{ t *testing.T }

func (d noAccessDB) Execute(context.Context, turso.Statement) (*turso.Result, error) {
	d.t.Fatal("unexpected database access")
	return nil, errors.New("unreachable")
}
func (d noAccessDB) Batch(context.Context, []turso.Statement) ([]turso.Result, error) {
	d.t.Fatal("unexpected database access")
	return nil, errors.New("unreachable")
}

func TestRemoteExportRequiresPauseBeforeReadOrOutput(t *testing.T) {
	output := filepath.Join(t.TempDir(), "no-output")
	if _, e := ExportRemote(context.Background(), noAccessDB{t}, output, RemoteExportOptions{}); e == nil {
		t.Fatal("unpaused export accepted")
	}
	if _, e := os.Stat(output); !os.IsNotExist(e) {
		t.Fatal("unpaused export wrote output")
	}
}
func TestRemoteExportRejectsUnknownSchemaAndLeavesNoPublishedOutput(t *testing.T) {
	for _, sql := range []string{"CREATE TABLE unowned(value TEXT)", "CREATE VIEW unowned AS SELECT id FROM accounts", "CREATE TRIGGER unowned BEFORE INSERT ON accounts BEGIN SELECT 1; END", "ALTER TABLE accounts ADD COLUMN unowned TEXT"} {
		db := sqlitetest.New(t, migrations.BaselineSQL)
		if _, e := db.Execute(context.Background(), turso.Statement{SQL: sql}); e != nil {
			t.Fatal(e)
		}
		output := filepath.Join(t.TempDir(), "no-output")
		if _, e := ExportRemote(context.Background(), db, output, RemoteExportOptions{WritersPaused: true}); e == nil {
			t.Fatal("unowned schema accepted")
		}
		if _, e := os.Stat(output); !os.IsNotExist(e) {
			t.Fatal("failed export published output")
		}
	}
}

// Counts alone cannot detect equal-count data edits. The independent typed
// digest pass must reject a writer that violated the pause declaration.
type changingExportDB struct {
	Database
	changed bool
}

func (d *changingExportDB) Execute(ctx context.Context, s turso.Statement) (*turso.Result, error) {
	if !d.changed && strings.HasPrefix(s.SQL, "SELECT rowid,") && strings.Contains(s.SQL, `FROM "projects"`) && strings.Contains(s.SQL, "LIMIT ?") {
		d.changed = true
		if _, e := d.Database.Execute(ctx, turso.Statement{SQL: "UPDATE projects SET name='concurrent edit'"}); e != nil {
			return nil, e
		}
	}
	return d.Database.Execute(ctx, s)
}
func TestRemoteExportDetectsSameCountWriterDrift(t *testing.T) {
	_, artifact, _ := fixtureSnapshot(t)
	ctx := context.Background()
	db := sqlitetest.New(t, "")
	if _, e := Transfer(ctx, db, artifact, Options{Initialize: true}); e != nil {
		t.Fatal(e)
	}
	changing := &changingExportDB{Database: db}
	output := filepath.Join(t.TempDir(), "no-output")
	if _, e := ExportRemote(ctx, changing, output, RemoteExportOptions{WritersPaused: true}); e == nil || !changing.changed {
		t.Fatal("same-count source edit accepted")
	}
	if _, e := os.Stat(output); !os.IsNotExist(e) {
		t.Fatal("incoherent export published")
	}
}
func TestRestoreLocalRejectsBrokenArtifactWithoutPublishing(t *testing.T) {
	_, artifact, _ := fixtureSnapshot(t)
	raw, e := os.ReadFile(artifact)
	if e != nil {
		t.Fatal(e)
	}
	// Well-typed but FK-invalid account ID cannot be committed or published.
	raw = []byte(strings.ReplaceAll(string(raw), `"value":"`+accountID+`"`, `"value":"missing-account"`))
	// Change only the account identity, preserving references to the old one.
	raw = []byte(strings.Replace(string(raw), `"value":"missing-account"`, `"value":"other-account"`, 1))
	broken := filepath.Join(t.TempDir(), "broken.jsonl")
	if e = os.WriteFile(broken, raw, 0600); e != nil {
		t.Fatal(e)
	}
	output := filepath.Join(t.TempDir(), "no-output.sqlite")
	if _, e = RestoreLocal(context.Background(), broken, output); e == nil {
		t.Fatal("broken foreign keys restored")
	}
	if _, e = os.Stat(output); !os.IsNotExist(e) {
		t.Fatal("invalid restore published")
	}
	entries, e := os.ReadDir(filepath.Dir(output))
	if e != nil || len(entries) != 0 {
		t.Fatal("partial private restore left behind")
	}
}

type boundedExportDB struct {
	Database
	dataPages int
	maxRows   int
}

func (d *boundedExportDB) Execute(ctx context.Context, s turso.Statement) (*turso.Result, error) {
	result, e := d.Database.Execute(ctx, s)
	if e != nil {
		return nil, e
	}
	if strings.HasPrefix(s.SQL, "SELECT rowid,") {
		if len(result.Rows) > 120 {
			return nil, errors.New("unbounded page")
		}
		if strings.Contains(s.SQL, `FROM "skill_package_files"`) && !strings.Contains(s.SQL, "COALESCE(") && !strings.Contains(s.SQL, "LIMIT ?") {
			d.dataPages++
		}
		encoded, e := json.Marshal(result)
		if e != nil || len(encoded) > turso.MaxResponseBytes {
			return nil, errors.New("unbounded response")
		}
		if len(result.Rows) > d.maxRows {
			d.maxRows = len(result.Rows)
		}
	}
	return result, nil
}
func TestRemoteExportPagesLargeTypedRowsWithinResponseBounds(t *testing.T) {
	_, artifact, _ := fixtureSnapshot(t)
	ctx := context.Background()
	db := sqlitetest.New(t, "")
	if _, e := Transfer(ctx, db, artifact, Options{Initialize: true}); e != nil {
		t.Fatal(e)
	}
	rows := []turso.Statement{}
	for i := 0; i < 130; i++ {
		rows = append(rows, turso.Statement{SQL: "INSERT INTO skill_package_files(id,skill_package_id,path,content,content_type,byte_count,checksum) VALUES(?, ?, ?, ?, 'application/octet-stream',5000,'test')", Args: []turso.Value{turso.Text(fmt.Sprintf("large-%d", i)), turso.Text("DDDDDDDD-DDDD-4DDD-8DDD-DDDDDDDDDDDD"), turso.Text(fmt.Sprintf("large-%d", i)), turso.Blob(bytes.Repeat([]byte{0, 255, 34, 92}, 1250))}})
	}
	for len(rows) > 0 {
		end := 60
		if len(rows) < end {
			end = len(rows)
		}
		if _, e := db.Batch(ctx, rows[:end]); e != nil {
			t.Fatal(e)
		}
		rows = rows[end:]
	}
	bounded := &boundedExportDB{Database: db}
	snapshot, e := ExportRemote(ctx, bounded, filepath.Join(t.TempDir(), "bounded.jsonl"), RemoteExportOptions{WritersPaused: true})
	if e != nil {
		t.Fatal(e)
	}
	if bounded.dataPages < 3 || bounded.maxRows != 120 {
		t.Fatal("size-aware pagination not exercised")
	}
	for _, table := range snapshot.Tables {
		if table.Name == "skill_package_files" && table.Count != 132 {
			t.Fatal("large typed rows lost")
		}
	}
}
