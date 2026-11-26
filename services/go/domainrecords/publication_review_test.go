package domainrecords

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/CountableNewt/my-context-protocol/services/go/internal/sqlitetest"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
)

func seedPrivateTelemetry(t *testing.T, db *sqlitetest.DB) {
	t.Helper()
	for _, sql := range []string{
		"INSERT INTO project_runtime_settings(id,project_id,telemetry_enabled) VALUES('settings','project',1)",
		"INSERT INTO request_logs(id,project_id,method,status) VALUES('log','project','tools/list','success')",
		"INSERT INTO skill_runtime_events(id,project_id,trace_id,event_type,detail_json) VALUES('event','project','trace','used','{}')",
		"INSERT INTO skill_usage_reports(id,project_id,client_identity,report_id,payload_hash,recorded_count) VALUES('usage','project','client','report','hash',1)",
		"INSERT INTO skill_feedback(id,project_id,skill_id,skill_version,source_path,category,summary,issue_draft_json) VALUES('feedback','project','skill','1','SKILL.md','quality','private feedback','{}')",
	} {
		if _, err := db.Execute(context.Background(), statement(sql)); err != nil {
			t.Fatal(err)
		}
	}
}
func publicationScalar(t *testing.T, db *sqlitetest.DB, sql string) string {
	t.Helper()
	r, err := db.Execute(context.Background(), statement(sql))
	if err != nil || len(r.Rows) != 1 {
		t.Fatalf("query failed: %v", err)
	}
	return fmt.Sprint(r.Rows[0][0].Value)
}
func TestMetadataOnlyPublicationPreservesPrivateHistoryAndVerifies(t *testing.T) {
	e, db, records := fixture(t)
	ctx := context.Background()
	seedPrivateTelemetry(t, db)
	full, err := e.Inventory(ctx, account, owner, "project")
	if err != nil || !full.HasTelemetry {
		t.Fatalf("full review lost telemetry: %v", err)
	}
	review, err := e.PublicationReview(ctx, account, owner, "project", false)
	if err != nil || review.HasTelemetry || review.Digest == full.Digest {
		t.Fatalf("metadata review invalid: %+v %v", review, err)
	}
	if !strings.Contains(strings.Join(review.Warnings, " "), "disables runtime telemetry") {
		t.Fatal("review did not explain disabled telemetry")
	}
	for _, entry := range review.Entries {
		d, _ := definition(entry.Table)
		if d.Telemetry {
			t.Fatalf("private history in review: %s", entry.Table)
		}
		if entry.Table == "project_runtime_settings" && fmt.Sprint(entry.Values["telemetry_enabled"].Value) != "0" {
			t.Fatal("review retained telemetry opt-in")
		}
	}
	if _, err = db.Execute(ctx, statement("INSERT INTO request_logs(id,project_id,method,status) VALUES('later','project','tools/call','success')")); err != nil {
		t.Fatal(err)
	}
	if _, err = e.Publish(ctx, account, review, Consent{ReviewDigest: review.Digest, Public: true}); err != nil {
		t.Fatal(err)
	}
	for uri, record := range records.values {
		for _, d := range definitions {
			if d.Telemetry && strings.Contains(uri, "/"+d.Collection+"/") {
				t.Fatalf("telemetry published: %s", uri)
			}
		}
		if strings.Contains(uri, "/"+Namespace+"runtimeSettings/") {
			var value map[string]any
			if json.Unmarshal(record.Value, &value) != nil || value["telemetry_enabled"] != false {
				t.Fatal("public telemetry enabled")
			}
		}
	}
	for table, want := range map[string]string{"request_logs": "2", "skill_runtime_events": "1", "skill_usage_reports": "1", "skill_feedback": "1"} {
		if got := publicationScalar(t, db, "SELECT COUNT(*) FROM "+table+" WHERE project_id='project'"); got != want {
			t.Fatalf("private %s history removed: %s", table, got)
		}
	}
	if publicationScalar(t, db, "SELECT telemetry_enabled FROM project_runtime_settings WHERE project_id='project'") != "0" || publicationScalar(t, db, "SELECT telemetry_consent FROM domain_canonical_projects WHERE project_id='project'") != "0" {
		t.Fatal("runtime telemetry was not disabled")
	}
	if _, err = e.Verify(ctx, account, owner, "project"); err != nil {
		t.Fatalf("private history broke verification: %v", err)
	}
	if _, err = e.Update(ctx, account, owner, "project", []Change{{Table: "projects", ID: "project", Values: map[string]turso.Value{"name": turso.Text("Updated")}}}); err != nil {
		t.Fatalf("canonical update failed: %v", err)
	}
	if _, err = e.Verify(ctx, account, owner, "project"); err != nil {
		t.Fatalf("updated metadata failed verification: %v", err)
	}
	if publicationScalar(t, db, "SELECT COUNT(*) FROM request_logs WHERE project_id='project'") != "2" {
		t.Fatal("canonical update pruned private history")
	}
}
func TestPublicationReviewModesRemainDigestBound(t *testing.T) {
	e, db, records := fixture(t)
	ctx := context.Background()
	seedPrivateTelemetry(t, db)
	full, err := e.PublicationReview(ctx, account, owner, "project", true)
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := e.PublicationReview(ctx, account, owner, "project", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.Publish(ctx, account, full, Consent{ReviewDigest: full.Digest, Public: true}); !errors.Is(err, ErrConsent) || records.puts != 0 {
		t.Fatalf("unconsented full review published: %v", err)
	}
	if _, err = e.Publish(ctx, account, metadata, Consent{ReviewDigest: full.Digest, Public: true}); !errors.Is(err, ErrConsent) || records.puts != 0 {
		t.Fatalf("mode digest ignored: %v", err)
	}
	if _, err = db.Execute(ctx, statement("UPDATE projects SET name='Changed' WHERE id='project'")); err != nil {
		t.Fatal(err)
	}
	if _, err = e.Publish(ctx, account, metadata, Consent{ReviewDigest: metadata.Digest, Public: true}); !errors.Is(err, ErrChanged) || records.puts != 0 {
		t.Fatalf("changed metadata published: %v", err)
	}
}
