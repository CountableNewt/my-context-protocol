package syncservice

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/CountableNewt/my-context-protocol/services/go/compiler"
	"github.com/CountableNewt/my-context-protocol/services/go/internal/sqlitetest"
	"github.com/CountableNewt/my-context-protocol/services/go/migrations"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
)

type canonicalTestDB struct {
	Database
	fail           bool
	lost           bool
	max            int
	reservePending bool
}

func (*canonicalTestDB) CanonicalScope(context.Context) bool { return true }
func (d *canonicalTestDB) Execute(ctx context.Context, st turso.Statement) (*turso.Result, error) {
	if d.reservePending && strings.HasPrefix(st.SQL, "INSERT INTO releases(") {
		d.reservePending = false
		return nil, errors.New("reservation pending")
	}
	return d.Database.Execute(ctx, st)
}

func (d *canonicalTestDB) Batch(ctx context.Context, statements []turso.Statement) ([]turso.Result, error) {
	native := len(statements) > 0 && strings.Contains(statements[0].SQL, "INSERT INTO sync_publication_guards")
	if native {
		if len(statements) > d.max {
			d.max = len(statements)
		}
		if d.fail {
			d.fail = false
			return nil, errors.New("PDS unavailable")
		}
	}
	r, e := d.Database.Batch(ctx, statements)
	if native && e == nil && d.lost {
		d.lost = false
		return nil, errors.New("lost acknowledgment")
	}
	return r, e
}
func planFixture(t *testing.T, n int) (SQLStore, *canonicalTestDB, Connection, Job) {
	t.Helper()
	ctx := context.Background()
	raw := sqlitetest.New(t, migrations.BaselineSQL+MigrationSQL+PublicationMigrationSQL+seedSQL)
	db := &canonicalTestDB{Database: raw}
	s := SQLStore{DB: db}
	c, e := s.Connection(ctx, "project")
	if e != nil {
		t.Fatal(e)
	}
	now := time.Now().UTC()
	j, _, e := s.Claim(ctx, c, "delivery", now)
	if e != nil {
		t.Fatal(e)
	}
	skills := make([]compiler.Compiled, 0, n)
	for i := 0; i < n; i++ {
		skill, e := compiler.Parse(fmt.Sprintf("skill%d/SKILL.md", i), []byte(fmt.Sprintf("---\nname: skill%d\ndescription: helpful\nuse_when: writing\n---\nbody", i)))
		if e != nil {
			t.Fatal(e)
		}
		compiled, e := compiler.Compile(skill, "owner/repo", strings.Repeat("a", 40), nil)
		if e != nil {
			t.Fatal(e)
		}
		skills = append(skills, compiled)
	}
	e = s.Publish(ctx, c, j, strings.Repeat("a", 40), skills, compiler.Report{Valid: true}, now)
	if !errors.Is(e, ErrPending) {
		t.Fatalf("initial publication: %v", e)
	}
	return s, db, c, j
}
func scalar(t *testing.T, db Database, sql string) string {
	t.Helper()
	r, e := db.Execute(context.Background(), stmt(sql))
	if e != nil || len(r.Rows) != 1 {
		t.Fatalf("%s: %v %+v", sql, e, r)
	}
	return value(r.Rows[0], 0)
}
func TestCanonicalPublicationStagesStableBoundedPlanAndActivatesLast(t *testing.T) {
	s, db, _, job := planFixture(t, 20)
	ctx := context.Background()
	if got := scalar(t, db, "SELECT COALESCE(active_release_id,'') FROM projects"); got != "" {
		t.Fatal("early activation", got)
	}
	initial := scalar(t, db, "SELECT group_concat(id) FROM skill_packages")
	loops := 0
	for {
		loops++
		j, found, e := s.ResumePublication(ctx, "project", time.Now().UTC())
		if !found {
			t.Fatal("missing plan")
		}
		if e == nil {
			if j.ID != job.ID || j.ReleaseID != job.ReleaseID || j.Status != "ready" {
				t.Fatal(j)
			}
			break
		}
		if !errors.Is(e, ErrPending) || loops > 20 {
			t.Fatal(e)
		}
	}
	if loops < 2 || db.max > 34 {
		t.Fatalf("loops=%d batch=%d", loops, db.max)
	}
	if got := scalar(t, db, "SELECT active_release_id FROM projects"); got != job.ReleaseID {
		t.Fatal(got)
	}
	if got := scalar(t, db, "SELECT COUNT(*) FROM skill_packages"); got != "20" {
		t.Fatal(got)
	}
	if initial == "" {
		t.Fatal("first bounded chunk not published")
	}
	if pending, e := s.PublicationPending(ctx, "project"); e != nil || pending {
		t.Fatal(pending, e)
	}
	if got := scalar(t, db, "SELECT COUNT(*) FROM sync_project_locks"); got != "0" {
		t.Fatal(got)
	}
}
func TestCanonicalLostAcknowledgmentDoesNotDuplicateCommittedChunk(t *testing.T) {
	s, db, _, _ := planFixture(t, 20)
	ctx := context.Background()
	db.lost = true
	before := scalar(t, db, "SELECT next_chunk FROM sync_publication_plans")
	_, _, e := s.ResumePublication(ctx, "project", time.Now().UTC())
	if !errors.Is(e, ErrPending) {
		t.Fatal(e)
	}
	after := scalar(t, db, "SELECT next_chunk FROM sync_publication_plans")
	if before == after {
		t.Fatal("cursor did not atomically advance")
	}
	for i := 0; i < 20; i++ {
		_, _, e = s.ResumePublication(ctx, "project", time.Now().UTC())
		if e == nil {
			break
		}
		if !errors.Is(e, ErrPending) {
			t.Fatal(e)
		}
	}
	if e != nil {
		t.Fatal(e)
	}
	if got := scalar(t, db, "SELECT COUNT(*) FROM skill_packages"); got != "20" {
		t.Fatal(got)
	}
}
func TestCanonicalCorruptPrivatePlanCannotExecuteSQL(t *testing.T) {
	s, db, _, _ := planFixture(t, 20)
	ctx := context.Background()
	_, e := db.Execute(ctx, stmt("UPDATE sync_publication_chunks SET payload=? WHERE chunk_index=(SELECT next_chunk FROM sync_publication_plans)", turso.Blob([]byte(`[{"sql":"DELETE FROM accounts"}]`))))
	if e != nil {
		t.Fatal(e)
	}
	j, _, e := s.ResumePublication(ctx, "project", time.Now().UTC())
	if !errors.Is(e, ErrInvalid) || j.ErrorCode != "publication_plan_corrupt" {
		t.Fatal(j, e)
	}
	if got := scalar(t, db, "SELECT COUNT(*) FROM accounts"); got != "1" {
		t.Fatal(got)
	}
	if got := scalar(t, db, "SELECT COALESCE(active_release_id,'') FROM projects"); got != "" {
		t.Fatal(got)
	}
}
func TestCanonicalLeasePreventsConcurrentCursorExecution(t *testing.T) {
	s, db, _, _ := planFixture(t, 20)
	ctx := context.Background()
	db.fail = true
	_, _, e := s.ResumePublication(ctx, "project", time.Now().UTC())
	if !errors.Is(e, ErrPending) {
		t.Fatal(e)
	}
	before := scalar(t, db, "SELECT next_chunk FROM sync_publication_plans")
	_, _, e = s.ResumePublication(ctx, "project", time.Now().UTC())
	if !errors.Is(e, ErrPending) {
		t.Fatal(e)
	}
	if got := scalar(t, db, "SELECT next_chunk FROM sync_publication_plans"); got != before {
		t.Fatal("lease stolen")
	}
	_, _, e = s.ResumePublication(ctx, "project", time.Now().UTC().Add(91*time.Second))
	if !errors.Is(e, ErrPending) {
		t.Fatal(e)
	}
	if got := scalar(t, db, "SELECT next_chunk FROM sync_publication_plans"); got == before {
		t.Fatal("expired lease did not recover")
	}
}

