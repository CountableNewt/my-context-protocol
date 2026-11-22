package snapshotimport

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/CountableNewt/my-context-protocol/services/go/turso"
)

// RemoteExportOptions records an operator precondition, not a database lock.
// Stop and drain every writer before setting WritersPaused, and keep them stopped
// until the snapshot and replacement SQLite database have been verified.
type RemoteExportOptions struct{ WritersPaused bool }

// ExportRemote reads only baseline application tables, retaining Fluent history
// but excluding Go-owned transient state. Independent HTTP queries cannot form a
// consistent snapshot while writers run. A second full digest pass detects drift
// but is not a substitute for the explicit pause/drain precondition.
func ExportRemote(ctx context.Context, db Database, output string, options RemoteExportOptions) (*Snapshot, error) {
	if !options.WritersPaused {
		return nil, errors.New("remote export requires all writers paused and drained")
	}
	if db == nil {
		return nil, errors.New("source database is required")
	}
	if _, e := os.Lstat(output); !os.IsNotExist(e) {
		return nil, errors.New("snapshot output must not already exist")
	}
	schema, e := destinationSchema(ctx, db)
	if e != nil {
		return nil, errors.New("cannot inspect remote schema")
	}
	if e = checkOwnedSchema(schema); e != nil {
		return nil, e
	}
	baselineSchema, definitions, e := baseline()
	if e != nil {
		return nil, e
	}
	h := header{Format: Format, Schema: baselineSchema}
	names := make([]string, 0, len(definitions))
	for name := range definitions {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		count, e := rowCount(ctx, db, name)
		if e != nil || count < 0 {
			return nil, errors.New("remote table count failed")
		}
		h.Tables = append(h.Tables, struct {
			Name    string   `json:"name"`
			Columns []string `json:"columns"`
			Count   int64    `json:"rows"`
		}{name, definitions[name].columns, count})
	}
	f, e := os.CreateTemp(filepath.Dir(output), ".mcp-remote-snapshot-")
	if e != nil {
		return nil, errors.New("cannot create private snapshot")
	}
	defer os.Remove(f.Name())
	defer f.Close()
	encoder := json.NewEncoder(f)
	if e = encoder.Encode(h); e != nil {
		return nil, errors.New("cannot write snapshot header")
	}
	for _, table := range h.Tables {
		columns := make([]string, len(table.Columns))
		sizes := make([]string, len(columns))
		for i, name := range table.Columns {
			columns[i] = quoted(name)
			sizes[i] = "COALESCE(length(CAST(" + quoted(name) + " AS BLOB)),0)"
		}
		last, first, seen := int64(0), true, int64(0)
		for seen < table.Count {
			where := ""
			args := []turso.Value{}
			if !first {
				where = " WHERE rowid>?"
				args = append(args, turso.Integer(last))
			}
			// Obtain bounded, non-content size metadata first, then select ranges whose
			// worst-case JSON escaping/base64 fits the response cap. This avoids fetching
			// 120 unexpectedly large BLOB/text rows in one response.
			page, e := db.Execute(ctx, turso.Statement{SQL: "SELECT rowid," + strings.Join(sizes, "+") + " FROM " + quoted(table.Name) + where + " ORDER BY rowid LIMIT 120", Args: args, WantRows: true})
			if e != nil || len(page.Rows) == 0 || int64(len(page.Rows)) > table.Count-seen {
				return nil, errors.New("remote size page failed or source changed")
			}
			for start := 0; start < len(page.Rows); {
				end, budget := start, 0
				for end < len(page.Rows) {
					row := page.Rows[end]
					if len(row) != 2 {
						return nil, errors.New("invalid remote size metadata")
					}
					size, e := integer(row[1])
					if e != nil || size < 0 || size > MaxRowBytes {
						return nil, errors.New("remote row exceeds supported size")
					}
					bound := int(size)*6 + len(columns)*128 + 256
					if bound > MaxRowBytes {
						return nil, errors.New("remote row exceeds conservative response bound")
					}
					if end > start && budget+bound > chunkBytes {
						break
					}
					budget += bound
					end++
				}
				high, e := integer(page.Rows[end-1][0])
				if e != nil {
					return nil, errors.New("invalid remote row identity")
				}
				filter := " WHERE rowid<=?"
				values := []turso.Value{turso.Integer(high)}
				if !first {
					filter = " WHERE rowid>? AND rowid<=?"
					values = []turso.Value{turso.Integer(last), turso.Integer(high)}
				}
				rows, e := db.Execute(ctx, turso.Statement{SQL: "SELECT rowid," + strings.Join(columns, ",") + " FROM " + quoted(table.Name) + filter + " ORDER BY rowid LIMIT 120", Args: values, WantRows: true})
				if e != nil || len(rows.Rows) != end-start {
					return nil, errors.New("remote data page failed or source changed")
				}
				for i, row := range rows.Rows {
					if len(row) != len(columns)+1 {
						return nil, errors.New("remote column count mismatch")
					}
					id, e := integer(row[0])
					wanted, ie := integer(page.Rows[start+i][0])
					if e != nil || ie != nil || id != wanted || !first && id <= last {
						return nil, errors.New("remote row ordering changed")
					}
					encoded, e := json.Marshal(row[1:])
					if e != nil || len(encoded) > MaxRowBytes {
						return nil, errors.New("remote typed row exceeds limit")
					}
					if e = encoder.Encode(struct {
						Table  string        `json:"table"`
						Values []turso.Value `json:"values"`
					}{table.Name, row[1:]}); e != nil {
						return nil, errors.New("cannot write snapshot rows")
					}
					first = false
					last = id
					seen++
				}
				stat, e := f.Stat()
				if e != nil || stat.Size() > MaxSnapshotBytes {
					return nil, errors.New("remote snapshot exceeds size limit")
				}
				start = end
			}
		}
	}
	if e = encoder.Encode(map[string]bool{"complete": true}); e != nil {
		return nil, errors.New("cannot complete snapshot")
	}
	if e = f.Sync(); e != nil {
		return nil, errors.New("cannot sync snapshot")
	}
	if e = f.Close(); e != nil {
		return nil, errors.New("cannot close snapshot")
	}
	snapshot, e := Read(f.Name())
	if e != nil {
		return nil, e
	}
	for _, table := range snapshot.Tables {
		if e = verifyTable(ctx, db, table, table.Count); e != nil {
			return nil, errors.New("remote count/digest verification failed; keep writers stopped")
		}
	}
	after, e := destinationSchema(ctx, db)
	if e != nil || len(after) != len(schema) {
		return nil, errors.New("remote schema changed during export")
	}
	for name, ddl := range schema {
		if after[name] != ddl {
			return nil, errors.New("remote schema changed during export")
		}
	}
	fk, e := db.Execute(ctx, turso.Statement{SQL: "PRAGMA foreign_key_check", WantRows: true})
	if e != nil || len(fk.Rows) != 0 {
		return nil, errors.New("remote foreign-key verification failed")
	}
	if e = os.Link(f.Name(), output); e != nil {
		return nil, errors.New("cannot publish snapshot; output must be new")
	}
	return snapshot, nil
}
