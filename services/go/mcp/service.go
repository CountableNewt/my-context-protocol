// Package mcp serves authenticated, tenant-bound MCP over stateless HTTP.
package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	skillruntime "github.com/CountableNewt/my-context-protocol/services/go/runtime"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type Config struct {
	ProjectContext      func(context.Context, Project) (context.Context, error)
	Path                string
	BaseDomain          string
	RequireTenantHost   bool
	TrustForwardedHost  bool
	TrustVercelClientIP bool
	AllowedOrigins      []string
	FrontendOrigin      string
	OAuthEnabled        bool
	RateLimitPerMinute  int
	StreamDuration      time.Duration
	PollInterval        time.Duration
}
type Service struct {
	config Config
	store  Store
	now    func() time.Time
}

func New(config Config, store Store) (*Service, error) {
	if store == nil {
		return nil, errors.New("MCP store is required")
	}
	if strings.ContainsAny(config.Path, "?#\\\r\n") {
		return nil, errors.New("invalid MCP path")
	}
	segments := []string{}
	for _, part := range strings.Split(strings.TrimSpace(config.Path), "/") {
		if part == "" {
			continue
		}
		if part == "." || part == ".." || strings.ContainsAny(part, "% \t") {
			return nil, errors.New("invalid MCP path")
		}
		segments = append(segments, part)
	}
	if len(segments) == 0 {
		segments = []string{"mcp"}
	}
	config.Path = "/" + strings.Join(segments, "/")
	normalizedOrigins := []string{}
	for _, raw := range config.AllowedOrigins {
		origin, err := url.Parse(strings.TrimRight(strings.TrimSpace(raw), "/"))
		if err != nil || origin.Host == "" || origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" || origin.Scheme != "http" && origin.Scheme != "https" {
			return nil, errors.New("invalid MCP allowed origin")
		}
		origin.Host = strings.ToLower(origin.Host)
		normalizedOrigins = append(normalizedOrigins, origin.String())
	}
	config.AllowedOrigins = normalizedOrigins
	config.BaseDomain = strings.ToLower(strings.Trim(strings.TrimSpace(config.BaseDomain), "."))
	if strings.ContainsAny(config.BaseDomain, "/:?#@\\ \r\n") {
		return nil, errors.New("invalid MCP base domain")
	}
	if config.StreamDuration <= 0 {
		config.StreamDuration = 25 * time.Second
	}
	if config.StreamDuration > 50*time.Second {
		config.StreamDuration = 50 * time.Second
	}
	if config.PollInterval <= 0 {
		config.PollInterval = 5 * time.Second
	}
	return &Service{config: config, store: store, now: time.Now}, nil
}
func (s *Service) ResolveTenant(r *http.Request) (*Project, error) {
	host := r.Host
	if s.config.TrustForwardedHost {
		if forwarded := r.Header.Get("X-Forwarded-Host"); forwarded != "" {
			if strings.Contains(forwarded, ",") {
				return nil, errors.New("ambiguous forwarded host")
			}
			host = forwarded
		}
	}
	host = strings.ToLower(strings.TrimSpace(host))
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.TrimSuffix(host, ".")
	if host == "" || strings.ContainsAny(host, "/@?#\\ \r\n\t") {
		return nil, errors.New("invalid host")
	}
	return s.store.HostProject(r.Context(), host, s.config.BaseDomain)
}
func (s *Service) PublicOrigin(r *http.Request) string {
	host := r.Host
	if s.config.TrustForwardedHost && r.Header.Get("X-Forwarded-Host") != "" {
		host = r.Header.Get("X-Forwarded-Host")
	}
	if strings.ContainsAny(host, "/@?#\\ \r\n\t,") {
		return ""
	}
	scheme := "https"
	hostname := host
	if h, _, err := net.SplitHostPort(host); err == nil {
		hostname = h
	}
	if hostname == "localhost" || hostname == "127.0.0.1" || hostname == "[::1]" || hostname == "::1" {
		scheme = "http"
	}
	return scheme + "://" + host
}
func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.URL.Path == "/" && r.Method == "GET" {
		tenant, err := s.ResolveTenant(r)
		if err != nil {
			s.plain(w, 503, "Tenant routing unavailable")
			return
		}
		if tenant != nil && s.config.OAuthEnabled {
			s.unauthorized(w, r)
			return
		}
		w.Write([]byte("MyContextProtocol"))
		return
	}
	if r.URL.Path == "/health" && r.Method == "GET" {
		w.Write([]byte("ok"))
		return
	}
	if r.URL.Path != s.config.Path && r.URL.Path != s.config.Path+"/events" && r.URL.Path != s.config.Path+"/ping" {
		http.NotFound(w, r)
		return
	}
	if r.Method == "OPTIONS" {
		if !s.allowOrigin(w, r) {
			s.plain(w, 403, "Invalid origin")
			return
		}
		requested := r.Header.Get("Access-Control-Request-Method")
		if requested != "GET" && requested != "POST" && requested != "HEAD" {
			s.plain(w, 405, "Method not allowed")
			return
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, HEAD, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-API-Key, MCP-Protocol-Version, MCP-Session-Id, Last-Event-ID, Accept")
		w.Header().Set("Access-Control-Max-Age", "600")
		w.WriteHeader(204)
		return
	}
	tenant, err := s.ResolveTenant(r)
	if err != nil {
		s.plain(w, 503, "Tenant routing unavailable")
		return
	}
	if s.config.RequireTenantHost && tenant == nil {
		s.plain(w, 403, "MCP is only available on your project MCP hostname")
		return
	}
	if allowed, err := s.AllowIngress(r); err != nil {
		s.plain(w, 503, "Rate limiting unavailable")
		return
	} else if !allowed {
		w.Header().Set("Retry-After", "60")
		s.plain(w, 429, "Rate limited")
		return
	}
	if r.URL.Path == s.config.Path+"/ping" {
		if r.Method != "GET" && r.Method != "HEAD" && r.Method != "POST" {
			s.plain(w, 405, "Method not allowed")
			return
		}
		if r.Method == "POST" {
			body, err := io.ReadAll(io.LimitReader(r.Body, 16*1024+1))
			if err != nil || len(body) > 16*1024 {
				s.plain(w, 413, "Ping body too large")
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Method != "HEAD" {
			json.NewEncoder(w).Encode(map[string]string{"status": "ok", "service": "MyContextProtocol"})
		}
		return
	}
	if r.Method != "POST" && r.Method != "GET" {
		s.plain(w, 405, "Method not allowed")
		return
	}
	if r.URL.Path == s.config.Path+"/events" && r.Method != "GET" {
		s.plain(w, 405, "Method not allowed")
		return
	}
	if status, message := s.transport(r); status != 0 {
		s.plain(w, status, message)
		return
	}
	if r.Header.Get("Origin") != "" {
		s.allowOrigin(w, r)
	}
	principal, err := s.authenticate(r)
	if errors.Is(err, ErrCredential) {
		s.unauthorized(w, r)
		return
	}
	if err != nil {
		s.plain(w, 503, "Credential service unavailable")
		return
	}
	if tenant != nil && tenant.ID != principal.Project.ID {
		s.plain(w, 403, "Credential does not match host")
		return
	}
	if s.config.ProjectContext != nil {
		ctx, e := s.config.ProjectContext(r.Context(), principal.Project)
		if e != nil {
			s.plain(w, 503, "Canonical project is temporarily unavailable")
			return
		}
		r = r.WithContext(ctx)
	}
	revision, err := s.store.Revision(r.Context(), principal.Project.ID)
	if err != nil {
		s.plain(w, 503, "Catalog revision unavailable")
		return
	}
	w.Header().Set("X-MCP-Catalog-Revision", strconv.FormatInt(revision, 10))
	if r.Method == "GET" {
		s.stream(w, r, principal, revision)
		return
	}
	s.rpc(w, r, principal)
}

// AllowIngress applies the shared persistent quota to MCP and OAuth ingress.
// Callers routing OAuth separately must invoke this once before its handler.
func (s *Service) AllowIngress(r *http.Request) (bool, error) {
	if s.config.RateLimitPerMinute <= 0 {
		return true, nil
	}
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		ip = r.RemoteAddr
	}
	if ip == "" {
		ip = "unknown"
	}
	if s.config.TrustVercelClientIP {
		if raw := strings.TrimSpace(r.Header.Get("X-Vercel-Forwarded-For")); net.ParseIP(raw) != nil {
			ip = raw
		}
	}
	return s.store.RateLimit(r.Context(), ip, s.config.RateLimitPerMinute, s.now())
}

// Path returns the normalized public MCP path used for metadata and routing.
func (s *Service) Path() string { return s.config.Path }
func (s *Service) allowOrigin(w http.ResponseWriter, r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	for _, allowed := range s.config.AllowedOrigins {
		if strings.EqualFold(origin, allowed) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Access-Control-Expose-Headers", "WWW-Authenticate, X-MCP-Catalog-Revision, MCP-Session-Id, X-Myctx-Telemetry-Publication")
			w.Header().Add("Vary", "Origin")
			return true
		}
	}
	return false
}
func (s *Service) authenticate(r *http.Request) (Principal, error) {
	if value := r.Header.Get("X-API-Key"); value != "" {
		return s.store.Authenticate(r.Context(), value, false, s.now())
	}
	authorization := r.Header.Get("Authorization")
	parts := strings.Fields(authorization)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return Principal{}, ErrCredential
	}
	return s.store.Authenticate(r.Context(), parts[1], s.config.OAuthEnabled, s.now())
}
func (s *Service) unauthorized(w http.ResponseWriter, r *http.Request) {
	if s.config.OAuthEnabled {
		if origin := s.PublicOrigin(r); origin != "" {
			w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token", resource_metadata="`+origin+"/.well-known/oauth-protected-resource"+s.config.Path+`", scope="mcp:invoke"`)
		}
	}
	s.plain(w, 401, "Unauthorized")
}
func (s *Service) transport(r *http.Request) (int, string) {
	if version := strings.TrimSpace(r.Header.Get("MCP-Protocol-Version")); version != "" && !supported(version) {
		return 400, "Unsupported MCP-Protocol-Version"
	}
	accept := strings.ToLower(r.Header.Get("Accept"))
	if accept != "" && accept != "*/*" {
		if r.Method == "GET" && !strings.Contains(accept, "text/event-stream") {
			return 400, "Accept must include text/event-stream"
		}
		if r.Method == "POST" && (!strings.Contains(accept, "text/event-stream") || !strings.Contains(accept, "application/json")) {
			return 400, "Accept must include application/json and text/event-stream"
		}
	}
	if raw := r.Header.Get("Origin"); raw != "" {
		allowed := false
		for _, origin := range s.config.AllowedOrigins {
			if strings.EqualFold(strings.TrimSpace(raw), origin) {
				allowed = true
			}
		}
		if !allowed {
			return 403, "Invalid origin"
		}
	}
	return 0, ""
}
func (s *Service) plain(w http.ResponseWriter, status int, message string) {
	http.Error(w, message, status)
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}
type rpcParams struct {
	Name            string                     `json:"name"`
	URI             string                     `json:"uri"`
	Cursor          string                     `json:"cursor"`
	ProtocolVersion string                     `json:"protocolVersion"`
	Arguments       map[string]json.RawMessage `json:"arguments"`
}

