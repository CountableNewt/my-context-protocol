package domainrecords

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/CountableNewt/my-context-protocol/services/go/atproto"
	"github.com/CountableNewt/my-context-protocol/services/go/security"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
)

var ErrPending = errors.New("canonical publication is pending")

type PendingError struct {
	Completed, Total int
	Digest           string
}

func (e *PendingError) Error() string {
	return fmt.Sprintf("canonical publication pending: %d/%d", e.Completed, e.Total)
}
func (e *PendingError) Unwrap() error { return ErrPending }

type progress struct {
	SavedEntries, SavedShards int
	RunID                     string
	Entries, Shards           []atproto.Reference
	Prior                     atproto.Reference
}

func (e Engine) progress(ctx context.Context, review Review, prior atproto.Reference) (progress, error) {
	// A record CID is content-addressed: A→B→A restores the same CID. Reusing
	// completed transition IDs would incorrectly acknowledge the next A→B without
	// updating its mutable manifest. Resume only unfinished runs; new transitions
	// receive a fresh durable operation ID even when content CIDs repeat.
	existing, err := e.DB.Execute(ctx, statement("SELECT run_id FROM domain_publication_runs WHERE project_id=? AND owner_did=? AND digest=? AND prior_cid=? AND completed=0 ORDER BY run_id LIMIT 1", turso.Text(review.ProjectID), turso.Text(review.OwnerDID), turso.Text(review.Digest), turso.Text(prior.CID)))
	if err != nil {
		return progress{}, err
	}
	runID := ""
	if len(existing.Rows) > 0 {
		runID = fmt.Sprint(existing.Rows[0][0].Value)
	} else {
		runID, err = security.NewOpaqueToken()
		if err != nil {
			return progress{}, err
		}
	}
	_, err = e.DB.Execute(ctx, statement("INSERT INTO domain_publication_runs(run_id,project_id,owner_did,digest,prior_uri,prior_cid) VALUES(?,?,?,?,?,?) ON CONFLICT(run_id) DO NOTHING", turso.Text(runID), turso.Text(review.ProjectID), turso.Text(review.OwnerDID), turso.Text(review.Digest), turso.Text(prior.URI), turso.Text(prior.CID)))
	if err != nil {
		return progress{}, err
	}
	r, err := e.DB.Execute(ctx, statement("SELECT owner_did,prior_uri,prior_cid,entries_done,shards_done FROM domain_publication_runs WHERE run_id=?", turso.Text(runID)))
	if err != nil {
		return progress{}, err
	}
	if len(r.Rows) != 1 || len(r.Rows[0]) != 5 {
		return progress{}, errors.New("invalid publication cursor")
	}
	row := r.Rows[0]
	if fmt.Sprint(row[0].Value) != review.OwnerDID {
		return progress{}, errors.New("publication cursor owner mismatch")
	}
	entries, err := strconv.Atoi(fmt.Sprint(row[3].Value))
	if err != nil || entries < 0 || entries > len(review.Entries) {
		return progress{}, errors.New("invalid publication entry cursor")
	}
	shards, err := strconv.Atoi(fmt.Sprint(row[4].Value))
	if err != nil || shards < 0 || shards > (len(review.Entries)+99)/100 {
		return progress{}, errors.New("invalid publication shard cursor")
	}
	p := progress{RunID: runID, Prior: atproto.Reference{URI: fmt.Sprint(row[1].Value), CID: fmt.Sprint(row[2].Value)}, SavedEntries: entries, SavedShards: shards, Entries: make([]atproto.Reference, entries)}
	// Completed entity refs are fetched only for the next 100-reference shard,
	// avoiding an ever-growing prefix scan on every bounded publication request.
	p.Shards, err = e.progressRefs(ctx, runID, "shard", 0, shards)
	return p, err
}
func (e Engine) progressRefs(ctx context.Context, runID, kind string, start, end int) ([]atproto.Reference, error) {
	if start == end {
		return []atproto.Reference{}, nil
	}
	if end-start > 1000 {
		return nil, errors.New("reference page exceeds limit")
	}
	r, err := e.DB.Execute(ctx, statement("SELECT item_index,uri,cid FROM domain_publication_items WHERE run_id=? AND kind=? AND item_index>=? AND item_index<? ORDER BY item_index", turso.Text(runID), turso.Text(kind), turso.Integer(int64(start)), turso.Integer(int64(end))))
	if err != nil {
		return nil, err
	}
	if len(r.Rows) != end-start {
		return nil, errors.New("publication cursor references are incomplete")
	}
	out := []atproto.Reference{}
	for i, row := range r.Rows {
		if len(row) != 3 || fmt.Sprint(row[0].Value) != strconv.Itoa(start+i) {
			return nil, errors.New("publication reference order changed")
		}
		out = append(out, atproto.Reference{URI: fmt.Sprint(row[1].Value), CID: fmt.Sprint(row[2].Value)})
	}
	return out, nil
}
func (e Engine) saveProgress(ctx context.Context, review Review, p *progress) error {
	batch := []turso.Statement{}
	for _, kind := range []string{"entry", "shard"} {
		refs, start := p.Entries, p.SavedEntries
		if kind == "shard" {
			refs, start = p.Shards, p.SavedShards
		}
		for index := start; index < len(refs); index++ {
			ref := refs[index]
			batch = append(batch, statement("INSERT INTO domain_publication_items(run_id,kind,item_index,uri,cid) VALUES(?,?,?,?,?) ON CONFLICT(run_id,kind,item_index) DO UPDATE SET uri=excluded.uri,cid=excluded.cid", turso.Text(p.RunID), turso.Text(kind), turso.Integer(int64(index)), turso.Text(ref.URI), turso.Text(ref.CID)))
		}
	}
	batch = append(batch, statement("UPDATE domain_publication_runs SET entries_done=?,shards_done=? WHERE run_id=? AND owner_did=? AND entries_done=? AND shards_done=? RETURNING digest", turso.Integer(int64(len(p.Entries))), turso.Integer(int64(len(p.Shards))), turso.Text(p.RunID), turso.Text(review.OwnerDID), turso.Integer(int64(p.SavedEntries)), turso.Integer(int64(p.SavedShards))))
	if len(batch) > 100 {
		return errors.New("publication checkpoint exceeds batch limit")
	}
	results, err := e.DB.Batch(ctx, batch)
	if err != nil {
		return err
	}
	if len(results) != len(batch) || len(results[len(results)-1].Rows) != 1 {
		return errors.New("publication cursor changed concurrently")
	}
	p.SavedEntries = len(p.Entries)
	p.SavedShards = len(p.Shards)
	return nil
}
