package domainrecords

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/CountableNewt/my-context-protocol/services/go/turso"
)

func (e Engine) stageProjection(ctx context.Context, review Review) error {
	_, err := e.DB.Execute(ctx, statement("INSERT INTO domain_projection_stage_runs(project_id,generation) VALUES(?,?) ON CONFLICT(project_id,generation) DO NOTHING", turso.Text(review.ProjectID), turso.Text(review.Digest)))
	if err != nil {
		return err
	}
	r, err := e.DB.Execute(ctx, statement("SELECT entries_done FROM domain_projection_stage_runs WHERE project_id=? AND generation=?", turso.Text(review.ProjectID), turso.Text(review.Digest)))
	if err != nil {
		return err
	}
	if len(r.Rows) != 1 {
		return errors.New("projection checkpoint disappeared")
	}
	done, err := strconv.Atoi(fmt.Sprint(r.Rows[0][0].Value))
	if err != nil || done < 0 || done > len(review.Entries) {
		return errors.New("invalid projection checkpoint")
	}
	chunks := e.WorkLimit
	if chunks <= 0 {
		chunks = 25
	}
	if chunks > 100 {
		chunks = 100
	}
	end := min(done+chunks*50, len(review.Entries))
	for done < end {
		next := min(done+50, end)
		batch := []turso.Statement{}
		for _, entry := range review.Entries[done:next] {
			item, err := encode(review.ProjectID, entry)
			if err != nil {
				return err
			}
			batch = append(batch, statement("INSERT INTO domain_projection_rows(project_id,generation,table_name,row_id,fields_json) VALUES(?,?,?,?,?) ON CONFLICT(project_id,generation,table_name,row_id) DO UPDATE SET fields_json=excluded.fields_json", turso.Text(review.ProjectID), turso.Text(review.Digest), turso.Text(entry.Table), turso.Text(entry.ID), turso.Text(string(item.Record))))
		}
		batch = append(batch, statement("UPDATE domain_projection_stage_runs SET entries_done=? WHERE project_id=? AND generation=? AND entries_done=? RETURNING entries_done", turso.Integer(int64(next)), turso.Text(review.ProjectID), turso.Text(review.Digest), turso.Integer(int64(done))))
		results, err := e.DB.Batch(ctx, batch)
		if err != nil {
			return err
		}
		if len(results) != len(batch) || len(results[len(results)-1].Rows) != 1 {
			return errors.New("projection checkpoint changed concurrently")
		}
		done = next
	}
	if done < len(review.Entries) {
		return &PendingError{Completed: done, Total: len(review.Entries), Digest: review.Digest}
	}
	return nil
}
