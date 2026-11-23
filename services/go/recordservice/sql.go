package recordservice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/CountableNewt/my-context-protocol/services/go/atproto"
	"github.com/CountableNewt/my-context-protocol/services/go/domainrecords"
	"github.com/CountableNewt/my-context-protocol/services/go/identity"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
)

const MigrationSQL = `
CREATE TABLE domain_native_intents(id TEXT PRIMARY KEY,project_id TEXT NOT NULL,account_id TEXT NOT NULL,source_cid TEXT NOT NULL,source_digest TEXT NOT NULL,target_digest TEXT NOT NULL,target_cid TEXT,status TEXT NOT NULL CHECK(status IN ('pending','complete','abandoned')),statements_json TEXT NOT NULL,changes_json TEXT NOT NULL,results_json TEXT NOT NULL,effect_count INTEGER NOT NULL,created_at INTEGER NOT NULL,completed_at INTEGER);
CREATE UNIQUE INDEX domain_native_open_project ON domain_native_intents(project_id) WHERE status='pending';
CREATE TABLE domain_native_counters(token TEXT PRIMARY KEY,before_count INTEGER NOT NULL);
CREATE TABLE domain_maintenance_visits(project_id TEXT PRIMARY KEY,last_attempt INTEGER NOT NULL);
`
const maxNativeStatements = 110

type scopeKey struct{}
type scope struct {
	service                              *Service
	account, project, cid, owner, digest string
	telemetry                            bool
}

// Scope runs after native ownership checks, resumes durable commands before new
// handler work, and reconciles the SQL projection against its canonical owner.
func (s *Service) Scope(ctx context.Context, account, project string) (context.Context, error) {
	canonical, err := s.IsCanonical(ctx, account, project)
	if err != nil || !canonical {
		return ctx, err
	}
	if err = s.resumeNative(ctx, account, project); err != nil {
		return ctx, err
	}
	if err = s.Reconcile(ctx, account, project); err != nil {
		return ctx, err
	}
	r, err := s.DB.Execute(ctx, turso.Statement{SQL: "SELECT manifest_cid,owner_did,review_digest,telemetry_consent,hydrated FROM domain_canonical_projects WHERE project_id=? AND account_id=?", Args: []turso.Value{turso.Text(project), turso.Text(account)}, WantRows: true})
	if err != nil {
		return ctx, err
	}
	if r == nil || len(r.Rows) != 1 || len(r.Rows[0]) != 5 {
		return ctx, domainrecords.ErrNotMigrated
	}
	if cell(r.Rows[0][4]) != "1" {
		return ctx, domainrecords.ErrHydrationRequired
	}
	return context.WithValue(ctx, scopeKey{}, scope{service: s, account: account, project: project, cid: cell(r.Rows[0][0]), owner: cell(r.Rows[0][1]), digest: cell(r.Rows[0][2]), telemetry: cell(r.Rows[0][3]) == "1"}), nil
}

// SQLProjection uses SQLite's rollback evaluation, never an expression
// interpreter. Native SQL/private data commit together after PDS confirmation.
type SQLProjection struct {
	Raw     Database
	Records *Service
	// Tests inject a real engine/PDS fixture without hosted OAuth.
	runIntent func(context.Context, nativeIntent) error
}
type dryRunner interface {
	DryRunBatch(context.Context, []turso.Statement) ([]turso.Result, error)
}

