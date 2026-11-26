// Package control serves owner-scoped dashboard APIs over the existing SQLite schema.
package control

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/CountableNewt/my-context-protocol/services/go/identity"
	"github.com/CountableNewt/my-context-protocol/services/go/session"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
	"github.com/CountableNewt/my-context-protocol/services/go/xrpc"
)

type DB interface {
	Execute(context.Context, turso.Statement) (*turso.Result, error)
	Batch(context.Context, []turso.Statement) ([]turso.Result, error)
}
type Auth interface {
	ReadSession(context.Context, string) (session.Payload, error)
	Account(context.Context, string) (*identity.Account, error)
}
type Catalog interface {
	Catalog(context.Context, string) (map[string]any, error)
}
type DomainProvider interface {
	Get(context.Context, string) (map[string]any, error)
	Set(context.Context, string, string) (map[string]any, error)
	Verify(context.Context, string) (map[string]any, error)
}
type Config struct {
	ResolveOwnerHandle      func(context.Context, string) (string, error)
	ListTangledRepositories func(context.Context, string, string) (map[string]any, error)
	Records                 CanonicalRecords
	// XRPCVerifier explicitly enables verified API authorization; nil keeps XRPC disabled.
	XRPCVerifier          xrpc.Verifier
	CronSecret            string
	CanonicalMaintenance  bool
	ExtraMaintenance      func(context.Context) (map[string]any, error)
	MaintenanceRows       int
	MaintenanceRollupRows int
	FrontendOrigins       []string
	MCPBaseDomain         string
	MCPPath               string
	MCPOAuthEnabled       bool
	Catalog               Catalog
	Domains               DomainProvider
	Sync                  func(context.Context, string) (map[string]any, error)
	SyncContinuation      func(context.Context, string) (bool, error)
	ConnectRepository     func(context.Context, string, string, map[string]json.RawMessage) (map[string]any, error)
	Writeback             func(context.Context, string, string, string, string) (map[string]any, error)
	SyncLimit             int
	SyncWindowSeconds     int64
}
type Service struct {
	xrpc *xrpc.Handler
	db   DB
	auth Auth
	cfg  Config
	now  func() time.Time
}
type row map[string]any
type apiError struct {
	status  int
	message string
}

