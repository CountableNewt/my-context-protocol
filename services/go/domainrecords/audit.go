package domainrecords

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/CountableNewt/my-context-protocol/services/go/atproto"
	"github.com/CountableNewt/my-context-protocol/services/go/security"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
)

var ErrAuditInvalid = errors.New("canonical records are missing or invalid; repair owner records and reconcile")

type headReader interface {
	Head(context.Context, string) (string, error)
}

// Audit validates every immutable reference even when the mutable manifest CID
// has not changed. A repository head change starts a new bounded audit; unfinished
// calls resume their durable cursor. Actual corruption blocks readiness until a
// full repair audit succeeds. No content is recreated from the SQL cache.
func (e Engine) Audit(ctx context.Context, account, owner, project string) error {
	if err := e.owner(ctx, account, owner); err != nil {
		return err
	}
	consent, local, err := e.state(ctx, account, project)
	if err != nil {
		return err
	}
	if !consent.Public {
		return ErrNotMigrated
	}
	reader, ok := e.Records.(headReader)
	if !ok {
		return errors.New("repository head reader is required for canonical audit")
	}
	head, err := reader.Head(ctx, owner)
	if err != nil {
		return err
	}
	m, ref, err := e.readManifest(ctx, owner, project)
	if err != nil {
		if auditCorruption(err) {
			if saved := e.invalidateAudit(ctx, owner, project, local.CID, head, "ManifestInvalid"); saved != nil {
				return saved
			}
			return errors.Join(ErrAuditInvalid, err)
		}
		return err
	}
	existing, err := e.DB.Execute(ctx, statement("SELECT manifest_cid,repo_head,status,cycle_id FROM domain_project_audits WHERE project_id=? AND owner_did=?", turso.Text(project), turso.Text(owner)))
	if err != nil {
		return err
	}
	start := true
	startHead := head
	if len(existing.Rows) == 1 {
		row := existing.Rows[0]
		if len(row) != 4 {
			return errors.New("invalid audit checkpoint")
		}
		if fmt.Sprint(row[0].Value) == ref.CID {
			if fmt.Sprint(row[2].Value) == "pending" {
				start = false
				startHead = fmt.Sprint(row[1].Value)
			} else if fmt.Sprint(row[2].Value) == "valid" && fmt.Sprint(row[1].Value) == head {
				return nil
			}
		}
	}
	if start {
		cycle, err := security.NewOpaqueToken()
		if err != nil {
			return err
		}
		_, err = e.DB.Batch(ctx, []turso.Statement{
			statement("INSERT INTO domain_project_audits(project_id,owner_did,manifest_cid,repo_head,cycle_id,status,invalid,checked_at) VALUES(?,?,?,?,?,'pending',0,?) ON CONFLICT(project_id) DO UPDATE SET owner_did=excluded.owner_did,manifest_cid=excluded.manifest_cid,repo_head=excluded.repo_head,cycle_id=excluded.cycle_id,status='pending',checked_at=excluded.checked_at", turso.Text(project), turso.Text(owner), turso.Text(ref.CID), turso.Text(head), turso.Text(cycle), turso.Integer(time.Now().Unix())),
			statement("DELETE FROM domain_reconciliation_entries WHERE project_id=? AND manifest_cid=?", turso.Text(project), turso.Text(ref.CID)),
			statement("DELETE FROM domain_reconciliation_runs WHERE project_id=? AND manifest_cid=?", turso.Text(project), turso.Text(ref.CID)),
		})
		if err != nil {
			return err
		}
	}
	_, err = e.rebuildReview(ctx, owner, project, m, ref)
	if err != nil {
		if !auditCorruption(err) {
			return err
		}
		// A newer manifest can legitimately retire records from the old snapshot.
		// Continue against that pointer rather than diagnosing old refs as damage.
		_, now, readErr := e.readManifest(ctx, owner, project)
		if readErr == nil && now.CID != ref.CID {
			return &PendingError{Digest: m.Digest}
		}
		if saved := e.invalidateAudit(ctx, owner, project, ref.CID, head, "ReferenceInvalid"); saved != nil {
			return saved
		}
		return errors.Join(ErrAuditInvalid, err)
	}
	_, now, err := e.readManifest(ctx, owner, project)
	if err != nil {
		return err
	}
	if now.CID != ref.CID {
		return &PendingError{Digest: m.Digest}
	}
	// Save the starting head, not a later head whose changed collections were not
	// necessarily covered by this scan. Concurrent writes schedule another cycle.
	_, err = e.DB.Execute(ctx, statement("UPDATE domain_project_audits SET status='valid',invalid=0,error_code=NULL,repo_head=?,checked_at=? WHERE project_id=? AND owner_did=? AND manifest_cid=? AND status='pending'", turso.Text(startHead), turso.Integer(time.Now().Unix()), turso.Text(project), turso.Text(owner), turso.Text(ref.CID)))
	return err
}
func auditCorruption(err error) bool {
	return errors.Is(err, atproto.ErrNotFound) || errors.Is(err, atproto.ErrConflict) || errors.Is(err, atproto.ErrInvalidResponse) || errors.Is(err, ErrInvalidCanonical)
}
func (e Engine) invalidateAudit(ctx context.Context, owner, project, manifestCID, head, code string) error {
	cycle, err := security.NewOpaqueToken()
	if err != nil {
		return err
	}
	_, err = e.DB.Execute(ctx, statement("INSERT INTO domain_project_audits(project_id,owner_did,manifest_cid,repo_head,cycle_id,status,invalid,error_code,checked_at) VALUES(?,?,?,?,?,'invalid',1,?,?) ON CONFLICT(project_id) DO UPDATE SET owner_did=excluded.owner_did,manifest_cid=excluded.manifest_cid,repo_head=excluded.repo_head,cycle_id=excluded.cycle_id,status='invalid',invalid=1,error_code=excluded.error_code,checked_at=excluded.checked_at", turso.Text(project), turso.Text(owner), turso.Text(manifestCID), turso.Text(head), turso.Text(cycle), turso.Text(code), turso.Integer(time.Now().Unix())))
	return err
}

// AuditBlocked is a private read-only gate for mutation and MCP readiness. A
// repair audit keeps this flag set while pending and clears it only on success.
func (e Engine) AuditBlocked(ctx context.Context, account, project string) (bool, error) {
	rows, err := e.DB.Execute(ctx, statement("SELECT a.invalid FROM domain_project_audits a JOIN domain_canonical_projects p ON p.project_id=a.project_id AND p.owner_did=a.owner_did WHERE a.project_id=? AND p.account_id=?", turso.Text(project), turso.Text(account)))
	if err != nil {
		return false, err
	}
	return len(rows.Rows) > 0 && fmt.Sprint(rows.Rows[0][0].Value) == "1", nil
}
