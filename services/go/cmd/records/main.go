// records performs explicit, review-bound canonical record migration. Services
// never invoke it at startup and inventory is the default action.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/CountableNewt/my-context-protocol/services/go/atplatform"
	"github.com/CountableNewt/my-context-protocol/services/go/atproto"
	"github.com/CountableNewt/my-context-protocol/services/go/domainrecords"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
)

type operations interface {
	Inventory(context.Context, string, string) (domainrecords.Review, error)
	Publish(context.Context, string, domainrecords.Review, domainrecords.Consent) (atproto.Reference, error)
	Reconcile(context.Context, string, string) error
	Verify(context.Context, string, string) (domainrecords.Review, error)
}
type options struct {
	account, project, digest, file                        string
	publish, reconcile, verify, dryRun, public, telemetry bool
}
type report struct {
	Status    string                `json:"status"`
	Account   string                `json:"account_id"`
	Project   string                `json:"project_id"`
	Review    *domainrecords.Review `json:"review,omitempty"`
	Reference *atproto.Reference    `json:"reference,omitempty"`
	Completed int                   `json:"completed,omitempty"`
	Total     int                   `json:"total,omitempty"`
}

func parse(args []string) (options, error) {
	var o options
	f := flag.NewFlagSet("records", flag.ContinueOnError)
	f.StringVar(&o.account, "account", "", "required linked application account UUID")
	f.StringVar(&o.project, "project", "", "required project UUID owned by account")
	f.StringVar(&o.digest, "review-digest", "", "exact independently reviewed inventory digest; required for publication")
	f.StringVar(&o.file, "review-file", "", "write inventory JSON to a new private file (never overwrite)")
	f.BoolVar(&o.publish, "publish", false, "publish at most one bounded batch; rerun with the same consent to continue")
	f.BoolVar(&o.reconcile, "reconcile", false, "rebuild private projections from validated canonical owner records")
	f.BoolVar(&o.verify, "verify", false, "read-only reconstruction and local projection comparison")
	f.BoolVar(&o.dryRun, "dry-run", false, "read-only inventory (default)")
	f.BoolVar(&o.public, "public-consent", false, "explicitly authorize public publication of reviewed product metadata/history")
	f.BoolVar(&o.telemetry, "telemetry-consent", false, "separately authorize reviewed existing telemetry to become public")
	if e := f.Parse(args); e != nil {
		return o, e
	}
	if f.NArg() != 0 {
		return o, errors.New("unexpected records arguments")
	}
	if o.account == "" || o.project == "" {
		return o, errors.New("--account and --project are required; accounts are never inferred")
	}
	modes := 0
	for _, v := range []bool{o.publish, o.reconcile, o.verify, o.dryRun} {
		if v {
			modes++
		}
	}
	if modes > 1 {
		return o, errors.New("choose one of --publish, --reconcile, --verify or --dry-run")
	}
	if o.publish && (o.digest == "" || !o.public) {
		return o, errors.New("--publish requires --review-digest and --public-consent")
	}
	if !o.publish && (o.public || o.telemetry || o.digest != "") {
		return o, errors.New("publication consent and digest flags require --publish")
	}
	if o.file != "" && (o.publish || o.reconcile || o.verify) {
		return o, errors.New("--review-file is only available with dry-run inventory")
	}
	return o, nil
}
func main() {
	if e := run(os.Args[1:], os.Stdout); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
func run(args []string, out io.Writer) error {
	o, e := parse(args)
	if e != nil {
		return e
	}
	db, e := turso.New(os.Getenv("TURSO_DATABASE_URL"), os.Getenv("TURSO_AUTH_TOKEN"))
	if e != nil {
		return e
	}
	p, e := atplatform.New(db, os.Getenv)
	if e != nil {
		return e
	}
	if p == nil {
		return errors.New("ATProto is not configured; link the account before record migration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 70*time.Second)
	defer cancel()
	return execute(ctx, o, platformOperations{p}, out)
}
func execute(ctx context.Context, o options, service operations, out io.Writer) error {
	result := report{Status: "dry-run", Account: o.account, Project: o.project}
	switch {
	case o.reconcile:
		if e := service.Reconcile(ctx, o.account, o.project); e != nil {
			return pendingOutput(e, &result, out)
		}
		result.Status = "reconciled"
	case o.verify:
		review, e := service.Verify(ctx, o.account, o.project)
		if e != nil {
			return e
		}
		result.Status = "verified"
		result.Review = &review
	default:
		review, e := service.Inventory(ctx, o.account, o.project)
		if e != nil {
			return e
		}
		if review.ProjectID != o.project || review.OwnerDID == "" || review.Digest == "" {
			return errors.New("invalid account-bound publication inventory")
		}
		result.Review = &review
		if o.publish {
			if review.Digest != o.digest {
				return domainrecords.ErrChanged
			}
			ref, e := service.Publish(ctx, o.account, review, domainrecords.Consent{ReviewDigest: o.digest, Public: o.public, Telemetry: o.telemetry})
			if e != nil {
				return pendingOutput(e, &result, out)
			}
			result.Status = "published"
			result.Reference = &ref
		}
	}
	if o.file != "" {
		file, e := os.OpenFile(o.file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			return e
		}
		e = encode(file, result)
		closeErr := file.Close()
		if e != nil {
			return e
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return encode(out, result)
}
func pendingOutput(e error, result *report, out io.Writer) error {
	var pending *domainrecords.PendingError
	if errors.As(e, &pending) {
		result.Status = "pending"
		result.Completed = pending.Completed
		result.Total = pending.Total
		return encode(out, result)
	}
	if errors.Is(e, domainrecords.ErrPending) {
		result.Status = "pending"
		return encode(out, result)
	}
	return e
}
func encode(out io.Writer, result any) error {
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(result)
}

type platformOperations struct{ p *atplatform.Platform }

func (s platformOperations) Inventory(ctx context.Context, account, project string) (domainrecords.Review, error) {
	linked, e := s.p.Accounts.Identities.ATProtoIdentity(ctx, account)
	if e != nil {
		return domainrecords.Review{}, e
	}
	if linked == nil || linked.AccountID != account {
		return domainrecords.Review{}, errors.New("account has no linked ATProto identity")
	}
	return (domainrecords.Engine{DB: s.p.Raw}).Inventory(ctx, account, linked.DID, project)
}
func (s platformOperations) Publish(ctx context.Context, account string, review domainrecords.Review, consent domainrecords.Consent) (ref atproto.Reference, err error) {
	err = s.p.Records.WithEngine(ctx, account, func(ctx context.Context, owner string, e *domainrecords.Engine) error {
		if owner != review.OwnerDID {
			return errors.New("linked owner changed; review again")
		}
		var err error
		ref, err = e.Publish(ctx, account, review, consent)
		return err
	})
	return
}
func (s platformOperations) Reconcile(ctx context.Context, account, project string) error {
	if err := s.p.Records.WithEngine(ctx, account, func(ctx context.Context, owner string, e *domainrecords.Engine) error {
		return e.Audit(ctx, account, owner, project)
	}); err != nil {
		return err
	}
	return s.p.Records.Reconcile(ctx, account, project)
}
func (s platformOperations) Verify(ctx context.Context, account, project string) (review domainrecords.Review, err error) {
	err = s.p.Records.WithEngine(ctx, account, func(ctx context.Context, owner string, e *domainrecords.Engine) error {
		var err error
		review, err = e.Verify(ctx, account, owner, project)
		return err
	})
	return
}
