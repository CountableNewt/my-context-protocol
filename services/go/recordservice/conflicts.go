package recordservice

import (
	"context"
	"errors"
	"time"

	"github.com/CountableNewt/my-context-protocol/services/go/domainrecords"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
)

// ConflictReview contains identifiers only; pending private SQL is never exposed.
type ConflictReview struct {
	IntentID           string `json:"intent_id"`
	SourceCID          string `json:"source_cid"`
	TargetDigest       string `json:"target_digest"`
	CurrentManifestCID string `json:"current_manifest_cid"`
	CurrentDigest      string `json:"current_digest"`
	Status             string `json:"status"`
}

func (s *Service) InspectConflict(ctx context.Context, account, project string) (*ConflictReview, error) {
	var result *ConflictReview
	err := s.WithEngine(ctx, account, func(ctx context.Context, owner string, e *domainrecords.Engine) error {
		var err error
		result, err = s.inspectConflict(ctx, e, account, owner, project)
		return err
	})
	return result, err
}
func (s *Service) inspectConflict(ctx context.Context, e *domainrecords.Engine, account, owner, project string) (*ConflictReview, error) {
	// Confirm local ownership before consulting owner records. No reconciliation may
	// execute the pending private effects while the owner is reviewing this choice.
	r, err := s.DB.Execute(ctx, turso.Statement{SQL: "SELECT 1 FROM domain_canonical_projects WHERE project_id=? AND account_id=? AND owner_did=?", Args: []turso.Value{turso.Text(project), turso.Text(account), turso.Text(owner)}, WantRows: true})
	if err != nil {
		return nil, err
	}
	if r == nil || len(r.Rows) != 1 {
		return nil, domainrecords.ErrNotMigrated
	}
	intent, err := loadNative(ctx, s.DB, account, project)
	if err != nil || intent == nil {
		return nil, err
	}
	ref, digest, err := e.ManifestInfo(ctx, owner, project)
	if err != nil {
		return nil, err
	}
	status := "superseded"
	if ref.CID == intent.SourceCID || digest == intent.TargetDigest {
		status = "resumable"
	}
	return &ConflictReview{IntentID: intent.ID, SourceCID: intent.SourceCID, TargetDigest: intent.TargetDigest, CurrentManifestCID: ref.CID, CurrentDigest: digest, Status: status}, nil
}

// ResolveConflict discards unapplied local effects only after an explicit owner
// decision pinned to a freshly read remote generation. It never deletes records.
// Discarding pending private provider configuration can require reconnection.
func (s *Service) ResolveConflict(ctx context.Context, account, project, intentID, expectedCurrentCID, action string) error {
	if action != "discard" || intentID == "" || expectedCurrentCID == "" {
		return errors.New("invalid publication conflict resolution")
	}
	return s.WithEngine(ctx, account, func(ctx context.Context, owner string, e *domainrecords.Engine) error {
		return s.resolveConflict(ctx, e, account, owner, project, intentID, expectedCurrentCID)
	})
}

func (s *Service) resolveConflict(ctx context.Context, e *domainrecords.Engine, account, owner, project, intentID, expectedCurrentCID string) error {
	review, err := s.inspectConflict(ctx, e, account, owner, project)
	if err != nil {
		return err
	}
	if review == nil || review.IntentID != intentID || review.CurrentManifestCID != expectedCurrentCID || review.Status != "superseded" {
		return domainrecords.ErrChanged
	}
	// Guard the exact pending source and local receipt. A concurrent successful
	// resume changes either status or receipt and makes this transaction fail.
	token := "discard:" + intentID
	_, err = s.DB.Batch(ctx, []turso.Statement{
		{SQL: "INSERT INTO domain_projection_guards(token,valid) SELECT ?,CASE WHEN EXISTS(SELECT 1 FROM domain_native_intents i JOIN domain_canonical_projects p ON p.project_id=i.project_id WHERE i.id=? AND i.account_id=? AND i.project_id=? AND i.status='pending' AND i.source_cid=? AND i.target_digest=? AND p.manifest_cid=i.source_cid AND p.owner_did=?) THEN 1 ELSE 0 END", Args: []turso.Value{turso.Text(token), turso.Text(intentID), turso.Text(account), turso.Text(project), turso.Text(review.SourceCID), turso.Text(review.TargetDigest), turso.Text(owner)}},
		{SQL: "UPDATE domain_native_intents SET status='abandoned',completed_at=?,statements_json='[]',changes_json='[]',results_json='[]' WHERE id=? AND status='pending'", Args: []turso.Value{turso.Integer(time.Now().Unix()), turso.Text(intentID)}},
		{SQL: "DELETE FROM domain_projection_guards WHERE token=?", Args: []turso.Value{turso.Text(token)}},
	})
	if err != nil {
		return err
	}
	return e.Reconcile(ctx, account, owner, project)
}
