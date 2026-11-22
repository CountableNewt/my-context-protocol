// Command import performs explicit offline and writers-paused remote export,
// staged loading, local rollback restore and verification. Database credentials
// are read only from environment variables, never CLI flags.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/CountableNewt/my-context-protocol/services/go/snapshotimport"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
)

func main() {
	if e := run(os.Args[1:]); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: import export|export-remote|restore-local|inspect|load|verify|repair-staged-floats [options]")
	}
	mode := args[0]
	flags := flag.NewFlagSet("import "+mode, flag.ContinueOnError)
	source := flags.String("source", "", "standalone SQLite backup (export only)")
	output := flags.String("output", "", "new private artifact or SQLite database (export/export-remote/restore-local)")
	path := flags.String("snapshot", "", "typed JSONL snapshot artifact")
	expectedHost := flags.String("expected-host", "", "required exact database hostname for load/verify/export-remote/repair; independently reviewed")
	writersPaused := flags.Bool("writers-paused", false, "export-remote/repair only: all database writers are stopped and drained")
	applyRepair := flags.Bool("apply", false, "repair-staged-floats only: explicitly permit bounded proven transport repair")
	expectedFloatCells := flags.Int("expected-float-cells", 0, "repair only: exact independently audited changed REAL cell count, 1..32")
	initialize := flags.Bool("initialize", false, "load only into a new empty destination")
	resume := flags.Bool("resume", false, "resume this exact snapshot's existing staged import")
	rows := flags.Int("batch-rows", 100, "maximum rows per import transaction, capped at 120")
	timeout := flags.Duration("timeout", 15*time.Minute, "overall command deadline")
	if e := flags.Parse(args[1:]); e != nil {
		return e
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	if *timeout <= 0 {
		return fmt.Errorf("timeout must be positive")
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	var result any
	switch mode {
	case "export":
		if *source == "" || *output == "" {
			return fmt.Errorf("export requires --source and --output")
		}
		snapshot, e := snapshotimport.Export(ctx, *source, *output)
		if e != nil {
			return e
		}
		result = snapshot
	case "export-remote":
		if !*writersPaused {
			return fmt.Errorf("export-remote requires --writers-paused after stopping and draining every writer")
		}
		if *output == "" {
			return fmt.Errorf("export-remote requires --output")
		}
		if e := validateExpectedHost(*expectedHost, os.Getenv("TURSO_DATABASE_URL")); e != nil {
			return e
		}
		db, e := turso.New(os.Getenv("TURSO_DATABASE_URL"), os.Getenv("TURSO_AUTH_TOKEN"))
		if e != nil {
			return e
		}
		snapshot, e := snapshotimport.ExportRemote(ctx, db, *output, snapshotimport.RemoteExportOptions{WritersPaused: *writersPaused})
		if e != nil {
			return e
		}
		result = snapshot
	case "restore-local":
		if *path == "" || *output == "" {
			return fmt.Errorf("restore-local requires --snapshot and new --output")
		}
		snapshot, e := snapshotimport.RestoreLocal(ctx, *path, *output)
		if e != nil {
			return e
		}
		result = snapshot
	case "inspect":
		if *path == "" {
			return fmt.Errorf("inspect requires --snapshot")
		}
		snapshot, e := snapshotimport.Read(*path)
		if e != nil {
			return e
		}
		result = snapshot
	case "repair-staged-floats":
		if !*applyRepair || !*writersPaused || *expectedFloatCells < 1 || *expectedFloatCells > 32 {
			return fmt.Errorf("repair-staged-floats requires --apply --writers-paused --expected-float-cells 1..32")
		}
		if *path == "" {
			return fmt.Errorf("repair-staged-floats requires --snapshot")
		}
		if e := validateExpectedHost(*expectedHost, os.Getenv("TURSO_DATABASE_URL")); e != nil {
			return e
		}
		db, e := turso.New(os.Getenv("TURSO_DATABASE_URL"), os.Getenv("TURSO_AUTH_TOKEN"))
		if e != nil {
			return e
		}
		report, e := snapshotimport.RepairStagedFloats(ctx, db, *path, snapshotimport.FloatRepairOptions{Apply: *applyRepair, WritersPaused: *writersPaused, ExpectedFloatCells: *expectedFloatCells})
		if e != nil {
			return e
		}
		result = report
	case "load", "verify":
		if *path == "" {
			return fmt.Errorf("load/verify requires --snapshot")
		}
		if e := validateExpectedHost(*expectedHost, os.Getenv("TURSO_DATABASE_URL")); e != nil {
			return e
		}
		db, e := turso.New(os.Getenv("TURSO_DATABASE_URL"), os.Getenv("TURSO_AUTH_TOKEN"))
		if e != nil {
			return e
		}
		if mode == "load" {
			report, e := snapshotimport.Transfer(ctx, db, *path, snapshotimport.Options{Initialize: *initialize, Resume: *resume, BatchRows: *rows})
			if e != nil {
				return e
			}
			result = report
		} else {
			if *initialize || *resume {
				return fmt.Errorf("verify is read-only and does not accept initialize/resume")
			}
			report, e := snapshotimport.Verify(ctx, db, *path)
			if e != nil {
				return e
			}
			result = report
		}
	default:
		return fmt.Errorf("unknown import mode; use export, export-remote, restore-local, inspect, load, verify or repair-staged-floats")
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(result)
}

// Validate independently supplied target intent before reading credentials or
// constructing a destination client. This function never performs network I/O.
func validateExpectedHost(expected, rawURL string) error {
	if expected == "" {
		return fmt.Errorf("load/verify/export-remote/repair requires --expected-host")
	}
	if len(expected) > 253 || strings.ContainsAny(expected, "/:@?#\\* []\t\r\n") || strings.TrimSpace(expected) != expected {
		return fmt.Errorf("expected-host must be one exact hostname, without a URL, port, wildcard or credentials")
	}
	for _, label := range strings.Split(expected, ".") {
		if len(label) < 1 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return fmt.Errorf("invalid expected-host")
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return fmt.Errorf("invalid expected-host")
			}
		}
	}
	parsed, e := url.Parse(rawURL)
	if e != nil {
		return fmt.Errorf("invalid destination database URL")
	}
	// Reuse the production origin/TLS checks with a non-secret placeholder. New
	// creates a client only; it sends no requests and does not need real credentials.
	if _, e = turso.New(rawURL, "target-validation-only"); e != nil {
		return fmt.Errorf("invalid destination database URL")
	}
	if !strings.EqualFold(parsed.Hostname(), expected) {
		return fmt.Errorf("destination hostname does not match --expected-host")
	}
	return nil
}