func (e apiError) Error() string   { return e.message }
func bad(message string) error     { return apiError{400, message} }
func missing(message string) error { return apiError{404, message} }
func New(db DB, auth Auth, cfg Config) (*Service, error) {
	if db == nil || auth == nil {
		return nil, errors.New("control database and authentication store are required")
	}
	if len(cfg.FrontendOrigins) == 0 {
		return nil, errors.New("frontend origins are required")
	}
	for i, raw := range cfg.FrontendOrigins {
		u, e := url.Parse(strings.TrimRight(strings.TrimSpace(raw), "/"))
		if e != nil || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") {
			return nil, errors.New("invalid frontend origin")
		}
		cfg.FrontendOrigins[i] = u.Scheme + "://" + strings.ToLower(u.Host)
	}
	if cfg.MCPPath == "" {
		cfg.MCPPath = "/mcp"
	}
	s := &Service{db: db, auth: auth, cfg: cfg, now: time.Now}
	if cfg.XRPCVerifier != nil {
		handler, err := xrpc.New(xrpc.Config{Verifier: cfg.XRPCVerifier, Invoker: controlXRPCInvoker{service: s}})
		if err != nil {
			return nil, err
		}
		s.xrpc = handler
	}
	return s, nil
}
func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.URL.Path == "/xrpc" || strings.HasPrefix(r.URL.Path, "/xrpc/") {
		if s.xrpc == nil {
			http.NotFound(w, r)
		} else {
			s.xrpc.ServeHTTP(w, r)
		}
		return
	}
	if r.URL.Path == "/internal/maintenance" {
		s.maintenance(w, r)
		return
	}
	if r.URL.Path == "/health" && r.Method == "GET" {
		w.Write([]byte("ok"))
		return
	}
	value, status, err := s.serve(r)
	if err != nil {
		err = recordError(err)
		status = 503
		message := "Control service temporarily unavailable"
		var ae apiError
		if errors.As(err, &ae) {
			status = ae.status
			message = ae.message
		}
		if public, ok := err.(interface{ HTTPStatus() int }); ok && public.HTTPStatus() >= 400 && public.HTTPStatus() < 500 {
			status = public.HTTPStatus()
			message = err.Error()
		}
		value = map[string]any{"error": message}
		if public, ok := err.(interface {
			HTTPStatus() int
			HTTPPayload() any
		}); ok && public.HTTPStatus() >= 400 && public.HTTPStatus() < 500 {
			value = public.HTTPPayload()
		}
	}
	if status == 0 {
		status = 200
	}
	if status == 429 {
		window := s.cfg.SyncWindowSeconds
		if window < 1 {
			window = 3600
		}
		w.Header().Set("Retry-After", strconv.FormatInt(window, 10))
	}
	if status == 204 {
		w.WriteHeader(status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(value)
}
func (s *Service) serve(r *http.Request) (any, int, error) {
	handles := &ownerHandleLookup{ctx: r.Context(), lookup: s.cfg.ResolveOwnerHandle, values: map[string]string{}}
	defer handles.close()
	account, err := s.account(r)
	if err != nil {
		return nil, 0, err
	}
	if r.Method != "GET" && r.Method != "HEAD" {
		if !s.validOrigin(r) {
			return nil, 0, apiError{403, "Invalid origin"}
		}
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) == 2 && parts[0] == "repositories" && parts[1] == "tangled" && r.Method == "GET" && s.cfg.ListTangledRepositories != nil {
		value, e := s.cfg.ListTangledRepositories(r.Context(), account.ID, r.URL.Query().Get("cursor"))
		return value, 200, e
	}
	if parts[0] == "admin" {
		if !account.IsAdmin {
			return nil, 0, apiError{403, "Admin access required"}
		}
		return s.admin(r, account, parts[1:])
	}
	if len(parts) == 2 && parts[0] == "dashboard" && r.Method == "GET" {
		return s.dashboard(r, account.ID, "", parts[1])
	}
	if parts[0] != "projects" {
		return nil, 0, missing("Not found")
	}
	if len(parts) == 1 {
		switch r.Method {
		case "GET":
			rows, e := s.query(r.Context(), projectSelect+" WHERE p.account_id=? ORDER BY p.created_at ASC,p.id ASC", account.ID)
			if e != nil {
				return nil, 0, e
			}
			if err := s.addPublicationCompletions(r.Context(), account.ID, rows); err != nil {
				return nil, 0, err
			}
			out := []row{}
			for _, p := range rows {
				out = append(out, s.projectResponseWithOwner(p, handles))
			}
			return out, 200, e
		case "POST":
			return s.createProject(r, account.ID)
		default:
			return nil, 0, apiError{405, "Method not allowed"}
		}
	}
	pid, err := uuid(parts[1])
	if err != nil {
		return nil, 0, bad("Invalid project ID")
	}
	project, err := s.one(r.Context(), projectSelect+" WHERE p.id=? AND p.account_id=?", pid, account.ID)
	if err != nil {
		return nil, 0, err
	}
	if project == nil {
		return nil, 0, missing("Project not found")
	}
	if len(parts) >= 3 && parts[2] == "atproto" {
		return s.publication(r, account.ID, pid, parts[3:])
	}
	if s.cfg.Records != nil && !(len(parts) == 3 && parts[2] == "sync" && r.Method == "POST") {
		ctx, e := s.cfg.Records.Scope(r.Context(), account.ID, pid)
		if e != nil {
			return nil, 0, recordError(e)
		}
		r = r.WithContext(ctx)
		project, e = s.one(ctx, projectSelect+" WHERE p.id=? AND p.account_id=?", pid, account.ID)
		if e != nil {
			return nil, 0, e
		}
		if project == nil {
			return nil, 0, missing("Project not found")
		}
	}
	if len(parts) == 2 || len(parts) == 3 && parts[2] == "set-active" {
		if err := s.addPublicationCompletion(r.Context(), account.ID, pid, project); err != nil {
			return nil, 0, err
		}
	}
	if len(parts) == 2 {
		if r.Method == "GET" {
			return s.projectResponseWithOwner(project, handles), 200, nil
		}
		if r.Method == "PATCH" {
			var body struct {
				Name string `json:"name"`
			}
			if e := decode(r, &body); e != nil {
				return nil, 0, e
			}
			name := strings.TrimSpace(body.Name)
			if name == "" || utf8.RuneCountInString(name) > 256 {
				return nil, 0, bad("Name must contain 1–256 characters")
			}
			_, e := s.exec(r.Context(), "UPDATE projects SET name=? WHERE id=? AND account_id=?", name, pid, account.ID)
			project["name"] = name
			return s.projectResponseWithOwner(project, handles), 200, e
		}
		return nil, 0, missing("Not found")
	}
	switch parts[2] {
	case "set-active":
		if len(parts) == 3 && r.Method == "POST" {
			query := "UPDATE projects SET suspended_at=NULL WHERE account_id=?"
			args := []any{account.ID}
			if s.cfg.Records != nil {
				// A legacy sibling must never mutate a canonical project outside
				// that project's ownership/publication scope.
				query += " AND id=?"
				args = append(args, pid)
			}
			_, e := s.exec(r.Context(), query, args...)
			return s.projectResponseWithOwner(project, handles), 200, e
		}
	case "api-keys":
		return s.keys(r, project, parts[3:])
	case "releases":
		return s.releases(r, project, parts[3:])
	case "request-logs":
		if len(parts) == 3 && r.Method == "GET" {
			return s.logs(r, pid)
		}
	case "connect-repo":
		if len(parts) == 3 && r.Method == "POST" && s.cfg.ConnectRepository != nil {
			var input map[string]json.RawMessage
			if e := decode(r, &input); e != nil {
				return nil, 0, e
			}
			v, e := s.cfg.ConnectRepository(r.Context(), account.ID, pid, input)
			return v, 200, e
		}
	case "repo-connection":
		if len(parts) == 3 && r.Method == "GET" {
			v, e := s.one(r.Context(), "SELECT project_id,provider,repo_owner,repo_name,default_branch,auth_type,webhook_id,github_installation_id IS NOT NULL AS github_installation_configured FROM repo_connections WHERE project_id=? ORDER BY created_at ASC,id ASC LIMIT 1", pid)
			if v == nil && e == nil {
				return nil, 0, missing("No repository connection")
			}
			if v != nil {
				if err := s.addPublicationCompletion(r.Context(), account.ID, pid, v); err != nil {
					return nil, 0, err
				}
				v["github_installation_configured"] = boolean(v["github_installation_configured"])
				if str(v["provider"]) == "tangled" {
					if handle := handles.get(str(v["repo_owner"])); handle != "" {
						v["owner_handle"] = handle
					}
				}
				if v["provider"] == "tangled" && s.cfg.ListTangledRepositories != nil {
					binding, e := s.one(r.Context(), "SELECT b.repository_did,b.owner_did,b.is_private FROM atproto_repo_bindings b JOIN repo_connections r ON r.id=b.connection_id WHERE r.project_id=?", pid)
					if e != nil {
						return nil, 0, e
					}
					if binding != nil {
						v["repository_did"] = binding["repository_did"]
						v["owner_did"] = binding["owner_did"]
						v["is_private"] = boolean(binding["is_private"])
					}
				}
			}
			return v, 200, e
		}
	case "skill-usage":
		if len(parts) == 3 && r.Method == "GET" {
			return s.usage(r, project)
		}
	case "skill-runtime":
		if len(parts) == 3 {
			return s.runtime(r, project)
		}
	case "catalog":
		if len(parts) == 3 && r.Method == "GET" && s.cfg.Catalog != nil {
			v, e := s.cfg.Catalog.Catalog(r.Context(), pid)
			return v, 200, e
		}
	case "catalog-markdown":
		if len(parts) == 3 && r.Method == "PATCH" && s.cfg.Catalog != nil {
			var body struct {
				Markdown string `json:"markdown"`
			}
			if e := decode(r, &body); e != nil {
				return nil, 0, e
			}
			if utf8.RuneCountInString(body.Markdown) > 512*1024 {
				return nil, 0, bad("Catalog markdown is too large")
			}
			value := optional(strings.TrimSpace(body.Markdown))
			if _, e := s.db.Batch(r.Context(), []turso.Statement{{SQL: "UPDATE projects SET mcp_catalog_markdown_override=? WHERE id=? AND account_id=?", Args: values(value, pid, account.ID)}, revisionStatement(pid)}); e != nil {
				return nil, 0, e
			}
			v, e := s.cfg.Catalog.Catalog(r.Context(), pid)
			if e != nil {
				return nil, 0, e
			}
			return row{"catalog_markdown": v["catalog_markdown"], "catalog_markdown_generated": v["catalog_markdown_generated"], "catalog_markdown_override": value}, 200, nil
		}
	case "sync":
		if len(parts) == 3 && r.Method == "POST" && s.cfg.Sync != nil {
			limit, window := s.cfg.SyncLimit, s.cfg.SyncWindowSeconds
			if limit < 1 {
				limit = 60
			}
			if window < 1 {
				window = 3600
			}
			continuation := false
			if s.cfg.SyncContinuation != nil {
				var err error
				continuation, err = s.cfg.SyncContinuation(r.Context(), pid)
				if err != nil {
					return nil, 0, err
				}
			}
			if !continuation {
				stamp := s.now().Unix() / window * window
				permitted, err := s.one(r.Context(), "INSERT INTO mcp_rate_limits(bucket_key,window_start,count) VALUES(?,?,1) ON CONFLICT(bucket_key,window_start) DO UPDATE SET count=count+1 WHERE count<? RETURNING count", "sync:"+account.ID+":"+pid, stamp, limit)
				if err != nil {
					return nil, 0, err
				}
				if permitted == nil {
					return nil, 0, apiError{429, "Sync rate limit exceeded"}
				}
			}
			result, err := s.cfg.Sync(r.Context(), pid)
			if err != nil {
				return nil, 0, err
			}
			if result["status"] == "publishing" {
				return result, 202, nil
			}
			if _, ok := s.verifiedXRPC(r.Context()); ok {
				return result, 200, nil
			}
			return nil, 204, nil
		}
	case "custom-domain":
		if s.cfg.Domains != nil {
			if len(parts) == 3 && r.Method == "GET" {
				v, e := s.cfg.Domains.Get(r.Context(), pid)
				return v, 200, e
			}
			if len(parts) == 3 && r.Method == "POST" {
				var body struct {
					Hostname string `json:"hostname"`
				}
				if e := decode(r, &body); e != nil {
					return nil, 0, e
				}
				v, e := s.cfg.Domains.Set(r.Context(), pid, body.Hostname)
				return v, 200, e
			}
			if len(parts) == 4 && parts[3] == "verify" && r.Method == "POST" {
				v, e := s.cfg.Domains.Verify(r.Context(), pid)
				return v, 200, e
			}
		}
	case "dashboard":
		if len(parts) == 4 && r.Method == "GET" {
			return s.dashboard(r, account.ID, pid, parts[3])
		}
	}
	return nil, 0, missing("Not found")
}
func (s *Service) account(r *http.Request) (*identity.Account, error) {
	if authorization, ok := s.verifiedXRPC(r.Context()); ok {
		account, err := s.auth.Account(r.Context(), authorization.accountID)
		if err != nil {
			return nil, err
		}
		if account == nil || !strings.EqualFold(account.ID, authorization.accountID) {
			return nil, apiError{401, "Invalid API account"}
		}
		return account, nil
	}
	c, e := r.Cookie("vapor-session")
	if e != nil || len(c.Value) != 64 {
		return nil, apiError{401, "Not authenticated"}
	}
	payload, e := s.auth.ReadSession(r.Context(), c.Value)
	if errors.Is(e, identity.ErrUnauthenticated) {
		return nil, apiError{401, "Not authenticated"}
	}
	if e != nil {
		return nil, e
	}
	id, e := uuid(payload["accountId"])
	if e != nil {
		return nil, apiError{401, "Invalid session"}
	}
	a, e := s.auth.Account(r.Context(), id)
	if e != nil {
		return nil, e
	}
	if a == nil {
		return nil, apiError{401, "Invalid session"}
	}
	return a, nil
}
func (s *Service) validOrigin(r *http.Request) bool {
	if _, ok := s.verifiedXRPC(r.Context()); ok {
		return true
	}
	raw := strings.TrimSpace(r.Header.Get("Origin"))
	if raw == "" {
		u, e := url.Parse(r.Header.Get("Referer"))
		if e != nil || u.User != nil {
			return false
		}
		raw = u.Scheme + "://" + u.Host
	}
	for _, base := range s.cfg.FrontendOrigins {
		if strings.EqualFold(raw, base) {
			return true
		}
	}
	return false
}

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func uuid(raw string) (string, error) {
	if !uuidPattern.MatchString(raw) {
		return "", errors.New("invalid UUID")
	}
	return strings.ToUpper(raw), nil
}
func decode(r *http.Request, out any) error {
	raw, e := io.ReadAll(io.LimitReader(r.Body, (4<<20)+1))
	if e != nil || len(raw) > 4<<20 {
		return bad("JSON body exceeds size limit")
	}
	d := json.NewDecoder(strings.NewReader(string(raw)))
	if e := d.Decode(out); e != nil {
		return bad("Invalid JSON body")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return bad("Invalid JSON body")
	}
	return nil
}

func str(v any) string {
	if v == nil {
		return ""
	}
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return string(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	}
	return ""
}
func num(v any) float64 { n, _ := strconv.ParseFloat(str(v), 64); return n }
func boolean(v any) bool {
	if b, ok := v.(bool); ok {
		return b
	}
	return num(v) != 0
}
func optional(v string) any {
	if v == "" {
		return nil
	}
	return v
}

// Foundation JSONSerialization (used by the Swift gateway) accepts top-level
// objects/arrays here, and rejects JSON fragments unless explicitly enabled.
func structuredJSON(raw string) (any, bool) {
	var value any
	if json.Unmarshal([]byte(raw), &value) != nil {
		return nil, false
	}
	switch value.(type) {
	case map[string]any, []any:
		return value, true
	}
	return nil, false
}
func requiredDate(v any) any {
	if v == nil {
		return ""
	}
	return date(v)
}
func date(v any) any {
	if v == nil {
		return nil
	}
	n := num(v)
	return time.Unix(int64(n), 0).UTC().Format(time.RFC3339)
}
func values(args ...any) []turso.Value {
	out := make([]turso.Value, len(args))
	for i, v := range args {
		switch x := v.(type) {
		case nil:
			out[i] = turso.Null()
		case string:
			out[i] = turso.Text(x)
		case bool:
			if x {
				out[i] = turso.Integer(1)
			} else {
				out[i] = turso.Integer(0)
			}
		case int:
			out[i] = turso.Integer(int64(x))
		case int64:
			out[i] = turso.Integer(x)
		case float64:
			out[i] = turso.Value{Type: "float", Value: x}
		default:
			panic("unsupported SQL bind")
		}
	}
	return out
}
func (s *Service) exec(ctx context.Context, sql string, args ...any) (*turso.Result, error) {
	return s.db.Execute(ctx, turso.Statement{SQL: sql, Args: values(args...)})
}
func (s *Service) query(ctx context.Context, sql string, args ...any) ([]row, error) {
	r, e := s.db.Execute(ctx, turso.Statement{SQL: sql, Args: values(args...), WantRows: true})
	if e != nil {
		return nil, e
	}
	out := make([]row, 0, len(r.Rows))
	for _, cells := range r.Rows {
		if len(cells) != len(r.Columns) {
			return nil, errors.New("invalid database row")
		}
		item := row{}
		for i, cell := range cells {
			if r.Columns[i].Name == nil {
				return nil, errors.New("missing database column")
			}
			var v any
			if cell.Type != "null" {
				v = cell.Value
				if cell.Type == "integer" {
					n, e := strconv.ParseInt(str(v), 10, 64)
					if e != nil {
						return nil, e
					}
					v = n
				}
			}
			item[*r.Columns[i].Name] = v
		}
		out = append(out, item)
	}
	return out, nil
}
func (s *Service) one(ctx context.Context, sql string, args ...any) (row, error) {
	rows, e := s.query(ctx, sql, args...)
	if e != nil || len(rows) == 0 {
		return nil, e
	}
	return rows[0], nil
}
