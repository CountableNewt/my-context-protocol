package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"
)

type Options struct {
	Words []string
	Flags map[string][]string
}

var booleanFlags = strings.Fields("help version no-browser session-stdin compact json")
var valueFlags = strings.Fields("url web-url profile config project id release-id compiled-skill-id key-id data query set timeout provider handle token session-id service-did")

func contains(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}
func (o Options) Get(key string) string {
	v := o.Flags[key]
	if len(v) == 0 {
		return ""
	}
	return v[len(v)-1]
}
func ParseOptions(args []string) (Options, error) {
	o := Options{Flags: map[string][]string{}}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "-h" {
			a = "--help"
		}
		if !strings.HasPrefix(a, "--") {
			o.Words = append(o.Words, a)
			continue
		}
		p := strings.SplitN(a[2:], "=", 2)
		k := p[0]
		if contains(booleanFlags, k) {
			if len(p) > 1 {
				return o, Usage("--" + k + " does not take a value")
			}
			o.Flags[k] = []string{"true"}
			continue
		}
		if !contains(valueFlags, k) {
			return o, Usage("Unknown flag --" + k)
		}
		var v string
		if len(p) == 2 {
			v = p[1]
		} else {
			i++
			if i == len(args) {
				return o, Usage("--" + k + " requires a value")
			}
			v = args[i]
		}
		if k != "query" && k != "set" && len(o.Flags[k]) > 0 {
			return o, Usage("--" + k + " may only be specified once")
		}
		o.Flags[k] = append(o.Flags[k], v)
	}
	return o, nil
}
func jsonErrors(args []string) bool {
	for i := 0; i < len(args); i++ {
		if args[i] == "--json" {
			return true
		}
		if strings.HasPrefix(args[i], "--") && contains(valueFlags, args[i][2:]) {
			i++
		}
	}
	return false
}
func decodeJSON(data []byte) (any, error) {
	// Match the existing bounded nesting contract and preserve arbitrary numeric tokens.
	depth := 0
	quoted, escaped := false, false
	for _, b := range data {
		if quoted {
			if escaped {
				escaped = false
			} else if b == '\\' {
				escaped = true
			} else if b == '"' {
				quoted = false
			}
			continue
		}
		if b == '"' {
			quoted = true
		}
		if b == '[' || b == '{' {
			depth++
			if depth >= 128 {
				return nil, Usage("JSON nesting exceeds 128 levels")
			}
		}
		if b == ']' || b == '}' {
			depth--
		}
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		return nil, Usage("Invalid JSON")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return nil, Usage("Invalid JSON")
	}
	return v, nil
}
func boundedRead(r io.Reader, limit int64) ([]byte, error) {
	b, e := io.ReadAll(io.LimitReader(r, limit+1))
	if e != nil {
		return nil, e
	}
	if int64(len(b)) > limit {
		return nil, Usage(fmt.Sprintf("Input exceeds %d bytes", limit))
	}
	return b, nil
}
func requestBody(o Options, input io.Reader) ([]byte, error) {
	var value any
	present := false
	if source := o.Get("data"); source != "" {
		present = true
		var b []byte
		var e error
		switch {
		case source == "-":
			b, e = boundedRead(input, 8<<20)
		case strings.HasPrefix(source, "@"):
			var f *os.File
			f, e = os.Open(source[1:])
			if e == nil {
				b, e = boundedRead(f, 8<<20)
				f.Close()
			}
		default:
			b = []byte(source)
			if len(b) > 8<<20 {
				e = Usage("Request exceeds 8 MiB")
			}
		}
		if e != nil {
			return nil, Usage("Invalid request body: " + e.Error())
		}
		value, e = decodeJSON(b)
		if e != nil {
			return nil, e
		}
	}
	if assignments := o.Flags["set"]; len(assignments) > 0 {
		object := map[string]any{}
		if present {
			var ok bool
			object, ok = value.(map[string]any)
			if !ok {
				return nil, Usage("--set requires an object body")
			}
		}
		for _, a := range assignments {
			p := strings.SplitN(a, "=", 2)
			if len(p) != 2 || p[0] == "" {
				return nil, Usage("--set requires field=JSON")
			}
			v, e := decodeJSON([]byte(p[1]))
			if e != nil {
				return nil, e
			}
			object[p[0]] = v
		}
		value = object
		present = true
	}
	if !present {
		return nil, nil
	}
	b, e := json.Marshal(value)
	if len(b) > 8<<20 {
		return nil, Usage("Request exceeds 8 MiB")
	}
	return b, e
}
func duration(raw string) (time.Duration, error) {
	d, e := time.ParseDuration(raw)
	if e != nil || d <= 0 || strings.HasPrefix(raw, "+") {
		return 0, Usage("--timeout must be a positive duration")
	}
	return d, nil
}
func readable(s string) string {
	r := []rune(s)
	if len(r) > 2000 {
		r = r[:2000]
	}
	for i, c := range r {
		if c < 32 || c == 127 {
			r[i] = ' '
		}
	}
	return string(r)
}
func responseJSON(b []byte) any {
	if len(b) == 0 {
		return map[string]any{"ok": true}
	}
	v, e := decodeJSON(b)
	if e == nil {
		return v
	}
	return map[string]any{"result": string(b)}
}

