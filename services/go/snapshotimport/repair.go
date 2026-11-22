package snapshotimport

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"

	"github.com/CountableNewt/my-context-protocol/services/go/turso"
)

type FloatRepairOptions struct {
	Apply, WritersPaused bool
	ExpectedFloatCells   int
}
type FloatRepairReport struct {
	SnapshotID         string `json:"snapshot_id"`
	Status             string `json:"status"`
	TablesAudited      int    `json:"tables_audited"`
	RowsAudited        int64  `json:"rows_audited"`
	FloatCellsRepaired int    `json:"float_cells_repaired"`
	Verified           bool   `json:"verified"`
}
type floatRepair struct {
	table, column, currentLiteral string
	id                            turso.Value
	desired                       float64
}

// RepairStagedFloats is an explicit recovery operation for a fully loaded but
// unfinalized import. Every application row is audited before any mutation. Only
// one-ULP REAL changes demonstrably reproduced by the float JSON input transport
// are eligible; all other drift fails closed. Writers must remain paused.
func RepairStagedFloats(ctx context.Context, db Database, path string, options FloatRepairOptions) (*FloatRepairReport, error) {
	if !options.Apply || !options.WritersPaused || options.ExpectedFloatCells < 1 || options.ExpectedFloatCells > 32 {
		return nil, errors.New("staged float repair requires explicit apply, paused writers and expected cell count 1..32")
	}
	if db == nil {
		return nil, errors.New("destination database is required")
	}
	snapshot, e := Read(path)
	if e != nil {
		return nil, e
	}
	if e = repairCheckpoint(ctx, db, snapshot); e != nil {
		return nil, e
	}
	plan := []floatRepair{}
	rows := int64(0)
	for _, table := range snapshot.Tables {
		found, e := auditFloatTable(ctx, db, table)
		if e != nil {
			return nil, e
		}
		plan = append(plan, found...)
		rows += table.Count
		if len(plan) > 32 {
			return nil, errors.New("staged float repair exceeds bounded cell limit")
		}
	}
	if len(plan) != options.ExpectedFloatCells {
		return nil, errors.New("staged float repair count differs from explicit expected count")
	}
	// Revalidate after the full audit. The atomic update predicates repeat these
	// marker/progress checks and compare each exact observed SQLite REAL literal.
	if e = repairCheckpoint(ctx, db, snapshot); e != nil {
		return nil, e
	}
	progressSQL, progressArgs := completeProgressPredicate(snapshot)
	statements := []turso.Statement{}
	for _, cell := range plan {
		expression, args, e := exactFloatSQL(cell.desired)
		if e != nil {
			return nil, e
		}
		args = append(args, cell.id, turso.Text(cell.currentLiteral), turso.Text(snapshot.ID))
		args = append(args, progressArgs...)
		predicate := "EXISTS(SELECT 1 FROM " + runTable + " WHERE id=1 AND snapshot_id=? AND status='loading') AND " + progressSQL
		statements = append(statements, turso.Statement{SQL: "UPDATE " + quoted(cell.table) + " SET " + quoted(cell.column) + "=" + expression + " WHERE id=? AND typeof(" + quoted(cell.column) + ")='real' AND printf('%!.26g'," + quoted(cell.column) + ")=? AND " + predicate, Args: args})
		// CHECK(status IN ('loading','verified')) forces the whole Batch to roll
		// back if the preceding CAS affected anything other than exactly one row.
		// If the marker itself vanished, every CAS is ineligible and no application
		// update can occur; result counts below still report failure.
		statements = append(statements, turso.Statement{SQL: "UPDATE " + runTable + " SET status=CASE WHEN changes()=1 THEN status ELSE 'float_repair_conflict' END WHERE id=1"})
	}
	result, e := db.Batch(ctx, statements)
	if e != nil {
		return nil, errors.New("staged float repair transaction failed or response was lost; keep writers paused and verify before retry")
	}
	if len(result) != len(statements) {
		return nil, errors.New("staged float repair transaction result incomplete; verify target before retry")
	}
	for _, item := range result {
		if item.AffectedRowCount != 1 {
			return nil, errors.New("staged float repair guard did not match; verify target before retry")
		}
	}
	verified, e := verifySnapshot(ctx, db, snapshot)
	if e != nil {
		return nil, errors.New("strict post-repair verification failed; keep target unrouted and writers paused")
	}
	if verified.Status != "loading" {
		return nil, errors.New("staged marker changed during repair; keep target unrouted")
	}
	return &FloatRepairReport{snapshot.ID, "loading", len(snapshot.Tables), rows, len(plan), true}, nil
}
func repairCheckpoint(ctx context.Context, db Database, snapshot *Snapshot) error {
	schema, e := destinationSchema(ctx, db)
	if e != nil {
		return errors.New("cannot inspect staged repair schema")
	}
	if e = checkImportSchema(schema); e != nil {
		return e
	}
	status, e := matchingRun(ctx, db, snapshot.ID)
	if e != nil {
		return e
	}
	if status != "loading" {
		return errors.New("only an owned loading checkpoint may be repaired")
	}
	progress, e := readProgress(ctx, db, snapshot)
	if e != nil {
		return e
	}
	for _, table := range snapshot.Tables {
		if progress[table.Name] != table.Count {
			return errors.New("staged float repair requires every table fully loaded")
		}
	}
	return nil
}
func completeProgressPredicate(snapshot *Snapshot) (string, []turso.Value) {
	clauses := []string{}
	args := []turso.Value{turso.Integer(int64(len(snapshot.Tables)))}
	for _, table := range snapshot.Tables {
		clauses = append(clauses, "(table_name=? AND next_row=? AND expected_rows=? AND expected_digest=?)")
		args = append(args, turso.Text(table.Name), turso.Integer(table.Count), turso.Integer(table.Count), turso.Text(table.Digest))
	}
	args = append(args, turso.Integer(int64(len(snapshot.Tables))))
	return "(SELECT COUNT(*) FROM " + progressTable + ")=? AND (SELECT COUNT(*) FROM " + progressTable + " WHERE " + strings.Join(clauses, " OR ") + ")=?", args
}
func auditFloatTable(ctx context.Context, db Database, table *Table) ([]floatRepair, error) {
	count, e := rowCount(ctx, db, table.Name)
	if e != nil || count != table.Count {
		return nil, errors.New("staged repair table count mismatch")
	}
	names := make([]string, len(table.Columns))
	key := -1
	for i, name := range table.Columns {
		names[i] = quoted(name)
		if name == "id" {
			key = i
		}
	}
	if key < 0 {
		return nil, errors.New("staged repair requires original primary identity")
	}
	expected := map[string][]turso.Value{}
	for _, row := range table.Data {
		id, _ := json.Marshal(row[key])
		if expected[string(id)] != nil {
			return nil, errors.New("source identity is not unique")
		}
		expected[string(id)] = row
	}
	found := []floatRepair{}
	offset, last := int64(0), int64(0)
	for offset < count {
		page, size := int64(0), 1024
		for offset+page < count && page < 120 {
			encoded, _ := json.Marshal(table.Data[offset+page])
			if page > 0 && size+len(encoded) > chunkBytes {
				break
			}
			size += len(encoded)
			page++
		}
		filter := ""
		args := []turso.Value{turso.Integer(page)}
		if offset > 0 {
			filter = " WHERE rowid>?"
			args = []turso.Value{turso.Integer(last), turso.Integer(page)}
		}
		result, e := db.Execute(ctx, turso.Statement{SQL: "SELECT rowid," + strings.Join(names, ",") + " FROM " + quoted(table.Name) + filter + " ORDER BY rowid LIMIT ?", Args: args, WantRows: true})
		if e != nil || len(result.Rows) != int(page) {
			return nil, errors.New("staged repair audit page incomplete")
		}
		for _, raw := range result.Rows {
			if len(raw) != len(names)+1 {
				return nil, errors.New("staged repair audit column mismatch")
			}
			id, e := integer(raw[0])
			if e != nil || offset > 0 && id <= last {
				return nil, errors.New("staged repair row ordering invalid")
			}
			last = id
			offset++
			row := raw[1:]
			encodedID, _ := json.Marshal(row[key])
			want := expected[string(encodedID)]
			if want == nil {
				return nil, errors.New("staged repair identity mismatch")
			}
			delete(expected, string(encodedID))
			for i, actual := range row {
				encoded, _ := json.Marshal(actual)
				original, _ := json.Marshal(want[i])
				if string(encoded) == string(original) {
					continue
				}
				a, aok := actual.Value.(float64)
				b, bok := want[i].Value.(float64)
				if actual.Type != "float" || want[i].Type != "float" || !aok || !bok || math.IsInf(a, 0) || math.IsNaN(a) {
					return nil, errors.New("staged repair refuses non-REAL or non-finite drift")
				}
				ab, bb := math.Float64bits(a), math.Float64bits(b)
				distance := ab - bb
				if bb > ab {
					distance = bb - ab
				}
				if distance != 1 {
					return nil, errors.New("staged repair refuses changes other than exactly one ULP")
				}
				cell := floatRepair{table: table.Name, column: table.Columns[i], id: want[key], desired: b}
				if e = proveFloatTransport(ctx, db, &cell, a); e != nil {
					return nil, e
				}
				found = append(found, cell)
			}
		}
	}
	if len(expected) != 0 {
		return nil, errors.New("staged repair source identities incomplete")
	}
	return found, nil
}
func proveFloatTransport(ctx context.Context, db Database, cell *floatRepair, actual float64) error {
	expression, args, e := exactFloatSQL(cell.desired)
	if e != nil {
		return e
	}
	args = append([]turso.Value{{Type: "float", Value: cell.desired}}, args...)
	args = append(args, cell.id)
	result, e := db.Execute(ctx, turso.Statement{SQL: "SELECT printf('%!.26g'," + quoted(cell.column) + "),printf('%!.26g',?),printf('%!.26g'," + expression + "),typeof(" + quoted(cell.column) + ") FROM " + quoted(cell.table) + " WHERE id=?", Args: args, WantRows: true})
	if e != nil || len(result.Rows) != 1 || len(result.Rows[0]) != 4 {
		return errors.New("cannot prove staged float transport mismatch")
	}
	row := result.Rows[0]
	current, cok := row[0].Value.(string)
	bound, bok := row[1].Value.(string)
	exact, eok := row[2].Value.(string)
	if !cok || !bok || !eok || row[3].Value != "real" || current != bound {
		return errors.New("staged float change is not reproduced by input transport")
	}
	parsed, e := strconv.ParseFloat(current, 64)
	fixed, fe := strconv.ParseFloat(exact, 64)
	if e != nil || fe != nil || math.Float64bits(parsed) != math.Float64bits(actual) || math.Float64bits(fixed) != math.Float64bits(cell.desired) {
		return errors.New("staged float storage or exact encoding proof failed")
	}
	cell.currentLiteral = current
	return nil
}
