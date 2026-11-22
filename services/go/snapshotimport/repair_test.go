package snapshotimport

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CountableNewt/my-context-protocol/services/go/internal/sqlitetest"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
)

// Emulate the proven JSON input parser fault, independently of the exact SQL
// expression path. The real SQLite integration still executes every statement.
type driftFloatTransport struct {
	Database
	reproduce     bool
	repairBatches int
	beforeRepair  func()
}

func (d *driftFloatTransport) Execute(ctx context.Context, s turso.Statement) (*turso.Result, error) {
	if d.reproduce && strings.HasPrefix(s.SQL, "SELECT printf('%!.26g',") {
		s.Args = append([]turso.Value{}, s.Args...)
		for i, v := range s.Args {
			if v.Type == "float" {
				v.Value = math.Nextafter(v.Value.(float64), math.Inf(1))
				s.Args[i] = v
			}
		}
	}
	return d.Database.Execute(ctx, s)
}
func (d *driftFloatTransport) Batch(ctx context.Context, s []turso.Statement) ([]turso.Result, error) {
	d.repairBatches++
	if d.beforeRepair != nil {
		d.beforeRepair()
		d.beforeRepair = nil
	}
	return d.Database.Batch(ctx, s)
}

const sourceAccountTime = 1700000000.123456
const sourceProjectTime = 1700000001.987654