func (d *SQLProjection) CanonicalScope(ctx context.Context) bool {
	sc, ok := ctx.Value(scopeKey{}).(scope)
	return d != nil && d.Records != nil && ok && sc.service == d.Records
}
func CanonicalScope(ctx context.Context) bool { _, ok := ctx.Value(scopeKey{}).(scope); return ok }
func (d *SQLProjection) Execute(ctx context.Context, stmt turso.Statement) (*turso.Result, error) {
	if !d.CanonicalScope(ctx) {
		return d.Raw.Execute(ctx, stmt)
	}
	info, err := inspectSQL(stmt.SQL)
	if err != nil {
		return nil, err
	}
	if info.kind == "read" || info.def == nil {
		return d.Raw.Execute(ctx, stmt)
	}
	result, err := d.Batch(ctx, []turso.Statement{stmt})
	if err != nil {
		return nil, err
	}
	if len(result) != 1 {
		return nil, errors.New("incomplete native command result")
	}
	return &result[0], nil
}
func (d *SQLProjection) Batch(ctx context.Context, statements []turso.Statement) ([]turso.Result, error) {
	if !d.CanonicalScope(ctx) {
		return d.Raw.Batch(ctx, statements)
	}
	sc := ctx.Value(scopeKey{}).(scope)
	metadata := make([]commandInfo, len(statements))
	public := false
	for i, stmt := range statements {
		info, err := inspectSQL(stmt.SQL)
		if err != nil {
			return nil, err
		}
		metadata[i] = info
		public = public || info.def != nil
	}
	if !public {
		return d.Raw.Batch(ctx, statements)
	}
	if len(statements) == 0 || len(statements) > maxNativeStatements {
		return nil, errors.New("canonical native batch exceeds 110 commands")
	}
	runner, ok := d.Raw.(dryRunner)
	if !ok {
		return nil, errors.New("canonical commands require native rollback evaluation")
	}
	if pending, err := loadNative(ctx, d.Raw, sc.account, sc.project); err != nil {
		return nil, err
	} else if pending != nil {
		return nil, domainrecords.ErrChanged
	}
	evaluated := make([]turso.Statement, len(statements))
	for i, stmt := range statements {
		evaluated[i] = stmt
		if metadata[i].def != nil {
			evaluated[i].SQL = metadata[i].withReturning()
			evaluated[i].Args = append(append([]turso.Value(nil), stmt.Args...), turso.Text(sc.project))
			evaluated[i].WantRows = true
		}
	}
	evaluated = append([]turso.Statement{{SQL: "SELECT total_changes()", WantRows: true}}, evaluated...)
	evaluated = append(evaluated, turso.Statement{SQL: "SELECT total_changes()", WantRows: true})
	results, err := runner.DryRunBatch(ctx, evaluated)
	if err != nil {
		return nil, err
	}
	if len(results) != len(statements)+2 {
		return nil, errors.New("incomplete native rollback evaluation")
	}
	if len(results[0].Rows) != 1 || len(results[len(results)-1].Rows) != 1 {
		return nil, errors.New("native effect counters missing")
	}
	before, err := strconv.ParseInt(cell(results[0].Rows[0][0]), 10, 64)
	if err != nil {
		return nil, err
	}
	after, err := strconv.ParseInt(cell(results[len(results)-1].Rows[0][0]), 10, 64)
	if err != nil || after < before {
		return nil, errors.New("invalid native effect count")
	}
	effectCount := after - before
	results = results[1 : len(results)-1]
	changes := []domainrecords.Change{}
	outputs := make([]turso.Result, len(results))
	for index, result := range results {
		info := metadata[index]
		outputs[index] = result
		if info.def == nil {
			continue
		}
		width := info.returnColumns + 1 + len(info.def.Fields) + 1
		if info.bodyCheck {
			width++
		}
		for _, row := range result.Rows {
			if len(row) != width {
				return nil, errors.New("native public row width mismatch")
			}
			if cell(row[info.returnColumns+1+len(info.def.Fields)]) != "1" {
				return nil, errors.New("native command crosses canonical project boundary")
			}
			id := cell(row[info.returnColumns])
			if id == "" || row[info.returnColumns].Type != "text" {
				return nil, errors.New("native command has invalid public identity")
			}
			change := domainrecords.Change{Table: info.def.Table, ID: id, Delete: info.kind == "delete", Values: map[string]turso.Value{}}
			if !change.Delete {
				for i, field := range info.def.Fields {
					change.Values[field.Name] = row[info.returnColumns+1+i]
				}
			}
			if info.bodyCheck {
				prior, err := d.Raw.Execute(ctx, turso.Statement{SQL: "SELECT skill_body FROM compiled_skills WHERE id=?", Args: []turso.Value{turso.Text(id)}, WantRows: true})
				if err != nil {
					return nil, err
				}
				if prior == nil || len(prior.Rows) != 1 || !reflect.DeepEqual(prior.Rows[0][0], row[len(row)-1]) {
					return nil, &SourceContentError{}
				}
			}
			changes = append(changes, change)
		}
		outputs[index].AffectedRowCount = int64(len(result.Rows))
		if info.returnColumns == 0 {
			outputs[index].Columns = nil
			outputs[index].Rows = nil
		} else {
			if len(result.Columns) < info.returnColumns {
				return nil, errors.New("native returning metadata missing")
			}
			outputs[index].Columns = append([]turso.Column(nil), result.Columns[:info.returnColumns]...)
			outputs[index].Rows = make([][]turso.Value, len(result.Rows))
			for i, row := range result.Rows {
				outputs[index].Rows[i] = append([]turso.Value(nil), row[:info.returnColumns]...)
			}
			if !statements[index].WantRows {
				outputs[index].Rows = nil
			}
		}
	}
	preview := domainrecords.Engine{DB: d.Raw}
	base, err := preview.Inventory(ctx, sc.account, sc.owner, sc.project)
	if err != nil {
		return nil, err
	}
	if base.Digest != sc.digest {
		return nil, domainrecords.ErrChanged
	}
	target, err := domainrecords.ApplyChanges(base, changes, sc.telemetry)
	if err != nil {
		return nil, err
	}
	id, err := identity.UUID()
	if err != nil {
		return nil, err
	}
	intent := nativeIntent{ID: id, Account: sc.account, Project: sc.project, SourceCID: sc.cid, SourceDigest: sc.digest, TargetDigest: target.Digest, Statements: statements, Changes: changes, Results: outputs, State: "pending", EffectCount: effectCount}
	if err = saveNative(ctx, d.Raw, intent); err != nil {
		return nil, err
	}
	if d.runIntent != nil {
		err = d.runIntent(ctx, intent)
	} else {
		err = d.Records.applyNative(ctx, intent)
	}
	if err != nil {
		return nil, err
	}
	complete, err := readNativeID(ctx, d.Raw, id)
	if err != nil {
		return nil, err
	}
	if complete.State != "complete" {
		return nil, errors.New("native canonical receipt is incomplete")
	}
	return outputs, nil
}

