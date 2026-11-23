package migrations

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/CountableNewt/my-context-protocol/services/go/turso"
)

type Database interface {
	Execute(context.Context, turso.Statement) (*turso.Result, error)
	Batch(context.Context, []turso.Statement) ([]turso.Result, error)
}
type Migration struct{ Version, SQL string }
type Options struct {
	Initialize, AdoptLegacy bool
	Additions               []Migration
}

// Apply is an explicit operator command, never a service-startup hook. A legacy
// baseline can only be adopted after exact schema verification; it is not replayed.
func Apply(ctx context.Context, db Database, options Options) error {
	if options.Initialize && options.AdoptLegacy {
		return errors.New("choose initialize or adopt-legacy, not both")
	}
	baseline, e := SplitSQL(BaselineSQL)
	if e != nil {
		return e
	}
	existing, e := db.Execute(ctx, turso.Statement{SQL: "SELECT name,sql FROM sqlite_schema WHERE type IN ('table','index') AND name NOT LIKE 'sqlite_%'", WantRows: true})
	if e != nil {
		return e
	}
	names := map[string]string{}
	for _, r := range existing.Rows {
		if len(r) != 2 {
			return errors.New("invalid schema row")
		}
		name, _ := r[0].Value.(string)
		sql, _ := r[1].Value.(string)
		names[name] = sql
	}
	registrySQL, hasRegistry := names["go_schema_migrations"]
	needsBaseline := !hasRegistry
	if hasRegistry {
		if normalize(registrySQL) != normalize(registryDDL) {
			return errors.New("migration registry schema mismatch")
		}
		rows, e := db.Execute(ctx, turso.Statement{SQL: "SELECT version,checksum FROM go_schema_migrations", WantRows: true})
		if e != nil {
			return e
		}
		hasBaseline := false
		for _, r := range rows.Rows {
			if len(r) != 2 {
				return errors.New("invalid migration registry row")
			}
			version, ok := r[0].Value.(string)
			if !ok || version == "" {
				return errors.New("invalid migration registry version")
			}
			if version == "001_swift_schema" {
				hasBaseline = true
			}
		}
		if len(rows.Rows) == 0 {
			needsBaseline = true
		} else if !hasBaseline {
			// A partial or foreign history never authorizes replaying baseline DDL.
			return errors.New("nonempty migration registry has no baseline record; explicit repair is required")
		}
	}
	if needsBaseline {
		applicationObjects := len(names)
		if hasRegistry {
			applicationObjects--
		}
		initialize := applicationObjects == 0
		if initialize {
			if !options.Initialize {
				return errors.New("empty database: explicit initialize is required")
			}
		} else {
			if !options.AdoptLegacy {
				return errors.New("existing database: explicit adopt-legacy and a verified backup are required")
			}
			if e := verifyBaseline(names, baseline); e != nil {
				return e
			}
		}
		statements := make([]turso.Statement, 0, len(baseline)+2)
		if !hasRegistry {
			statements = append(statements, turso.Statement{SQL: registryDDL})
		}
		if initialize {
			for _, sql := range baseline {
				statements = append(statements, turso.Statement{SQL: sql})
			}
		}
		statements = append(statements, record("001_swift_schema", BaselineSQL))
		if len(statements) > turso.MaxStatements {
			return errors.New("baseline exceeds transaction statement limit")
		}
		// Registry creation, baseline initialization (or verified adoption), and
		// its record commit together. A failed first attempt leaves no registry
		// that a retry could mistake for permission to replay an imported schema.
		if _, e = db.Batch(ctx, statements); e != nil {
			return fmt.Errorf("baseline setup failed: %w", e)
		}
	}
	all := append([]Migration{{"001_swift_schema", BaselineSQL}}, options.Additions...)
	for _, m := range all {
		if m.Version == "" || m.SQL == "" {
			return errors.New("migration version and SQL are required")
		}
		prior, e := db.Execute(ctx, turso.Statement{SQL: "SELECT checksum FROM go_schema_migrations WHERE version=?", Args: []turso.Value{turso.Text(m.Version)}, WantRows: true})
		if e != nil {
			return e
		}
		if len(prior.Rows) > 0 {
			if len(prior.Rows[0]) != 1 || prior.Rows[0][0].Value != checksum(m.SQL) {
				return fmt.Errorf("migration checksum mismatch for %s", m.Version)
			}
			continue
		}
		if m.Version == AccountIdentityVersion {
			if e := rebuildAccounts(ctx, db, m); e != nil {
				return fmt.Errorf("account identity migration failed: %w", e)
			}
			continue
		}
		sqls, e := SplitSQL(m.SQL)
		if e != nil {
			return e
		}
		if len(sqls)+1 > turso.MaxStatements {
			return errors.New("migration exceeds transaction statement limit")
		}
		statements := make([]turso.Statement, 0, len(sqls)+1)
		for _, sql := range sqls {
			statements = append(statements, turso.Statement{SQL: sql})
		}
		statements = append(statements, record(m.Version, m.SQL))
		if _, e = db.Batch(ctx, statements); e != nil {
			return fmt.Errorf("migration %s failed: %w", m.Version, e)
		}
	}
	return nil
}

