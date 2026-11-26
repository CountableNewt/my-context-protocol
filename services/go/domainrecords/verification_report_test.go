package domainrecords

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/CountableNewt/my-context-protocol/services/go/atproto"
)

func publishedVerification(t *testing.T) (Engine, *memoryRecords) {
	t.Helper()
	e, db, pds := fixture(t)
	review, err := e.Inventory(context.Background(), account, owner, "project")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.Publish(context.Background(), account, review, approved(review)); err != nil {
		t.Fatal(err)
	}
	e.DB = readOnlyAuditDB{db}
	return e, pds
}
func TestVerifyReportCountsValidatedRecordsWithoutWrites(t *testing.T) {
	e, pds := publishedVerification(t)
	puts := pds.puts
	report, err := e.VerifyReport(context.Background(), account, owner, "project")
	if err != nil || report.Status != "verified" || report.CheckedRecords != len(pds.values) || pds.puts != puts {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	if len(report.Issues) != 0 || len(report.Collections) != 4 {
		t.Fatalf("report=%+v", report)
	}
	total := 0
	for _, collection := range report.Collections {
		total += collection.CheckedRecords
	}
	if total != report.CheckedRecords {
		t.Fatal("collection counts disagree")
	}
	if _, err = e.VerifyReport(context.Background(), "other", owner, "project"); err == nil {
		t.Fatal("foreign account verified")
	}
	if _, err = e.VerifyReport(context.Background(), account, owner, "other-project"); err == nil {
		t.Fatal("foreign project verified")
	}
}
func TestVerifyReportDistinguishesUnpublishedAndMissingManifest(t *testing.T) {
	e, db, _ := fixture(t)
	e.DB = readOnlyAuditDB{db}
	report, err := e.VerifyReport(context.Background(), account, owner, "project")
	if err != nil || report.Status != "not_published" || report.CheckedRecords != 0 || len(report.Issues) != 1 {
		t.Fatalf("%+v %v", report, err)
	}
	e, pds := publishedVerification(t)
	for uri := range pds.values {
		if strings.Contains(uri, "/"+Namespace+"manifest/") {
			delete(pds.values, uri)
		}
	}
	report, err = e.VerifyReport(context.Background(), account, owner, "project")
	if err != nil || report.Status != "missing" || report.CheckedRecords != 0 {
		t.Fatalf("%+v %v", report, err)
	}
}
func TestVerifyReportReportsMissingAndMismatchedEntity(t *testing.T) {
	for _, mode := range []string{"missing", "wrong-cid", "invalid-json"} {
		t.Run(mode, func(t *testing.T) {
			e, pds := publishedVerification(t)
			var target atproto.Record
			for uri, record := range pds.values {
				if strings.Contains(uri, "/"+Namespace+"repository/") {
					target = record
					break
				}
			}
			if target.URI == "" {
				t.Fatal("fixture missing entity")
			}
			switch mode {
			case "missing":
				delete(pds.values, target.URI)
			case "wrong-cid":
				changed := target
				changed.CID = "different-cid"
				pds.values[target.URI] = changed
			case "invalid-json":
				changed := target
				changed.Value = json.RawMessage(`{`)
				pds.values[target.URI] = changed
			}
			report, err := e.VerifyReport(context.Background(), account, owner, "project")
			want := "invalid"
			if mode == "missing" {
				want = "missing"
			}
			if err != nil || report.Status != want || len(report.Issues) != 1 || report.CheckedRecords >= len(pds.values)+1 {
				t.Fatalf("%+v %v", report, err)
			}
			issue := report.Issues[0]
			if issue.URI != target.URI || issue.ExpectedCID != target.CID {
				t.Fatalf("wrong issue %+v", issue)
			}
			if mode == "wrong-cid" && issue.ActualCID != "different-cid" {
				t.Fatal("actual CID omitted")
			}
		})
	}
}

type failingVerificationRecords struct {
	atproto.Records
	err error
}

func (r failingVerificationRecords) Get(context.Context, atproto.Mutation) (atproto.Record, error) {
	return atproto.Record{}, r.err
}
func TestVerifyReportTransportFailureIsNotMissingAndDeadlineIsIncomplete(t *testing.T) {
	for _, check := range []struct {
		err    error
		status string
	}{{errors.New("private credential must not appear"), "unavailable"}, {context.DeadlineExceeded, "incomplete"}, {context.Canceled, "incomplete"}} {
		e, _ := publishedVerification(t)
		e.Records = failingVerificationRecords{Records: e.Records, err: check.err}
		report, err := e.VerifyReport(context.Background(), account, owner, "project")
		if err != nil || report.Status != check.status || report.CheckedRecords != 0 || len(report.Issues) != 1 {
			t.Fatalf("%+v %v", report, err)
		}
		raw, _ := json.Marshal(report)
		if strings.Contains(string(raw), "private credential") || strings.Contains(string(raw), `"collections":null`) || strings.Contains(string(raw), `"issues":null`) {
			t.Fatalf("unsafe report %s", raw)
		}
	}
}
func TestVerifyReportLocalDriftIsNotRemoteCorruption(t *testing.T) {
	e, db, pds := fixture(t)
	review, err := e.Inventory(context.Background(), account, owner, "project")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.Publish(context.Background(), account, review, approved(review)); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Execute(context.Background(), statement("UPDATE projects SET name='local drift' WHERE id='project'")); err != nil {
		t.Fatal(err)
	}
	e.DB = readOnlyAuditDB{db}
	report, err := e.VerifyReport(context.Background(), account, owner, "project")
	if err != nil || report.Status != "out_of_sync" || report.CheckedRecords != len(pds.values) {
		t.Fatalf("%+v %v", report, err)
	}
}

type switchedVerificationManifest struct {
	atproto.Records
	reads int
}

func (r *switchedVerificationManifest) Get(ctx context.Context, m atproto.Mutation) (atproto.Record, error) {
	record, err := r.Records.Get(ctx, m)
	if m.Collection == Namespace+"manifest" {
		r.reads++
		if r.reads > 1 {
			record.CID = "concurrent-manifest"
		}
	}
	return record, err
}
func TestVerifyReportConcurrentManifestChangeIsIncomplete(t *testing.T) {
	e, _ := publishedVerification(t)
	e.Records = &switchedVerificationManifest{Records: e.Records}
	report, err := e.VerifyReport(context.Background(), account, owner, "project")
	if err != nil || report.Status != "incomplete" || len(report.Issues) != 1 || report.Issues[0].Code != "ManifestChanged" {
		t.Fatalf("%+v %v", report, err)
	}
}
