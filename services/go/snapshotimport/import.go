package snapshotimport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/CountableNewt/my-context-protocol/services/go/migrations"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
)

type Database interface {
	Execute(context.Context, turso.Statement) (*turso.Result, error)
	Batch(context.Context, []turso.Statement) ([]turso.Result, error)
}
type Options struct {
	Initialize, Resume bool
	BatchRows          int
}
type Report struct {
	SnapshotID string   `json:"snapshot_id"`
	Status     string   `json:"status"`
	Verified   bool     `json:"verified"`
	Tables     []*Table `json:"tables"`
}

const runTable = "go_snapshot_import"
const progressTable = "go_snapshot_import_progress"
const runDDL = `CREATE TABLE go_snapshot_import(id INTEGER PRIMARY KEY CHECK(id=1),snapshot_id TEXT NOT NULL,status TEXT NOT NULL CHECK(status IN ('loading','verified')),updated_at REAL NOT NULL)`
const progressDDL = `CREATE TABLE go_snapshot_import_progress(table_name TEXT PRIMARY KEY,next_row INTEGER NOT NULL CHECK(next_row>=0 AND next_row<=expected_rows),expected_rows INTEGER NOT NULL,expected_digest TEXT NOT NULL)`

// Transfer only accepts a new empty destination, or an explicit resume of this
// exact snapshot with already-staged data matching every recorded prefix. The
// destination must remain unrouted and have no other writer until verification.
func Transfer(ctx context.Context, db Database, path string, options Options) (*Report, error) {
	if db == nil {
		return nil, errors.New("destination database is required")
	}
	if options.Initialize == options.Resume {
		return nil, errors.New("choose exactly one of initialize or resume")
	}
	snapshot, e := Read(path)
	if e != nil {
		return nil, e
	}
	tables, e := orderedTables(snapshot)
	if e != nil {
		return nil, e
	}
	if len(snapshot.Tables)+3 > turso.MaxStatements {
		return nil, errors.New("snapshot checkpoint exceeds transaction limit")
	}
	schema, e := destinationSchema(ctx, db)
	if e != nil {
		return nil, e
	}
	if len(schema) == 0 {
		if !options.Initialize {
			return nil, errors.New("empty destination requires explicit initialize")
		}
		if e = migrations.Apply(ctx, db, migrations.Options{Initialize: true}); e != nil {
			return nil, e
		}
		if e = createProgress(ctx, db, snapshot); e != nil {
			return nil, fmt.Errorf("snapshot checkpoint setup failed; resume this empty staged target: %w", e)
		}
	} else if _, hasRun := schema[runTable]; !hasRun {
		if !options.Resume {
			return nil, errors.New("destination is not empty; initialize never overwrites an existing database")
		}
		// Recovery is permitted only for the exact, still-empty baseline left when a
		// first attempt stopped after initialization but before checkpoint creation.
		expected, _, e := baseline()
		if e != nil {
			return nil, e
		}
		if len(schema) != len(expected)+1 {
			return nil, errors.New("destination is not an empty initialized staging database")
		}
		if e = checkBaselineSubset(schema); e != nil {
			return nil, e
		}
		if _, ok := schema["go_schema_migrations"]; !ok {
			return nil, errors.New("destination has no owned migration registry")
		}
		if e = migrations.Apply(ctx, db, migrations.Options{}); e != nil {
			return nil, e
		}
		for _, table := range snapshot.Tables {
			count, e := rowCount(ctx, db, table.Name)
			if e != nil {
				return nil, e
			}
			if count != 0 {
				return nil, errors.New("destination contains data without a matching snapshot checkpoint")
			}
		}
		if e = createProgress(ctx, db, snapshot); e != nil {
			return nil, e
		}
	} else if !options.Resume {
		return nil, errors.New("staged snapshot exists; use resume or verify, never initialize")
	}
	schema, e = destinationSchema(ctx, db)
	if e != nil {
		return nil, e
	}
	if e = checkImportSchema(schema); e != nil {
		return nil, e
	}
	status, e := matchingRun(ctx, db, snapshot.ID)
	if e != nil {
		return nil, e
	}
	progress, e := readProgress(ctx, db, snapshot)
	if e != nil {
		return nil, e
	}
	if status == "verified" {
		return verifySnapshot(ctx, db, snapshot)
	}
	// Verify all existing rows before resuming. A checkpoint alone never permits
	// overwriting, ignoring or adopting unrelated application data.
	for _, table := range snapshot.Tables {
		if e = verifyTable(ctx, db, table, progress[table.Name]); e != nil {
			return nil, fmt.Errorf("staged prefix verification failed for %s: %w", table.Name, e)
		}
	}
	batchRows := options.BatchRows
	if batchRows < 1 {
		batchRows = 100
	}
	if batchRows > 120 {
		batchRows = 120
	}
	for _, table := range tables {
		offset := progress[table.Name]
		for offset < int64(len(table.Data)) {
			end := offset
			statements := []turso.Statement{}
			encodedBytes := 1024
			for end < int64(len(table.Data)) && len(statements) < batchRows {
				statement, err := insert(table, table.Data[end])
				if err != nil {
					return nil, err
				}
				raw, _ := json.Marshal(statement)
				if len(raw) > MaxRowBytes+16384 {
					return nil, errors.New("snapshot row exceeds transport statement limit")
				}
				if len(statements) > 0 && encodedBytes+len(raw)+128 > chunkBytes {
					break
				}
				statements = append(statements, statement)
				encodedBytes += len(raw) + 128
				end++
			}
			statements = append(statements, turso.Statement{SQL: "UPDATE " + progressTable + " SET next_row=CASE WHEN next_row=? THEN ? ELSE -1 END WHERE table_name=?", Args: []turso.Value{turso.Integer(offset), turso.Integer(end), turso.Text(table.Name)}})
			results, e := db.Batch(ctx, statements)
			if e != nil {
				return nil, fmt.Errorf("table %s import transaction failed; resume the same snapshot: %w", table.Name, e)
			}
			if len(results) != len(statements) || results[len(results)-1].AffectedRowCount != 1 {
				return nil, errors.New("snapshot checkpoint was not advanced; verification is required")
			}
			offset = end
		}
	}
	report, e := verifySnapshot(ctx, db, snapshot)
	if e != nil {
		return nil, e
	}
	final, e := db.Execute(ctx, turso.Statement{SQL: "UPDATE " + runTable + " SET status='verified',updated_at=? WHERE id=1 AND snapshot_id=? AND status='loading' AND NOT EXISTS(SELECT 1 FROM " + progressTable + " WHERE next_row<>expected_rows)", Args: []turso.Value{turso.Integer(time.Now().Unix()), turso.Text(snapshot.ID)}})
	if e != nil {
		return nil, fmt.Errorf("data verification passed but final checkpoint failed; resume to finalize: %w", e)
	}
	if final.AffectedRowCount != 1 {
		return nil, errors.New("snapshot was not finalized")
	}
	report.Status = "verified"
	return report, nil
}