func TestRunResumesPublicationBeforeFetchingOrClaiming(t *testing.T) {
	store, db, _, job := planFixture(t, 20)
	fetcher := &fakeFetcher{err: errors.New("must not refetch")}
	scopeCalls := 0
	service, e := New(Config{Store: store, Fetcher: fetcher, InternalToken: "internal", ProjectContext: func(ctx context.Context, _ string) (context.Context, error) { scopeCalls++; return ctx, nil }})
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 20; i++ {
		j, e := service.Run(context.Background(), "project", fmt.Sprintf("different-key-%d", i))
		if j.ID != job.ID || j.ReleaseID != job.ReleaseID {
			t.Fatal("changed identity", j)
		}
		if e == nil {
			break
		}
		if !errors.Is(e, ErrPending) {
			t.Fatal(e)
		}
	}
	if fetcher.calls != 0 || scopeCalls < 2 {
		t.Fatalf("fetch=%d scope=%d", fetcher.calls, scopeCalls)
	}
	if got := scalar(t, db, "SELECT COUNT(*) FROM sync_jobs"); got != "1" {
		t.Fatal(got)
	}
	if got := scalar(t, db, "SELECT status FROM sync_jobs"); got != "ready" {
		t.Fatal(got)
	}
}
func TestCanonicalFinalActivationRejectsChangedPriorRelease(t *testing.T) {
	s, db, _, _ := planFixture(t, 20)
	ctx := context.Background()
	for scalar(t, db, "SELECT next_chunk+1=chunk_count FROM sync_publication_plans") != "1" {
		_, _, e := s.ResumePublication(ctx, "project", time.Now().UTC())
		if !errors.Is(e, ErrPending) {
			t.Fatal(e)
		}
	}
	_, e := db.Batch(ctx, []turso.Statement{stmt("INSERT INTO releases(id,project_id,commit_sha,status) VALUES('other','project','other','ready')"), stmt("UPDATE projects SET active_release_id='other' WHERE id='project'")})
	if e != nil {
		t.Fatal(e)
	}
	j, _, e := s.ResumePublication(ctx, "project", time.Now().UTC())
	if !errors.Is(e, ErrInvalid) || j.ErrorCode != "active_release_changed" {
		t.Fatal(j, e)
	}
	if got := scalar(t, db, "SELECT active_release_id FROM projects"); got != "other" {
		t.Fatal(got)
	}
}

