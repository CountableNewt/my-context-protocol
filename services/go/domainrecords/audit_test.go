package domainrecords

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/CountableNewt/my-context-protocol/services/go/atproto"
)

func completeAudit(t *testing.T, e Engine) {
	t.Helper()
	for i := 0; i < 30; i++ {
		err := e.Audit(context.Background(), account, owner, "project")
		if err == nil {
			return
		}
		if !errors.Is(err, ErrPending) {
			t.Fatal(err)
		}
	}
	t.Fatal("audit did not finish")
}
func TestAuditDetectsDeletionWithUnchangedManifestAndRequiresFullRepair(t *testing.T) {
	e, records, _ := migratedAndRenamed(t)
	ctx := context.Background()
	e.WorkLimit = 1
	completeAudit(t, e)
	var removed atproto.Record
	records.mu.Lock()
	for uri, r := range records.values {
		if strings.Contains(uri, "/"+Namespace+"project/") && strings.Contains(string(r.Value), "Current") {
			removed = r
			delete(records.values, uri)
			break
		}
	}
	records.mu.Unlock()
	if removed.URI == "" {
		t.Fatal("missing test entity")
	}
	var err error
	for i := 0; i < 30; i++ {
		err = e.Audit(ctx, account, owner, "project")
		if !errors.Is(err, ErrPending) {
			break
		}
	}
	if !errors.Is(err, ErrAuditInvalid) {
		t.Fatalf("deletion was not rejected: %v", err)
	}
	blocked, err := e.AuditBlocked(ctx, account, "project")
	if err != nil || !blocked {
		t.Fatalf("missing invalid gate: %v %v", blocked, err)
	}
	if blocked, err = e.AuditBlocked(ctx, "other", "project"); err != nil || blocked {
		t.Fatal("cross-account audit disclosure")
	}
	if _, err := e.Update(ctx, account, owner, "project", nil); !errors.Is(err, ErrAuditInvalid) {
		t.Fatalf("invalid owner graph permitted mutation: %v", err)
	}
	// Repair is an owner action. Auditing never manufactures missing PDS data.
	records.mu.Lock()
	_, exists := records.values[removed.URI]
	records.values[removed.URI] = removed
	records.mu.Unlock()
	if exists {
		t.Fatal("audit republished missing record")
	}
	err = e.Audit(ctx, account, owner, "project")
	if !errors.Is(err, ErrPending) {
		t.Fatalf("repair should be bounded: %v", err)
	}
	blocked, _ = e.AuditBlocked(ctx, account, "project")
	if !blocked {
		t.Fatal("pending repair cleared invalid gate")
	}
	completeAudit(t, e)
	blocked, err = e.AuditBlocked(ctx, account, "project")
	if err != nil || blocked {
		t.Fatalf("repair gate persisted: %v %v", blocked, err)
	}
}
func TestAuditMissingManifestBlocksReadiness(t *testing.T) {
	e, records, _ := migratedAndRenamed(t)
	records.mu.Lock()
	for uri := range records.values {
		if strings.Contains(uri, "/"+Namespace+"manifest/") {
			delete(records.values, uri)
		}
	}
	records.mu.Unlock()
	if err := e.Audit(context.Background(), account, owner, "project"); !errors.Is(err, ErrAuditInvalid) {
		t.Fatalf("missing manifest: %v", err)
	}
}