type SourceContentError struct{}

func (*SourceContentError) Error() string {
	return "Edit canonical skill content in its source repository"
}
func (*SourceContentError) HTTPStatus() int    { return 409 }
func (e *SourceContentError) HTTPPayload() any { return map[string]string{"error": e.Error()} }

type commandInfo struct {
	kind, sql, returning string
	returnColumns        int
	def                  *domainrecords.Definition
	bodyCheck            bool
}

var mutationStart = regexp.MustCompile(`(?is)^(UPDATE|INSERT\s+INTO|DELETE\s+FROM)\s+([a-z_][a-z_0-9]*)\b`)
var identifier = regexp.MustCompile(`^[a-z_][a-z_0-9]*$`)
var volatileSQL = regexp.MustCompile(`(?i)\b(?:random|randomblob|changes|total_changes)\s*\(|\bcurrent_(?:timestamp|date|time)\b|\b(?:datetime|strftime|julianday|unixepoch)\s*\(\s*'now'`)

func inspectSQL(raw string) (commandInfo, error) {
	sql := strings.TrimSpace(raw)
	sql = strings.TrimSuffix(sql, ";")
	words, err := topWords(sql)
	if err != nil {
		return commandInfo{}, err
	}
	if len(words) == 0 {
		return commandInfo{}, errors.New("empty native command")
	}
	first := strings.ToUpper(words[0].text)
	if first == "SELECT" || first == "EXPLAIN" {
		return commandInfo{kind: "read", sql: sql}, nil
	}
	matched := mutationStart.FindStringSubmatch(sql)
	if matched == nil {
		return commandInfo{}, errors.New("unsupported canonical native command shape")
	}
	kind := "insert"
	if first == "UPDATE" {
		kind = "update"
	}
	if first == "DELETE" {
		kind = "delete"
	}
	info := commandInfo{kind: kind, sql: sql}
	for _, definition := range domainrecords.Registry() {
		if strings.EqualFold(definition.Table, matched[2]) {
			copy := definition
			info.def = &copy
			break
		}
	}
	if info.def == nil {
		return info, nil
	}
	if volatileSQL.MatchString(sql) {
		return commandInfo{}, errors.New("canonical SQL clocks and identities must be supplied as durable parameters")
	}
	for _, word := range words {
		if strings.EqualFold(word.text, "RETURNING") {
			info.returning = strings.TrimSpace(sql[word.end:])
			info.sql = strings.TrimSpace(sql[:word.start])
			expressions, err := splitReturning(info.returning)
			if err != nil {
				return commandInfo{}, err
			}
			info.returnColumns = len(expressions)
			break
		}
	}
	if kind == "update" {
		set, where := -1, len(info.sql)
		for _, word := range words {
			if strings.EqualFold(word.text, "SET") {
				set = word.end
			}
			if strings.EqualFold(word.text, "WHERE") || strings.EqualFold(word.text, "RETURNING") {
				where = word.start
				break
			}
		}
		if set < 0 || where < set {
			return commandInfo{}, errors.New("invalid native update")
		}
		assignments, err := splitReturning(info.sql[set:where])
		if err != nil {
			return commandInfo{}, err
		}
		for _, assignment := range assignments {
			parts := strings.SplitN(assignment, "=", 2)
			column := strings.ToLower(strings.TrimSpace(parts[0]))
			if len(parts) != 2 || !identifier.MatchString(column) {
				return commandInfo{}, errors.New("unsupported native update target")
			}
			if column == info.def.IDColumn() {
				return commandInfo{}, errors.New("canonical primary identities cannot be reassigned")
			}
			if info.def.Table == "compiled_skills" && column == "skill_body" {
				info.bodyCheck = true
			}
		}
	}
	return info, nil
}
func (info commandInfo) withReturning() string {
	parts := []string{}
	if info.returnColumns > 0 {
		parts = append(parts, info.returning)
	}
	parts = append(parts, info.def.IDColumn()+" AS __domain_identity")
	for _, field := range info.def.Fields {
		parts = append(parts, field.Name+" AS __domain_"+field.Name)
	}
	parts = append(parts, "CASE WHEN ("+info.def.Selector+") THEN 1 ELSE 0 END AS __domain_scope")
	if info.bodyCheck {
		parts = append(parts, "skill_body AS __domain_private_body")
	}
	return info.sql + " RETURNING " + strings.Join(parts, ",")
}

