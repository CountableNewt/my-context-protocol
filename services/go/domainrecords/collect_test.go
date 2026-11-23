package domainrecords

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/CountableNewt/my-context-protocol/services/go/atproto"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
	cidlib "github.com/ipfs/go-cid"
	"github.com/multiformats/go-multihash"
)

func (m *memoryRecords) headLocked() string {
	raw, _ := json.Marshal(m.values)
	hash, _ := multihash.Sum(raw, multihash.SHA2_256, -1)
	return cidlib.NewCidV1(cidlib.DagCBOR, hash).String()
}
func (m *memoryRecords) Head(context.Context, string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.headLocked(), nil
}
func (m *memoryRecords) Delete(ctx context.Context, mutation atproto.Mutation) error {
	head, err := m.Head(ctx, mutation.Repo)
	if err != nil {
		return err
	}
	return m.DeleteAtCommit(ctx, mutation, head)
}
func (m *memoryRecords) DeleteAtCommit(_ context.Context, mutation atproto.Mutation, head string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.headLocked() != head {
		return atproto.ErrConflict
	}
	current, ok := m.values[mutation.URI()]
	if !ok {
		return atproto.ErrNotFound
	}
	if mutation.ExpectedCID == nil || current.CID != *mutation.ExpectedCID {
		return atproto.ErrConflict
	}
	delete(m.values, mutation.URI())
	return nil
}
func migratedAndRenamed(t *testing.T) (Engine, *memoryRecords, Review) {
	t.Helper()
	e, _, records := fixture(t)
	ctx := context.Background()
	review, err := e.Inventory(ctx, account, owner, "project")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.Publish(ctx, account, review, approved(review)); err != nil {
		t.Fatal(err)
	}
	if _, err = e.Update(ctx, account, owner, "project", []Change{{Table: "projects", ID: "project", Values: map[string]turso.Value{"name": turso.Text("Current")}}}); err != nil {
		t.Fatal(err)
	}
	return e, records, review
}

func TestCollectionDeletesRetiredVersionsAndPreservesCurrentManifest(t *testing.T) {
	e, _, _ := migratedAndRenamed(t)
	ctx := context.Background()
	before, ref, err := e.readManifest(ctx, owner, "project")
	if err != nil {
		t.Fatal(err)
	}
	first, err := e.Collect(ctx, account, owner, "project", 1)
	if err != nil || first.Deleted != 1 || !first.More {
		t.Fatalf("bounded collection: %+v %v", first, err)
	}
	last, err := e.Collect(ctx, account, owner, "project", 25)
	if err != nil || last.Deleted != 1 || last.More {
		t.Fatalf("retirement completion: %+v %v", last, err)
	}
	after, current, err := e.readManifest(ctx, owner, "project")
	if err != nil || current.CID != ref.CID || before.Digest != after.Digest {
		t.Fatal("current manifest deleted or changed")
	}
	if err = e.Reconcile(ctx, account, owner, "project"); err != nil {
		t.Fatal("live references damaged", err)
	}
}
func TestCollectionProtectsReferencesInPendingPublication(t *testing.T) {
	e, _, old := migratedAndRenamed(t)
	ctx := context.Background()
	// Protect all records belonging to an unfinished target generation even
	// before its per-entry cursor acknowledgement has been persisted.
	_, err := e.DB.Execute(ctx, statement("INSERT INTO domain_publication_runs(run_id,project_id,owner_did,digest,prior_uri,prior_cid) VALUES('pending','project',?,?,'','')", turso.Text(owner), turso.Text(old.Digest)))
	if err != nil {
		t.Fatal(err)
	}
	report, err := e.Collect(ctx, account, owner, "project", 25)
	if err != nil || report.Deleted != 0 {
		t.Fatalf("pending references collected: %+v %v", report, err)
	}
}

type racingRecords struct {
	*memoryRecords
	raced bool
}

