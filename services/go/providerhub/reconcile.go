package providerhub

import (
	"context"
	"errors"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
	"time"
)

type ReconcileReport struct {
	Checked int `json:"checked"`
	Changed int `json:"changed"`
	Synced  int `json:"synced"`
	Failed  int `json:"failed"`
}

// Reconcile claims bounded due rows and runs existing bounded sync callbacks.
// Checkpoints advance only after successful sync, so a failed request is retried
// after its lease rather than silently losing the branch change.
func (h *Hub) Reconcile(ctx context.Context, limit int) (ReconcileReport, error) {
	var report ReconcileReport
	if limit < 1 || limit > 20 {
		return report, errors.New("Tangled reconciliation limit must be 1..20")
	}
	if h.cfg.Tangled == nil || h.cfg.Sync == nil {
		return report, errors.New("Tangled reconciliation is not configured")
	}
	ctx, cancel := context.WithTimeout(ctx, 80*time.Second)
	defer cancel()
	rows, err := h.query(ctx, "UPDATE atproto_repo_bindings SET next_check_at=? WHERE connection_id IN (SELECT connection_id FROM atproto_repo_bindings WHERE next_check_at<=? ORDER BY next_check_at,connection_id LIMIT ?) RETURNING connection_id,last_revision", turso.Integer(h.now().Add(2*time.Minute).Unix()), turso.Integer(h.now().Unix()), turso.Integer(int64(limit)))
	if err != nil {
		return report, err
	}
	for _, row := range rows {
		if err = ctx.Err(); err != nil {
			return report, err
		}
		id := scalar(row, 0)
		binding, checkErr := h.ResolveTangledConnection(ctx, id)
		if checkErr != nil {
			report.Failed++
			continue
		}
		report.Checked++
		head, checkErr := h.cfg.Tangled.ResolveAuthenticatedBranch(ctx, binding.ActorDID, binding.RepositoryDID, binding.Branch)
		if checkErr != nil {
			report.Failed++
			continue
		}
		if head != scalar(row, 1) {
			report.Changed++
			projects, lookupErr := h.query(ctx, "SELECT project_id FROM repo_connections WHERE id=? AND provider='tangled'", turso.Text(id))
			if lookupErr != nil || len(projects) != 1 {
				report.Failed++
				continue
			}
			result, syncErr := h.cfg.Sync(ctx, scalar(projects[0], 0))
			if syncErr != nil || result["status"] != "ready" {
				report.Failed++
				continue
			}
			report.Synced++
		}
		_, checkErr = h.cfg.DB.Execute(ctx, statement("UPDATE atproto_repo_bindings SET last_revision=?,last_checked_at=?,next_check_at=? WHERE connection_id=? AND repository_did=? AND actor_did=?", turso.Text(head), turso.Integer(h.now().Unix()), turso.Integer(h.now().Add(5*time.Minute).Unix()), turso.Text(id), turso.Text(binding.RepositoryDID), turso.Text(binding.ActorDID)))
		if checkErr != nil {
			report.Failed++
		}
	}
	return report, nil
}