// Verify performs read-only count/digest/FK checks. If data is complete but the
// checkpoint still says loading, use resume to atomically finalize that marker.
func Verify(ctx context.Context, db Database, path string) (*Report, error) {
	snapshot, e := Read(path)
	if e != nil {
		return nil, e
	}
	return verifySnapshot(ctx, db, snapshot)
}
func verifySnapshot(ctx context.Context, db Database, snapshot *Snapshot) (*Report, error) {
	schema, e := destinationSchema(ctx, db)
	if e != nil {
		return nil, e
	}
	if e = checkImportSchema(schema); e != nil {
		return nil, e
	}
	status, e := matchingRun(ctx, db, snapshot.ID)
	if e != nil {
		return nil, e
	}
	progress, e := readProgress(ctx, db, snapshot)
	if e != nil {
		return nil, e
	}
	for _, table := range snapshot.Tables {
		if progress[table.Name] != table.Count {
			return nil, errors.New("snapshot import is incomplete")
		}
		if e = verifyTable(ctx, db, table, table.Count); e != nil {
			return nil, fmt.Errorf("table %s verification failed: %w", table.Name, e)
		}
	}
	fk, e := db.Execute(ctx, turso.Statement{SQL: "PRAGMA foreign_key_check", WantRows: true})
	if e != nil {
		return nil, e
	}
	if len(fk.Rows) != 0 {
		return nil, errors.New("destination foreign-key verification failed")
	}
	return &Report{snapshot.ID, status, true, snapshot.Tables}, nil
}
func createProgress(ctx context.Context, db Database, snapshot *Snapshot) error {
	statements := []turso.Statement{{SQL: runDDL}, {SQL: progressDDL}, {SQL: "INSERT INTO " + runTable + "(id,snapshot_id,status,updated_at) VALUES(1,?,'loading',?)", Args: []turso.Value{turso.Text(snapshot.ID), turso.Integer(time.Now().Unix())}}}
	for _, table := range snapshot.Tables {
		statements = append(statements, turso.Statement{SQL: "INSERT INTO " + progressTable + "(table_name,next_row,expected_rows,expected_digest) VALUES(?,0,?,?)", Args: []turso.Value{turso.Text(table.Name), turso.Integer(table.Count), turso.Text(table.Digest)}})
	}
	_, e := db.Batch(ctx, statements)
	return e
}
func insert(table *Table, row []turso.Value) (turso.Statement, error) {
	names := make([]string, len(table.Columns))
	marks := make([]string, len(names))
	args := []turso.Value{}
	for i, name := range table.Columns {
		names[i] = quoted(name)
		marks[i] = "?"
		if row[i].Type == "float" {
			value, ok := row[i].Value.(float64)
			if !ok {
				return turso.Statement{}, errors.New("invalid SQLite REAL argument")
			}
			expression, parameters, err := exactFloatSQL(value)
			if err != nil {
				return turso.Statement{}, err
			}
			marks[i] = expression
			args = append(args, parameters...)
		} else {
			args = append(args, row[i])
		}
	}
	return turso.Statement{SQL: "INSERT INTO " + quoted(table.Name) + "(" + strings.Join(names, ",") + ") VALUES(" + strings.Join(marks, ",") + ")", Args: args}, nil
}
func destinationSchema(ctx context.Context, db Database) (map[string]string, error) {
	r, e := db.Execute(ctx, turso.Statement{SQL: "SELECT name,sql FROM sqlite_schema WHERE type IN ('table','index','view','trigger') AND name NOT LIKE 'sqlite_%'", WantRows: true})
	if e != nil {
		return nil, e
	}
	schema := map[string]string{}
	for _, row := range r.Rows {
		if len(row) != 2 {
			return nil, errors.New("invalid destination schema result")
		}
		name, ok := row[0].Value.(string)
		if !ok {
			return nil, errors.New("invalid destination schema name")
		}
		sql, ok := row[1].Value.(string)
		if !ok {
			return nil, errors.New("invalid destination schema definition")
		}
		if _, exists := schema[name]; exists {
			return nil, errors.New("destination contains colliding schema object names")
		}
		schema[name] = sql
	}
	return schema, nil
}
func checkBaselineSubset(actual map[string]string) error {
	expected, _, e := baseline()
	if e != nil {
		return e
	}
	for name, sql := range expected {
		if normalized(actual[name]) != sql {
			return errors.New("destination baseline schema mismatch")
		}
	}
	return nil
}
func checkOwnedSchema(schema map[string]string) error {
	if e := checkBaselineSubset(schema); e != nil {
		return e
	}
	expected, e := ownedSchema()
	if e != nil {
		return e
	}
	for name, actual := range schema {
		ddl, ok := expected[name]
		if !ok || normalizedCreate(actual) != normalizedCreate(ddl) {
			return errors.New("destination contains an unknown or altered schema object")
		}
	}
	return nil
}
func checkImportSchema(schema map[string]string) error {
	if e := checkOwnedSchema(schema); e != nil {
		return e
	}
	for _, required := range []string{"go_schema_migrations", runTable, progressTable} {
		if _, ok := schema[required]; !ok {
			return errors.New("snapshot checkpoint schema is missing")
		}
	}
	return nil
}

