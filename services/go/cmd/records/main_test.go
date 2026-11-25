package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CountableNewt/my-context-protocol/services/go/atproto"
	"github.com/CountableNewt/my-context-protocol/services/go/domainrecords"
)

type fakeOperations struct {
	publishCalls, reconcileCalls, verifyCalls int
	consent                                   domainrecords.Consent
	err                                       error
}

func (*fakeOperations) Inventory(context.Context, string, string) (domainrecords.Review, error) {
	return domainrecords.Review{OwnerDID: "did:plc:owner", ProjectID: "project", Digest: "reviewed", Entries: []domainrecords.Entry{{Table: "projects", ID: "stable-legacy-id"}}, Warnings: []string{"Review sensitive metadata"}, HasTelemetry: true}, nil
}
func (s *fakeOperations) Publish(_ context.Context, _ string, _ domainrecords.Review, c domainrecords.Consent) (atproto.Reference, error) {
	s.publishCalls++
	s.consent = c
	return atproto.Reference{URI: "at://owner/manifest/project", CID: "cid"}, s.err
}
func (s *fakeOperations) Reconcile(context.Context, string, string) error {
	s.reconcileCalls++
	return s.err
}
func (s *fakeOperations) Verify(context.Context, string, string) (domainrecords.Review, error) {
	s.verifyCalls++
	return domainrecords.Review{Digest: "verified"}, s.err
}
func TestParseRequiresExplicitBoundReviewedPublication(t *testing.T) {
	for _, args := range [][]string{{}, {"--account", "account"}, {"--account", "account", "--project", "project", "--publish"}, {"--account", "account", "--project", "project", "--publish", "--public-consent"}, {"--account", "account", "--project", "project", "--dry-run", "--publish"}, {"--account", "account", "--project", "project", "--telemetry-consent"}} {
		if _, e := parse(args); e == nil {
			t.Fatal(args)
		}
	}
}
func TestDryRunWritesPrivateReviewWithoutPublishingAndNeverOverwrites(t *testing.T) {
	s := &fakeOperations{}
	path := filepath.Join(t.TempDir(), "review.json")
	o, e := parse([]string{"--account", "account", "--project", "project", "--review-file", path})
	if e != nil {
		t.Fatal(e)
	}
	var out bytes.Buffer
	if e = execute(context.Background(), o, s, &out); e != nil {
		t.Fatal(e)
	}
	if s.publishCalls != 0 || !strings.Contains(out.String(), "stable-legacy-id") || !strings.Contains(out.String(), "Review sensitive metadata") {
		t.Fatal(out.String())
	}
	info, e := os.Stat(path)
	if e != nil || info.Mode().Perm() != 0600 {
		t.Fatal(info, e)
	}
	if e = execute(context.Background(), o, s, &out); e == nil {
		t.Fatal("overwrote review")
	}
}
func TestPublicationChecksCurrentDigestAndKeepsPendingExplicit(t *testing.T) {
	s := &fakeOperations{err: &domainrecords.PendingError{Completed: 25, Total: 50, Digest: "reviewed"}}
	o := options{account: "account", project: "project", publish: true, public: true, digest: "changed"}
	var out bytes.Buffer
	if e := execute(context.Background(), o, s, &out); !errors.Is(e, domainrecords.ErrChanged) || s.publishCalls != 0 {
		t.Fatal(e)
	}
	o.digest = "reviewed"
	if e := execute(context.Background(), o, s, &out); e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(out.String(), `"status": "pending"`) || s.consent.Telemetry || !s.consent.Public {
		t.Fatal(out.String(), s.consent)
	}
	o.telemetry = true
	if e := execute(context.Background(), o, s, &out); e != nil || !s.consent.Telemetry {
		t.Fatal(e)
	}
}
func TestReconstructionActionsNeverPublish(t *testing.T) {
	for _, verify := range []bool{false, true} {
		s := &fakeOperations{}
		var out bytes.Buffer
		if e := execute(context.Background(), options{account: "account", project: "project", verify: verify, reconcile: !verify}, s, &out); e != nil || s.publishCalls != 0 {
			t.Fatal(e)
		}
		if verify && s.verifyCalls != 1 || !verify && s.reconcileCalls != 1 {
			t.Fatal(s)
		}
	}
}