func (s *Service) rpc(w http.ResponseWriter, r *http.Request, principal Principal) {
	start := time.Now()
	if contentType := strings.ToLower(r.Header.Get("Content-Type")); contentType != "" && !strings.HasPrefix(contentType, "application/json") {
		s.rpcError(w, nil, -32700, "Parse error")
		return
	}
	request := rpcRequest{}
	raw, err := io.ReadAll(io.LimitReader(r.Body, (2<<20)+1))
	if err != nil || len(raw) > 2<<20 {
		s.rpcError(w, nil, -32700, "Parse error")
		return
	}
	if !utf8.Valid(raw) || json.Unmarshal(raw, &request) != nil || request.Method == "" {
		s.rpcError(w, nil, -32700, "Parse error")
		return
	}
	notification := request.Method == "notifications/initialized" || request.Method == "notifications/cancelled"
	if request.JSONRPC != "2.0" || notification && request.ID != nil || !notification && (request.ID == nil || string(request.ID) == "null" || !validID(request.ID)) {
		s.rpcError(w, request.ID, -32600, "Invalid Request: invalid JSON-RPC envelope")
		return
	}
	if notification {
		w.WriteHeader(202)
		return
	}
	params := rpcParams{}
	if len(request.Params) > 0 && string(request.Params) != "null" {
		if request.Params[0] != '{' || json.Unmarshal(request.Params, &params) != nil {
			s.rpcError(w, request.ID, -32602, "Invalid params")
			return
		}
	}
	snapshot, err := s.store.Snapshot(r.Context(), principal.Project.ID)
	if err != nil {
		s.rpcError(w, request.ID, -32603, "Catalog unavailable")
		return
	}
	version := strings.TrimSpace(r.Header.Get("MCP-Protocol-Version"))
	if version == "" {
		version = "2025-03-26"
	}
	result, code, message, observations := s.dispatch(r.Context(), snapshot, principal, request.Method, params, version)
	status := 200
	var encodedResponse []byte
	if code == 0 {
		encodedResponse, err = json.Marshal(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
		if err != nil || len(encodedResponse) > 4<<20 {
			code = -32603
			message = "Response exceeds the server limit; narrow the request or read individual skills"
			observations = nil
		}
	}
	if code != 0 {
		status = rpcStatus(code)
	}
	var telemetryErr error
	if snapshot.Settings.TelemetryEnabled && len(observations) > 0 {
		telemetryErr = s.store.Record(r.Context(), snapshot.ProjectID, principal.ClientIdentity, observations)
	}
	kind, key := "", ""
	switch request.Method {
	case "tools/call":
		kind = "tool"
		key = params.Name
	case "resources/read":
		kind = "resource"
		key = params.URI
	case "prompts/get":
		kind = "prompt"
		key = params.Name
	}
	if telemetryErr == nil || s.telemetryPublication(r.Context(), telemetryErr) == "" {
		telemetryErr = s.store.Log(r.Context(), RequestLog{ProjectID: snapshot.ProjectID, ReleaseID: snapshot.ReleaseID, ClientIdentity: principal.ClientIdentity, Method: request.Method, LatencyMS: time.Since(start).Milliseconds(), Status: status, ErrorCode: code, ErrorMessage: message, CapabilityKind: kind, CapabilityKey: key})
	}
	if publication := s.telemetryPublication(r.Context(), telemetryErr); publication != "" {
		w.Header().Set("X-Myctx-Telemetry-Publication", publication)
	}
	if code != 0 {
		s.rpcError(w, request.ID, code, message)
	} else {
		w.Header().Set("Content-Type", "application/json")
		w.Write(encodedResponse)
	}
}
func validID(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	if raw[0] == '"' {
		var value string
		return json.Unmarshal(raw, &value) == nil
	}
	var number json.Number
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if decoder.Decode(&number) != nil {
		return false
	}
	_, err := strconv.ParseInt(string(number), 10, 64)
	return err == nil
}
func rpcStatus(code int) int {
	switch code {
	case -32601:
		return 404
	case -32603:
		return 500
	default:
		return 400
	}
}
func (s *Service) rpcError(w http.ResponseWriter, id json.RawMessage, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(rpcStatus(code))
	var responseID any
	if validID(id) {
		responseID = id
	}
	json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": responseID, "error": map[string]any{"code": code, "message": message}})
}
func (s *Service) stream(w http.ResponseWriter, r *http.Request, p Principal, revision int64) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		s.plain(w, 500, "Streaming unavailable")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	fmt.Fprint(w, "retry: 3000\n:connected\n\n")
	flusher.Flush()
	if last := r.Header.Get("Last-Event-ID"); last != "" {
		previous, err := strconv.ParseInt(last, 10, 64)
		if err == nil && previous != revision {
			s.writeCatalogEvents(w, revision)
			s.writeSubscribedEvents(w, r, p, revision)
			flusher.Flush()
		}
	}
	ticker := time.NewTicker(s.config.PollInterval)
	defer ticker.Stop()
	deadline := time.NewTimer(s.config.StreamDuration)
	defer deadline.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-deadline.C:
			fmt.Fprint(w, ":reconnect\n\n")
			flusher.Flush()
			return
		case <-ticker.C:
			current, err := s.store.Revision(r.Context(), p.Project.ID)
			if err != nil {
				return
			}
			if current != revision {
				revision = current
				s.writeCatalogEvents(w, current)
				s.writeSubscribedEvents(w, r, p, current)
			} else {
				fmt.Fprint(w, ":heartbeat\n\n")
			}
			flusher.Flush()
		}
	}
}
func (s *Service) writeSubscribedEvents(w io.Writer, r *http.Request, p Principal, revision int64) {
	uris, err := s.store.Subscriptions(r.Context(), p)
	if err != nil {
		return
	}
	for _, uri := range uris {
		encoded, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "notifications/resources/updated", "params": map[string]string{"uri": uri}})
		fmt.Fprintf(w, "id: %d\ndata: %s\n\n", revision, encoded)
	}
}
func (s *Service) writeCatalogEvents(w io.Writer, revision int64) {
	for _, kind := range []string{"tools", "resources", "prompts"} {
		fmt.Fprintf(w, "id: %d\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/%s/list_changed\"}\n\n", revision, kind)
	}
}
func (s *Service) dispatch(ctx context.Context, snapshot skillruntime.Snapshot, p Principal, method string, params rpcParams, version string) (any, int, string, []Observation) {
	switch method {
	case "initialize":
		negotiated := strings.TrimSpace(params.ProtocolVersion)
		if !supported(negotiated) {
			negotiated = "2025-11-25"
		}
		instructions := fmt.Sprintf("You are connected to MyContextProtocol project %q.\nStart by calling `resolve_context` with the current user request and available tools. It returns active and suggested skills, conflicts, provenance, capability bindings, and a resolution trace.\nUse `get_skill` for the complete versioned skill document and `report_skill_feedback` for observed missing, ambiguous, incorrect, conflicting or outdated guidance.\nPrefer tools for callable procedures; resources provide long Markdown context through ctx://skill/... URIs, and prompts provide reusable templates.\nIf catalogs are empty, sync the connected Git repository and activate a ready release in the dashboard.", snapshot.ProjectName)
		if snapshot.Settings.TelemetryEnabled {
			instructions += "\nAt task completion, send one best-effort report_skill_usage batch with a stable report_id and exact skill versions and release_id. Report only actual uses or consciously considered skips; no reasoning transcripts or explanatory notes. Missing reports remain unknown."
		}
		serverInfo := map[string]any{"name": "MyContextProtocol", "version": "1.2.0", "title": "MyContextProtocol — " + snapshot.ProjectName, "description": fmt.Sprintf("Hosted MCP skills for project %q (MyContextProtocol).", snapshot.ProjectName)}
		if s.config.FrontendOrigin != "" {
			serverInfo["websiteUrl"] = strings.TrimRight(s.config.FrontendOrigin, "/") + "/projects/" + snapshot.ProjectID
		}
		return map[string]any{"protocolVersion": negotiated, "capabilities": map[string]any{"tools": map[string]bool{"listChanged": true}, "resources": map[string]bool{"subscribe": true, "listChanged": true}, "prompts": map[string]bool{"listChanged": true}}, "serverInfo": serverInfo, "instructions": instructions}, 0, "", nil
	case "tools/list", "resources/list", "prompts/list":
		kind := strings.TrimSuffix(method, "/list")
		items := tools(snapshot, version)
		scope := fmt.Sprintf("tools:%s:%s:%t", snapshot.ProjectID, emptyRelease(snapshot.ReleaseID), snapshot.Settings.LegacyCompiledTools)
		if kind == "resources" {
			items = resources(snapshot)
			scope = "resources:" + snapshot.ProjectID + ":" + snapshot.ReleaseID
		}
		if kind == "prompts" {
			items = prompts(snapshot)
			scope = "prompts:" + snapshot.ProjectID + ":" + snapshot.ReleaseID
		}
		if snapshot.ReleaseID == "" && kind != "tools" && params.Cursor != "" {
			return nil, -32602, "Invalid pagination cursor", nil
		}
		values, next, err := page(items, params.Cursor, scope)
		if err != nil {
			return nil, -32602, err.Error(), nil
		}
		result := map[string]any{kind: values}
		if next != "" {
			result["nextCursor"] = next
		}
		observations := []Observation{}
		for _, entry := range values {
			for _, cap := range snapshot.Capabilities {
				match := kind == "tools" && cap.Type == "tool" && entry["name"] == cap.Name || kind == "prompts" && cap.Type == "prompt" && entry["name"] == cap.Name
				if kind == "resources" && cap.Type == "resource" {
					meta, ok := resourceMetadata(cap)
					match = ok && entry["uri"] == meta.URI
				}
				if match {
					if skill := findCompiled(snapshot, cap.SkillID); skill != nil {
						observations = append(observations, Observation{Skill: skill, Type: "surfaced", Source: method})
					}
				}
			}
		}
		return result, 0, "", observations
	case "resources/subscribe", "resources/unsubscribe":
		uri := strings.TrimSpace(params.URI)
		if uri == "" {
			return nil, -32602, "Invalid params: missing uri", nil
		}
		allowed, err := s.store.Subscribe(ctx, p, uri, method == "resources/subscribe")
		if err != nil {
			return nil, -32603, "Subscription storage unavailable", nil
		}
		if !allowed {
			return nil, -32000, "Resource subscription limit exceeded", nil
		}
		return map[string]any{}, 0, "", nil
	case "resources/read":
		return s.readResource(ctx, snapshot, params.URI)
	case "prompts/get":
		if params.Name == "" || strings.Contains(params.Name, ":") {
			return nil, -32602, "Prompt not found", nil
		}
		for _, cap := range snapshot.Capabilities {
			if cap.Type == "prompt" && cap.Name == params.Name {
				skill := findCompiled(snapshot, cap.SkillID)
				if skill == nil {
					continue
				}
				text := skill.Body
				if text == "" {
					text = skill.Summary
				}
				if len(params.Arguments) > 0 {
					keys := []string{}
					for key := range params.Arguments {
						keys = append(keys, key)
					}
					sortStrings(keys)
					lines := []string{}
					for _, key := range keys {
						var value any
						if json.Unmarshal(params.Arguments[key], &value) != nil {
							return nil, -32602, "Invalid prompt arguments", nil
						}
						raw, _ := json.Marshal(value)
						lines = append(lines, key+": "+string(raw))
					}
					text = "Context:\n" + strings.Join(lines, "\n") + "\n\n" + text
				}
				observations := []Observation{}
				if skill.Body != "" {
					observations = append(observations, Observation{Skill: skill, Type: "instructions_delivered", Source: method})
				}
				return map[string]any{"description": skill.Summary, "messages": []any{map[string]any{"role": "user", "content": map[string]any{"type": "text", "text": text}}}}, 0, "", observations
			}
		}
		return nil, -32602, "Prompt not found", nil
	case "tools/call":
		if params.Name == "" {
			return nil, -32602, "Invalid params: missing name", nil
		}
		return s.callTool(ctx, snapshot, p, params, version)
	}
	return nil, -32601, "Method not found", nil
}
func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}
func emptyRelease(value string) string {
	if value == "" {
		return "none"
	}
	return value
}
func (s *Service) readResource(ctx context.Context, snapshot skillruntime.Snapshot, rawURI string) (any, int, string, []Observation) {
	uri := strings.TrimSpace(rawURI)
	if uri == "" {
		return nil, -32602, "Invalid params: missing uri", nil
	}
	ref, err := skillruntime.ParseResourceURI(uri)
	if err != nil {
		return nil, -32602, err.Error(), nil
	}
	if ref != nil {
		skill, err := snapshot.FindSkill(ref.SkillID, ref.Version)
		if err != nil {
			return nil, -32602, err.Error(), nil
		}
		entry := map[string]any{"uri": uri, "mimeType": "text/markdown", "text": skill.Document.Instructions}
		kind := "instructions_delivered"
		if ref.Path != "" {
			file, err := s.store.File(ctx, *skill, ref.Path)
			if err != nil {
				return nil, -32602, "Package file not found", nil
			}
			entry = map[string]any{"uri": uri, "mimeType": file.MediaType}
			if utf8.Valid(file.Content) {
				entry["text"] = string(file.Content)
			} else {
				entry["blob"] = base64.StdEncoding.EncodeToString(file.Content)
			}
			kind = "supporting_file_read"
		}
		return map[string]any{"contents": []any{entry}}, 0, "", []Observation{{Skill: skill, Type: kind, Source: "resources/read"}}
	}
	for _, cap := range snapshot.Capabilities {
		meta, ok := resourceMetadata(cap)
		if cap.Type != "resource" || !ok || meta.URI != uri {
			continue
		}
		skill := findCompiled(snapshot, cap.SkillID)
		if skill == nil {
			continue
		}
		text := skill.Body
		if text == "" {
			text = skill.Summary
		}
		text = resourcePreamble(meta, skill.Summary) + text
		return map[string]any{"contents": []any{map[string]any{"uri": uri, "mimeType": meta.MIME, "text": text}}}, 0, "", []Observation{{Skill: skill, Type: "instructions_delivered", Source: "resources/read"}}
	}
	return nil, -32602, "Resource not found", nil
}
func resourcePreamble(meta resourceMeta, summary string) string {
	lines := []string{}
	if meta.InvokeFirst {
		lines = append(lines, "**Invoke first:** consider loading this resource before other skills on the same task.")
	}
	for _, part := range []struct {
		label  string
		values []string
	}{{"**Read when:**", meta.UseWhen}, {"**Skip when:**", meta.AvoidWhen}, {"**Failure modes / fallbacks:**", meta.FailureModes}} {
		if len(part.values) > 0 {
			lines = append(lines, part.label)
			for _, value := range part.values {
				lines = append(lines, "- "+value)
			}
		}
	}
	if len(lines) == 0 {
		return ""
	}
	text := "<!-- MyContextProtocol: agent routing (from SKILL front matter) -->\n\n" + strings.Join(lines, "\n") + "\n\n---\n\n"
	if strings.TrimSpace(summary) != "" {
		text += "*Summary:* " + strings.TrimSpace(summary) + "\n\n---\n\n"
	}
	return text
}
