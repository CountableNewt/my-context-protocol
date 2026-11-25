// Package operatorjob implements fixed, explicit production recovery operations.
package operatorjob

import (
	"context"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/CountableNewt/my-context-protocol/services/go/appschema"
	"github.com/CountableNewt/my-context-protocol/services/go/migrations"
	"github.com/CountableNewt/my-context-protocol/services/go/operatorbackup"
	"github.com/CountableNewt/my-context-protocol/services/go/snapshotimport"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

type Database interface{ migrations.Database }
type Result struct {
	Proof      map[string]any
	Ciphertext []byte
}

const MaxCiphertext = 4 << 20

func Run(ctx context.Context, db Database, mode, host string, recipient *rsa.PublicKey, review *operatorbackup.Receipt) (Result, error) {
	if mode == "preflight" {
		r, err := db.Execute(ctx, turso.Statement{SQL: "SELECT 1,(SELECT COUNT(*) FROM go_schema_migrations)", WantRows: true})
		if err != nil || r == nil || len(r.Rows) != 1 || len(r.Rows[0]) != 2 {
			return Result{}, errors.New("database readiness query failed")
		}
		return Result{Proof: map[string]any{"mode": mode, "database_reachable": true, "expected_host": host, "migration_count": r.Rows[0][1].Value}}, nil
	}
	if mode != "snapshot" && mode != "apply" {
		return Result{}, errors.New("unknown operator command")
	}
	if recipient == nil {
		return Result{}, errors.New("recipient required")
	}
	if mode == "apply" {
		if review == nil {
			return Result{}, errors.New("reviewed backup required")
		}
		if err := review.Verify(recipient, host); err != nil {
			return Result{}, err
		}
	}
	temp, err := os.MkdirTemp("", "myctx-operator-job-")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(temp)
	source := filepath.Join(temp, "before.jsonl")
	snapshotDB := SnapshotDatabase(db, "operatorjob:"+operatorbackup.KeyID(recipient)+":")
	before, err := snapshotimport.ExportRemote(ctx, snapshotDB, source, snapshotimport.RemoteExportOptions{WritersPaused: true, CurrentSchema: true})
	if err != nil {
		return Result{}, err
	}
	if mode == "snapshot" {
		plain, err := readSnapshot(source)
		if err != nil {
			return Result{}, err
		}
		sealed, err := operatorbackup.Seal(plain, recipient)
		if err != nil {
			return Result{}, err
		}
		if len(sealed) > MaxCiphertext {
			return Result{}, errors.New("encrypted compressed snapshot exceeds bounded HTTP response; no database changes made")
		}
		hash := sha256.Sum256(sealed)
		return Result{Ciphertext: sealed, Proof: map[string]any{"mode": mode, "expected_host": host, "snapshot_id": before.ID, "envelope_sha256": hex.EncodeToString(hash[:])}}, nil
	}
	if before.ID != review.SnapshotID {
		return Result{}, errors.New("production changed since reviewed backup; obtain fresh verified backup")
	}
	if err = migrations.Apply(ctx, db, migrations.Options{Additions: appschema.Registry()}); err != nil {
		return Result{}, errors.New("explicit migration failed; keep writers paused and inspect state")
	}
	after, err := snapshotimport.ExportRemote(ctx, snapshotDB, filepath.Join(temp, "after.jsonl"), snapshotimport.RemoteExportOptions{WritersPaused: true, CurrentSchema: true})
	if err != nil {
		return Result{}, errors.New("post-migration export failed; keep writers paused")
	}
	if err = preserved(before, after); err != nil {
		return Result{}, err
	}
	return Result{Proof: map[string]any{"mode": mode, "expected_host": host, "reviewed_snapshot_id": before.ID, "after_snapshot_id": after.ID, "source_rows_preserved": true}}, nil
}
func ValidHost(host string) bool {
	if len(host) == 0 || len(host) > 253 || strings.ContainsAny(host, "/:@?#\\* []\t\r\n") {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}
func ExpectedHost(host, raw string) error {
	if !ValidHost(host) {
		return errors.New("expected-host must be one exact independent hostname")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() != host || u.User != nil || u.Port() != "" || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" {
		return errors.New("production database hostname does not match reviewed target")
	}
	if u.Scheme != "libsql" && u.Scheme != "https" {
		return errors.New("production database requires TLS")
	}
	return nil
}
func preserved(before, after *snapshotimport.Snapshot) error {
	tables := map[string]*snapshotimport.Table{}
	for _, t := range after.Tables {
		tables[t.Name] = t
	}
	for _, prior := range before.Tables {
		if prior.Name == "go_schema_migrations" {
			continue
		}
		current := tables[prior.Name]
		if current == nil || current.Count != prior.Count || current.Digest != prior.Digest || strings.Join(current.Columns, "\x00") != strings.Join(prior.Columns, "\x00") {
			return errors.New("post-migration source table differs: " + prior.Name + "; keep writers paused")
		}
	}
	return nil
}

func readSnapshot(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, operatorbackup.MaxPlaintext+1))
	if err != nil || len(raw) > operatorbackup.MaxPlaintext {
		return nil, errors.New("snapshot exceeds plaintext bound")
	}
	return raw, nil
}
