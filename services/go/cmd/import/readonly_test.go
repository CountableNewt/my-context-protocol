package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CountableNewt/my-context-protocol/services/go/internal/sqlitetest"
	"github.com/CountableNewt/my-context-protocol/services/go/migrations"
	"github.com/CountableNewt/my-context-protocol/services/go/snapshotimport"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
)

type blockedWriterReader struct {
	db                *sqlitetest.DB
	queries, fkChecks int
	brokenFK          bool
}

func (r *blockedWriterReader) QueryReadOnly(ctx context.Context, s turso.Statement) (*turso.Result, error) {
	sql := strings.ToUpper(strings.TrimSpace(s.SQL))
	if !strings.HasPrefix(sql, "SELECT ") && sql != "PRAGMA FOREIGN_KEY_CHECK" {
		return nil, errors.New("provider blocked non-read statement")
	}
	r.queries++
	if sql == "PRAGMA FOREIGN_KEY_CHECK" {
		r.fkChecks++
		if r.brokenFK {
			return &turso.Result{Rows: [][]turso.Value{{turso.Text("fixture")}}}, nil
		}
	}
	return r.db.Execute(ctx, s)
}
func TestReadOnlySnapshotAdapterNeverPermitsBatch(t *testing.T) {
	db := readOnlyDatabase{}
	if _, e := db.Batch(context.Background(), []turso.Statement{{SQL: "SELECT 1"}}); e == nil {
		t.Fatal("read-only adapter allowed transaction")
	}
}
func TestCompleteExportUsesOnlyReadOnlyQueriesWhileCheckingForeignKeys(t *testing.T) {
	db := sqlitetest.New(t, migrations.BaselineSQL)
	// Include exact integer, nonempty text and REAL data rather than testing an
	// empty-schema shortcut. Snapshot export computes and verifies typed hashes.
	if _, e := db.Execute(context.Background(), turso.Statement{SQL: "INSERT INTO accounts(id,github_id,login,created_at) VALUES('test-account',9007199254740993,'fixture-owner',1700000000.5)"}); e != nil {
		t.Fatal(e)
	}
	reader := &blockedWriterReader{db: db}
	path := filepath.Join(t.TempDir(), "snapshot.jsonl")
	report, e := snapshotimport.ExportRemote(context.Background(), readOnlyDatabase{query: reader}, path, snapshotimport.RemoteExportOptions{WritersPaused: true})
	if e != nil {
		t.Fatal(e)
	}
	if len(report.Tables) != 30 || reader.queries < 60 || reader.fkChecks != 1 {
		t.Fatal("complete read-only export or foreign-key audit missing")
	}
	found := false
	for _, table := range report.Tables {
		if table.Name == "accounts" && table.Count == 1 {
			found = true
		}
	}
	if !found {
		t.Fatal("read-only export lost application rows")
	}
	reader.brokenFK = true
	rejected := filepath.Join(t.TempDir(), "rejected.jsonl")
	if _, e = snapshotimport.ExportRemote(context.Background(), readOnlyDatabase{query: reader}, rejected, snapshotimport.RemoteExportOptions{WritersPaused: true}); e == nil {
		t.Fatal("read-only foreign-key failure accepted")
	}
	if _, e = os.Stat(rejected); !os.IsNotExist(e) {
		t.Fatal("foreign-key failure published output")
	}
}