func repairFixture(t *testing.T) (string, *driftFloatTransport) {
	t.Helper()
	_, artifact, _ := fixtureSnapshot(t)
	db := sqlitetest.New(t, "")
	if _, e := Transfer(context.Background(), db, artifact, Options{Initialize: true}); e != nil {
		t.Fatal(e)
	}
	if _, e := db.Batch(context.Background(), []turso.Statement{{SQL: "UPDATE go_snapshot_import SET status='loading'"}, {SQL: "UPDATE accounts SET created_at=?", Args: []turso.Value{{Type: "float", Value: math.Nextafter(sourceAccountTime, math.Inf(1))}}}, {SQL: "UPDATE projects SET created_at=?", Args: []turso.Value{{Type: "float", Value: math.Nextafter(sourceProjectTime, math.Inf(1))}}}}); e != nil {
		t.Fatal(e)
	}
	return artifact, &driftFloatTransport{Database: db, reproduce: true}
}
func repairOptions() FloatRepairOptions {
	return FloatRepairOptions{Apply: true, WritersPaused: true, ExpectedFloatCells: 2}
}
func TestStagedFloatRepairRestoresExactBitsAndLeavesLoadingUntilResume(t *testing.T) {
	artifact, db := repairFixture(t)
	report, e := RepairStagedFloats(context.Background(), db, artifact, repairOptions())
	if e != nil {
		t.Fatal(e)
	}
	if !report.Verified || report.Status != "loading" || report.FloatCellsRepaired != 2 || report.TablesAudited != 30 || db.repairBatches != 1 {
		t.Fatal("repair report or transaction count invalid")
	}
	for _, tc := range []struct {
		table string
		want  float64
	}{{"accounts", sourceAccountTime}, {"projects", sourceProjectTime}} {
		got := scalar(t, db.Database, "SELECT created_at FROM "+tc.table).Value.(float64)
		if math.Float64bits(got) != math.Float64bits(tc.want) {
			t.Fatal("exact source bits not restored")
		}
	}
	if _, e = Verify(context.Background(), db.Database, artifact); e != nil {
		t.Fatal(e)
	}
	if _, e = Transfer(context.Background(), db.Database, artifact, Options{Resume: true}); e != nil {
		t.Fatal(e)
	}
}
func TestStagedFloatRepairRequiresExplicitBoundedOptIn(t *testing.T) {
	for _, options := range []FloatRepairOptions{{}, {Apply: true, ExpectedFloatCells: 2}, {Apply: true, WritersPaused: true}, {Apply: true, WritersPaused: true, ExpectedFloatCells: 33}} {
		if _, e := RepairStagedFloats(context.Background(), noAccessDB{t}, "missing", options); e == nil {
			t.Fatal("unsafe opt-in accepted")
		}
	}
}
func TestStagedFloatRepairRejectsAnyUnownedOrNonTransportDriftBeforeWrite(t *testing.T) {
	for _, name := range []string{"nonfloat", "type", "two-ulp", "verified", "incomplete", "artifact", "unproven", "wrong-count", "schema"} {
		t.Run(name, func(t *testing.T) {
			artifact, db := repairFixture(t)
			options := repairOptions()
			sql := ""
			switch name {
			case "nonfloat":
				sql = "UPDATE projects SET name='changed user data'"
			case "type":
				sql = "UPDATE accounts SET created_at='unexpected text'"
			case "two-ulp":
				value := math.Nextafter(math.Nextafter(sourceAccountTime, math.Inf(1)), math.Inf(1))
				if _, e := db.Database.Execute(context.Background(), turso.Statement{SQL: "UPDATE accounts SET created_at=?", Args: []turso.Value{{Type: "float", Value: value}}}); e != nil {
					t.Fatal(e)
				}
			case "verified":
				sql = "UPDATE go_snapshot_import SET status='verified'"
			case "incomplete":
				sql = "UPDATE go_snapshot_import_progress SET next_row=0 WHERE table_name='accounts'"
			case "artifact":
				raw, e := os.ReadFile(artifact)
				if e != nil {
					t.Fatal(e)
				}
				raw = []byte(strings.Replace(string(raw), "source-owner", "different-owner", 1))
				artifact = filepath.Join(t.TempDir(), "different.jsonl")
				if e = os.WriteFile(artifact, raw, 0600); e != nil {
					t.Fatal(e)
				}
			case "unproven":
				db.reproduce = false
			case "wrong-count":
				options.ExpectedFloatCells = 1
			case "schema":
				sql = "CREATE TRIGGER unowned BEFORE UPDATE ON accounts BEGIN SELECT 1; END"
			}
			if sql != "" {
				if _, e := db.Database.Execute(context.Background(), turso.Statement{SQL: sql}); e != nil {
					t.Fatal(e)
				}
			}
			if _, e := RepairStagedFloats(context.Background(), db, artifact, options); e == nil {
				t.Fatal("unsafe staged repair accepted")
			}
			if db.repairBatches != 0 {
				t.Fatal("repair wrote before completing all-table audit")
			}
		})
	}
}
func TestStagedFloatRepairCASConflictRollsBackEveryPriorCell(t *testing.T) {
	for _, name := range []string{"float", "marker", "progress"} {
		t.Run(name, func(t *testing.T) {
			artifact, db := repairFixture(t)
			db.beforeRepair = func() {
				var s turso.Statement
				switch name {
				case "float":
					s = turso.Statement{SQL: "UPDATE projects SET created_at=?", Args: []turso.Value{{Type: "float", Value: math.Nextafter(math.Nextafter(sourceProjectTime, math.Inf(1)), math.Inf(1))}}}
				case "marker":
					s = turso.Statement{SQL: "UPDATE go_snapshot_import SET status='verified'"}
				case "progress":
					s = turso.Statement{SQL: "UPDATE go_snapshot_import_progress SET next_row=0 WHERE table_name='projects'"}
				}
				if _, e := db.Database.Execute(context.Background(), s); e != nil {
					t.Fatal(e)
				}
			}
			if _, e := RepairStagedFloats(context.Background(), db, artifact, repairOptions()); e == nil {
				t.Fatal("stale repair committed")
			}
			got := scalar(t, db.Database, "SELECT created_at FROM accounts").Value.(float64)
			if math.Float64bits(got) != math.Float64bits(math.Nextafter(sourceAccountTime, math.Inf(1))) {
				t.Fatal("earlier repair escaped transaction rollback")
			}
		})
	}
}
