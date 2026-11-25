// Package sqlitetest executes the real application SQL against SQLite through
// Python's bundled driver, avoiding a production CGO/SQLite dependency.
// It is intended solely for Go integration tests.
package sqlitetest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CountableNewt/my-context-protocol/services/go/turso"
)

type DB struct{ path, python string }

func New(t testing.TB, schema string) *DB {
	t.Helper()
	python, e := exec.LookPath("python3")
	if e != nil {
		t.Skip("SQLite SQL integration requires python3")
	}
	db := &DB{filepath.Join(t.TempDir(), "test.sqlite"), python}
	var lines []string
	for _, line := range strings.Split(schema, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "--") {
			lines = append(lines, line)
		}
	}
	schema = strings.Join(lines, "\n")
	var statements []turso.Statement
	for _, sql := range strings.Split(schema, ";") {
		if strings.TrimSpace(sql) != "" {
			statements = append(statements, turso.Statement{SQL: sql})
		}
	}
	if len(statements) == 0 {
		return db
	}
	if _, e = db.Batch(context.Background(), statements); e != nil {
		t.Fatal(e)
	}
	return db
}
func (db *DB) Execute(ctx context.Context, s turso.Statement) (*turso.Result, error) {
	r, e := db.Batch(ctx, []turso.Statement{s})
	if e != nil {
		return nil, e
	}
	return &r[0], nil
}
func (db *DB) Batch(ctx context.Context, statements []turso.Statement) ([]turso.Result, error) {
	return db.batch(ctx, statements, "runtime")
}
func (db *DB) SchemaBatch(ctx context.Context, statements []turso.Statement) ([]turso.Result, error) {
	return db.batch(ctx, statements, "schema")
}
func (db *DB) DryRunBatch(ctx context.Context, statements []turso.Statement) ([]turso.Result, error) {
	return db.batch(ctx, statements, "dryrun")
}
func (db *DB) batch(ctx context.Context, statements []turso.Statement, mode string) ([]turso.Result, error) {
	input, e := json.Marshal(statements)
	if e != nil {
		return nil, e
	}
	cmd := exec.CommandContext(ctx, db.python, "-c", driver, db.path, mode)
	cmd.Stdin = bytes.NewReader(input)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	raw, e := cmd.Output()
	if e != nil {
		return nil, fmt.Errorf("SQLite integration statement failed: %w (%s)", e, stderr.String())
	}
	var out []turso.Result
	e = json.Unmarshal(raw, &out)
	return out, e
}

const driver = `
import sys,json,sqlite3,base64
conn=sqlite3.connect(sys.argv[1],timeout=15)
conn.execute('PRAGMA foreign_keys='+('OFF' if sys.argv[2]=='schema' else 'ON'))
def decode(v):
 t=v['type']
 if t=='null': return None
 if t=='integer': return int(v['value'])
 if t=='float': return float(v['value'])
 if t=='blob': return base64.b64decode(v['base64'])
 return v['value']
def encode(v):
 if v is None: return {'type':'null'}
 if isinstance(v,int): return {'type':'integer','value':str(v)}
 if isinstance(v,float): return {'type':'float','value':v}
 if isinstance(v,bytes): return {'type':'blob','base64':base64.b64encode(v).decode()}
 return {'type':'text','value':v}
try:
 conn.execute('BEGIN IMMEDIATE')
 out=[]
 for stmt in json.load(sys.stdin):
  cursor=conn.execute(stmt['sql'],[decode(v) for v in stmt.get('args',[])])
  rows=cursor.fetchall() if cursor.description else []
  out.append({'cols':[{'name':c[0]} for c in cursor.description] if cursor.description else [],'rows':[[encode(v) for v in row] for row in rows],'affected_row_count':max(cursor.rowcount,0)})
 if sys.argv[2]=='schema' and conn.execute('PRAGMA foreign_key_check').fetchone() is not None:
  raise ValueError('schema migration foreign key violation')
 if sys.argv[2]=='dryrun':
  conn.rollback()
 else:
  conn.commit()
 print(json.dumps(out))
except:
 conn.rollback()
 raise
finally:
 conn.close()
`