func (r *racingRecords) DeleteAtCommit(ctx context.Context, m atproto.Mutation, head string) error {
	if !r.raced {
		r.raced = true
		_, err := r.memoryRecords.Put(ctx, atproto.Mutation{Repo: owner, Collection: Namespace + "project", RKey: "external-write", Record: json.RawMessage(`{"$type":"` + Namespace + `project","projectId":"other","legacyId":"external"}`)})
		if err != nil {
			return err
		}
	}
	return r.memoryRecords.DeleteAtCommit(ctx, m, head)
}
func TestCollectionRepoCASRejectsConcurrentExternalWrite(t *testing.T) {
	e, records, _ := migratedAndRenamed(t)
	ctx := context.Background()
	e.Records = &racingRecords{memoryRecords: records}
	report, err := e.Collect(ctx, account, owner, "project", 25)
	if !errors.Is(err, atproto.ErrConflict) || report.Deleted != 0 {
		t.Fatalf("concurrent repo write ignored: %+v %v", report, err)
	}
	if _, _, err = e.readManifest(ctx, owner, "project"); err != nil {
		t.Fatal("live manifest damaged")
	}
	if report, err = e.Collect(ctx, account, owner, "project", 25); err != nil || report.Deleted != 2 {
		t.Fatalf("collection failed to resume: %+v %v", report, err)
	}
}
func TestCanonicalRecordDoesNotContainGeneratedSourceInstructions(t *testing.T) {
	document := `{"instructions":"private Git source body","source":{"path":"SKILL.md"},"standardFrontmatterJson":"{\"unknown\":true}","futureUnknown":1}`
	encoded, err := encode("project", Entry{Table: "compiled_skills", ID: "compiled", Values: map[string]turso.Value{"canonical_json": turso.Text(document)}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded.Record), "private Git source body") || !strings.Contains(string(encoded.Record), "futureUnknown") || !strings.Contains(string(encoded.Record), "standardFrontmatterJson") {
		t.Fatal("private source leaked or unknown metadata removed")
	}
}

func TestRevertingAfterGCRecreatesDeletedImmutableRecords(t *testing.T) {
	e, _, _ := migratedAndRenamed(t)
	ctx := context.Background()
	if _, err := e.Collect(ctx, account, owner, "project", 25); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"Project", "Current", "Project", "Current"} {
		if _, err := e.Update(ctx, account, owner, "project", []Change{{Table: "projects", ID: "project", Values: map[string]turso.Value{"name": turso.Text(value)}}}); err != nil {
			t.Fatal(err)
		}
		currentManifest, _, err := e.readManifest(ctx, owner, "project")
		if err != nil {
			t.Fatal(err)
		}
		inventory, err := e.Inventory(ctx, account, owner, "project")
		if err != nil || currentManifest.Digest != inventory.Digest {
			t.Fatal("repeated content CID acknowledged a stale mutable manifest")
		}
		for _, shardRef := range currentManifest.Refs {
			var shard manifest
			if err = e.fetch(ctx, owner, shardRef, &shard); err != nil {
				t.Fatal("manifest points at collected shard", err)
			}
			for _, entityRef := range shard.Refs {
				var entry map[string]json.RawMessage
				if err = e.fetch(ctx, owner, entityRef, &entry); err != nil {
					t.Fatal("manifest points at collected entity", err)
				}
			}
		}
	}
}

type lostDeleteRecords struct {
	*memoryRecords
	lost bool
}

func (r *lostDeleteRecords) DeleteAtCommit(ctx context.Context, m atproto.Mutation, head string) error {
	err := r.memoryRecords.DeleteAtCommit(ctx, m, head)
	if err == nil && !r.lost {
		r.lost = true
		return errors.New("delete acknowledgement lost")
	}
	return err
}
func TestLostDeleteAcknowledgementCannotPoisonFutureRevert(t *testing.T) {
	e, records, _ := migratedAndRenamed(t)
	ctx := context.Background()
	e.Records = &lostDeleteRecords{memoryRecords: records}
	if _, err := e.Collect(ctx, account, owner, "project", 1); err == nil {
		t.Fatal("lost acknowledgement accepted")
	}
	if _, err := e.Update(ctx, account, owner, "project", []Change{{Table: "projects", ID: "project", Values: map[string]turso.Value{"name": turso.Text("Project")}}}); err != nil {
		t.Fatal(err)
	}
	m, _, err := e.readManifest(ctx, owner, "project")
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range m.Refs {
		var shard manifest
		if err = e.fetch(ctx, owner, ref, &shard); err != nil {
			t.Fatal("dangling shard after ambiguous GC", err)
		}
		for _, ref := range shard.Refs {
			var entry map[string]json.RawMessage
			if err = e.fetch(ctx, owner, ref, &entry); err != nil {
				t.Fatal("dangling entity after ambiguous GC", err)
			}
		}
	}
}