const registryDDL = "CREATE TABLE go_schema_migrations(version TEXT PRIMARY KEY,checksum TEXT NOT NULL,applied_at REAL NOT NULL)"

func verifyBaseline(names map[string]string, baseline []string) error {
	// Fluent generated these objects verbatim. Whitespace differences are safe;
	// missing columns, altered constraints or absent indexes require repair.
	for _, sql := range baseline {
		parts := strings.Fields(sql)
		if len(parts) < 3 {
			return errors.New("invalid baseline SQL")
		}
		index := 2
		if strings.EqualFold(parts[1], "UNIQUE") {
			index = 3
		}
		if len(parts) <= index {
			return errors.New("invalid baseline schema object")
		}
		name := strings.Trim(parts[index], "\"`")
		actual, found := names[name]
		if !found || normalize(actual) != normalize(sql) {
			return fmt.Errorf("legacy schema mismatch for %s", name)
		}
	}
	return nil
}

func record(version, sql string) turso.Statement {
	return turso.Statement{SQL: "INSERT INTO go_schema_migrations(version,checksum,applied_at) VALUES (?,?,?)", Args: []turso.Value{turso.Text(version), turso.Text(checksum(sql)), turso.Integer(time.Now().Unix())}}
}
func checksum(sql string) string {
	sum := sha256.Sum256([]byte(sql))
	return hex.EncodeToString(sum[:])
}
func normalize(sql string) string { return strings.Join(strings.Fields(sql), " ") }

// SplitSQL handles SQLite quoted identifiers/literals and SQL comments. It
// deliberately rejects unterminated constructs instead of applying partial DDL.
func SplitSQL(input string) ([]string, error) {
	var out []string
	var current strings.Builder
	var quote byte
	line, block := false, false
	flush := func() {
		sql := strings.TrimSpace(current.String())
		if sql != "" {
			out = append(out, sql)
		}
		current.Reset()
	}
	for i := 0; i < len(input); i++ {
		c := input[i]
		var next byte
		if i+1 < len(input) {
			next = input[i+1]
		}
		if line {
			if c == '\n' {
				line = false
				current.WriteByte(' ')
			}
			continue
		}
		if block {
			if c == '*' && next == '/' {
				block = false
				i++
				current.WriteByte(' ')
			}
			continue
		}
		if quote != 0 {
			current.WriteByte(c)
			if c == quote {
				if next == quote {
					current.WriteByte(next)
					i++
				} else {
					quote = 0
				}
			}
			continue
		}
		if c == '-' && next == '-' {
			line = true
			i++
			continue
		}
		if c == '/' && next == '*' {
			block = true
			i++
			continue
		}
		if c == '\'' || c == '"' || c == '`' {
			quote = c
			current.WriteByte(c)
			continue
		}
		if c == '[' {
			quote = ']'
			current.WriteByte(c)
			continue
		}
		if c == ';' {
			flush()
			continue
		}
		current.WriteByte(c)
	}
	if quote != 0 || block {
		return nil, errors.New("unterminated SQL literal or comment")
	}
	flush()
	return out, nil
}