type sqlWord struct {
	text       string
	start, end int
}

func topWords(sql string) ([]sqlWord, error) {
	out := []sqlWord{}
	depth := 0
	for i := 0; i < len(sql); {
		c := sql[i]
		if c == '\'' || c == '"' || c == '`' || c == '[' {
			end := c
			if c == '[' {
				end = ']'
			}
			i++
			closed := false
			for i < len(sql) {
				if sql[i] == end {
					if i+1 < len(sql) && sql[i+1] == end {
						i += 2
						continue
					}
					i++
					closed = true
					break
				}
				i++
			}
			if !closed {
				return nil, errors.New("unterminated native SQL quote")
			}
			continue
		}
		if c == ';' || c == '-' && i+1 < len(sql) && sql[i+1] == '-' || c == '/' && i+1 < len(sql) && sql[i+1] == '*' {
			return nil, errors.New("native commands must be single uncommented statements")
		}
		if c == '(' {
			depth++
			i++
			continue
		}
		if c == ')' {
			depth--
			if depth < 0 {
				return nil, errors.New("unbalanced native SQL")
			}
			i++
			continue
		}
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_' {
			start := i
			i++
			for i < len(sql) && (sql[i] >= 'a' && sql[i] <= 'z' || sql[i] >= 'A' && sql[i] <= 'Z' || sql[i] >= '0' && sql[i] <= '9' || sql[i] == '_') {
				i++
			}
			if depth == 0 {
				out = append(out, sqlWord{sql[start:i], start, i})
			}
			continue
		}
		i++
	}
	if depth != 0 {
		return nil, errors.New("unbalanced native SQL")
	}
	return out, nil
}
func splitReturning(value string) ([]string, error) {
	if strings.TrimSpace(value) == "" {
		return nil, errors.New("empty native SQL expression")
	}
	if _, err := topWords(value); err != nil {
		return nil, err
	}
	depth, start := 0, 0
	quote := byte(0)
	parts := []string{}
	for i := 0; i < len(value); i++ {
		c := value[i]
		if quote != 0 {
			if c == quote {
				if i+1 < len(value) && value[i+1] == quote {
					i++
					continue
				}
				quote = 0
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
			parts = append(parts, strings.TrimSpace(value[start:i]))
			start = i + 1
		}
	}
	parts = append(parts, strings.TrimSpace(value[start:]))
	for _, part := range parts {
		if part == "" || part == "*" || strings.HasSuffix(part, ".*") {
			return nil, errors.New("unsupported native returning expression")
		}
	}
	return parts, nil
}

type nativeIntent struct {
	ID, Account, Project, SourceCID, SourceDigest, TargetDigest, TargetCID, State string
	Statements                                                                    []turso.Statement
	Changes                                                                       []domainrecords.Change
	Results                                                                       []turso.Result
	EffectCount                                                                   int64
}

func cell(value turso.Value) string {
	if value.Type == "null" {
		return ""
	}
	return fmt.Sprint(value.Value)
}
func saveNative(ctx context.Context, db Database, intent nativeIntent) error {
	commands, err := json.Marshal(intent.Statements)
	if err != nil {
		return err
	}
	changes, err := json.Marshal(intent.Changes)
	if err != nil {
		return err
	}
	results, err := json.Marshal(intent.Results)
	if err != nil {
		return err
	}
	if len(commands)+len(changes)+len(results) > 3<<20 {
		return errors.New("native intent exceeds private journal limit")
	}
	r, err := db.Execute(ctx, turso.Statement{SQL: "INSERT INTO domain_native_intents(id,project_id,account_id,source_cid,source_digest,target_digest,status,statements_json,changes_json,results_json,effect_count,created_at) SELECT ?,?,?,?,?,?,'pending',?,?,?,?,? WHERE EXISTS(SELECT 1 FROM domain_canonical_projects WHERE project_id=? AND account_id=? AND manifest_cid=? AND review_digest=?) ON CONFLICT DO NOTHING RETURNING id", Args: []turso.Value{turso.Text(intent.ID), turso.Text(intent.Project), turso.Text(intent.Account), turso.Text(intent.SourceCID), turso.Text(intent.SourceDigest), turso.Text(intent.TargetDigest), turso.Text(string(commands)), turso.Text(string(changes)), turso.Text(string(results)), turso.Integer(intent.EffectCount), turso.Integer(time.Now().Unix()), turso.Text(intent.Project), turso.Text(intent.Account), turso.Text(intent.SourceCID), turso.Text(intent.SourceDigest)}, WantRows: true})
	if err != nil {
		return err
	}
	if r == nil || len(r.Rows) != 1 {
		return atproto.ErrConflict
	}
	return nil
}

const intentColumns = "id,account_id,project_id,source_cid,source_digest,target_digest,target_cid,status,statements_json,changes_json,results_json,effect_count"

func decodeNative(r *turso.Result) (nativeIntent, error) {
	if r == nil || len(r.Rows) != 1 || len(r.Rows[0]) != 12 {
		return nativeIntent{}, errors.New("invalid durable native intent")
	}
	row := r.Rows[0]
	intent := nativeIntent{ID: cell(row[0]), Account: cell(row[1]), Project: cell(row[2]), SourceCID: cell(row[3]), SourceDigest: cell(row[4]), TargetDigest: cell(row[5]), TargetCID: cell(row[6]), State: cell(row[7])}
	if json.Unmarshal([]byte(cell(row[8])), &intent.Statements) != nil || json.Unmarshal([]byte(cell(row[9])), &intent.Changes) != nil || json.Unmarshal([]byte(cell(row[10])), &intent.Results) != nil {
		return nativeIntent{}, errors.New("invalid native intent payload")
	}
	count, err := strconv.ParseInt(cell(row[11]), 10, 64)
	if err != nil || count < 0 {
		return nativeIntent{}, errors.New("invalid native effect count")
	}
	intent.EffectCount = count
	return intent, nil
}
func loadNative(ctx context.Context, db Database, account, project string) (*nativeIntent, error) {
	r, err := db.Execute(ctx, turso.Statement{SQL: "SELECT " + intentColumns + " FROM domain_native_intents WHERE account_id=? AND project_id=? AND status='pending'", Args: []turso.Value{turso.Text(account), turso.Text(project)}, WantRows: true})
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, errors.New("invalid native intent query")
	}
	if len(r.Rows) == 0 {
		return nil, nil
	}
	intent, err := decodeNative(r)
	return &intent, err
}
func readNativeID(ctx context.Context, db Database, id string) (nativeIntent, error) {
	r, err := db.Execute(ctx, turso.Statement{SQL: "SELECT " + intentColumns + " FROM domain_native_intents WHERE id=?", Args: []turso.Value{turso.Text(id)}, WantRows: true})
	if err != nil {
		return nativeIntent{}, err
	}
	return decodeNative(r)
}
func (s *Service) resumeNative(ctx context.Context, account, project string) error {
	intent, err := loadNative(ctx, s.DB, account, project)
	if err != nil || intent == nil {
		return err
	}
	return s.applyNative(ctx, *intent)
}
func (s *Service) applyNative(ctx context.Context, intent nativeIntent) error {
	return s.WithEngine(ctx, intent.Account, func(ctx context.Context, owner string, e *domainrecords.Engine) error {
		return executeNative(ctx, s.DB, e, owner, intent)
	})
}

