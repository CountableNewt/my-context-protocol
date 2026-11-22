// Command import performs explicit offline snapshot export, staged loading and
// verification. Credentials are read only from destination environment variables.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
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
		return fmt.Errorf("usage: import export|inspect|load|verify [options]")
	}
	mode := args[0]
	flags := flag.NewFlagSet("import "+mode, flag.ContinueOnError)
	source := flags.String("source", "", "standalone SQLite backup (export only)")
	output := flags.String("output", "", "new private JSONL artifact (export only)")
	path := flags.String("snapshot", "", "typed JSONL snapshot artifact")
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
	case "inspect":
		if *path == "" {
			return fmt.Errorf("inspect requires --snapshot")
		}
		snapshot, e := snapshotimport.Read(*path)
		if e != nil {
			return e
		}
		result = snapshot
	case "load", "verify":
		if *path == "" {
			return fmt.Errorf("load/verify requires --snapshot")
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
		return fmt.Errorf("unknown import mode; use export, inspect, load or verify")
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(result)
}
