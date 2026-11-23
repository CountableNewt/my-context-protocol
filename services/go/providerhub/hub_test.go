package providerhub

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/CountableNewt/my-context-protocol/services/go/internal/sqlitetest"
	"github.com/CountableNewt/my-context-protocol/services/go/migrations"
	"github.com/CountableNewt/my-context-protocol/services/go/reposync"
	"github.com/CountableNewt/my-context-protocol/services/go/syncservice"
	"github.com/CountableNewt/my-context-protocol/services/go/tangled"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
)

const did = "did:plc:aaaaaaaaaaaaaaaaaaaaaaaa"
const nativeRepo = "did:plc:bbbbbbbbbbbbbbbbbbbbbbbb"
const cid = "bafyreiaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type fakeTangled struct {
	head                         string
	public                       bool
	lookupError                  error
	proposalError                error
	writes, proposals, snapshots int
	change                       tangled.MetadataChange
}

func (f *fakeTangled) LookupRepository(_ context.Context, actor, owner, name string) (tangled.RepositoryView, error) {
	if f.lookupError != nil {
		return tangled.RepositoryView{}, f.lookupError
	}
	return tangled.RepositoryView{RepositoryDID: nativeRepo, OwnerDID: owner, Name: name, DefaultBranch: "main", URI: "at://" + nativeRepo + "/org.tangled.repo.manifest/self", CID: cid}, nil
}
func (f *fakeTangled) IsPublicRepository(context.Context, string, string, string) (bool, error) {
	return f.public, nil
}
func (f *fakeTangled) ListRepositories(context.Context, string, string, string, int) (tangled.RepositoryPage, error) {
	return tangled.RepositoryPage{Repositories: []tangled.RepositoryView{{RepositoryDID: nativeRepo, OwnerDID: did, Name: "repo"}}}, nil
}
func (f *fakeTangled) Snapshot(context.Context, string, string) (reposync.Outcome, error) {
	return reposync.Outcome{}, errors.New("anonymous snapshot must not be used")
}
func (f *fakeTangled) AuthenticatedSnapshot(_ context.Context, actor, repo, branch string) (reposync.Outcome, error) {
	f.snapshots++
	if actor != did || repo != nativeRepo || branch != "main" {
		return reposync.Outcome{}, errors.New("wrong native identity")
	}
	return reposync.Outcome{ResolvedCommitSHA: f.head}, nil
}
func (f *fakeTangled) ResolveBranch(context.Context, string, string) (string, error) {
	return "", errors.New("anonymous head lookup must not be used")
}
func (f *fakeTangled) ResolveAuthenticatedBranch(_ context.Context, actor, repo, branch string) (string, error) {
	if actor != did || repo != nativeRepo {
		return "", errors.New("wrong native identity")
	}
	return f.head, nil
}
func (f *fakeTangled) WriteMetadataBranch(_ context.Context, c tangled.MetadataChange) (tangled.MetadataBranch, error) {
	f.writes++
	f.change = c
	return tangled.MetadataBranch{Base: c.ExpectedBase, Head: strings.Repeat("b", 40), Branch: c.Branch, SourceRepositoryDID: c.RepositoryDID}, nil
}
func (f *fakeTangled) WriteAuthenticatedMetadataBranch(ctx context.Context, c tangled.MetadataChange) (tangled.MetadataBranch, error) {
	return f.WriteMetadataBranch(ctx, c)
}
func (f *fakeTangled) CreateProposal(_ context.Context, p tangled.PatchProposal) (tangled.ProposalReference, error) {
	f.proposals++
	if f.proposalError != nil {
		return tangled.ProposalReference{}, f.proposalError
	}
	return tangled.ProposalReference{TicketURI: "at://" + nativeRepo + "/org.tangled.track.ticket/ticket1", TicketCID: cid, PatchURI: "at://" + nativeRepo + "/org.tangled.review.patch/patch1", PatchCID: cid}, nil
}
func (f *fakeTangled) SubmitPatch(context.Context, tangled.PatchProposal, string) (tangled.ProposalReference, error) {
	return tangled.ProposalReference{}, errors.New("unexpected patch resubmission")
}

