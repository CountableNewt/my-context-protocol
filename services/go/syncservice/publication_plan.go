package syncservice

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/CountableNewt/my-context-protocol/services/go/compiler"
	"github.com/CountableNewt/my-context-protocol/services/go/security"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
)

var ErrPending = errors.New("sync canonical publication is pending")

const PublicationMigrationSQL = `
CREATE TABLE sync_publication_plans(project_id TEXT PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,job_id TEXT NOT NULL UNIQUE REFERENCES sync_jobs(id) ON DELETE CASCADE,release_id TEXT NOT NULL,prior_release_id TEXT NOT NULL,state TEXT NOT NULL CHECK(state IN ('staging','publishing','complete','failed')),next_chunk INTEGER NOT NULL DEFAULT 0,chunk_count INTEGER NOT NULL,checksums_json TEXT NOT NULL,all_ready INTEGER NOT NULL,report_valid INTEGER NOT NULL,execution_owner TEXT NOT NULL DEFAULT '',execution_until INTEGER NOT NULL DEFAULT 0,created_at INTEGER NOT NULL,updated_at INTEGER NOT NULL);
CREATE TABLE sync_publication_chunks(job_id TEXT NOT NULL REFERENCES sync_jobs(id) ON DELETE CASCADE,chunk_index INTEGER NOT NULL,payload BLOB NOT NULL,PRIMARY KEY(job_id,chunk_index));
CREATE TABLE sync_publication_guards(project_id TEXT PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,valid INTEGER NOT NULL CHECK(valid=1));
`

// A bridged DB establishes this marker only after current owner PDS state and
// account/project ownership have been verified. Raw legacy stores stay unchanged.
type canonicalPublicationDB interface{ CanonicalScope(context.Context) bool }
type PublicationResumer interface {
	PendingPublication(context.Context, string) (Job, bool, error)
	ResumePublication(context.Context, string, time.Time) (Job, bool, error)
}
type claimResumer interface {
	ResumeClaim(context.Context, Connection, Job, time.Time) error
}

const maxPlanChunks = 10000
const planChunkBytes = 2 << 20
const maxPlanChunkBytes = (3 << 20) - 16384

type publicationPlan struct {
	Job                        Job
	State, PriorRelease, Owner string
	Next, Count                int
	Hashes                     []string
	AllReady, Valid            bool
	LeaseUntil                 int64
}

func (s SQLStore) hasPublicationPlans() bool { _, ok := s.DB.(canonicalPublicationDB); return ok }
func (s SQLStore) publicationEnabled(ctx context.Context) bool {
	db, ok := s.DB.(canonicalPublicationDB)
	return ok && db.CanonicalScope(ctx)
}

func (s SQLStore) Publish(ctx context.Context, c Connection, job Job, revision string, skills []compiler.Compiled, report compiler.Report, now time.Time) error {
	batches, allReady, err := s.buildPublication(ctx, c, job, revision, skills, report, now)
	if err != nil {
		return err
	}
	if !s.publicationEnabled(ctx) {
		for index, batch := range batches {
			results, err := s.DB.Batch(ctx, batch)
			if err != nil {
				return err
			}
			if index == len(batches)-1 && allReady && (len(results) < 3 || len(results[2].Rows) != 1) {
				return ErrBusy
			}
		}
		return nil
	}
	chunks, hashes, err := encodePublicationChunks(batches)
	if err != nil {
		return err
	}
	hashesJSON, _ := json.Marshal(hashes)
	_, err = s.DB.Batch(ctx, []turso.Statement{
		stmt("DELETE FROM sync_publication_chunks WHERE job_id IN(SELECT job_id FROM sync_publication_plans WHERE project_id=? AND state IN ('complete','failed'))", turso.Text(c.ProjectID)),
		stmt("DELETE FROM sync_publication_plans WHERE project_id=? AND state IN ('complete','failed')", turso.Text(c.ProjectID)),
		stmt("INSERT INTO sync_publication_plans(project_id,job_id,release_id,prior_release_id,state,chunk_count,checksums_json,all_ready,report_valid,created_at,updated_at) VALUES(?,?,?,?,'staging',?,?,?,?,?,?)", turso.Text(c.ProjectID), turso.Text(job.ID), turso.Text(job.ReleaseID), turso.Text(c.PriorReleaseID), turso.Integer(int64(len(chunks))), turso.Text(string(hashesJSON)), flag(allReady), flag(report.Valid), turso.Integer(now.Unix()), turso.Integer(now.Unix())),
		stmt("UPDATE sync_jobs SET status='publishing',updated_at=? WHERE id=? AND release_id=?", turso.Integer(now.Unix()), turso.Text(job.ID), turso.Text(job.ReleaseID)),
	})
	if err != nil {
		return errors.Join(ErrPending, err)
	}
	for index, payload := range chunks {
		if _, err = s.DB.Execute(ctx, stmt("INSERT INTO sync_publication_chunks(job_id,chunk_index,payload) VALUES(?,?,?)", turso.Text(job.ID), turso.Integer(int64(index)), turso.Blob(payload))); err != nil {
			return errors.Join(ErrPending, err)
		}
	}
	if _, err = s.DB.Execute(ctx, stmt("UPDATE sync_publication_plans SET state='publishing',updated_at=? WHERE project_id=? AND job_id=? AND chunk_count=(SELECT COUNT(*) FROM sync_publication_chunks WHERE job_id=?)", turso.Integer(now.Unix()), turso.Text(c.ProjectID), turso.Text(job.ID), turso.Text(job.ID))); err != nil {
		return errors.Join(ErrPending, err)
	}
	_, _, err = s.ResumePublication(ctx, c.ProjectID, time.Now().UTC())
	return err
}

