// lexicon validates local schemas by default. Publication and public DNS/PDS
// verification are explicit operator actions; neither runs during service boot.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"time"

	"github.com/CountableNewt/my-context-protocol/services/go/ataccount"
	"github.com/CountableNewt/my-context-protocol/services/go/atproto"
	"github.com/CountableNewt/my-context-protocol/services/go/lexiconpublish"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("lexicon", flag.ContinueOnError)
	directory := flags.String("directory", "../../lexicons", "directory of reviewed product Lexicon JSON files")
	authority := flags.String("expected-authority", "", "required independently reviewed authority DID")
	account := flags.String("account", "", "linked application account UUID; required for publication")
	publish := flags.Bool("publish", false, "explicitly publish compatible schemas through the linked account's PDS")
	verify := flags.Bool("verify", false, "independently verify public DNS authority and exact PDS schema CIDs")
	dryRun := flags.Bool("dry-run", false, "validate locally without credentials, writes or network access (default)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected Lexicon arguments")
	}
	if *authority != lexiconpublish.Authority {
		return errors.New("--expected-authority must name the reviewed samclemente.me DID")
	}
	if *dryRun && (*publish || *verify) {
		return errors.New("--dry-run cannot be combined with --publish or --verify")
	}
	if *publish && *account == "" {
		return errors.New("--publish requires --account for the explicitly linked authority")
	}
	schemas, err := lexiconpublish.LoadDirectory(*directory)
	if err != nil {
		return err
	}
	dns, err := lexiconpublish.DNSManifest(*authority, schemas)
	if err != nil {
		return err
	}
	report := struct {
		Status      string                     `json:"status"`
		Authority   string                     `json:"authority"`
		Account     string                     `json:"account_id,omitempty"`
		SchemaCount int                        `json:"schema_count"`
		Schemas     []lexiconpublish.Schema    `json:"schemas"`
		DNS         []lexiconpublish.DNSRecord `json:"dns_records"`
		Receipts    []lexiconpublish.Receipt   `json:"receipts,omitempty"`
	}{Status: "dry-run", Authority: *authority, Account: *account, SchemaCount: len(schemas), Schemas: schemas, DNS: dns}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if *publish {
		db, err := turso.New(os.Getenv("TURSO_DATABASE_URL"), os.Getenv("TURSO_AUTH_TOKEN"))
		if err != nil {
			return err
		}
		manager, _, err := ataccount.FromEnvironment(db, os.Getenv)
		if err != nil {
			return err
		}
		if manager == nil {
			return errors.New("ATProto authorization is not configured; link the authority account before publication")
		}
		linked, err := manager.Identities.ATProtoIdentity(ctx, *account)
		if err != nil {
			return err
		}
		if linked == nil || linked.DID != *authority || linked.AccountID != *account {
			return errors.New("application account is not linked to the reviewed Lexicon authority")
		}
		report.Receipts, err = lexiconpublish.Publish(ctx, leasedRecords{manager: manager, account: *account}, atproto.SQLJournal{DB: db}, *authority, schemas)
		if err != nil {
			return err
		}
		report.Status = "written_unverified"
	}
	if *verify {
		records, err := lexiconpublish.PublicRecords(ctx, *authority)
		if err != nil {
			return err
		}
		report.Receipts, err = lexiconpublish.Verify(ctx, records, net.DefaultResolver, *authority, schemas)
		if err != nil {
			return err
		}
		report.Status = "verified"
	}
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	return encoder.Encode(report)
}

// Each PDS request holds a refresh lease. The multi-schema operator operation
// can span bounded leases; schema writes still use the preflight CID guard.
type leasedRecords struct {
	manager *ataccount.Manager
	account string
}

func (r leasedRecords) Get(ctx context.Context, m atproto.Mutation) (atproto.Record, error) {
	var result atproto.Record
	err := r.manager.WithRecords(ctx, r.account, func(ctx context.Context, records atproto.Records) error {
		var err error
		result, err = records.Get(ctx, m)
		return err
	})
	return result, err
}
func (r leasedRecords) Put(ctx context.Context, m atproto.Mutation) (atproto.Reference, error) {
	var result atproto.Reference
	err := r.manager.WithRecords(ctx, r.account, func(ctx context.Context, records atproto.Records) error {
		var err error
		result, err = records.Put(ctx, m)
		return err
	})
	return result, err
}
