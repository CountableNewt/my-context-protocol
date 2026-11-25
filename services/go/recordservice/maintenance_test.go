package recordservice

import (
	"context"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
	"testing"
	"time"
)

func TestCanonicalRetentionPublishesBeforeDeletingAndRemainsBounded(t *testing.T) {
	f := fixtureSQL(t, false)
	ctx := f.ctx(t)
	commands := []turso.Statement{}
	for _, v := range []struct {
		id string
		at int64
	}{{"old-a", 1}, {"old-b", 2}, {"fresh", 9999999}} {
		commands = append(commands, turso.Statement{SQL: "INSERT INTO skill_runtime_events(id,project_id,trace_id,event_type,detail_json,created_at) VALUES(?,'project','trace','surfaced','{}',?)", Args: []turso.Value{turso.Text(v.id), turso.Integer(v.at)}})
	}
	if _, err := f.bridge.Batch(ctx, commands); err != nil {
		t.Fatal(err)
	}
	ctx = f.ctx(t)
	f.pds.fail = true
	if _, err := f.bridge.Batch(ctx, retentionStatements("project", 1, time.Unix(10000000, 0))); err == nil {
		t.Fatal("PDS failure reported complete")
	}
	r, err := f.db.Execute(context.Background(), turso.Statement{SQL: "SELECT COUNT(*) FROM skill_runtime_events", WantRows: true})
	if err != nil || cell(r.Rows[0][0]) != "3" {
		t.Fatal("deleted telemetry before publication", err)
	}
	f.pds.fail = false
	intent, err := loadNative(context.Background(), f.db, "account", "project")
	if err != nil || intent == nil {
		t.Fatal(intent, err)
	}
	if err := f.bridge.runIntent(context.Background(), *intent); err != nil {
		t.Fatal(err)
	}
	r, err = f.db.Execute(context.Background(), turso.Statement{SQL: "SELECT id FROM skill_runtime_events ORDER BY id", WantRows: true})
	if err != nil || len(r.Rows) != 2 || cell(r.Rows[0][0]) != "fresh" || cell(r.Rows[1][0]) != "old-b" {
		t.Fatal("retention bound/selection wrong", r, err)
	}
}
