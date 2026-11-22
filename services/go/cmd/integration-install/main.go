// Command integration-install prepares a one-use loopback callback. It never
// creates an integration or initiates installation/grants on the user's behalf.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const projectID = "prj_jC1eRUJ0CHZvHp7Eej3AUAS4yUq7"
const teamID = "team_bkhdFggoSZpUiLHe0aLuShOR"
const scope = "sam-clemente-s-team"
const host = "127.0.0.1:7338"

type credentials struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}
type callback struct {
	credentials credentials
	path        string
	api         string
	client      *http.Client
	save        func(context.Context, string, string) error
	done        chan error
	mu          sync.Mutex
	used        bool
}

func safeError() error {
	return errors.New("integration setup failed; credentials and provider response withheld")
}
func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
func run() error {
	save := flag.Bool("save-production", false, "authorize saving only MyContextProtocol Production integration credentials")
	flag.Parse()
	if !*save || flag.NArg() != 0 {
		return errors.New("requires --save-production after integration approval; pipe credentials JSON through stdin")
	}
	info, e := os.Stdin.Stat()
	if e != nil || info.Mode()&os.ModeCharDevice != 0 {
		return errors.New("credentials must arrive through private stdin, never command arguments or an echoed terminal")
	}
	var c credentials
	decoder := json.NewDecoder(io.LimitReader(os.Stdin, 16385))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&c) != nil || !valid(c.ClientID) || !valid(c.ClientSecret) {
		return safeError()
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return safeError()
	}
	random := make([]byte, 32)
	if _, e = rand.Read(random); e != nil {
		return safeError()
	}
	handler := &callback{credentials: c, path: "/callback/" + hex.EncodeToString(random), api: "https://api.vercel.com", client: &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, save: saveSecret, done: make(chan error, 1)}
	listener, e := net.Listen("tcp4", host)
	if e != nil {
		return errors.New("cannot bind private loopback callback on port7338")
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 2 * time.Minute, IdleTimeout: 10 * time.Second, MaxHeaderBytes: 16384, ErrorLog: log.New(io.Discard, "", 0)}
	defer listener.Close()
	fmt.Println("Register this exact Redirect URL, then manually approve installation for only MyContextProtocol:")
	fmt.Println("http://" + host + handler.path)
	go func() {
		if e := server.Serve(listener); e != nil && e != http.ErrServerClosed {
			select {
			case handler.done <- safeError():
			default:
			}
		}
	}()
	timer := time.NewTimer(30 * time.Minute)
	defer timer.Stop()
	select {
	case e = <-handler.done:
	case <-timer.C:
		e = errors.New("installation callback timed out")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = server.Shutdown(ctx)
	if e == nil {
		fmt.Println("Validated installation and saved three Production secrets. No deployment was triggered.")
	}
	return e
}
func valid(s string) bool {
	if s == "" || len(s) > 8192 {
		return false
	}
	for _, c := range s {
		if c <= 32 || c >= 127 {
			return false
		}
	}
	return true
}
func (h *callback) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
	ip, _, e := net.SplitHostPort(r.RemoteAddr)
	if e != nil || ip != "127.0.0.1" || r.Host != host || r.URL.Path != h.path || r.Method != "GET" {
		http.Error(w, "Not found", 404)
		return
	}
	query, e := url.ParseQuery(r.URL.RawQuery)
	codes := query["code"]
	if e != nil || len(codes) != 1 || !valid(codes[0]) {
		http.Error(w, "Invalid callback", 400)
		return
	}
	h.mu.Lock()
	if h.used {
		h.mu.Unlock()
		http.Error(w, "Callback already used", 409)
		return
	}
	h.used = true
	h.mu.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	e = h.install(ctx, codes[0])
	if e != nil {
		http.Error(w, "Setup failed; see local utility status", 500)
	} else {
		fmt.Fprintln(w, "Installation validated. Production secrets saved. You can close this tab.")
	}
	h.done <- e
}
func (h *callback) install(ctx context.Context, code string) error {
	form := url.Values{"client_id": {h.credentials.ClientID}, "client_secret": {h.credentials.ClientSecret}, "code": {code}, "redirect_uri": {"http://" + host + h.path}}
	req, e := http.NewRequestWithContext(ctx, "POST", h.api+"/v2/oauth/access_token", strings.NewReader(form.Encode()))
	if e != nil {
		return safeError()
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var token struct {
		Token        string `json:"access_token"`
		Type         string `json:"token_type"`
		Installation string `json:"installation_id"`
		Team         string `json:"team_id"`
	}
	if h.read(req, &token) != nil || !valid(token.Token) || !strings.EqualFold(token.Type, "Bearer") || !strings.HasPrefix(token.Installation, "icfg_") || !valid(token.Installation) || token.Team != teamID {
		return safeError()
	}
	// Mandatory Integration Configuration Read lets us verify the granted scopes
	// and explicit project selection without relying on an optional token scope field.
	req, e = http.NewRequestWithContext(ctx, "GET", h.api+"/v1/integrations/configuration/"+url.PathEscape(token.Installation)+"?teamId="+teamID, nil)
	if e != nil {
		return safeError()
	}
	req.Header.Set("Authorization", "Bearer "+token.Token)
	var configuration struct {
		ID        string   `json:"id"`
		Team      string   `json:"teamId"`
		Projects  []string `json:"projects"`
		Selection string   `json:"projectSelection"`
		Scopes    []string `json:"scopes"`
		Disabled  *int64   `json:"disabledAt"`
		Deleted   *int64   `json:"deletedAt"`
	}
	if h.read(req, &configuration) != nil || configuration.ID != token.Installation || configuration.Team != teamID || len(configuration.Projects) != 1 || configuration.Projects[0] != projectID || configuration.Selection == "all" || configuration.Disabled != nil || configuration.Deleted != nil || !allowedScopes(configuration.Scopes) {
		return safeError()
	}
	// Save the client secret last. Partial CLI failures do not enable the provider
	// and are reported without values; this utility never silently overwrites.
	for _, item := range [][2]string{{"VERCEL_INTEGRATION_CLIENT_ID", h.credentials.ClientID}, {"VERCEL_INTEGRATION_INSTALLATION_ID", token.Installation}, {"VERCEL_INTEGRATION_CLIENT_SECRET", h.credentials.ClientSecret}} {
		if h.save(ctx, item[0], item[1]) != nil {
			return errors.New("Production credential save incomplete; inspect variable names only before retrying; values withheld")
		}
	}
	return nil
}
func allowedScopes(scopes []string) bool {
	expected := map[string]bool{"read:integration-configuration": false, "read-write:project": false, "read-write:domain": false}
	for _, scope := range scopes {
		if _, ok := expected[scope]; !ok || expected[scope] {
			return false
		}
		expected[scope] = true
	}
	return len(scopes) == len(expected) && expected["read:integration-configuration"] && expected["read-write:project"] && expected["read-write:domain"]
}
func (h *callback) read(req *http.Request, out any) error {
	res, e := h.client.Do(req)
	if e != nil {
		return safeError()
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return safeError()
	}
	raw, e := io.ReadAll(io.LimitReader(res.Body, 65537))
	if e != nil || len(raw) > 65536 || json.Unmarshal(raw, out) != nil {
		return safeError()
	}
	return nil
}
func saveSecret(ctx context.Context, name, value string) error {
	command := exec.CommandContext(ctx, "vercel", "env", "add", name, "--environment", "production", "--type", "secret", "--project", projectID, "--scope", scope, "--yes", "--non-interactive")
	command.Stdin = strings.NewReader(value)
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	if command.Run() != nil {
		return safeError()
	}
	return nil
}