func encodePublicationChunks(batches [][]turso.Statement) ([][]byte, []string, error) {
	if len(batches) == 0 {
		return nil, nil, errors.New("empty sync publication plan")
	}
	chunks := [][]byte{}
	hashes := []string{}
	appendChunk := func(statements []turso.Statement) error {
		if len(statements) == 0 {
			return nil
		}
		if len(statements) > 32 {
			return errors.New("sync publication chunk exceeds native command budget")
		}
		for _, stmt := range statements {
			if !validPublicationStatement(stmt) {
				return errors.New("unrecognized sync publication statement")
			}
		}
		raw, err := json.Marshal(statements)
		if err != nil {
			return err
		}
		if len(raw) > maxPlanChunkBytes {
			return errors.New("sync publication statement exceeds bounded private staging size")
		}
		sum := sha256.Sum256(raw)
		chunks = append(chunks, raw)
		hashes = append(hashes, hex.EncodeToString(sum[:]))
		if len(chunks) > maxPlanChunks {
			return errors.New("sync publication plan exceeds chunk limit")
		}
		return nil
	}
	current := []turso.Statement{}
	for index, batch := range batches {
		// Keep release readiness, activation, job completion and lock release in
		// the same final transaction, even when report JSON is comparatively large.
		if index == len(batches)-1 {
			if err := appendChunk(current); err != nil {
				return nil, nil, err
			}
			if err := appendChunk(batch); err != nil {
				return nil, nil, err
			}
			continue
		}
		for _, statement := range batch {
			candidate := append(append([]turso.Statement(nil), current...), statement)
			raw, err := json.Marshal(candidate)
			if err != nil {
				return nil, nil, err
			}
			if len(current) > 0 && (len(candidate) > 32 || len(raw) > planChunkBytes) {
				if err := appendChunk(current); err != nil {
					return nil, nil, err
				}
				current = nil
			}
			current = append(current, statement)
		}
	}
	return chunks, hashes, nil
}

