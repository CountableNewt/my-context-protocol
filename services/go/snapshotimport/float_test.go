package snapshotimport

import (
	"context"
	"math"
	"math/rand"
	"testing"

	"github.com/CountableNewt/my-context-protocol/services/go/internal/sqlitetest"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
)

type roundedFloatDatabase struct {
	Database
	floats int
}

func (d *roundedFloatDatabase) distort(s turso.Statement) turso.Statement {
	s.Args = append([]turso.Value{}, s.Args...)
	for i, v := range s.Args {
		if v.Type == "float" {
			n, ok := v.Value.(float64)
			if ok {
				s.Args[i].Value = math.Nextafter(n, math.Inf(1))
				d.floats++
			}
		}
	}
	return s
}
func (d *roundedFloatDatabase) Execute(ctx context.Context, s turso.Statement) (*turso.Result, error) {
	return d.Database.Execute(ctx, d.distort(s))
}
func (d *roundedFloatDatabase) Batch(ctx context.Context, statements []turso.Statement) ([]turso.Result, error) {
	out := make([]turso.Statement, len(statements))
	for i, s := range statements {
		out[i] = d.distort(s)
	}
	return d.Database.Batch(ctx, out)
}

func TestExactFloatSQLPreservesIEEEBitsWithoutFloatParameters(t *testing.T) {
	db := &roundedFloatDatabase{Database: sqlitetest.New(t, "")}
	values := []float64{0, math.Copysign(0, -1), 1, -1, 1700000000.123456, math.MaxFloat64, -math.MaxFloat64, math.SmallestNonzeroFloat64, -math.SmallestNonzeroFloat64, math.Float64frombits(0x0010000000000000), math.Float64frombits(0x000fffffffffffff), math.Nextafter(1, 2), math.Nextafter(1, 0)}
	random := rand.New(rand.NewSource(7))
	for len(values) < 100 {
		n := math.Float64frombits(random.Uint64())
		if !math.IsNaN(n) && !math.IsInf(n, 0) {
			values = append(values, n)
		}
	}
	statements := []turso.Statement{}
	for _, n := range values {
		expression, args, e := exactFloatSQL(n)
		if e != nil {
			t.Fatal(e)
		}
		for _, arg := range args {
			if arg.Type != "integer" {
				t.Fatal("float JSON parameter escaped")
			}
		}
		statements = append(statements, turso.Statement{SQL: "SELECT " + expression, Args: args, WantRows: true})
	}
	results, e := db.Batch(context.Background(), statements)
	if e != nil {
		t.Fatal(e)
	}
	for i, r := range results {
		if len(r.Rows) != 1 || len(r.Rows[0]) != 1 || r.Rows[0][0].Type != "float" {
			t.Fatal("REAL expression changed type")
		}
		got := r.Rows[0][0].Value.(float64)
		if math.Float64bits(got) != math.Float64bits(values[i]) {
			t.Fatalf("REAL bits changed at fixture%d", i)
		}
	}
	if db.floats != 0 {
		t.Fatal("expression used float parameters")
	}
	// Prove fault injection is active for the unsafe control path.
	r, e := db.Execute(context.Background(), turso.Statement{SQL: "SELECT ?", Args: []turso.Value{{Type: "float", Value: values[4]}}, WantRows: true})
	if e != nil || r.Rows[0][0].Value == values[4] || db.floats != 1 {
		t.Fatal("float transport fault was not exercised")
	}
	for _, n := range []float64{math.Inf(1), math.Inf(-1), math.NaN()} {
		if _, _, e := exactFloatSQL(n); e == nil {
			t.Fatal("nonfinite accepted")
		}
	}
}

func TestSnapshotImportSurvivesRoundedFloatParameterTransport(t *testing.T) {
	_, artifact, _ := fixtureSnapshot(t)
	db := &roundedFloatDatabase{Database: sqlitetest.New(t, "")}
	report, e := Transfer(context.Background(), db, artifact, Options{Initialize: true, BatchRows: 5})
	if e != nil || !report.Verified {
		t.Fatal("exact REAL import failed", e)
	}
	if db.floats != 0 {
		t.Fatal("import bound JSON float values")
	}
	if _, e = Verify(context.Background(), db, artifact); e != nil {
		t.Fatal(e)
	}
}