func executeNative(ctx context.Context, db Database, e *domainrecords.Engine, owner string, intent nativeIntent) error {
	e.ExpectedPriorCID = intent.SourceCID
	e.ExpectedDigest = intent.TargetDigest
	e.BeforePublish = func(_ context.Context, review domainrecords.Review) error {
		if review.ProjectID != intent.Project || review.Digest != intent.TargetDigest {
			return atproto.ErrConflict
		}
		return nil
	}
	e.NativeProjection = nativeProjection(db, intent)
	ref, err := e.Update(ctx, intent.Account, owner, intent.Project, intent.Changes)
	if err != nil {
		return err
	}
	current, err := readNativeID(ctx, db, intent.ID)
	if err != nil {
		return err
	}
	if current.State == "complete" {
		return nil
	}
	// A private-column-only change still passes the canonical barrier, but needs
	// no new manifest. Commit its original SQL against that unchanged receipt.
	if intent.SourceDigest == intent.TargetDigest && ref.CID == intent.SourceCID {
		review, err := e.Inventory(ctx, intent.Account, owner, intent.Project)
		if err != nil {
			return err
		}
		statements, _, err := e.NativeProjection(ctx, intent.Account, review, ref)
		if err != nil {
			return err
		}
		_, err = db.Batch(ctx, statements)
		return err
	}
	return errors.New("canonical native projection was not confirmed")
}