func TestCanonicalPendingReservationRetainsJobAndRelease(t *testing.T) {
	ctx := context.Background()
	db := &canonicalTestDB{Database: sqlitetest.New(t, migrations.BaselineSQL+MigrationSQL+PublicationMigrationSQL+seedSQL), reservePending: true}
	s := SQLStore{DB: db}
	c, e := s.Connection(ctx, "project")
	if e != nil {
		t.Fatal(e)
	}
	now := time.Now().UTC()
	job, claimed, e := s.Claim(ctx, c, "delivery", now)
	if !errors.Is(e, ErrPending) || claimed || job.Status != "publishing" {
		t.Fatal(job, claimed, e)
	}
	pending, found, e := s.PendingPublication(ctx, "project")
	if e != nil || !found || pending.ID != job.ID {
		t.Fatal(pending, found, e)
	}
	if e = s.ResumeClaim(ctx, c, pending, now); e != nil {
		t.Fatal(e)
	}
	if e = s.ResumeClaim(ctx, c, pending, now); e != nil {
		t.Fatal(e)
	}
	if got := scalar(t, db, "SELECT COUNT(*) FROM releases"); got != "1" {
		t.Fatal(got)
	}
	if got := scalar(t, db, "SELECT id FROM releases"); got != job.ReleaseID {
		t.Fatal(got)
	}
}
func TestCanonicalIncompleteStagingFailsOnlyAfterLeaseExpires(t *testing.T) {
	s, db, _, job := planFixture(t, 20)
	ctx := context.Background()
	_, e := db.Batch(ctx, []turso.Statement{stmt("UPDATE sync_publication_plans SET state='staging'"), stmt("DELETE FROM sync_publication_chunks WHERE chunk_index=1")})
	if e != nil {
		t.Fatal(e)
	}
	j, _, e := s.ResumePublication(ctx, "project", time.Now().UTC())
	if !errors.Is(e, ErrPending) || j.ID != job.ID {
		t.Fatal(j, e)
	}
	j, _, e = s.ResumePublication(ctx, "project", time.Now().UTC().Add(121*time.Second))
	if !errors.Is(e, ErrInvalid) || j.ErrorCode != "publication_staging_interrupted" {
		t.Fatal(j, e)
	}
	if got := scalar(t, db, "SELECT COALESCE(active_release_id,'') FROM projects"); got != "" {
		t.Fatal(got)
	}
}
