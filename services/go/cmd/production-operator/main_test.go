package main

import (
	"context"
	"encoding/json"
	"github.com/CountableNewt/my-context-protocol/services/go/internal/sqlitetest"
	"github.com/CountableNewt/my-context-protocol/services/go/migrations"
	"github.com/CountableNewt/my-context-protocol/services/go/operatorbackup"
	"github.com/CountableNewt/my-context-protocol/services/go/operatorservice"
	"github.com/CountableNewt/my-context-protocol/services/go/snapshotimport"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTargetGuardsRejectWrongEnvironmentBeforeNetwork(t *testing.T) {
	t.Setenv("VERCEL_ENV", "preview")
	t.Setenv("VERCEL_PROJECT_ID", "prj_jC1eRUJ0CHZvHp7Eej3AUAS4yUq7")
	if err := run([]string{"snapshot", "--writers-paused", "--expected-host", "production.example.com"}); err == nil {
		t.Fatal("preview accepted")
	}
	t.Setenv("VERCEL_ENV", "production")
	t.Setenv("VERCEL_PROJECT_ID", "other")
	if err := run([]string{"apply", "--writers-paused", "--expected-host", "production.example.com"}); err == nil {
		t.Fatal("different project accepted")
	}
	for _, host := range []string{"", "https://example.com", "example.com:443", "*.example.com", " example.com", "example.com/path"} {
		if expectedHost(host, "libsql://example.com") == nil {
			t.Fatalf("invalid host accepted: %q", host)
		}
	}
	if expectedHost("example.com", "libsql://other.com") == nil || expectedHost("example.com", "http://example.com") == nil {
		t.Fatal("wrong target accepted")
	}
	if err := expectedHost("example.com", "libsql://example.com"); err != nil {
		t.Fatal(err)
	}
}
func TestLocalReviewRequiresSuccessfulPrivateRestore(t *testing.T) {
	ctx := context.Background()
	db := sqlitetest.New(t, migrations.BaselineSQL)
	root := t.TempDir()
	os.Chmod(root, 0700)
	source := filepath.Join(root, "source.jsonl")
	snapshot, err := snapshotimport.ExportRemote(ctx, db, source, snapshotimport.RemoteExportOptions{WritersPaused: true, CurrentSchema: true})
	if err != nil {
		t.Fatal(err)
	}
	keys := filepath.Join(root, "keys")
	if err = run([]string{"keygen", "--directory", keys}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(keys, "recipient.pem"))
	if err != nil {
		t.Fatal(err)
	}
	public, err := operatorbackup.Public(raw)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := operatorbackup.Seal(plain, public)
	if err != nil {
		t.Fatal(err)
	}
	encrypted := filepath.Join(root, "snapshot.enc")
	if err = exclusive(encrypted, sealed); err != nil {
		t.Fatal(err)
	}
	reviewDir := filepath.Join(root, "reviewed")
	if err = run([]string{"review", "--directory", reviewDir, "--key", filepath.Join(keys, "recipient-private.pem"), "--envelope", encrypted, "--expected-host", "production.example.com"}); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(filepath.Join(reviewDir, "review.json"))
	if err != nil {
		t.Fatal(err)
	}
	var receipt operatorbackup.Receipt
	if json.Unmarshal(raw, &receipt) != nil || receipt.SnapshotID != snapshot.ID {
		t.Fatal("review not bound to exact snapshot")
	}
	if err = receipt.Verify(public, "production.example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(reviewDir, "restored.sqlite")); err != nil {
		t.Fatal("signed without persisted restore")
	}
	signedDir := filepath.Join(root, "signed")
	if err = run([]string{"sign", "--mode", "apply", "--writers-paused", "--expected-host", "production.example.com", "--audience", "operator.vercel.app", "--key", filepath.Join(keys, "recipient-private.pem"), "--receipt", filepath.Join(reviewDir, "review.json"), "--directory", signedDir}); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(filepath.Join(signedDir, "request.json"))
	if err != nil {
		t.Fatal(err)
	}
	var command operatorservice.Command
	if json.Unmarshal(raw, &command) != nil || command.Review == nil || command.Review.SnapshotID != snapshot.ID {
		t.Fatal("signed request lost reviewed source binding")
	}
	if err = command.Verify(public, "operator.vercel.app", "production.example.com", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err = exclusive(encrypted, []byte("replacement")); err == nil {
		t.Fatal("backup overwritable")
	}
}
func TestPostMigrationComparisonRejectsSourceChanges(t *testing.T) {
	before := &snapshotimport.Snapshot{Tables: []*snapshotimport.Table{{Name: "accounts", Columns: []string{"id"}, Count: 1, Digest: "same"}}}
	after := &snapshotimport.Snapshot{Tables: []*snapshotimport.Table{{Name: "accounts", Columns: []string{"id"}, Count: 1, Digest: "same"}, {Name: "new_table"}}}
	if err := preserved(before, after); err != nil {
		t.Fatal(err)
	}
	after.Tables[0].Digest = "changed"
	if preserved(before, after) == nil {
		t.Fatal("changed rows accepted")
	}
}
