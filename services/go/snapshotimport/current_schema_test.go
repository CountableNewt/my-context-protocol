package snapshotimport

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CountableNewt/my-context-protocol/services/go/internal/sqlitetest"
	"github.com/CountableNewt/my-context-protocol/services/go/migrations"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
)

func TestCurrentSnapshotPreservesOAuthAndPublicationState(t *testing.T) {
	ctx := context.Background()
	db := sqlitetest.New(t, "")
	if err := migrations.Apply(ctx, db, migrations.Options{Initialize: true, Additions: additiveMigrations()}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Batch(ctx, []turso.Statement{
		{SQL: `INSERT INTO accounts(id,login) VALUES('ATPROTO-NATIVE','native')`},
		{SQL: `INSERT INTO atproto_oauth_requests(state_hash,payload_encrypted,expires_at) VALUES('request-hash','encrypted-request',1234)`},
		{SQL: `INSERT INTO atproto_oauth_sessions(did,session_hash,payload_encrypted,updated_at,revoked) VALUES('did:plc:aaaaaaaaaaaaaaaaaaaaaaaa','session-hash','encrypted-session',1234,1)`},
		{SQL: `INSERT INTO atproto_publication_intents(owner_did,intent_key,digest,mutation_json,record_uri,status,created_at,updated_at) VALUES('did:plc:aaaaaaaaaaaaaaaaaaaaaaaa','intent','digest','{"private":"unpublished Ω"}','at://did:plc:aaaaaaaaaaaaaaaaaaaaaaaa/dev.mycontextprotocol.project.record/key','pending',1234,1235)`},
	}); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	legacy := filepath.Join(dir, "legacy.jsonl")
	if _, err := ExportRemote(ctx, db, legacy, RemoteExportOptions{WritersPaused: true}); err == nil {
		t.Fatal("legacy export silently omitted ATProto state")
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatal("failed legacy export published")
	}
	artifact := filepath.Join(dir, "current.jsonl")
	snapshot, err := ExportRemote(ctx, db, artifact, RemoteExportOptions{WritersPaused: true, CurrentSchema: true})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Format != CurrentFormat {
		t.Fatal("wrong current format")
	}
	for _, name := range []string{"atproto_oauth_requests", "atproto_oauth_sessions", "atproto_publication_intents"} {
		found := false
		for _, table := range snapshot.Tables {
			if table.Name == name {
				found = true
				if table.Count != 1 {
					t.Fatal("missing private rows")
				}
			}
		}
		if !found {
			t.Fatalf("missing table %s", name)
		}
	}
	target := sqlitetest.New(t, "")
	if _, err = Transfer(ctx, target, artifact, Options{Initialize: true}); err != nil {
		t.Fatal("current import", err)
	}
	if got := scalar(t, target, "SELECT payload_encrypted FROM atproto_oauth_sessions"); got.Value != "encrypted-session" {
		t.Fatal("session changed")
	}
	if got := scalar(t, target, "SELECT revoked FROM atproto_oauth_sessions"); got.Value != "1" {
		t.Fatal("logout tombstone changed")
	}
	if got := scalar(t, target, "SELECT mutation_json FROM atproto_publication_intents"); !strings.Contains(got.Value.(string), "unpublished Ω") {
		t.Fatal("intent changed")
	}
	if _, err = Verify(ctx, target, artifact); err != nil {
		t.Fatal("current verify", err)
	}
	restored := filepath.Join(dir, "current.sqlite")
	if _, err = RestoreLocal(ctx, artifact, restored); err != nil {
		t.Fatal("current restore", err)
	}
	checked, err := Export(ctx, restored, filepath.Join(dir, "reexport.jsonl"))
	if err != nil || checked.ID != snapshot.ID {
		t.Fatalf("current local export changed: %v", err)
	}
}

func TestCurrentSnapshotRejectsUnownedSchema(t *testing.T) {
	_, legacy, _ := fixtureSnapshot(t)
	raw, err := os.ReadFile(legacy)
	if err != nil {
		t.Fatal(err)
	}
	// v2 is not permission to introduce unknown tables or silently omit rows.
	raw = []byte(strings.Replace(string(raw), Format, CurrentFormat, 1))
	raw = []byte(strings.Replace(string(raw), `"schema":{`, `"schema":{"unowned":"CREATE TABLE unowned(value TEXT)",`, 1))
	path := filepath.Join(t.TempDir(), "unowned.jsonl")
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = Read(path); err == nil {
		t.Fatal("current snapshot accepted unowned table")
	}
}