func matchingRun(ctx context.Context, db Database, id string) (string, error) {
	r, e := db.Execute(ctx, turso.Statement{SQL: "SELECT snapshot_id,status FROM " + runTable + " WHERE id=1", WantRows: true})
	if e != nil {
		return "", e
	}
	if len(r.Rows) != 1 || len(r.Rows[0]) != 2 || r.Rows[0][0].Value != id {
		return "", errors.New("destination belongs to a different or missing snapshot checkpoint")
	}
	status, ok := r.Rows[0][1].Value.(string)
	if !ok || (status != "loading" && status != "verified") {
		return "", errors.New("invalid snapshot checkpoint status")
	}
	return status, nil
}
func readProgress(ctx context.Context, db Database, snapshot *Snapshot) (map[string]int64, error) {
	r, e := db.Execute(ctx, turso.Statement{SQL: "SELECT table_name,next_row,expected_rows,expected_digest FROM " + progressTable, WantRows: true})
	if e != nil {
		return nil, e
	}
	if len(r.Rows) != len(snapshot.Tables) {
		return nil, errors.New("snapshot checkpoint table set mismatch")
	}
	expected := map[string]*Table{}
	for _, table := range snapshot.Tables {
		expected[table.Name] = table
	}
	progress := map[string]int64{}
	for _, row := range r.Rows {
		if len(row) != 4 {
			return nil, errors.New("invalid snapshot progress row")
		}
		name, _ := row[0].Value.(string)
		table := expected[name]
		next, e := integer(row[1])
		if e != nil || table == nil || next < 0 || next > table.Count {
			return nil, errors.New("invalid snapshot progress offset")
		}
		count, e := integer(row[2])
		if e != nil || count != table.Count || row[3].Value != table.Digest {
			return nil, errors.New("snapshot progress does not match source manifest")
		}
		progress[name] = next
		delete(expected, name)
	}
	if len(expected) != 0 {
		return nil, errors.New("snapshot checkpoint table set mismatch")
	}
	return progress, nil
}
func integer(v turso.Value) (int64, error) {
	if v.Type != "integer" {
		return 0, errors.New("expected an integer database scalar")
	}
	raw, ok := v.Value.(string)
	if !ok {
		return 0, errors.New("expected an exact integer string")
	}
	return strconv.ParseInt(raw, 10, 64)
}
func rowCount(ctx context.Context, db Database, table string) (int64, error) {
	r, e := db.Execute(ctx, turso.Statement{SQL: "SELECT COUNT(*) FROM " + quoted(table), WantRows: true})
	if e != nil {
		return 0, e
	}
	if len(r.Rows) != 1 || len(r.Rows[0]) != 1 {
		return 0, errors.New("invalid table count result")
	}
	return integer(r.Rows[0][0])
}
func verifyTable(ctx context.Context, db Database, table *Table, count int64) error {
	actual, e := rowCount(ctx, db, table.Name)
	if e != nil {
		return e
	}
	if actual != count {
		return errors.New("destination row count differs from snapshot progress")
	}
	names := make([]string, len(table.Columns))
	for i, name := range table.Columns {
		names[i] = quoted(name)
	}
	lastRowID := int64(-9223372036854775808)
	rows := [][]turso.Value{}
	offset := int64(0)
	for offset < count {
		page := int64(0)
		size := 1024
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
			args = []turso.Value{turso.Integer(lastRowID), turso.Integer(page)}
		}
		r, e := db.Execute(ctx, turso.Statement{SQL: "SELECT rowid," + strings.Join(names, ",") + " FROM " + quoted(table.Name) + filter + " ORDER BY rowid LIMIT ?", Args: args, WantRows: true})
		if e != nil {
			return e
		}
		if int64(len(r.Rows)) != page {
			return errors.New("destination verification page is incomplete")
		}
		for _, cells := range r.Rows {
			if len(cells) != len(table.Columns)+1 {
				return errors.New("destination verification column count mismatch")
			}
			rowid, e := integer(cells[0])
			if e != nil || offset > 0 && rowid <= lastRowID {
				return errors.New("invalid destination row ordering")
			}
			lastRowID = rowid
			rows = append(rows, cells[1:])
			offset++
		}
	}
	if digestRows(rows) != digestRows(table.Data[:count]) {
		return errors.New("destination typed row digest differs from snapshot")
	}
	return nil
}
