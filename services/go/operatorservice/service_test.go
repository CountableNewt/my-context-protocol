package operatorservice

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/CountableNewt/my-context-protocol/services/go/internal/sqlitetest"
	"github.com/CountableNewt/my-context-protocol/services/go/mcp"
	"github.com/CountableNewt/my-context-protocol/services/go/migrations"
	"github.com/CountableNewt/my-context-protocol/services/go/operatorbackup"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHTTPCommandAuthAndCiphertextOnlySnapshot(t *testing.T) {
	pub, priv, err := operatorbackup.Generate()
	if err != nil {
		t.Fatal(err)
	}
	public, _ := operatorbackup.Public(pub)
	private, _ := operatorbackup.Private(priv)
	db := sqlitetest.New(t, migrations.BaselineSQL+mcp.MigrationSQL+"CREATE TABLE go_schema_migrations(version TEXT PRIMARY KEY,checksum TEXT NOT NULL,applied_at REAL NOT NULL);")
	if _, err = db.Execute(context.Background(), turso.Statement{SQL: "INSERT INTO accounts(id,github_id,login,github_token_encrypted) VALUES('private',1,'owner','do-not-disclose-secret')"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	service, err := New(Config{DB: db, PublicKey: public, Audience: "operator.example.com", Host: "production.example.com", Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	command := Command{Version: 1, Method: "POST", Path: "/operator", Mode: "snapshot", Audience: "operator.example.com", Host: "production.example.com", Nonce: strings.Repeat("a", 64), IssuedAt: now.Unix(), ExpiresAt: now.Add(time.Minute).Unix(), WritersPaused: true}
	if err = command.Sign(private); err != nil {
		t.Fatal(err)
	}
	call := func(c Command) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(c)
		req := httptest.NewRequest("POST", "https://operator.example.com/operator", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		service.ServeHTTP(w, req)
		return w
	}
	bad := command
	bad.Audience = "different.example.com"
	if call(bad).Code != 403 {
		t.Fatal("cross-audience authorization accepted")
	}
	bad = command
	bad.Mode = "apply"
	if call(bad).Code != 403 {
		t.Fatal("signed command modified")
	}
	bad = command
	bad.ExpiresAt = now.Add(time.Hour).Unix()
	bad.Sign(private)
	if call(bad).Code != 403 {
		t.Fatal("long authorization accepted")
	}
	response := call(command)
	if response.Code != 200 {
		t.Fatalf("snapshot failed %d %s", response.Code, response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "private, no-store" || bytes.Contains(response.Body.Bytes(), []byte("do-not-disclose-secret")) {
		t.Fatal("private snapshot leaked/cacheable")
	}
	plain, err := operatorbackup.Open(response.Body.Bytes(), private)
	if err != nil || !bytes.Contains(plain, []byte("do-not-disclose-secret")) {
		t.Fatal("ciphertext response could not recover exact private source", err)
	}
	if call(command).Code != 409 {
		t.Fatal("snapshot nonce replay accepted")
	}
}
