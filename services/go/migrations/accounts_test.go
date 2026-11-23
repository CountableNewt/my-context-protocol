package migrations_test

import (
	"context"
	"testing"

	"github.com/CountableNewt/my-context-protocol/services/go/internal/sqlitetest"
	"github.com/CountableNewt/my-context-protocol/services/go/migrations"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
)

func TestAccountIdentityRebuildPreservesChildrenAndAllowsATProtoOnly(t *testing.T) {
	ctx := context.Background()
	db := sqlitetest.New(t, "")
	if err := migrations.Apply(ctx, db, migrations.Options{Initialize: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Batch(ctx, []turso.Statement{
		{SQL: `INSERT INTO accounts(id,github_id,login,github_token_encrypted) VALUES('owner',42,'old','encrypted')`},
		{SQL: `INSERT INTO projects(id,account_id,name,slug,subdomain) VALUES('project','owner','name','slug','slug')`},
	}); err != nil {
		t.Fatal(err)
	}
	options := migrations.Options{Additions: []migrations.Migration{{Version: migrations.AccountIdentityVersion, SQL: migrations.AccountIdentitySQL}}}
	if err := migrations.Apply(ctx, db, options); err != nil {
		t.Fatal(err)
	}
	if err := migrations.Apply(ctx, db, options); err != nil {
		t.Fatal("idempotent", err)
	}
	r, err := db.Execute(ctx, turso.Statement{SQL: `SELECT a.github_id,a.github_token_encrypted,p.account_id FROM accounts a JOIN projects p ON p.account_id=a.id`, WantRows: true})
	if err != nil || len(r.Rows) != 1 || r.Rows[0][0].Value != "42" || r.Rows[0][1].Value != "encrypted" || r.Rows[0][2].Value != "owner" {
		t.Fatalf("lost original account/project: %+v %v", r, err)
	}
	if _, err = db.Execute(ctx, turso.Statement{SQL: `INSERT INTO accounts(id,login) VALUES('atproto-one','alice'),('atproto-two','bob')`}); err != nil {
		t.Fatal("ATProto accounts require no fake GitHub ID", err)
	}
	if _, err = db.Execute(ctx, turso.Statement{SQL: `INSERT INTO projects(id,account_id,name,slug,subdomain) VALUES('bad','missing','name','bad','bad')`}); err == nil {
		t.Fatal("ordinary writes lost FK enforcement")
	}
}

func TestGuardedSchemaBatchRejectsOrphansAndRollsBack(t *testing.T) {
	db := sqlitetest.New(t, `CREATE TABLE parent(id TEXT PRIMARY KEY);CREATE TABLE child(id TEXT PRIMARY KEY,parent_id TEXT REFERENCES parent(id));INSERT INTO parent VALUES('p');INSERT INTO child VALUES('c','p');`)
	if _, err := db.SchemaBatch(context.Background(), []turso.Statement{{SQL: `DROP TABLE parent`}, {SQL: `CREATE TABLE parent(id TEXT PRIMARY KEY)`}}); err == nil {
		t.Fatal("orphaning schema rebuild committed")
	}
	r, err := db.Execute(context.Background(), turso.Statement{SQL: `SELECT id FROM parent`, WantRows: true})
	if err != nil || len(r.Rows) != 1 {
		t.Fatalf("rebuild was not rolled back: %+v %v", r, err)
	}
}
