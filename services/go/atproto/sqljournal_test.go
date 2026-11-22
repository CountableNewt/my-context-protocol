package atproto

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/CountableNewt/my-context-protocol/services/go/internal/sqlitetest"
)

func digestFor(m Mutation) string {
	data, _ := json.Marshal(m)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestSQLJournalReserveCompleteAndReconcile(t *testing.T) {
	ctx := context.Background()
	db := sqlitetest.New(t, MigrationSQL)
	j := SQLJournal{DB: db}
	m := mutation()
	digest := digestFor(m)
	if ref, err := j.Reserve(ctx, m.Repo, "intent", digest, m); err != nil || ref != nil {
		t.Fatalf("%+v %v", ref, err)
	}
	pending, err := j.Pending(ctx, m.Repo, "", 10)
	if err != nil || len(pending) != 1 || pending[0].Key != "intent" || pending[0].Mutation.URI() != m.URI() {
		t.Fatalf("%+v %v", pending, err)
	}
	if ref, err := j.Reserve(ctx, m.Repo, "intent", digest, m); err != nil || ref != nil {
		t.Fatalf("%+v %v", ref, err)
	}
	ref := Reference{URI: m.URI(), CID: "bafyreiaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	if err = j.Complete(ctx, m.Repo, "intent", digest, ref); err != nil {
		t.Fatal(err)
	}
	if err = j.Complete(ctx, m.Repo, "intent", digest, ref); err != nil {
		t.Fatal("idempotent completion", err)
	}
	completed, err := j.Reserve(ctx, m.Repo, "intent", digest, m)
	if err != nil || completed == nil || *completed != ref {
		t.Fatalf("%+v %v", completed, err)
	}
	pending, err = j.Pending(ctx, m.Repo, "", 10)
	if err != nil || len(pending) != 0 {
		t.Fatalf("%+v %v", pending, err)
	}
	changed := m
	changed.Record = json.RawMessage(`{"$type":"dev.mycontextprotocol.project","name":"different"}`)
	if _, err = j.Reserve(ctx, m.Repo, "intent", digestFor(changed), changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed payload: %v", err)
	}
	ref.CID = "bafyreiaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaae"
	if err = j.Complete(ctx, m.Repo, "intent", digest, ref); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed completion: %v", err)
	}
}

func TestSQLJournalOwnershipAndBounds(t *testing.T) {
	ctx := context.Background()
	j := SQLJournal{DB: sqlitetest.New(t, MigrationSQL)}
	m := mutation()
	digest := digestFor(m)
	if _, err := j.Reserve(ctx, "did:plc:foreign", "intent", digest, m); err == nil {
		t.Fatal("foreign owner")
	}
	if _, err := j.Reserve(ctx, m.Repo, "intent", "bad digest", m); err == nil {
		t.Fatal("unchecked digest")
	}
	if _, err := j.Reserve(ctx, m.Repo, "intent", digest, m); err != nil {
		t.Fatal(err)
	}
	pending, err := j.Pending(ctx, "did:plc:foreign", "", 100)
	if err != nil || len(pending) != 0 {
		t.Fatal("owner leaked", err)
	}
	if _, err = j.Pending(ctx, m.Repo, "", 101); err == nil {
		t.Fatal("unbounded list")
	}
	if err = j.Complete(ctx, m.Repo, "missing", digest, Reference{m.URI(), "bafyreiaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}); !errors.Is(err, ErrConflict) {
		t.Fatal("unreserved completion", err)
	}
	for _, key := range []string{"a", "b", "c"} {
		if _, err = j.Reserve(ctx, m.Repo, key, digest, m); err != nil {
			t.Fatal(err)
		}
	}
	page, err := j.Pending(ctx, m.Repo, "a", 2)
	if err != nil || len(page) != 2 || page[0].Key != "b" || page[1].Key != "c" {
		t.Fatalf("%+v %v", page, err)
	}
}

func TestSQLJournalConcurrentDigestConflict(t *testing.T) {
	ctx := context.Background()
	j := SQLJournal{DB: sqlitetest.New(t, MigrationSQL)}
	m := mutation()
	other := m
	other.Record = json.RawMessage(`{"$type":"dev.mycontextprotocol.project","name":"other"}`)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, candidate := range []Mutation{m, other} {
		wg.Add(1)
		go func(candidate Mutation) {
			defer wg.Done()
			_, err := j.Reserve(ctx, candidate.Repo, "same-key", digestFor(candidate), candidate)
			results <- err
		}(candidate)
	}
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, ErrConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("success=%d conflict=%d", success, conflict)
	}
}