func nativeProjection(db Database, intent nativeIntent) func(context.Context, string, domainrecords.Review, atproto.Reference) ([]turso.Statement, bool, error) {
	return func(ctx context.Context, account string, review domainrecords.Review, target atproto.Reference) ([]turso.Statement, bool, error) {
		if account != intent.Account || review.ProjectID != intent.Project || review.Digest != intent.TargetDigest {
			return nil, false, atproto.ErrConflict
		}
		current, err := readNativeID(ctx, db, intent.ID)
		if err != nil {
			return nil, false, err
		}
		if current.State == "complete" {
			if current.TargetCID != target.CID {
				return nil, false, atproto.ErrConflict
			}
			return nil, true, nil
		}
		if current.TargetCID != "" && current.TargetCID != target.CID {
			return nil, false, atproto.ErrConflict
		}
		_, err = db.Execute(ctx, turso.Statement{SQL: "UPDATE domain_native_intents SET target_cid=? WHERE id=? AND status='pending' AND target_digest=? AND (target_cid IS NULL OR target_cid=?)", Args: []turso.Value{turso.Text(target.CID), turso.Text(intent.ID), turso.Text(review.Digest), turso.Text(target.CID)}})
		if err != nil {
			return nil, false, err
		}
		token := "native:" + intent.ID
		batch := []turso.Statement{{SQL: "INSERT INTO domain_projection_guards(token,valid) SELECT ?,CASE WHEN EXISTS(SELECT 1 FROM domain_native_intents i JOIN domain_canonical_projects p ON p.project_id=i.project_id WHERE i.id=? AND i.status='pending' AND i.account_id=? AND i.source_cid=p.manifest_cid AND i.source_digest=p.review_digest AND i.target_digest=?) THEN 1 ELSE 0 END", Args: []turso.Value{turso.Text(token), turso.Text(intent.ID), turso.Text(account), turso.Text(review.Digest)}}}
		batch = append(batch, turso.Statement{SQL: "INSERT INTO domain_native_counters(token,before_count) VALUES(?,total_changes())", Args: []turso.Value{turso.Text(token)}})
		privateGuards := int64(0)
		for index, statement := range intent.Statements {
			info, err := inspectSQL(statement.SQL)
			if err != nil {
				return nil, false, err
			}
			batch = append(batch, statement)
			if info.kind != "read" && info.def == nil {
				expected := intent.Results[index].AffectedRowCount
				if expected < 0 {
					return nil, false, errors.New("invalid private effect count")
				}
				guardToken := fmt.Sprintf("%s:%d", token, index)
				if expected == 0 {
					// Successful guards must preserve changes() for the next native
					// command; inserting a single valid guard would change 0 to 1.
					batch = append(batch, turso.Statement{SQL: "INSERT INTO domain_projection_guards(token,valid) SELECT ?,0 WHERE changes()<>0", Args: []turso.Value{turso.Text(guardToken)}})
				} else {
					privateGuards += expected
					batch = append(batch, turso.Statement{SQL: "INSERT INTO domain_projection_guards(token,valid) WITH RECURSIVE n(i) AS(SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<?) SELECT ?||':'||i,CASE WHEN changes()=? THEN 1 ELSE 0 END FROM n", Args: []turso.Value{turso.Integer(expected), turso.Text(guardToken), turso.Integer(expected)}})
				}
			}
		}
		batch = append(batch, turso.Statement{SQL: "INSERT INTO domain_projection_guards(token,valid) SELECT ?,CASE WHEN total_changes()-(SELECT before_count FROM domain_native_counters WHERE token=?)-1=? THEN 1 ELSE 0 END", Args: []turso.Value{turso.Text(token + ":count"), turso.Text(token), turso.Integer(intent.EffectCount + privateGuards)}})
		guards, err := publicStateGuards(intent, token)
		if err != nil {
			return nil, false, err
		}
		batch = append(batch, guards...)
		batch = append(batch, turso.Statement{SQL: "UPDATE domain_native_intents SET status='complete',completed_at=?,statements_json='[]',changes_json='[]' WHERE id=? AND status='pending' AND target_cid=?", Args: []turso.Value{turso.Integer(time.Now().Unix()), turso.Text(intent.ID), turso.Text(target.CID)}}, turso.Statement{SQL: "DELETE FROM domain_projection_guards WHERE token=? OR token LIKE ?", Args: []turso.Value{turso.Text(token), turso.Text(token + ":%")}}, turso.Statement{SQL: "DELETE FROM domain_native_counters WHERE token=?", Args: []turso.Value{turso.Text(token)}})
		if len(batch) > turso.MaxStatements-8 {
			return nil, false, errors.New("native projection exceeds bounded receipt transaction")
		}
		return batch, true, nil
	}
}