type attestFunc func(context.Context, string, string, string, string) error

func (f attestFunc) Attest(ctx context.Context, actor, uri, cid, change string) error {
	return f(ctx, actor, uri, cid, change)
}

func fixture(t *testing.T) (*Hub, *sqlitetest.DB, *fakeTangled) {
	t.Helper()
	db := sqlitetest.New(t, migrations.BaselineSQL+MigrationSQL)
	for _, stmt := range []turso.Statement{
		statement("INSERT INTO accounts(id,github_id,login) VALUES('account',1,'owner'),('other',2,'other')"),
		statement("INSERT INTO projects(id,account_id,name,slug,subdomain) VALUES('project','account','Project','project','project')"),
	} {
		if _, err := db.Execute(context.Background(), stmt); err != nil {
			t.Fatal(err)
		}
	}
	fake := &fakeTangled{head: strings.Repeat("a", 40), public: true}
	h, err := New(Config{DB: db, Tangled: fake, Identity: func(context.Context, string) (string, error) { return did, nil }, Now: func() time.Time { return time.Unix(2000, 0) }, Attestor: func(context.Context, string) (tangled.Attestor, error) {
		return attestFunc(func(context.Context, string, string, string, string) error { return nil }), nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	return h, db, fake
}
func connect(t *testing.T, h *Hub) string {
	t.Helper()
	input := map[string]json.RawMessage{"provider": json.RawMessage(`"tangled"`), "repo": json.RawMessage(`"repo"`)}
	if _, err := h.ConnectRepository(context.Background(), "account", "project", input); err != nil {
		t.Fatal(err)
	}
	rows, err := h.query(context.Background(), "SELECT id FROM repo_connections WHERE project_id='project'")
	if err != nil || len(rows) != 1 {
		t.Fatal(err)
	}
	return scalar(rows[0], 0)
}
func seedCompiled(t *testing.T, db *sqlitetest.DB) {
	t.Helper()
	document := `{"id":"guide","source":{"checksum":"` + strings.Repeat("a", 64) + `"},"validation":{"clarificationRequired":false},"scope":"repository"}`
	for _, stmt := range []turso.Statement{
		statement("INSERT INTO releases(id,project_id,commit_sha,status) VALUES('release','project',?,'ready')", turso.Text(strings.Repeat("a", 40))),
		statement("INSERT INTO skill_packages(id,release_id,path,name,validation_status) VALUES('package','release','SKILL.md','guide','valid')"),
		statement("INSERT INTO compiled_skills(id,release_id,skill_package_id,path,name,exposure_type,risk_level,repo_specific,status,canonical_json) VALUES('compiled','release','package','SKILL.md','guide','tool','low',0,'ready',?)", turso.Text(document)),
	} {
		if _, err := db.Execute(context.Background(), stmt); err != nil {
			t.Fatal(err)
		}
	}
}
func TestConnectionOwnershipNativeBindingsAndVisibility(t *testing.T) {
	h, db, fake := fixture(t)
	if _, err := h.ConnectRepository(context.Background(), "other", "project", nil); err == nil {
		t.Fatal("cross-account connect accepted")
	}
	fake.public = false
	id := connect(t, h)
	b, err := h.ResolveTangledConnection(context.Background(), id)
	if err != nil || !b.Private || b.RepositoryDID != nativeRepo || b.ActorDID != did {
		t.Fatalf("native identity not persisted %+v %v", b, err)
	}
	if _, err = h.FetchRepository(context.Background(), syncservice.Connection{ID: id, Provider: "tangled", Repository: "github-repo", Owner: "github-owner", AccountEncryptedToken: "secret"}); err != nil || fake.snapshots != 1 {
		t.Fatalf("authenticated snapshot failed %v", err)
	}
	h.cfg.Identity = func(context.Context, string) (string, error) { return "did:plc:cccccccccccccccccccccccc", nil }
	if _, err = h.ResolveTangledConnection(context.Background(), id); err == nil {
		t.Fatal("stale actor binding accepted")
	}
	_ = db
}
func TestProviderConnectionNeverOverwritesOtherProvider(t *testing.T) {
	h, db, _ := fixture(t)
	if _, err := db.Execute(context.Background(), statement("INSERT INTO repo_connections(id,project_id,provider,repo_owner,repo_name,default_branch,auth_type) VALUES('github','project','github','owner','repo','main','oauth')")); err != nil {
		t.Fatal(err)
	}
	if _, err := h.ConnectRepository(context.Background(), "account", "project", map[string]json.RawMessage{"provider": json.RawMessage(`"tangled"`), "repo": json.RawMessage(`"repo"`)}); err == nil {
		t.Fatal("GitHub connection overwritten")
	}
	rows, _ := h.query(context.Background(), "SELECT provider FROM repo_connections WHERE id='github'")
	if scalar(rows[0], 0) != "github" {
		t.Fatal("provider modified")
	}
}
func TestWritebackResumesAttestationWithoutDuplicatingExternalWork(t *testing.T) {
	h, db, fake := fixture(t)
	connect(t, h)
	seedCompiled(t, db)
	failed := true
	attestations := 0
	h.cfg.Attestor = func(context.Context, string) (tangled.Attestor, error) {
		return attestFunc(func(context.Context, string, string, string, string) error {
			attestations++
			if failed {
				return errors.New("PDS unavailable")
			}
			return nil
		}), nil
	}
	if _, err := h.Writeback(context.Background(), "account", "project", "release", "compiled"); err == nil {
		t.Fatal("PDS failure reported success")
	}
	rows, _ := h.query(context.Background(), "SELECT status FROM atproto_metadata_proposals")
	if scalar(rows[0], 0) != "patch_complete" {
		t.Fatal("proposal references not durably saved")
	}
	failed = false
	result, err := h.Writeback(context.Background(), "account", "project", "release", "compiled")
	if err != nil {
		t.Fatal(err)
	}
	if result["status"] != "complete" || fake.writes != 1 || fake.proposals != 1 || attestations != 3 {
		t.Fatalf("external work duplicated %+v writes=%d proposals=%d attest=%d", result, fake.writes, fake.proposals, attestations)
	}
	if fake.change.ActorDID != did || fake.change.RepositoryDID != nativeRepo || fake.change.CommitTime.Unix() != 2000 {
		t.Fatal("stable native metadata intent lost")
	}
	if _, err = h.Writeback(context.Background(), "account", "project", "release", "compiled"); err != nil || fake.writes != 1 || fake.proposals != 1 {
		t.Fatal("completed operation was not idempotent")
	}
}
func TestWritebackUncertainTicketCreationFailsClosedOnRetry(t *testing.T) {
	h, db, fake := fixture(t)
	connect(t, h)
	seedCompiled(t, db)
	fake.proposalError = errors.New("lost acknowledgement")
	if _, err := h.Writeback(context.Background(), "account", "project", "release", "compiled"); err == nil {
		t.Fatal("lost response accepted")
	}
	fake.proposalError = nil
	if _, err := h.Writeback(context.Background(), "account", "project", "release", "compiled"); err == nil {
		t.Fatal("uncertain ticket blindly retried")
	}
	if fake.proposals != 1 {
		t.Fatal("duplicate external ticket created")
	}
}
func TestPrivateWritebackUsesAuthenticatedGitTreeRead(t *testing.T) {
	h, db, fake := fixture(t)
	fake.public = false
	connect(t, h)
	seedCompiled(t, db)
	if _, err := h.Writeback(context.Background(), "account", "project", "release", "compiled"); err != nil {
		t.Fatal(err)
	}
	if fake.writes != 1 || fake.proposals != 1 {
		t.Fatal("private metadata proposal did not complete")
	}
}
func TestReconciliationDoesNotAdvanceHeadAfterFailedSync(t *testing.T) {
	h, db, fake := fixture(t)
	id := connect(t, h)
	if _, err := db.Execute(context.Background(), statement("UPDATE atproto_repo_bindings SET next_check_at=0")); err != nil {
		t.Fatal(err)
	}
	fake.head = strings.Repeat("b", 40)
	h.cfg.Sync = func(context.Context, string) (map[string]any, error) { return nil, errors.New("sync failed") }
	report, err := h.Reconcile(context.Background(), 1)
	if err != nil || report.Changed != 1 || report.Failed != 1 {
		t.Fatalf("failed sync report %+v %v", report, err)
	}
	rows, _ := h.query(context.Background(), "SELECT last_revision FROM atproto_repo_bindings WHERE connection_id=?", turso.Text(id))
	if scalar(rows[0], 0) != strings.Repeat("a", 40) {
		t.Fatal("failed sync advanced checkpoint")
	}
	h.cfg.Now = func() time.Time { return time.Unix(2200, 0) }
	h.cfg.Sync = func(context.Context, string) (map[string]any, error) { return map[string]any{"status": "ready"}, nil }
	report, err = h.Reconcile(context.Background(), 1)
	if err != nil || report.Synced != 1 {
		t.Fatalf("retry %+v %v", report, err)
	}
	rows, _ = h.query(context.Background(), "SELECT last_revision FROM atproto_repo_bindings WHERE connection_id=?", turso.Text(id))
	if scalar(rows[0], 0) != fake.head {
		t.Fatal("successful sync did not advance checkpoint")
	}
}

func TestCompletedProposalRetriesUnpersistedMetadataLink(t *testing.T) {
	h, db, fake := fixture(t)
	connect(t, h)
	seedCompiled(t, db)
	if _, err := db.Execute(context.Background(), statement("INSERT INTO skill_runtime_overrides(id,project_id,skill_id,metadata_json,scope) VALUES('override','project','guide','{}','repository')")); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Writeback(context.Background(), "account", "project", "release", "compiled"); err != nil {
		t.Fatal(err)
	}
	// Simulate failed final projection persistence after the external proposal and
	// attestations were durably confirmed. Retry must repair only the local link.
	if _, err := db.Execute(context.Background(), statement("UPDATE skill_runtime_overrides SET writeback_pr_url=NULL")); err != nil {
		t.Fatal(err)
	}
	result, err := h.Writeback(context.Background(), "account", "project", "release", "compiled")
	if err != nil {
		t.Fatal(err)
	}
	rows, err := h.query(context.Background(), "SELECT writeback_pr_url FROM skill_runtime_overrides WHERE id='override'")
	if err != nil || len(rows) != 1 || scalar(rows[0], 0) != result["pull_request_url"] {
		t.Fatal("completed retry skipped metadata link", rows, err)
	}
	if fake.writes != 1 || fake.proposals != 1 {
		t.Fatal("retry duplicated external work")
	}
}

func TestRepositoryIdentityUsesBoundDIDAndRechecksLinkedActor(t *testing.T) {
	h, _, _ := fixture(t)
	id := connect(t, h)
	connection := syncservice.Connection{ID: id, Provider: "tangled", Owner: "mutable-owner.example", Repository: "renamed-repo"}
	got, err := h.RepositoryIdentity(context.Background(), connection)
	if err != nil || got != nativeRepo {
		t.Fatal("unstable native identity", got, err)
	}
	h.cfg.Identity = func(context.Context, string) (string, error) { return "did:plc:cccccccccccccccccccccccc", nil }
	if _, err = h.RepositoryIdentity(context.Background(), connection); err == nil {
		t.Fatal("provenance accepted stale linked actor")
	}
}
