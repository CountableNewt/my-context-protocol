// production-operator is an explicit two-stage operator job. It is never a
// service hook. Only encrypted snapshots and aggregate receipts may be deployed.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/CountableNewt/my-context-protocol/services/go/operatorbackup"
	"github.com/CountableNewt/my-context-protocol/services/go/snapshotimport"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "operator job failed:", err)
		os.Exit(1)
	}
}
func run(args []string) error {
	if len(args) == 0 {
		return errors.New("use keygen, review, preflight, snapshot or apply")
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	host := flags.String("expected-host", "", "independently reviewed exact production database hostname")
	project := flags.String("expected-project", "prj_jC1eRUJ0CHZvHp7Eej3AUAS4yUq7", "exact existing Vercel project ID")
	directory := flags.String("directory", "", "private local output directory (keygen/review only)")
	publicPath := flags.String("recipient", "../../operators/recipient.pem", "recipient public PEM; never a private key")
	privatePath := flags.String("key", "", "local recipient private key (review only)")
	envelopePath := flags.String("envelope", "", "downloaded encrypted backup (review only)")
	receiptPath := flags.String("receipt", "../../operators/review.json", "locally signed restore verification receipt")
	output := flags.String("output", "../../.operator-output", "only ciphertext and aggregate metadata are written here")
	paused := flags.Bool("writers-paused", false, "operator confirms all production writers stopped and drained")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected arguments")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	switch args[0] {
	case "keygen":
		if *directory == "" {
			return errors.New("keygen requires private directory")
		}
		if err := privateDirectory(*directory); err != nil {
			return err
		}
		pub, priv, err := operatorbackup.Generate()
		if err != nil {
			return err
		}
		if err = exclusive(filepath.Join(*directory, "recipient-private.pem"), priv); err != nil {
			return err
		}
		return exclusive(filepath.Join(*directory, "recipient.pem"), pub)
	case "review":
		if *directory == "" || *privatePath == "" || *envelopePath == "" || !validHost(*host) {
			return errors.New("review requires directory, key, envelope and expected-host")
		}
		if err := privateDirectory(*directory); err != nil {
			return err
		}
		info, err := os.Stat(*privatePath)
		if err != nil || info.Mode().Perm()&0077 != 0 {
			return errors.New("private key must be owner-readable only")
		}
		raw, err := bounded(*privatePath, 16384)
		if err != nil {
			return err
		}
		key, err := operatorbackup.Private(raw)
		if err != nil {
			return err
		}
		encrypted, err := bounded(*envelopePath, operatorbackup.MaxPlaintext+2048)
		if err != nil {
			return err
		}
		plain, err := operatorbackup.Open(encrypted, key)
		if err != nil {
			return err
		}
		snapshotPath := filepath.Join(*directory, "snapshot.jsonl")
		if err = exclusive(snapshotPath, plain); err != nil {
			return err
		}
		restored, err := snapshotimport.RestoreLocal(ctx, snapshotPath, filepath.Join(*directory, "restored.sqlite"))
		if err != nil {
			return err
		}
		hash := sha256.Sum256(encrypted)
		receipt := operatorbackup.Receipt{Version: 1, Host: *host, SnapshotID: restored.ID, EnvelopeSHA256: hex.EncodeToString(hash[:])}
		if err = receipt.Sign(key); err != nil {
			return err
		}
		raw, err = json.MarshalIndent(receipt, "", "  ")
		if err != nil {
			return err
		}
		if err = exclusive(filepath.Join(*directory, "review.json"), raw); err != nil {
			return err
		}
		fmt.Println("Backup decrypted, restored and typed reconstruction verified:", restored.ID)
		return nil
	case "preflight", "snapshot", "apply":
		if args[0] != "preflight" && !*paused {
			return errors.New("remote job requires explicit --writers-paused after stopping and draining all writers")
		}
		if os.Getenv("VERCEL_ENV") != "production" || os.Getenv("VERCEL_PROJECT_ID") != *project || *project == "" {
			return errors.New("remote job requires the exact production Vercel project")
		}
		if err := expectedHost(*host, os.Getenv("TURSO_DATABASE_URL")); err != nil {
			return err
		}
		if args[0] == "preflight" {
			db, err := turso.New(os.Getenv("TURSO_DATABASE_URL"), os.Getenv("TURSO_AUTH_TOKEN"))
			if err != nil {
				return errors.New("invalid production database configuration")
			}
			ready, err := db.Execute(ctx, turso.Statement{SQL: "SELECT 1,(SELECT COUNT(*) FROM go_schema_migrations)", WantRows: true})
			if err != nil || len(ready.Rows) != 1 || len(ready.Rows[0]) != 2 {
				return errors.New("production database readiness query failed")
			}
			if err = os.MkdirAll(*output, 0700); err != nil {
				return err
			}
			return metadata(*output, map[string]any{"mode": "preflight", "production_project_verified": true, "database_reachable": true, "expected_host": *host, "migration_count": ready.Rows[0][1].Value})
		}
		pub, err := bounded(*publicPath, 16384)
		if err != nil {
			return err
		}
		recipient, err := operatorbackup.Public(pub)
		if err != nil {
			return err
		}
		var reviewed operatorbackup.Receipt
		if args[0] == "apply" {
			raw, err := bounded(*receiptPath, 16384)
			if err != nil {
				return err
			}
			if json.Unmarshal(raw, &reviewed) != nil {
				return errors.New("invalid backup review receipt")
			}
			if err = reviewed.Verify(recipient, *host); err != nil {
				return err
			}
		}
		db, err := turso.New(os.Getenv("TURSO_DATABASE_URL"), os.Getenv("TURSO_AUTH_TOKEN"))
		if err != nil {
			return errors.New("invalid production database configuration")
		}
		temp, err := os.MkdirTemp("", "myctx-production-operator-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(temp)
		sourcePath := filepath.Join(temp, "before.jsonl")
		before, err := snapshotimport.ExportRemote(ctx, db, sourcePath, snapshotimport.RemoteExportOptions{WritersPaused: true, CurrentSchema: true})
		if err != nil {
			return err
		}
		if err = os.MkdirAll(*output, 0700); err != nil {
			return err
		}
		if args[0] == "snapshot" {
			plain, err := bounded(sourcePath, operatorbackup.MaxPlaintext)
			if err != nil {
				return err
			}
			sealed, err := operatorbackup.Seal(plain, recipient)
			if err != nil {
				return err
			}
			if err = exclusive(filepath.Join(*output, "snapshot.enc"), sealed); err != nil {
				return err
			}
			hash := sha256.Sum256(sealed)
			return metadata(*output, map[string]any{"mode": "snapshot", "snapshot_id": before.ID, "envelope_sha256": hex.EncodeToString(hash[:]), "expected_host": *host})
		}
		if before.ID != reviewed.SnapshotID {
			return errors.New("production changed since reviewed backup; take and verify a fresh snapshot")
		}
		// The fixed existing operator command owns the migration registry. Neither
		// credentials nor arbitrary SQL are accepted in arguments or emitted here.
		cmd := exec.CommandContext(ctx, "go", "run", "./cmd/migrate")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err = cmd.Run(); err != nil {
			return errors.New("explicit migration command failed; keep writers paused and inspect migration state")
		}
		after, err := snapshotimport.ExportRemote(ctx, db, filepath.Join(temp, "after.jsonl"), snapshotimport.RemoteExportOptions{WritersPaused: true, CurrentSchema: true})
		if err != nil {
			return errors.New("post-migration snapshot verification failed; keep writers paused")
		}
		if err = preserved(before, after); err != nil {
			return err
		}
		return metadata(*output, map[string]any{"mode": "apply", "reviewed_snapshot_id": before.ID, "after_snapshot_id": after.ID, "source_rows_preserved": true, "expected_host": *host})
	default:
		return errors.New("unknown operator mode")
	}
}
func validHost(host string) bool {
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
func expectedHost(host, raw string) error {
	if !validHost(host) {
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
func bounded(path string, max int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("cannot open operator input")
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, int64(max)+1))
	if err != nil || len(raw) > max {
		return nil, errors.New("operator input exceeds bounds")
	}
	return raw, nil
}
func privateDirectory(path string) error {
	if err := os.Mkdir(path, 0700); err != nil && !os.IsExist(err) {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return errors.New("local output directory must have mode0700 and not be a symlink")
	}
	return nil
}
func exclusive(path string, raw []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return errors.New("operator output already exists or cannot be created")
	}
	if _, err = f.Write(raw); err != nil {
		f.Close()
		os.Remove(path)
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		os.Remove(path)
		return err
	}
	return f.Close()
}
func metadata(output string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if err = exclusive(filepath.Join(output, "status.json"), raw); err != nil {
		return err
	}
	fmt.Println(string(raw))
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