func publicStateGuards(intent nativeIntent, token string) ([]turso.Statement, error) {
	last := map[string]domainrecords.Change{}
	for _, change := range intent.Changes {
		last[change.Table+"\x00"+change.ID] = change
	}
	byTable := map[string][]domainrecords.Change{}
	for _, change := range last {
		byTable[change.Table] = append(byTable[change.Table], change)
	}
	out := []turso.Statement{}
	for _, definition := range domainrecords.Registry() {
		changes := byTable[definition.Table]
		if len(changes) == 0 {
			continue
		}
		encoded, err := json.Marshal(changes)
		if err != nil {
			return nil, err
		}
		identity := "t." + definition.IDColumn() + "=json_extract(expected.value,'$.ID')"
		fields := []string{}
		for _, field := range definition.Fields {
			expression := "json_extract(expected.value,'$.Values." + field.Name + ".value')"
			switch field.Type {
			case "integer", "boolean":
				expression = "CAST(" + expression + " AS INTEGER)"
			case "decimal":
				expression = "CAST(" + expression + " AS REAL)"
			}
			fields = append(fields, "t."+field.Name+" IS "+expression)
		}
		condition := "NOT EXISTS(SELECT 1 FROM " + definition.Table + " t WHERE " + identity + ")"
		if len(fields) > 0 {
			condition = "CASE WHEN json_extract(expected.value,'$.Delete')=1 THEN " + condition + " ELSE EXISTS(SELECT 1 FROM " + definition.Table + " t WHERE " + identity + " AND " + strings.Join(fields, " AND ") + ") END"
		}
		out = append(out, turso.Statement{SQL: "INSERT INTO domain_projection_guards(token,valid) SELECT ?,CASE WHEN NOT EXISTS(SELECT 1 FROM json_each(?) expected WHERE NOT(" + condition + ")) THEN 1 ELSE 0 END", Args: []turso.Value{turso.Text(token + ":" + definition.Table), turso.Text(string(encoded))}})
	}
	return out, nil
}
