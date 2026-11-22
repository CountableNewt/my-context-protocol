// Package snapshotimport transfers a verified, offline SQLite snapshot into an
// otherwise empty Turso database. It never reads or changes a hosted source.
package snapshotimport

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/CountableNewt/my-context-protocol/services/go/migrations"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
)

const Format = "mycontext-sqlite-snapshot-v1"
const MaxSnapshotBytes = 256 << 20
const MaxRowBytes = 3 << 20
const chunkBytes = 2 << 20

//go:embed export.py
var exportScript string

type Table struct {
	Name         string          `json:"name"`
	Columns      []string        `json:"columns"`
	Count        int64           `json:"rows"`
	Digest       string          `json:"typed_multiset_sha256"`
	EncodedBytes int64           `json:"encoded_bytes"`
	LargestRow   int             `json:"largest_row_bytes"`
	Data         [][]turso.Value `json:"-"`
}
type Snapshot struct {
	ID     string            `json:"snapshot_id"`
	Schema map[string]string `json:"-"`
	Tables []*Table          `json:"tables"`
}
type header struct {
	Format string            `json:"format"`
	Schema map[string]string `json:"schema"`
	Tables []struct {
		Name    string   `json:"name"`
		Columns []string `json:"columns"`
		Count   int64    `json:"rows"`
	} `json:"tables"`
}

