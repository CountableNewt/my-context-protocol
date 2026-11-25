package recordservice

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/CountableNewt/my-context-protocol/services/go/domainrecords"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
)

func TestConflictReviewReadsRemoteWithoutApplyingPrivateIntent(t *testing.T) {
	f := fixtureSQL(t, false)
	ctx := context.Background()
	sc := f.ctx(t).Value(scopeKey{}).(scope)
	intent := nativeIntent{ID: "reviewed", Account: "account", Project: "project", SourceCID: sc.cid, SourceDigest: sc.digest, TargetDigest: "unpublished", Statements: []turso.Statement{{SQL: "UPDATE private_switch SET value=2"}}}
	if err := saveNative(ctx, f.db, intent); err != nil {
		t.Fatal(err)
	}
	review, err := f.service.inspectConflict(ctx, &f.engine, "account", testOwner, "project")
	if err != nil {
		t.Fatal(err)
	}
	if review.Status != "resumable" || review.CurrentManifestCID != sc.cid {
		t.Fatalf("unexpected review: %+v", review)
	}
	// An independently published owner edit changes the authoritative pointer but
	// does not execute the lost local private operation.
	_, err = f.engine.Update(ctx, "account", testOwner, "project", []domainrecords.Change{{Table: "projects", ID: "project", Values: map[string]turso.Value{"name": turso.Text("External")}}})
	if err != nil {
		t.Fatal(err)
	}
	review, err = f.service.inspectConflict(ctx, &f.engine, "account", testOwner, "project")
	if err != nil {
		t.Fatal(err)
	}
	if review.Status != "superseded" || review.CurrentManifestCID == sc.cid {
		t.Fatalf("stale review: %+v", review)
	}
	r, err := f.db.Execute(ctx, turso.Statement{SQL: "SELECT value FROM private_switch WHERE id='guard'", WantRows: true})
	if err != nil || cell(r.Rows[0][0]) != "1" {
		t.Fatal("review executed private effects", err)
	}
	encoded, _ := json.Marshal(review)
	if string(encoded) == "" {
		t.Fatal("empty review")
	}
	// Target digest, even at a new CID, must remain resumable rather than discardable.
	intent.TargetDigest = review.CurrentDigest
	_, err = f.db.Execute(ctx, turso.Statement{SQL: "UPDATE domain_native_intents SET target_digest=? WHERE id='reviewed'", Args: []turso.Value{turso.Text(intent.TargetDigest)}})
	if err != nil {
		t.Fatal(err)
	}
	review, err = f.service.inspectConflict(ctx, &f.engine, "account", testOwner, "project")
	if err != nil || review.Status != "resumable" {
		t.Fatal(review, err)
	}
	_, err = f.service.inspectConflict(ctx, &f.engine, "other", testOwner, "project")
	if err == nil {
		t.Fatal("cross tenant review allowed")
	}
}

func TestExplicitConflictDiscardNeverAppliesLostPrivateEffects(t *testing.T) {
	f := fixtureSQL(t, false)
	ctx := context.Background()
	sc := f.ctx(t).Value(scopeKey{}).(scope)
	intent := nativeIntent{ID: "discarded", Account: "account", Project: "project", SourceCID: sc.cid, SourceDigest: sc.digest, TargetDigest: "lost-target", Statements: []turso.Statement{{SQL: "UPDATE private_switch SET value=99"}}}
	if err := saveNative(ctx, f.db, intent); err != nil {
		t.Fatal(err)
	}
	if err := f.service.resolveConflict(ctx, &f.engine, "account", testOwner, "project", intent.ID, sc.cid); err == nil {
		t.Fatal("discarded resumable source")
	}
	_, err := f.engine.Update(ctx, "account", testOwner, "project", []domainrecords.Change{{Table: "projects", ID: "project", Values: map[string]turso.Value{"name": turso.Text("External generation")}}})
	if err != nil {
		t.Fatal(err)
	}
	remote, _, err := f.engine.ManifestInfo(ctx, testOwner, "project")
	if err != nil {
		t.Fatal(err)
	}
	// Simulate the normal conflict: owner PDS advanced while local receipt is old.
	_, err = f.db.Batch(ctx, []turso.Statement{{SQL: "UPDATE domain_canonical_projects SET manifest_cid=?,review_digest=? WHERE project_id='project'", Args: []turso.Value{turso.Text(sc.cid), turso.Text(sc.digest)}}, {SQL: "UPDATE projects SET name='Original' WHERE id='project'"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.service.resolveConflict(ctx, &f.engine, "account", testOwner, "project", intent.ID, sc.cid); err == nil {
		t.Fatal("accepted stale review CID")
	}
	if err := f.service.resolveConflict(ctx, &f.engine, "account", testOwner, "project", intent.ID, remote.CID); err != nil {
		t.Fatal(err)
	}
	r, err := f.db.Execute(ctx, turso.Statement{SQL: "SELECT status,statements_json,(SELECT value FROM private_switch WHERE id='guard'),(SELECT name FROM projects WHERE id='project') FROM domain_native_intents WHERE id='discarded'", WantRows: true})
	if err != nil {
		t.Fatal(err)
	}
	if cell(r.Rows[0][0]) != "abandoned" || cell(r.Rows[0][1]) != "[]" || cell(r.Rows[0][2]) != "1" || cell(r.Rows[0][3]) != "External generation" {
		t.Fatalf("unsafe discard: %+v", r.Rows)
	}
}