func validPublicationStatement(stmt turso.Statement) bool {
	known := false
	for _, sql := range []string{publicationPackageSQL, publicationCompiledSQL, publicationRoutingSQL, publicationCapabilitySQL, publicationFileSQL, publicationValidationSQL, publicationReleaseSQL, publicationActivateSQL, publicationRevisionSQL, publicationCompleteSQL, publicationUnlockSQL} {
		known = known || stmt.SQL == sql
	}
	if !known || strings.Count(stmt.SQL, "?") != len(stmt.Args) {
		return false
	}
	for _, arg := range stmt.Args {
		switch arg.Type {
		case "null", "text", "integer", "float":
		case "blob":
			if arg.Base64 == nil {
				return false
			}
			if _, err := base64.StdEncoding.DecodeString(*arg.Base64); err != nil {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func (s SQLStore) loadPublication(ctx context.Context, project string) (publicationPlan, bool, error) {
	var p publicationPlan
	if !s.hasPublicationPlans() {
		return p, false, nil
	}
	r, err := s.DB.Execute(ctx, stmt("SELECT p.job_id,p.release_id,p.state,p.next_chunk,p.chunk_count,p.checksums_json,p.all_ready,p.report_valid,p.prior_release_id,p.execution_owner,p.execution_until FROM sync_publication_plans p WHERE p.project_id=? AND p.state IN ('staging','publishing')", turso.Text(project)))
	if err != nil {
		return p, false, err
	}
	if len(r.Rows) == 0 {
		return p, false, nil
	}
	if len(r.Rows) != 1 || len(r.Rows[0]) != 11 {
		return p, false, errors.New("invalid sync publication plan row")
	}
	row := r.Rows[0]
	p.Job = Job{ID: value(row, 0), ProjectID: project, ReleaseID: value(row, 1), Status: "publishing"}
	p.State = value(row, 2)
	p.PriorRelease = value(row, 8)
	p.Owner = value(row, 9)
	if _, err = fmt.Sscan(value(row, 3), &p.Next); err != nil {
		return p, false, err
	}
	if _, err = fmt.Sscan(value(row, 4), &p.Count); err != nil {
		return p, false, err
	}
	if _, err = fmt.Sscan(value(row, 10), &p.LeaseUntil); err != nil {
		return p, false, err
	}
	p.AllReady = value(row, 6) == "1"
	p.Valid = value(row, 7) == "1"
	if json.Unmarshal([]byte(value(row, 5)), &p.Hashes) != nil || p.Count < 1 || p.Count > maxPlanChunks || len(p.Hashes) != p.Count || p.Next < 0 || p.Next >= p.Count {
		return p, false, errors.New("invalid sync publication cursor")
	}
	return p, true, nil
}

func (s SQLStore) PendingPublication(ctx context.Context, project string) (Job, bool, error) {
	p, found, err := s.loadPublication(ctx, project)
	if err != nil || found || !s.hasPublicationPlans() {
		return p.Job, found, err
	}
	// A canonical release reservation can itself need a bounded PDS retry before
	// compilation has begun. Preserve its original job/release IDs as well.
	r, err := s.DB.Execute(ctx, stmt("SELECT id,project_id,release_id,status,COALESCE(error_code,''),lease_until FROM sync_jobs WHERE project_id=? AND status='publishing' AND NOT EXISTS(SELECT 1 FROM sync_publication_plans WHERE job_id=sync_jobs.id) ORDER BY created_at,id LIMIT 1", turso.Text(project)))
	if err != nil {
		return Job{}, false, err
	}
	if len(r.Rows) == 0 {
		return Job{}, false, nil
	}
	job, err := decodeJob(r.Rows[0])
	return job, true, err
}
func (s SQLStore) PublicationPending(ctx context.Context, project string) (bool, error) {
	_, found, err := s.PendingPublication(ctx, project)
	return found, err
}

func (s SQLStore) ResumePublication(ctx context.Context, project string, now time.Time) (Job, bool, error) {
	p, found, err := s.loadPublication(ctx, project)
	if err != nil || !found {
		return p.Job, found, err
	}
	if !s.publicationEnabled(ctx) {
		return p.Job, true, errors.New("canonical project scope is required to resume sync publication")
	}
	if p.State == "staging" {
		r, err := s.DB.Execute(ctx, stmt("SELECT COUNT(*) FROM sync_publication_chunks WHERE job_id=?", turso.Text(p.Job.ID)))
		if err != nil {
			return p.Job, true, errors.Join(ErrPending, err)
		}
		if len(r.Rows) != 1 || value(r.Rows[0], 0) != fmt.Sprint(p.Count) {
			lock, err := s.DB.Execute(ctx, stmt("SELECT lease_until FROM sync_project_locks WHERE project_id=? AND job_id=?", turso.Text(project), turso.Text(p.Job.ID)))
			if err != nil {
				return p.Job, true, errors.Join(ErrPending, err)
			}
			var lease int64
			if len(lock.Rows) == 1 {
				_, _ = fmt.Sscan(value(lock.Rows[0], 0), &lease)
			}
			if lease > now.Unix() {
				return p.Job, true, ErrPending
			}
			return s.abortPublication(ctx, p, "publication_staging_interrupted", now)
		}
		if _, err = s.DB.Execute(ctx, stmt("UPDATE sync_publication_plans SET state='publishing',updated_at=? WHERE project_id=? AND job_id=?", turso.Integer(now.Unix()), turso.Text(project), turso.Text(p.Job.ID))); err != nil {
			return p.Job, true, errors.Join(ErrPending, err)
		}
	}
	owner, err := security.NewOpaqueToken()
	if err != nil {
		return p.Job, true, err
	}
	r, err := s.DB.Execute(ctx, stmt("UPDATE sync_publication_plans SET execution_owner=?,execution_until=?,updated_at=? WHERE project_id=? AND job_id=? AND state='publishing' AND next_chunk=? AND execution_until<=? RETURNING next_chunk", turso.Text(owner), turso.Integer(now.Add(90*time.Second).Unix()), turso.Integer(now.Unix()), turso.Text(project), turso.Text(p.Job.ID), turso.Integer(int64(p.Next)), turso.Integer(now.Unix())))
	if err != nil {
		return p.Job, true, errors.Join(ErrPending, err)
	}
	if len(r.Rows) != 1 {
		return p.Job, true, ErrPending
	}
	// Do not clear this lease on a pending native publication. Its original SQL
	// guard is durably captured by the bridge and must remain replayable. Scope
	// resumes that exact native intent before another lease can be acquired.
	locks, err := s.DB.Batch(ctx, []turso.Statement{
		stmt("INSERT INTO sync_project_locks(project_id,job_id,lease_until) VALUES(?,?,?) ON CONFLICT(project_id) DO UPDATE SET job_id=excluded.job_id,lease_until=excluded.lease_until WHERE sync_project_locks.job_id=excluded.job_id OR sync_project_locks.lease_until<=? RETURNING job_id", turso.Text(project), turso.Text(p.Job.ID), turso.Integer(now.Add(120*time.Second).Unix()), turso.Integer(now.Unix())),
		stmt("UPDATE sync_jobs SET lease_until=?,updated_at=? WHERE id=? AND release_id=? AND status='publishing'", turso.Integer(now.Add(120*time.Second).Unix()), turso.Integer(now.Unix()), turso.Text(p.Job.ID), turso.Text(p.Job.ReleaseID)),
	})
	if err != nil {
		return p.Job, true, errors.Join(ErrPending, err)
	}
	if len(locks) != 2 || len(locks[0].Rows) != 1 {
		return p.Job, true, ErrPending
	}
	chunk, err := s.DB.Execute(ctx, stmt("SELECT payload FROM sync_publication_chunks WHERE job_id=? AND chunk_index=?", turso.Text(p.Job.ID), turso.Integer(int64(p.Next))))
	if err != nil {
		return p.Job, true, errors.Join(ErrPending, err)
	}
	if len(chunk.Rows) != 1 || len(chunk.Rows[0]) != 1 || chunk.Rows[0][0].Type != "blob" || chunk.Rows[0][0].Base64 == nil {
		return s.abortPublication(ctx, p, "publication_plan_corrupt", now)
	}
	raw, err := base64.StdEncoding.DecodeString(*chunk.Rows[0][0].Base64)
	if err != nil || len(raw) > maxPlanChunkBytes {
		return s.abortPublication(ctx, p, "publication_plan_corrupt", now)
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != p.Hashes[p.Next] {
		return s.abortPublication(ctx, p, "publication_plan_corrupt", now)
	}
	var statements []turso.Statement
	if json.Unmarshal(raw, &statements) != nil || len(statements) == 0 || len(statements) > 32 {
		return s.abortPublication(ctx, p, "publication_plan_corrupt", now)
	}
	for _, statement := range statements {
		if !validPublicationStatement(statement) {
			return s.abortPublication(ctx, p, "publication_plan_corrupt", now)
		}
	}
	final := p.Next+1 == p.Count
	if final && p.AllReady {
		current, err := s.DB.Execute(ctx, stmt("SELECT COALESCE(active_release_id,'') FROM projects WHERE id=?", turso.Text(project)))
		if err != nil {
			return p.Job, true, errors.Join(ErrPending, err)
		}
		if len(current.Rows) != 1 || value(current.Rows[0], 0) != p.PriorRelease {
			return s.abortPublication(ctx, p, "active_release_changed", now)
		}
	}
	guard := stmt("INSERT INTO sync_publication_guards(project_id,valid) VALUES(?,CASE WHEN EXISTS(SELECT 1 FROM sync_publication_plans WHERE project_id=? AND job_id=? AND next_chunk=? AND execution_owner=? AND execution_until>?) AND EXISTS(SELECT 1 FROM sync_project_locks WHERE project_id=? AND job_id=? AND lease_until>?) AND (?=0 OR EXISTS(SELECT 1 FROM projects WHERE id=? AND COALESCE(active_release_id,'')=?)) THEN 1 ELSE 0 END) ON CONFLICT(project_id) DO UPDATE SET valid=excluded.valid", turso.Text(project), turso.Text(project), turso.Text(p.Job.ID), turso.Integer(int64(p.Next)), turso.Text(owner), turso.Integer(now.Unix()), turso.Text(project), turso.Text(p.Job.ID), turso.Integer(now.Unix()), flag(final && p.AllReady), turso.Text(project), turso.Text(p.PriorRelease))
	state := "publishing"
	if final {
		state = "complete"
	}
	batch := append([]turso.Statement{guard}, statements...)
	batch = append(batch, stmt("UPDATE sync_publication_plans SET next_chunk=?,state=?,execution_owner='',execution_until=0,updated_at=? WHERE project_id=? AND job_id=? AND next_chunk=? AND execution_owner=? RETURNING next_chunk", turso.Integer(int64(p.Next+1)), turso.Text(state), turso.Integer(now.Unix()), turso.Text(project), turso.Text(p.Job.ID), turso.Integer(int64(p.Next)), turso.Text(owner)))
	results, err := s.DB.Batch(ctx, batch)
	if err != nil {
		return p.Job, true, errors.Join(ErrPending, err)
	}
	if len(results) != len(batch) || len(results[len(results)-1].Rows) != 1 {
		return p.Job, true, ErrPending
	}
	if !final {
		return p.Job, true, ErrPending
	}
	p.Job.Status = "ready"
	if !p.Valid {
		p.Job.Status = "failed"
		p.Job.ErrorCode = "validation_failed"
		return p.Job, true, ErrInvalid
	}
	return p.Job, true, nil
}

func (s SQLStore) ResumeClaim(ctx context.Context, c Connection, job Job, now time.Time) error {
	if !s.publicationEnabled(ctx) {
		return errors.New("canonical scope is required to resume a release reservation")
	}
	r, err := s.DB.Execute(ctx, stmt("INSERT INTO sync_project_locks(project_id,job_id,lease_until) VALUES(?,?,?) ON CONFLICT(project_id) DO UPDATE SET job_id=excluded.job_id,lease_until=excluded.lease_until WHERE sync_project_locks.job_id=excluded.job_id OR sync_project_locks.lease_until<=? RETURNING job_id", turso.Text(c.ProjectID), turso.Text(job.ID), turso.Integer(now.Add(120*time.Second).Unix()), turso.Integer(now.Unix())))
	if err != nil {
		return errors.Join(ErrPending, err)
	}
	if len(r.Rows) != 1 {
		return ErrPending
	}
	_, err = s.DB.Execute(ctx, stmt("INSERT INTO releases(id,project_id,commit_sha,status,created_at) SELECT release_id,project_id,'pending','pending',created_at FROM sync_jobs WHERE id=? AND release_id=? AND NOT EXISTS(SELECT 1 FROM releases WHERE id=?)", turso.Text(job.ID), turso.Text(job.ReleaseID), turso.Text(job.ReleaseID)))
	if err != nil {
		return errors.Join(ErrPending, err)
	}
	return nil
}

func (s SQLStore) abortPublication(ctx context.Context, p publicationPlan, code string, now time.Time) (Job, bool, error) {
	_, err := s.DB.Batch(ctx, []turso.Statement{
		stmt("UPDATE sync_jobs SET status='failed',error_code=?,updated_at=? WHERE id=? AND release_id=?", turso.Text(code), turso.Integer(now.Unix()), turso.Text(p.Job.ID), turso.Text(p.Job.ReleaseID)),
		stmt("UPDATE releases SET status='failed',error_summary=? WHERE id=?", turso.Text(code), turso.Text(p.Job.ReleaseID)),
		stmt("DELETE FROM sync_project_locks WHERE project_id=? AND job_id=?", turso.Text(p.Job.ProjectID), turso.Text(p.Job.ID)),
		stmt("UPDATE sync_publication_plans SET state='failed',execution_owner='',execution_until=0,updated_at=? WHERE project_id=? AND job_id=?", turso.Integer(now.Unix()), turso.Text(p.Job.ProjectID), turso.Text(p.Job.ID)),
	})
	if err != nil {
		return p.Job, true, errors.Join(ErrPending, err)
	}
	p.Job.Status = "failed"
	p.Job.ErrorCode = code
	return p.Job, true, errors.Join(ErrInvalid, errors.New(code))
}