// Export atomically publishes an owner-readable JSONL artifact after complete
// local validation. Existing files are never replaced; partial output is removed.
func Export(ctx context.Context, source, output string) (*Snapshot, error) {
	info, e := os.Lstat(source)
	if e != nil || !info.Mode().IsRegular() {
		return nil, errors.New("source must be a regular standalone SQLite backup")
	}
	if info.Size() > MaxSnapshotBytes {
		return nil, errors.New("SQLite backup exceeds inspection size limit")
	}
	temporary, e := os.CreateTemp(filepath.Dir(output), ".mcp-snapshot-")
	if e != nil {
		return nil, errors.New("cannot create private snapshot artifact")
	}
	name := temporary.Name()
	defer os.Remove(name)
	cmd := exec.CommandContext(ctx, "python3", "-c", exportScript, source)
	cmd.Stdin = strings.NewReader(migrations.BaselineSQL)
	cmd.Stdout = temporary
	cmd.Stderr = io.Discard
	runErr := cmd.Run()
	syncErr := temporary.Sync()
	closeErr := temporary.Close()
	if runErr != nil || syncErr != nil || closeErr != nil {
		return nil, errors.New("SQLite inspection failed; verify the standalone backup, exact baseline schema, integrity and foreign keys")
	}
	after, e := os.Lstat(source)
	if e != nil || after.Size() != info.Size() || after.ModTime() != info.ModTime() {
		return nil, errors.New("source changed during export; take a consistent SQLite backup first")
	}
	snapshot, e := Read(name)
	if e != nil {
		return nil, e
	}
	if e = os.Link(name, output); e != nil {
		return nil, errors.New("cannot publish snapshot artifact; output must not already exist")
	}
	return snapshot, nil
}
func Read(path string) (*Snapshot, error) {
	info, e := os.Lstat(path)
	if e != nil || !info.Mode().IsRegular() || info.Size() > MaxSnapshotBytes {
		return nil, errors.New("snapshot must be a regular file within the size limit")
	}
	file, e := os.Open(path)
	if e != nil {
		return nil, errors.New("cannot read snapshot artifact")
	}
	defer file.Close()
	raw, e := io.ReadAll(io.LimitReader(file, MaxSnapshotBytes+1))
	if e != nil || len(raw) > MaxSnapshotBytes || !utf8.Valid(raw) {
		return nil, errors.New("snapshot is not valid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var h header
	if decoder.Decode(&h) != nil || h.Format != Format {
		return nil, errors.New("unsupported snapshot format")
	}
	schema, tables, e := baseline()
	if e != nil {
		return nil, e
	}
	if e = matchSchema(h.Schema, schema); e != nil {
		return nil, e
	}
	snapshot := &Snapshot{Schema: h.Schema, Tables: []*Table{}}
	byName := map[string]*Table{}
	if len(h.Tables) != len(tables) {
		return nil, errors.New("snapshot table set differs from baseline")
	}
	for _, item := range h.Tables {
		expected, ok := tables[item.Name]
		if !ok || byName[item.Name] != nil || item.Count < 0 || !equalStrings(item.Columns, expected.columns) {
			return nil, errors.New("snapshot table metadata differs from baseline")
		}
		table := &Table{Name: item.Name, Columns: item.Columns, Count: item.Count, Data: [][]turso.Value{}}
		snapshot.Tables = append(snapshot.Tables, table)
		byName[item.Name] = table
	}
	complete := false
	for {
		var entry struct {
			Table    string        `json:"table"`
			Values   []turso.Value `json:"values"`
			Complete bool          `json:"complete"`
		}
		e := decoder.Decode(&entry)
		if e == io.EOF {
			break
		}
		if e != nil || complete {
			return nil, errors.New("invalid or trailing snapshot records")
		}
		if entry.Complete {
			if entry.Table != "" || len(entry.Values) != 0 {
				return nil, errors.New("invalid completion marker")
			}
			complete = true
			continue
		}
		table := byName[entry.Table]
		if table == nil || len(entry.Values) != len(table.Columns) || int64(len(table.Data)) >= table.Count {
			return nil, errors.New("snapshot rows differ from declared table metadata")
		}
		for i := range entry.Values {
			if e := validateValue(&entry.Values[i]); e != nil {
				return nil, e
			}
		}
		encoded, e := json.Marshal(entry.Values)
		if e != nil || len(encoded) > MaxRowBytes {
			return nil, errors.New("snapshot row exceeds transfer size limit")
		}
		table.EncodedBytes += int64(len(encoded))
		if len(encoded) > table.LargestRow {
			table.LargestRow = len(encoded)
		}
		table.Data = append(table.Data, entry.Values)
	}
	if !complete {
		return nil, errors.New("snapshot completion marker is missing")
	}
	sort.Slice(snapshot.Tables, func(i, j int) bool { return snapshot.Tables[i].Name < snapshot.Tables[j].Name })
	for _, table := range snapshot.Tables {
		if int64(len(table.Data)) != table.Count {
			return nil, errors.New("snapshot row count mismatch")
		}
		table.Digest = digestRows(table.Data)
	}
	identities := make([]struct {
		Name    string   `json:"name"`
		Columns []string `json:"columns"`
		Count   int64    `json:"rows"`
		Digest  string   `json:"typed_multiset_sha256"`
	}, len(snapshot.Tables))
	for i, table := range snapshot.Tables {
		identities[i].Name = table.Name
		identities[i].Columns = table.Columns
		identities[i].Count = table.Count
		identities[i].Digest = table.Digest
	}
	encoded, _ := json.Marshal(struct {
		Format string            `json:"format"`
		Schema map[string]string `json:"schema"`
		Tables any               `json:"tables"`
	}{Format, schema, identities})
	sum := sha256.Sum256(encoded)
	snapshot.ID = hex.EncodeToString(sum[:])
	return snapshot, nil
}
func validateValue(v *turso.Value) error {
	bad := errors.New("snapshot contains an invalid typed SQLite value")
	switch v.Type {
	case "null":
		if v.Value != nil || v.Base64 != nil {
			return bad
		}
	case "text":
		if _, ok := v.Value.(string); !ok || v.Base64 != nil {
			return bad
		}
	case "integer":
		raw, ok := v.Value.(string)
		if !ok || v.Base64 != nil {
			return bad
		}
		n, e := strconv.ParseInt(raw, 10, 64)
		if e != nil {
			return bad
		}
		v.Value = strconv.FormatInt(n, 10)
	case "float":
		number, ok := v.Value.(json.Number)
		if !ok || v.Base64 != nil {
			return bad
		}
		n, e := number.Float64()
		if e != nil || math.IsInf(n, 0) || math.IsNaN(n) {
			return bad
		}
		v.Value = n
	case "blob":
		if v.Value != nil || v.Base64 == nil {
			return bad
		}
		if _, e := base64.StdEncoding.DecodeString(*v.Base64); e != nil {
			return bad
		}
	default:
		return bad
	}
	return nil
}
func digestRows(rows [][]turso.Value) string {
	sum := new(big.Int)
	modulus := new(big.Int).Lsh(big.NewInt(1), 256)
	for _, row := range rows {
		encoded, _ := json.Marshal(row)
		hash := sha256.Sum256(encoded)
		sum.Add(sum, new(big.Int).SetBytes(hash[:]))
		sum.Mod(sum, modulus)
	}
	hash := sha256.Sum256([]byte(fmt.Sprintf("%d:%064x", len(rows), sum)))
	return hex.EncodeToString(hash[:])
}
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
func normalized(sql string) string { return strings.Join(strings.Fields(sql), " ") }
func quoted(name string) string    { return `"` + strings.ReplaceAll(name, `"`, `""`) + `"` }

type tableSpec struct{ columns, dependencies []string }

var createPattern = regexp.MustCompile(`(?is)^CREATE\s+(?:UNIQUE\s+)?(TABLE|INDEX)\s+("[^"]+"|[a-zA-Z_][a-zA-Z_0-9]*)`)
var referencePattern = regexp.MustCompile(`(?i)\bREFERENCES\s+("[^"]+"|[a-zA-Z_][a-zA-Z_0-9]*)`)

func baseline() (map[string]string, map[string]tableSpec, error) {
	statements, e := migrations.SplitSQL(migrations.BaselineSQL)
	if e != nil {
		return nil, nil, e
	}
	schema := map[string]string{}
	tables := map[string]tableSpec{}
	for _, sql := range statements {
		parts := createPattern.FindStringSubmatch(sql)
		if len(parts) != 3 {
			return nil, nil, errors.New("unsupported baseline schema object")
		}
		name := strings.Trim(parts[2], `"`)
		schema[name] = normalized(sql)
		if strings.EqualFold(parts[1], "TABLE") {
			columns, e := columnsOf(sql)
			if e != nil {
				return nil, nil, e
			}
			dependencies := []string{}
			for _, ref := range referencePattern.FindAllStringSubmatch(sql, -1) {
				dependencies = append(dependencies, strings.Trim(ref[1], `"`))
			}
			tables[name] = tableSpec{columns, dependencies}
		}
	}
	return schema, tables, nil
}
func columnsOf(sql string) ([]string, error) {
	start, end := strings.IndexByte(sql, '('), strings.LastIndexByte(sql, ')')
	if start < 0 || end <= start {
		return nil, errors.New("invalid baseline table")
	}
	body := sql[start+1 : end]
	segments := []string{}
	depth, from := 0, 0
	var quote byte
	for i := 0; i < len(body); i++ {
		c := body[i]
		if quote != 0 {
			if c == quote {
				if i+1 < len(body) && body[i+1] == quote {
					i++
				} else {
					quote = 0
				}
			}
			continue
		}
		if c == '\'' || c == '"' || c == '`' {
			quote = c
			continue
		}
		if c == '(' {
			depth++
		}
		if c == ')' {
			depth--
		}
		if c == ',' && depth == 0 {
			segments = append(segments, strings.TrimSpace(body[from:i]))
			from = i + 1
		}
	}
	segments = append(segments, strings.TrimSpace(body[from:]))
	columns := []string{}
	for _, segment := range segments {
		if !strings.HasPrefix(segment, `"`) {
			continue
		}
		end := strings.Index(segment[1:], `"`)
		if end < 0 {
			return nil, errors.New("invalid baseline column")
		}
		columns = append(columns, segment[1:1+end])
	}
	if len(columns) == 0 {
		return nil, errors.New("missing baseline columns")
	}
	return columns, nil
}
func matchSchema(actual, expected map[string]string) error {
	if len(actual) != len(expected) {
		return errors.New("snapshot schema objects differ from baseline")
	}
	for name, sql := range expected {
		if normalized(actual[name]) != sql {
			return errors.New("snapshot schema does not match the exact baseline")
		}
	}
	return nil
}
func orderedTables(snapshot *Snapshot) ([]*Table, error) {
	_, specs, e := baseline()
	if e != nil {
		return nil, e
	}
	pending := map[string]*Table{}
	for _, table := range snapshot.Tables {
		pending[table.Name] = table
	}
	done := map[string]bool{}
	out := []*Table{}
	for len(pending) > 0 {
		progress := false
		for _, table := range snapshot.Tables {
			if pending[table.Name] == nil {
				continue
			}
			ready := true
			for _, dependency := range specs[table.Name].dependencies {
				if !done[dependency] {
					ready = false
					break
				}
			}
			if ready {
				out = append(out, table)
				delete(pending, table.Name)
				done[table.Name] = true
				progress = true
			}
		}
		if !progress {
			return nil, errors.New("baseline foreign-key cycle requires a different transfer strategy")
		}
	}
	return out, nil
}