// Run executes one command with injectable process streams and environment.
func Run(ctx context.Context, args []string, env map[string]string, input io.Reader, output, diagnostics io.Writer, version string) int {
	err := run(ctx, args, env, input, output, diagnostics, version)
	if err == nil {
		return 0
	}
	var failure *CLIError
	if !errors.As(err, &failure) {
		failure = &CLIError{Kind: "request", Message: err.Error()}
	}
	if errors.Is(err, context.Canceled) {
		failure = &CLIError{Kind: "canceled", Message: "Command canceled"}
	}
	if failure.Message == "" {
		failure.Message = failure.Error()
	}
	code := failure.ExitCode()
	if jsonErrors(args) {
		name := map[int]string{2: "invalid_arguments", 3: "unauthenticated", 4: "forbidden", 130: "canceled"}[code]
		if name == "" {
			name = "request_failed"
		}
		v := map[string]any{"error": failure.Message, "code": name}
		if failure.Status != 0 {
			v["status"] = failure.Status
			v["details"] = failure.Payload
		}
		b, _ := json.Marshal(v)
		fmt.Fprintln(diagnostics, string(b))
	} else {
		hint := "Check your connection and the selected API origin, then try again."
		fmt.Fprintln(diagnostics, "Error: "+readable(failure.Message))
		if failure.Status != 0 {
			fmt.Fprintf(diagnostics, "HTTP %d\n", failure.Status)
			if details, ok := failure.Payload.(map[string]any); ok {
				for _, k := range []string{"error", "reason", "message"} {
					if s, ok := details[k].(string); ok && s != "" {
						fmt.Fprintln(diagnostics, readable(s))
						break
					}
				}
			}
			switch failure.Status {
			case 401:
				hint = "Run myctx auth login for the selected profile and API origin, then try again."
			case 403:
				hint = "Check your account permissions and service token scope."
			case 404:
				hint = "Check the resource ID and selected API origin."
			case 429:
				hint = "Wait before retrying; the API rate limit was reached."
			default:
				if failure.Status >= 400 && failure.Status < 500 {
					hint = "Check the command arguments and request data with myctx help."
				}
			}
		}
		if code == 2 {
			hint = "Run myctx help or myctx help <group> for usage."
		}
		if code == 130 {
			hint = "Run the command again when ready."
		}
		fmt.Fprintln(diagnostics, "Hint: "+hint)
	}
	return code
}
func run(ctx context.Context, args []string, env map[string]string, input io.Reader, output, diagnostics io.Writer, version string) error {
	o, e := ParseOptions(args)
	if e != nil {
		return e
	}
	name := strings.Join(o.Words, " ")
	emit := func(v any) {
		var b []byte
		if o.Get("compact") != "" {
			b, _ = json.Marshal(v)
		} else {
			b, _ = json.MarshalIndent(v, "", "  ")
		}
		fmt.Fprintln(output, string(b))
	}
	if o.Get("version") != "" || name == "version" {
		emit(map[string]any{"name": "MyContextCLI", "version": version})
		return nil
	}
	if o.Get("help") != "" || name == "" || o.Words[0] == "help" {
		prefix := strings.TrimSpace(strings.TrimPrefix(name, "help"))
		fmt.Fprint(output, "MyContextCLI — agent-friendly MyContextProtocol CLI\n\nUsage: myctx <group> <command> [flags]\nHosted service: https://mycontextprotocol.dev\n\nAuthentication: auth login [--provider github|atproto] [--handle HANDLE_OR_DID] [--service-did DID] [--no-browser] | auth login --session-stdin | auth status | auth logout\nConfiguration: config show | config set --url ORIGIN [--project ID]\nDiscovery: commands (JSON) | api operations (JSON) | help\nRaw API: api METHOD /path [--data JSON|@file|-] [--query key=value]\nRaw XRPC: xrpc NSID [--data JSON|@file|-] [--query key=value]\n\nGlobal flags: --url ORIGIN --web-url ORIGIN --profile NAME --config FILE --project ID --timeout 30s --compact --json\nRequest flags: --id ID --release-id ID --compiled-skill-id ID --key-id ID\n               --data JSON|@file|- --set field=JSON --query key=value (repeatable)\nEnvironment: MYCTX_URL, MYCTX_PROFILE, MYCTX_CONFIG, MYCTX_PROJECT, MYCTX_SESSION, MYCTX_TOKEN, MYCTX_SERVICE_DID (legacy CONTEXT_* also supported)\n\nCommands:\n")
		for _, op := range Operations {
			if prefix == "" || op.Name == prefix || strings.HasPrefix(op.Name, prefix+" ") {
				fmt.Fprintf(output, "  %-28s %s\n", op.Name, op.Description)
			}
		}
		return nil
	}
	if name == "commands" || name == "api operations" {
		emit(Operations)
		return nil
	}
	timeout, e := duration(First(o.Get("timeout"), "30s"))
	if e != nil {
		return e
	}
	path := First(o.Get("config"), ConfigurationPath(env))
	cfg, e := LoadConfiguration(path)
	if e != nil {
		return e
	}
	profileName := First(o.Get("profile"), Environment("PROFILE", env), "default")
	profile := cfg.Profiles[profileName]
	origin, e := ValidateAPIBaseURL(First(o.Get("url"), Environment("URL", env), profile.URL, "https://mycontextprotocol.dev/api"))
	if e != nil {
		return e
	}

	sameOrigin := profile.URL == origin
	if name == "config show" {
		emit(map[string]any{"profile": profileName, "url": origin, "web_url": profile.WebURL, "project": profile.Project, "authenticated": sameOrigin && (profile.OAuth != nil || profile.Session != "") || Environment("TOKEN", env) != "" || Environment("SESSION", env) != "", "config": path})
		return nil
	}
	if name == "config set" {
		if profile.URL != "" && !sameOrigin {
			profile = Profile{}
		}
		profile.URL = origin
		if o.Get("web-url") != "" {
			profile.WebURL, e = ValidateOrigin(o.Get("web-url"))
			if e != nil {
				return e
			}
		}
		if o.Get("project") != "" {
			profile.Project = o.Get("project")
		}
		if o.Get("service-did") != "" {
			profile.ServiceDID = o.Get("service-did")
		}
		cfg.Profiles[profileName] = profile
		if e = cfg.Save(path); e != nil {
			return e
		}
		emit(map[string]any{"ok": true})
		return nil
	}
	client := Client{Origin: origin, Timeout: timeout, Token: First(o.Get("token"), Environment("TOKEN", env))}
	if sameOrigin {
		client.OAuth = profile.OAuth
		client.Session = profile.Session
	}
	client.Session = First(Environment("SESSION", env), client.Session)
	useXRPC := client.Token != "" || (client.OAuth != nil && Environment("SESSION", env) == "")
	if !useXRPC {
		client.OAuth = nil
	}
	client.PersistOAuth = func(credential *OAuthCredential) error {
		profile.OAuth = credential
		cfg.Profiles[profileName] = profile
		return cfg.Save(path)
	}
	if web := First(o.Get("web-url"), Environment("WEB_URL", env), profile.WebURL); web != "" {
		client.WebOrigin, e = ValidateOrigin(web)
		if e != nil {
			return e
		}
	}
	if name == "auth login" {
		provider := First(o.Get("provider"), "github")
		if provider != "github" && provider != "atproto" {
			return Usage("--provider must be github or atproto")
		}
		if provider == "atproto" && o.Get("handle") == "" {
			return Usage("ATProto login requires --handle HANDLE_OR_DID")
		}
		if provider == "github" && o.Get("handle") != "" {
			return Usage("--handle is supported only with --provider atproto")
		}
		if o.Get("session-stdin") != "" && (o.Get("provider") != "" || o.Get("handle") != "") {
			return Usage("--session-stdin cannot be combined with provider login options")
		}
		if !sameOrigin {
			profile = Profile{}
		}
		profile.URL = origin
		if o.Get("session-stdin") != "" {
			raw, err := boundedRead(input, 8192)
			if err != nil {
				return err
			}
			session := strings.TrimSpace(string(raw))
			if session == "" || strings.ContainsAny(session, "\r\n;\x00") {
				return Usage("Stdin must contain a single session cookie value")
			}
			client.Session = session
			client.OAuth = nil
			client.Token = ""
			if _, _, err = client.Checked(ctx, "GET", "/auth/me", nil, nil); err != nil {
				return err
			}
			profile.Session = session
			profile.OAuth = nil
		} else if provider == "atproto" {
			client.PersistOAuth = nil
			client.Token = ""
			client.Session = ""
			client.Origin = strings.TrimSuffix(origin, "/api")
			credential, err := ATProtoLogin(ctx, &client, o.Get("handle"), First(o.Get("service-did"), Environment("SERVICE_DID", env), profile.ServiceDID), o.Get("no-browser") != "", func(s string) { fmt.Fprint(diagnostics, s) })
			if err != nil {
				return err
			}
			if _, _, err = client.Checked(ctx, "GET", "/xrpc/dev.mycontextprotocol.account.getAccount", nil, nil); err != nil {
				return err
			}
			profile.OAuth = client.OAuth
			profile.Session = ""
			profile.ServiceDID = credential.ServiceDID
		} else {
			client.Token = ""
			client.OAuth = nil
			credential, err := BrowserLogin(ctx, &client, o.Get("no-browser") != "", provider, o.Get("handle"), func(s string) { fmt.Fprint(diagnostics, s) })
			if err != nil {
				return err
			}
			profile.Session = credential
			profile.OAuth = nil
		}
		profile.WebURL = client.WebOrigin
		cfg.Profiles[profileName] = profile
		if err := cfg.Save(path); err != nil {
			return err
		}
		emit(map[string]any{"ok": true, "profile": profileName})
		return nil
	}
	if name == "auth logout" && o.Get("session-id") == "" && o.Get("data") == "" && len(o.Flags["set"]) == 0 {
		if useXRPC && client.OAuth != nil {
			client.Origin = strings.TrimSuffix(origin, "/api")
			if err := client.RevokeOAuth(ctx); err != nil {
				return err
			}
		}
		if !useXRPC && client.Session != "" {
			_, _, err := client.Checked(ctx, "POST", "/auth/logout", nil, nil)
			if err != nil {
				var ce *CLIError
				if !errors.As(err, &ce) || ce.Status != 401 {
					return err
				}
			}
		}
		if sameOrigin {
			profile.OAuth = nil
			profile.Session = ""
			cfg.Profiles[profileName] = profile
			if e = cfg.Save(path); e != nil {
				return e
			}
		}
		emit(map[string]any{"ok": true})
		return nil
	}
	if name == "auth status" {
		name = "auth me"
	}
	var op Operation
	found := false
	for _, candidate := range Operations {
		if candidate.Name == name || (len(o.Words) == 2 && o.Words[0] == "xrpc" && candidate.NSID == o.Words[1]) {
			op = candidate
			found = true
			break
		}
	}
	if len(o.Words) > 0 && o.Words[0] == "api" {
		if len(o.Words) != 3 {
			return Usage("Usage: myctx api GET|POST /xrpc/NSID")
		}
		method := strings.ToUpper(o.Words[1])
		route := o.Words[2]
		if !contains([]string{"GET", "POST", "PATCH", "PUT", "DELETE"}, method) {
			return Usage("Raw API calls require GET or POST and an /xrpc/NSID path")
		}
		op = Operation{Method: method, Path: route, NSID: ""}
		if strings.HasPrefix(route, "/xrpc/") {
			op.NSID = strings.TrimPrefix(strings.SplitN(route, "?", 2)[0], "/xrpc/")
		}
		found = true
	}
	if !found {
		return Usage("Unknown command " + name + "; run myctx help")
	}
	if o.Words[0] == "xrpc" {
		useXRPC = true
	}
	if useXRPC {
		if op.NSID == "" && name != "health" && name != "github install" {
			return Usage("This command has no XRPC endpoint")
		}
		client.Origin = strings.TrimSuffix(origin, "/api")
	} else if op.RESTPath != "" {
		op.Method = op.RESTMethod
		op.Path = op.RESTPath
		op.NSID = ""
	} else if op.NSID != "" {
		return Usage("This command requires ATProto authentication or a service token")
	}
	isXRPC := strings.HasPrefix(op.Path, "/xrpc/")
	query := url.Values{}
	for _, q := range o.Flags["query"] {
		p := strings.SplitN(q, "=", 2)
		if len(p) != 2 || p[0] == "" {
			return Usage("--query requires key=value")
		}
		query.Add(p[0], p[1])
	}
	body, e := requestBody(o, input)
	if e != nil {
		return e
	}
	if (op.Method == "GET" || op.Method == "DELETE") && body != nil {
		return Usage(op.Method + " does not accept --data or --set")
	}
	fields := map[string]any{}
	if body != nil && (isXRPC || name == "tangled connect") {
		value, err := decodeJSON(body)
		if err != nil {
			return err
		}
		var ok bool
		fields, ok = value.(map[string]any)
		if !ok {
			return Usage("XRPC procedures require a JSON object")
		}
	}
	project := First(o.Get("id"), o.Get("project"), Environment("PROJECT", env))
	if sameOrigin {
		project = First(project, profile.Project)
	}
	identifiers := map[string]string{"project_id": project, "release_id": o.Get("release-id"), "skill_id": o.Get("compiled-skill-id"), "key_id": o.Get("key-id")}
	for _, k := range op.Parameters {
		v := identifiers[k]
		if v == "" {
			if op.Method == "POST" {
				if existing, ok := fields[k].(string); ok {
					v = existing
				}
			} else {
				v = query.Get(k)
			}
		}
		if v == "" {
			return Usage("Missing path parameter " + k + "; see myctx help")
		}
		if v == "." || v == ".." || strings.ContainsAny(v, "/\\?#%\x00\r\n") {
			return Usage("Invalid path parameter " + k)
		}
		if !isXRPC {
			segment := map[string]string{"project_id": "id", "release_id": "releaseId", "skill_id": "compiledSkillId", "key_id": "keyId"}[k]
			op.Path = strings.ReplaceAll(op.Path, ":"+segment, url.PathEscape(v))
		} else if op.Method == "GET" {
			query.Set(k, v)
		} else {
			if old, ok := fields[k]; ok && old != v {
				return Usage("Conflicting identifier " + k)
			}
			fields[k] = v
		}
	}
	if isXRPC && op.Method == "POST" {
		for k, vs := range query {
			if len(vs) != 1 {
				return Usage("XRPC procedure query values cannot repeat")
			}
			if _, ok := fields[k]; ok {
				return Usage("Duplicate procedure field " + k)
			}
			v, err := decodeJSON([]byte(vs[0]))
			if err != nil {
				v = vs[0]
			}
			fields[k] = v
		}
		query = url.Values{}
		if name == "tangled connect" {
			if p, ok := fields["provider"]; ok && p != "tangled" {
				return Usage("tangled connect requires provider tangled")
			}
			fields["provider"] = "tangled"
		}
		if name == "publication discard" {
			if a, ok := fields["action"]; ok && a != "discard" {
				return Usage("publication discard requires action discard")
			}
			fields["action"] = "discard"
		}
		if name == "auth logout" && o.Get("session-id") != "" {
			fields["session_id"] = o.Get("session-id")
		}
		body, e = json.Marshal(fields)
		if e != nil {
			return e
		}
	}
	if !isXRPC && name == "tangled connect" {
		if p, ok := fields["provider"]; ok && p != "tangled" {
			return Usage("tangled connect requires provider tangled")
		}
		fields["provider"] = "tangled"
		body, _ = json.Marshal(fields)
	}
	if isXRPC && name == "github repos" {
		query.Set("provider", "github")
	}
	if name == "github install" {
		data, response, err := client.Request(ctx, op.Method, op.Path, query, body)
		if err != nil {
			return err
		}
		if response.StatusCode >= 300 && response.StatusCode < 400 {
			emit(map[string]any{"url": response.Header.Get("Location")})
			return nil
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return &CLIError{Kind: "api", Message: fmt.Sprintf("API request failed (HTTP %d)", response.StatusCode), Status: response.StatusCode, Payload: responseJSON(data)}
		}
		emit(responseJSON(data))
		return nil
	}
	data, _, e := client.Checked(ctx, op.Method, op.Path, query, body)
	if e != nil {
		return e
	}
	emit(responseJSON(data))
	return nil
}
