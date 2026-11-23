package domainrecords

import (
	"context"
	"errors"
	"fmt"

	"github.com/CountableNewt/my-context-protocol/services/go/atproto"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
)

type CollectionReport struct {
	Deleted int  `json:"deleted"`
	More    bool `json:"more"`
}

// Collect removes retired immutable versions with both record and repository
// compare-and-swap. Call under the same owner lease used for publication. Current
// generations and any generation with unfinished publication remain protected.
// This removes PDS records only; already replicated public data cannot be recalled.
func (e Engine) Collect(ctx context.Context, account, owner, project string, limit int) (CollectionReport, error) {
	report := CollectionReport{}
	if limit < 1 || limit > 100 {
		return report, errors.New("record collection limit must be between 1 and 100")
	}
	if err := e.owner(ctx, account, owner); err != nil {
		return report, err
	}
	deleter, ok := e.Records.(atproto.RepositoryDeleter)
	if !ok {
		return report, errors.New("repository-CAS record deletion is required")
	}
	journal, ok := e.Publisher.Journal.(atproto.RetirableJournal)
	if !ok {
		return report, errors.New("retirable publication journal is required")
	}
	consent, local, err := e.state(ctx, account, project)
	if err != nil {
		return report, err
	}
	if !consent.Public {
		return report, ErrNotMigrated
	}
	state, err := e.DB.Execute(ctx, statement("SELECT hydrated FROM domain_canonical_projects WHERE project_id=? AND account_id=?", turso.Text(project), turso.Text(account)))
	if err != nil {
		return report, err
	}
	if len(state.Rows) != 1 || fmt.Sprint(state.Rows[0][0].Value) != "1" {
		return report, ErrHydrationRequired
	}
	rows, err := e.DB.Execute(ctx, statement(`SELECT x.collection,x.rkey,x.uri,x.cid FROM domain_record_retirements x JOIN domain_record_versions v ON v.owner_did=x.owner_did AND v.collection=x.collection AND v.rkey=x.rkey WHERE x.owner_did=? AND x.project_id=? AND v.generation<>? AND NOT EXISTS(SELECT 1 FROM domain_publication_runs p WHERE p.project_id=v.project_id AND p.owner_did=v.owner_did AND p.digest=v.generation AND p.completed=0) AND NOT EXISTS(SELECT 1 FROM domain_publication_items i JOIN domain_publication_runs p ON p.run_id=i.run_id WHERE i.uri=x.uri AND p.completed=0) ORDER BY x.retired_at,x.rkey LIMIT ?`, turso.Text(owner), turso.Text(project), turso.Text(consent.ReviewDigest), turso.Integer(int64(limit+1))))
	if err != nil {
		return report, err
	}
	report.More = len(rows.Rows) > limit
	for _, row := range rows.Rows[:min(limit, len(rows.Rows))] {
		if len(row) != 4 {
			return report, errors.New("invalid retirement row")
		}
		collection, key, uri, cid := fmt.Sprint(row[0].Value), fmt.Sprint(row[1].Value), fmt.Sprint(row[2].Value), fmt.Sprint(row[3].Value)
		if collection == Namespace+"manifest" || uri != "at://"+owner+"/"+collection+"/"+key {
			return report, errors.New("invalid retired record identity")
		}
		if collection != Namespace+"manifestShard" {
			if _, err = byCollection(collection); err != nil {
				return report, err
			}
		}
		// Exclude an ambiguously deleted version from fast caches before touching
		// PDS state, then invalidate its completed journal reference. A lost delete
		// acknowledgement can no longer make a future revert reuse a missing CID.
		if _, err = e.DB.Execute(ctx, statement("UPDATE domain_record_retirements SET deleting=1 WHERE owner_did=? AND collection=? AND rkey=? AND cid=?", turso.Text(owner), turso.Text(collection), turso.Text(key), turso.Text(cid))); err != nil {
			return report, err
		}
		if err = journal.Retire(ctx, owner, atproto.Reference{URI: uri, CID: cid}); err != nil {
			return report, err
		}
		// Capture the repo head before checking the manifest. If any other write
		// races with this check, swapCommit rejects the deletion atomically.
		head, err := deleter.Head(ctx, owner)
		if err != nil {
			return report, err
		}
		_, current, err := e.readManifest(ctx, owner, project)
		if err != nil {
			return report, err
		}
		if current.CID != local.CID {
			return report, atproto.ErrConflict
		}
		mutation := placeholder(owner, collection, key)
		mutation.ExpectedCID = &cid
		if err = deleter.DeleteAtCommit(ctx, mutation, head); err != nil && !errors.Is(err, atproto.ErrNotFound) {
			return report, err
		}
		if _, err = e.DB.Batch(ctx, []turso.Statement{statement("DELETE FROM domain_record_retirements WHERE owner_did=? AND collection=? AND rkey=? AND cid=?", turso.Text(owner), turso.Text(collection), turso.Text(key), turso.Text(cid)), statement("DELETE FROM domain_record_versions WHERE owner_did=? AND collection=? AND rkey=? AND cid=? AND generation<>?", turso.Text(owner), turso.Text(collection), turso.Text(key), turso.Text(cid), turso.Text(consent.ReviewDigest))}); err != nil {
			return report, err
		}
		report.Deleted++
	}
	return report, nil
}
