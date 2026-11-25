package operatorjob

import (
	"context"
	"github.com/CountableNewt/my-context-protocol/services/go/appschema"
	"github.com/CountableNewt/my-context-protocol/services/go/internal/sqlitetest"
	"github.com/CountableNewt/my-context-protocol/services/go/mcp"
	"github.com/CountableNewt/my-context-protocol/services/go/migrations"
	"github.com/CountableNewt/my-context-protocol/services/go/operatorbackup"
	"github.com/CountableNewt/my-context-protocol/services/go/snapshotimport"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
	"path/filepath"
	"strings"
	"testing"
)

func TestExactRecipientCoordinationExcludedAndOtherQuotaPreserved(t *testing.T) {
	ctx := context.Background()
	db := sqlitetest.New(t, migrations.BaselineSQL+mcp.MigrationSQL)
	pub, _, err := operatorbackup.Generate()
	if err != nil {
		t.Fatal(err)
	}
	key, err := operatorbackup.Public(pub)
	if err != nil {
		t.Fatal(err)
	}
	prefix := "operatorjob:" + operatorbackup.KeyID(key) + ":"
	keys := []string{prefix + "nonce:own", "normal-" + prefix + "text", "operatorjob:" + strings.Repeat("b", 64) + ":nonce:other", "normal-quota"}
	for i, k := range keys {
		if _, err = db.Execute(ctx, turso.Statement{SQL: "INSERT INTO mcp_rate_limits(bucket_key,window_start,count) VALUES(?,?,?)", Args: []turso.Value{turso.Text(k), turso.Integer(int64(i)), turso.Integer(int64(i + 1))}}); err != nil {
			t.Fatal(err)
		}
	}
	before, err := snapshotimport.ExportRemote(ctx, SnapshotDatabase(db, prefix), filepath.Join(t.TempDir(), "before.jsonl"), snapshotimport.RemoteExportOptions{WritersPaused: true, CurrentSchema: true})
	if err != nil {
		t.Fatal(err)
	}
	var quota *snapshotimport.Table
	for _, table := range before.Tables {
		if table.Name == "mcp_rate_limits" {
			quota = table
		}
	}
	if quota == nil || quota.Count != 3 {
		t.Fatalf("unexpected preserved quota count %+v", quota)
	}
	if _, err = db.Execute(ctx, turso.Statement{SQL: "UPDATE mcp_rate_limits SET count=999 WHERE bucket_key=?", Args: []turso.Value{turso.Text(keys[0])}}); err != nil {
		t.Fatal(err)
	}
	after, err := snapshotimport.ExportRemote(ctx, SnapshotDatabase(db, prefix), filepath.Join(t.TempDir(), "after.jsonl"), snapshotimport.RemoteExportOptions{WritersPaused: true, CurrentSchema: true})
	if err != nil {
		t.Fatal(err)
	}
	if before.ID != after.ID {
		t.Fatal("own disposable coordination changed reviewed fingerprint")
	}
	if _, err = db.Execute(ctx, turso.Statement{SQL: "UPDATE mcp_rate_limits SET count=999 WHERE bucket_key=?", Args: []turso.Value{turso.Text(keys[1])}}); err != nil {
		t.Fatal(err)
	}
	changed, err := snapshotimport.ExportRemote(ctx, SnapshotDatabase(db, prefix), filepath.Join(t.TempDir(), "changed.jsonl"), snapshotimport.RemoteExportOptions{WritersPaused: true, CurrentSchema: true})
	if err != nil {
		t.Fatal(err)
	}
	if changed.ID == before.ID {
		t.Fatal("nonmatching quota mutation omitted")
	}
}
func TestSnapshotApplyRequiresExactReviewedSource(t *testing.T) {
	ctx := context.Background()
	db := sqlitetest.New(t, migrations.BaselineSQL+mcp.MigrationSQL)
	pub, priv, err := operatorbackup.Generate()
	if err != nil {
		t.Fatal(err)
	}
	key, _ := operatorbackup.Public(pub)
	private, _ := operatorbackup.Private(priv)
	snapshot, err := Run(ctx, db, "snapshot", "production.example.com", key, nil)
	if err != nil {
		t.Fatal(err)
	}
	receipt := &operatorbackup.Receipt{Version: 1, Host: "production.example.com", SnapshotID: snapshot.Proof["snapshot_id"].(string), EnvelopeSHA256: snapshot.Proof["envelope_sha256"].(string)}
	if err = receipt.Sign(private); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Execute(ctx, turso.Statement{SQL: "INSERT INTO accounts(id,github_id,login) VALUES('new',1,'new')"}); err != nil {
		t.Fatal(err)
	}
	if _, err = Run(ctx, db, "apply", receipt.Host, key, receipt); err == nil {
		t.Fatal("changed source accepted for migration")
	}
	rows, err := db.Execute(ctx, turso.Statement{SQL: "SELECT name FROM sqlite_schema WHERE name='atproto_oauth_sessions'", WantRows: true})
	if err != nil || len(rows.Rows) != 0 {
		t.Fatal("schema changed before exact source gate")
	}
}

func TestInProcessApplyPreservesSourceAcrossAccountSchemaRebuild(t *testing.T) {
	ctx := context.Background()
	db := sqlitetest.New(t, "")
	if err := migrations.Apply(ctx, db, migrations.Options{Initialize: true, Additions: appschema.Registry()[:4]}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Execute(ctx, turso.Statement{SQL: "INSERT INTO accounts(id,github_id,login,github_token_encrypted) VALUES('account',42,'owner','private-encrypted-token')"}); err != nil {
		t.Fatal(err)
	}
	pub, priv, err := operatorbackup.Generate()
	if err != nil {
		t.Fatal(err)
	}
	key, _ := operatorbackup.Public(pub)
	private, _ := operatorbackup.Private(priv)
	snapshot, err := Run(ctx, db, "snapshot", "production.example.com", key, nil)
	if err != nil {
		t.Fatal(err)
	}
	receipt := &operatorbackup.Receipt{Version: 1, Host: "production.example.com", SnapshotID: snapshot.Proof["snapshot_id"].(string), EnvelopeSHA256: snapshot.Proof["envelope_sha256"].(string)}
	if err = receipt.Sign(private); err != nil {
		t.Fatal(err)
	}
	result, err := Run(ctx, db, "apply", receipt.Host, key, receipt)
	if err != nil {
		t.Fatal(err)
	}
	if result.Proof["source_rows_preserved"] != true {
		t.Fatal("no source preservation evidence")
	}
	if _, err = db.Execute(ctx, turso.Statement{SQL: "INSERT INTO accounts(id,login) VALUES('atproto-only','did-owner')"}); err != nil {
		t.Fatal("identity schema was not migrated", err)
	}
	rows, err := db.Execute(ctx, turso.Statement{SQL: "SELECT github_token_encrypted FROM accounts WHERE id='account'", WantRows: true})
	if err != nil || rows.Rows[0][0].Value != "private-encrypted-token" {
		t.Fatal("source credential changed")
	}
}
