package snapshotimport

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CountableNewt/my-context-protocol/services/go/identity"
	"github.com/CountableNewt/my-context-protocol/services/go/internal/sqlitetest"
	"github.com/CountableNewt/my-context-protocol/services/go/mcp"
	"github.com/CountableNewt/my-context-protocol/services/go/migrations"
	"github.com/CountableNewt/my-context-protocol/services/go/security"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
)

// Opt-in local acceptance: never makes network requests or prints source values.
func TestApprovedLocalSnapshotRehearsal(t *testing.T) {
	artifact := os.Getenv("MCP_SQLITE_REHEARSAL_SNAPSHOT")
	if artifact == "" {
		t.Skip("private local snapshot rehearsal is opt-in")
	}
	parent := filepath.Dir(artifact)
	if filepath.Clean(os.Getenv("TMPDIR")) != parent {
		t.Fatal("private TMPDIR must match artifact parent")
	}
	info, e := os.Stat(parent)
	if e != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		t.Fatal("rehearsal parent must be private")
	}
	hash := func(path string) string {
		t.Helper()
		info, e := os.Lstat(path)
		if e != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
			t.Fatal("input must be private regular file")
		}
		f, e := os.Open(path)
		if e != nil {
			t.Fatal("cannot read input")
		}
		defer f.Close()
		h := sha256.New()
		if _, e = io.Copy(h, f); e != nil {
			t.Fatal("cannot hash input")
		}
		return hex.EncodeToString(h.Sum(nil))
	}
	source := filepath.Join(parent, "source.sqlite")
	sourceHash, artifactHash := hash(source), hash(artifact)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	db := sqlitetest.New(t, "")
	report, e := Transfer(ctx, db, artifact, Options{Initialize: true})
	if e != nil || report == nil || !report.Verified {
		t.Fatal("local snapshot transfer failed")
	}
	if _, e = Verify(ctx, db, artifact); e != nil {
		t.Fatal("typed-row verification failed")
	}
	if e = migrations.Apply(ctx, db, migrations.Options{Additions: additiveMigrations()}); e != nil {
		t.Fatal("local additive migrations failed")
	}
	if _, e = Verify(ctx, db, artifact); e != nil {
		t.Fatal("additive migrations changed imported data")
	}
	projects, e := db.Execute(ctx, turso.Statement{SQL: "SELECT id,subdomain,custom_domain,custom_domain_verified_at FROM projects ORDER BY id", WantRows: true})
	if e != nil || len(projects.Rows) == 0 {
		t.Fatal("imported projects unavailable")
	}
	// Model a Go-era application write before rollback. The snapshot files stay
	// readonly; only this ephemeral local database is edited.
	localProject, ok := projects.Rows[0][0].Value.(string)
	if !ok {
		t.Fatal("local rollback project unavailable")
	}
	if _, e = db.Execute(ctx, turso.Statement{SQL: "UPDATE projects SET name=? WHERE id=?", Args: []turso.Value{turso.Text("Local rollback acceptance edit"), turso.Text(localProject)}}); e != nil {
		t.Fatal("local rollback edit failed")
	}
	rollbackDir := t.TempDir()
	rollbackArtifact := filepath.Join(rollbackDir, "post-write.jsonl")
	rollbackSnapshot, e := ExportRemote(ctx, db, rollbackArtifact, RemoteExportOptions{WritersPaused: true})
	if e != nil || rollbackSnapshot.ID == report.SnapshotID {
		t.Fatal("local post-write rollback export failed")
	}
	restored, e := RestoreLocal(ctx, rollbackArtifact, filepath.Join(rollbackDir, "restored.sqlite"))
	if e != nil || restored.ID != rollbackSnapshot.ID {
		t.Fatal("local post-write rollback restore failed")
	}
	editPreserved := false
	for _, table := range restored.Tables {
		if table.Name != "projects" {
			continue
		}
		idColumn, nameColumn := -1, -1
		for i, name := range table.Columns {
			if name == "id" {
				idColumn = i
			}
			if name == "name" {
				nameColumn = i
			}
		}
		if idColumn < 0 || nameColumn < 0 {
			t.Fatal("local rollback columns unavailable")
		}
		for _, row := range table.Data {
			if row[idColumn].Value == localProject && row[nameColumn].Value == "Local rollback acceptance edit" {
				editPreserved = true
			}
		}
	}
	if !editPreserved {
		t.Fatal("post-cutover edit lost in rollback")
	}
	store := mcp.SQLStore{DB: db}
	service, e := mcp.New(mcp.Config{Path: "/mcp", BaseDomain: "mcp.local-rehearsal.invalid", RequireTenantHost: true}, store)
	if e != nil {
		t.Fatal("local MCP setup failed")
	}
	calls, skills, capabilities, tools := 0, 0, 0, 0
	for _, row := range projects.Rows {
		pid, ok := row[0].Value.(string)
		if !ok {
			t.Fatal("invalid imported identity")
		}
		sub, _ := row[1].Value.(string)
		host := sub + ".mcp.local-rehearsal.invalid"
		if sub == "" {
			host, _ = row[2].Value.(string)
			if host == "" || row[3].Type == "null" {
				t.Fatal("tenant host unavailable")
			}
		}
		snapshot, e := store.Snapshot(ctx, pid)
		if e != nil {
			t.Fatal("imported snapshot cannot be loaded")
		}
		skills += len(snapshot.Skills)
		capabilities += len(snapshot.Capabilities)
		key, e := security.NewOpaqueToken()
		if e != nil {
			t.Fatal("local key generation failed")
		}
		id, e := identity.UUID()
		if e != nil {
			t.Fatal("local identity generation failed")
		}
		_, e = db.Execute(ctx, turso.Statement{SQL: "INSERT INTO api_keys(id,project_id,key_prefix,key_hash,status) VALUES(?,?,'local-rehearsal',?,'active')", Args: []turso.Value{turso.Text(id), turso.Text(pid), turso.Text(security.HashToken(key))}})
		if e != nil {
			t.Fatal("local synthetic key creation failed")
		}
		invoke := func(method string, params any) map[string]any {
			t.Helper()
			raw, e := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
			if e != nil {
				t.Fatal("RPC encoding failed")
			}
			req := httptest.NewRequest("POST", "https://"+host+"/mcp", strings.NewReader(string(raw)))
			req.Header.Set("X-API-Key", key)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json, text/event-stream")
			req.Header.Set("MCP-Protocol-Version", "2025-11-25")
			w := httptest.NewRecorder()
			service.ServeHTTP(w, req)
			var decoded map[string]any
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &decoded) != nil || decoded["error"] != nil {
				t.Fatal("local imported MCP request failed")
			}
			result, ok := decoded["result"].(map[string]any)
			if !ok {
				t.Fatal("MCP result missing")
			}
			calls++
			return result
		}
		result := invoke("initialize", map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{}, "clientInfo": map[string]string{"name": "local-restore-acceptance", "version": "1"}})
		if result["protocolVersion"] != "2025-11-25" {
			t.Fatal("MCP protocol mismatch")
		}
		listed := invoke("tools/list", map[string]any{})
		items, ok := listed["tools"].([]any)
		if !ok {
			t.Fatal("MCP tools missing")
		}
		tools += len(items)
		required := map[string]bool{"resolve_context": false, "get_skill": false, "report_skill_feedback": false, "report_skill_usage": false}
		for _, item := range items {
			if tool, ok := item.(map[string]any); ok {
				if name, ok := tool["name"].(string); ok {
					if _, wanted := required[name]; wanted {
						required[name] = true
					}
				}
			}
		}
		for _, present := range required {
			if !present {
				t.Fatal("MCP runtime tool missing")
			}
		}
		catalog := invoke("tools/call", map[string]any{"name": "mycontext_catalog", "arguments": map[string]string{"mode": "overview"}})
		content, ok := catalog["content"].([]any)
		if catalog["isError"] == true || !ok || len(content) == 0 {
			t.Fatal("imported MCP catalog failed")
		}
		textPresent := false
		for _, item := range content {
			if block, ok := item.(map[string]any); ok {
				if text, ok := block["text"].(string); ok && strings.TrimSpace(text) != "" {
					textPresent = true
				}
			}
		}
		if !textPresent {
			t.Fatal("imported MCP catalog content missing")
		}
		if _, e = db.Execute(ctx, turso.Statement{SQL: "DELETE FROM api_keys WHERE id=?", Args: []turso.Value{turso.Text(id)}}); e != nil {
			t.Fatal("synthetic key cleanup failed")
		}
	}
	if skills == 0 || capabilities == 0 {
		t.Fatal("active catalogs have no compiled content")
	}
	if hash(source) != sourceHash || hash(artifact) != artifactHash {
		t.Fatal("readonly inputs changed")
	}
	rows := int64(0)
	for _, table := range report.Tables {
		rows += table.Count
	}
	metadata := map[string]any{"status": "passed", "verified_at": time.Now().UTC(), "source_sha256": sourceHash, "artifact_sha256": artifactHash, "snapshot_id": report.SnapshotID, "tables": report.Tables, "table_count": len(report.Tables), "row_count": rows, "project_count": len(projects.Rows), "mcp_calls": calls, "ready_skills": skills, "capabilities": capabilities, "listed_tools": tools, "source_unchanged": true, "external_requests": 0, "post_cutover_edit_restored": editPreserved, "rollback_snapshot_id": rollbackSnapshot.ID}
	f, e := os.CreateTemp(parent, "go-local-rehearsal-report-*.json")
	if e != nil {
		t.Fatal("private report creation failed")
	}
	if e = json.NewEncoder(f).Encode(metadata); e != nil {
		f.Close()
		t.Fatal("private report write failed")
	}
	if e = f.Close(); e != nil {
		t.Fatal("private report close failed")
	}
	t.Logf("passed: tables=%d rows=%d projects=%d MCP calls=%d ready skills=%d capabilities=%d tools=%d; post-cutover edit restored; readonly inputs unchanged; external requests=0", len(report.Tables), rows, len(projects.Rows), calls, skills, capabilities, tools)
}
