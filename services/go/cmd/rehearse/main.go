// rehearse verifies remote Turso behavior using disposable synthetic rows.
// It refuses to run without an exact operator-supplied preview-host match.
package main

import (
	"context"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/CountableNewt/my-context-protocol/services/go/identity"
	"github.com/CountableNewt/my-context-protocol/services/go/security"
	"github.com/CountableNewt/my-context-protocol/services/go/session"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
)

func main() {
	expected := flag.String("expected-preview-host", "", "Exact approved preview database hostname")
	flag.Parse()
	if e := run(*expected); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
func run(expected string) (err error) {
	u, e := url.Parse(os.Getenv("TURSO_DATABASE_URL"))
	if e != nil || expected == "" || u.Hostname() != expected || !strings.Contains(expected, "preview") || os.Getenv("VERCEL_ENV") == "production" {
		return errors.New("rehearsal requires an exact approved preview hostname; production is forbidden")
	}
	db, e := turso.New(u.String(), os.Getenv("TURSO_AUTH_TOKEN"))
	if e != nil {
		return e
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	scalar, e := db.Execute(ctx, turso.Statement{SQL: "SELECT ? AS precise_integer,? AS binary,? AS empty_text", Args: []turso.Value{turso.Integer(9007199254740993), turso.Blob([]byte{0, 255, 1}), turso.Text("")}, WantRows: true})
	if e != nil {
		return e
	}
	if len(scalar.Rows) != 1 || scalar.Rows[0][0].Value != "9007199254740993" || scalar.Rows[0][1].Base64 == nil || *scalar.Rows[0][1].Base64 != "AP8B" || scalar.Rows[0][2].Value != "" {
		return errors.New("remote scalar type/precision check failed")
	}
	marker, e := security.NewOpaqueToken()
	if e != nil {
		return e
	}
	table := "migration_rehearsal_" + marker[:12]
	if _, e = db.Execute(ctx, turso.Statement{SQL: "CREATE TABLE " + table + "(id TEXT PRIMARY KEY)"}); e != nil {
		return e
	}
	defer func() {
		cleanup, c := context.WithTimeout(context.Background(), 15*time.Second)
		defer c()
		if _, e := db.Execute(cleanup, turso.Statement{SQL: "DROP TABLE " + table}); err == nil && e != nil {
			err = e
		}
	}()
	if _, e = db.Batch(ctx, []turso.Statement{{SQL: "INSERT INTO " + table + "(id) VALUES (?)", Args: []turso.Value{turso.Text(marker)}}, {SQL: "INSERT INTO nonexistent_rehearsal_target(id) VALUES (1)"}}); e == nil {
		return errors.New("remote transaction unexpectedly committed invalid SQL")
	}
	remaining, e := db.Execute(ctx, turso.Statement{SQL: "SELECT COUNT(*) FROM " + table, WantRows: true})
	if e != nil {
		return e
	}
	if len(remaining.Rows) != 1 || remaining.Rows[0][0].Value != "0" {
		return errors.New("remote rollback left a partial write")
	}
	keyBytes := make([]byte, 32)
	for i := range keyBytes {
		keyBytes[i] = byte(i)
	}
	cipher, e := security.NewTokenCipher(base64.StdEncoding.EncodeToString(keyBytes))
	if e != nil {
		return e
	}
	encrypted, e := cipher.Encrypt("synthetic-rehearsal-token")
	if e != nil {
		return e
	}
	store := identity.SQLStore{DB: db}
	account, e := store.UpsertAccount(ctx, identity.GitHubUser{ID: -time.Now().UnixNano(), Login: "migration-rehearsal-" + marker[:8]}, encrypted, false)
	if e != nil {
		return e
	}
	defer func() {
		cleanup, c := context.WithTimeout(context.Background(), 15*time.Second)
		defer c()
		_, e := db.Execute(cleanup, turso.Statement{SQL: "DELETE FROM accounts WHERE id=?", Args: []turso.Value{turso.Text(account.ID)}})
		if err == nil && e != nil {
			err = e
		}
	}()
	stored, e := store.Account(ctx, account.ID)
	if e != nil || stored == nil || stored.EncryptedToken == nil {
		return errors.New("remote account persistence failed")
	}
	plain, e := cipher.Decrypt(*stored.EncryptedToken)
	if e != nil || plain != "synthetic-rehearsal-token" {
		return errors.New("stored encrypted-token round trip failed")
	}
	sessionKey, e := security.NewOpaqueToken()
	if e != nil {
		return e
	}
	if e = store.CreateSession(ctx, sessionKey, session.Payload{"accountId": account.ID}); e != nil {
		return e
	}
	defer func() {
		cleanup, c := context.WithTimeout(context.Background(), 15*time.Second)
		defer c()
		_ = store.DeleteSession(cleanup, sessionKey)
	}()
	if p, e := store.ReadSession(ctx, sessionKey); e != nil || p["accountId"] != account.ID {
		return errors.New("remote session payload incompatible")
	}
	if e = store.DeleteSession(ctx, sessionKey); e != nil {
		return e
	}
	if e = store.UpdateSession(ctx, sessionKey, session.Payload{"accountId": account.ID}); !errors.Is(e, identity.ErrUnauthenticated) {
		return errors.New("revoked session was recreated")
	}
	handoff, e := security.NewOpaqueToken()
	if e != nil {
		return e
	}
	if e = store.IssueHandoff(ctx, handoff, account.ID, time.Now().Add(time.Minute)); e != nil {
		return e
	}
	if id, e := store.ConsumeHandoff(ctx, handoff, time.Now()); e != nil || id != account.ID {
		return errors.New("handoff could not be consumed")
	}
	if _, e = store.ConsumeHandoff(ctx, handoff, time.Now()); !errors.Is(e, identity.ErrUnauthenticated) {
		return errors.New("handoff replay was accepted")
	}
	if _, e = db.Execute(ctx, turso.Statement{SQL: "INSERT INTO projects(id,account_id,name,slug) VALUES (?,?,?,?)", Args: []turso.Value{turso.Text(marker), turso.Text("NONEXISTENT_REHEARSAL_ACCOUNT"), turso.Text("probe"), turso.Text("probe")}}); e == nil {
		return errors.New("remote foreign-key enforcement is disabled")
	}
	check, e := db.Execute(ctx, turso.Statement{SQL: "PRAGMA foreign_key_check", WantRows: true})
	if e != nil {
		return e
	}
	if len(check.Rows) != 0 {
		return errors.New("remote foreign-key integrity check failed")
	}
	fmt.Println("Remote preview rehearsal passed: precise values, binary data, rollback, encryption, sessions, replay prevention, foreign keys. Synthetic rows cleaned up on exit.")
	return nil
}
