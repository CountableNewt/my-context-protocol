package domainrecords

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/CountableNewt/my-context-protocol/services/go/atproto"
	"github.com/CountableNewt/my-context-protocol/services/go/internal/sqlitetest"
	"github.com/CountableNewt/my-context-protocol/services/go/migrations"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
	"github.com/bluesky-social/indigo/atproto/lexicon"
	cidlib "github.com/ipfs/go-cid"
	"github.com/multiformats/go-multihash"
)

const account = "account"
const owner = "did:plc:aaaaaaaaaaaaaaaaaaaaaaaa"

type memoryRecords struct {
	mu        sync.Mutex
	values    map[string]atproto.Record
	puts      int
	failAfter int
}

func (m *memoryRecords) Get(_ context.Context, v atproto.Mutation) (atproto.Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.values[v.URI()]
	if !ok {
		return r, atproto.ErrNotFound
	}
	r.Value = append([]byte(nil), r.Value...)
	return r, nil
}
func (m *memoryRecords) Put(_ context.Context, v atproto.Mutation) (atproto.Reference, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failAfter > 0 && m.puts >= m.failAfter {
		return atproto.Reference{}, errors.New("PDS unavailable")
	}
	previous, ok := m.values[v.URI()]
	if v.ExpectedCID == nil && ok || v.ExpectedCID != nil && (!ok || previous.CID != *v.ExpectedCID) {
		return atproto.Reference{}, atproto.ErrConflict
	}
	hash, _ := multihash.Sum(v.Record, multihash.SHA2_256, -1)
	ref := atproto.Reference{URI: v.URI(), CID: cidlib.NewCidV1(cidlib.DagCBOR, hash).String()}
	m.values[v.URI()] = atproto.Record{Reference: ref, Value: append([]byte(nil), v.Record...)}
	m.puts++
	return ref, nil
}
func fixture(t *testing.T) (Engine, *sqlitetest.DB, *memoryRecords) {
	t.Helper()
	db := sqlitetest.New(t, migrations.BaselineSQL+MigrationSQL+atproto.MigrationSQL+`CREATE TABLE mcp_catalog_revisions(project_id TEXT PRIMARY KEY,revision INTEGER NOT NULL);CREATE TABLE account_atproto_identities(did TEXT PRIMARY KEY,account_id TEXT);CREATE TABLE atproto_repo_bindings(connection_id TEXT PRIMARY KEY REFERENCES repo_connections(id),repository_did TEXT NOT NULL,owner_did TEXT NOT NULL,actor_did TEXT NOT NULL,is_private INTEGER NOT NULL,branch TEXT NOT NULL,last_checked_at INTEGER NOT NULL DEFAULT 0,next_check_at INTEGER NOT NULL DEFAULT 0);`)
	_, err := db.Batch(context.Background(), []turso.Statement{{SQL: `INSERT INTO accounts(id,github_id,login) VALUES('account',1,'owner'),('other',2,'other')`}, {SQL: `INSERT INTO account_atproto_identities(did,account_id) VALUES('` + owner + `','account')`}, {SQL: `INSERT INTO projects(id,account_id,name,slug,subdomain,created_at,custom_domain_verification_token) VALUES('project','account','Project','project','project',1234.25,'never-publish')`}, {SQL: `INSERT INTO repo_connections(id,project_id,provider,repo_owner,repo_name,default_branch,auth_type,token_encrypted,webhook_secret) VALUES('repo','project','github','owner','repo','main','oauth','secret-token','secret-webhook')`}})
	if err != nil {
		t.Fatal(err)
	}
	records := &memoryRecords{values: map[string]atproto.Record{}}
	return Engine{DB: db, Records: records, Publisher: atproto.Publisher{Records: records, Journal: atproto.SQLJournal{DB: db}}}, db, records
}
func approved(r Review) Consent {
	return Consent{ReviewDigest: r.Digest, Public: true, Telemetry: true}
}
func name(t *testing.T, db *sqlitetest.DB) string {
	t.Helper()
	r, err := db.Execute(context.Background(), statement("SELECT name FROM projects WHERE id='project'"))
	if err != nil {
		t.Fatal(err)
	}
	return r.Rows[0][0].Value.(string)
}

func TestInventoryPublicationConsentAndCanonicalFirstUpdate(t *testing.T) {
	e, db, records := fixture(t)
	ctx := context.Background()
	review, err := e.Inventory(ctx, account, owner, "project")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(review)
	for _, secret := range []string{"never-publish", "secret-token", "secret-webhook", "token_encrypted"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("secret exported: %s", secret)
		}
	}
	if _, err = e.Publish(ctx, account, review, Consent{}); !errors.Is(err, ErrConsent) {
		t.Fatalf("missing consent: %v", err)
	}
	ref, err := e.Publish(ctx, account, review, approved(review))
	if err != nil {
		t.Fatal(err)
	}
	if ref.CID == "" {
		t.Fatal("missing manifest")
	}
	records.failAfter = records.puts
	if _, err = e.Update(ctx, account, owner, "project", []Change{{Table: "projects", ID: "project", Values: map[string]turso.Value{"name": turso.Text("Renamed")}}}); err == nil {
		t.Fatal("PDS outage acknowledged")
	}
	if got := name(t, db); got != "Project" {
		t.Fatal("SQL changed before PDS commit")
	}
	records.failAfter = 0
	if _, err = e.Update(ctx, account, owner, "project", []Change{{Table: "projects", ID: "project", Values: map[string]turso.Value{"name": turso.Text("Renamed")}}}); err != nil {
		t.Fatal(err)
	}
	if got := name(t, db); got != "Renamed" {
		t.Fatal("canonical update not projected")
	}
	// Returning to a previously published payload must publish a new transition,
	// rather than reusing an old completed manifest intent against a newer CID.
	if _, err = e.Update(ctx, account, owner, "project", []Change{{Table: "projects", ID: "project", Values: map[string]turso.Value{"name": turso.Text("Project")}}}); err != nil {
		t.Fatal(err)
	}
	if err = e.Reconcile(ctx, account, owner, "project"); err != nil {
		t.Fatal(err)
	}
}
func TestPublicationResumesWithoutReplayingCompletedPrefix(t *testing.T) {
	e, db, records := fixture(t)
	e.WorkLimit = 1
	ctx := context.Background()
	review, err := e.Inventory(ctx, account, owner, "project")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		before := records.puts
		_, err = e.Publish(ctx, account, review, approved(review))
		if records.puts-before > 1 {
			t.Fatal("request exceeded publication budget")
		}
		if err == nil {
			break
		}
		if !errors.Is(err, ErrPending) {
			t.Fatal(err)
		}
		if name(t, db) != "Project" {
			t.Fatal("partial generation activated")
		}
		if i == 9 {
			t.Fatal("cursor failed to progress")
		}
	}
	if _, _, err = e.readManifest(ctx, owner, "project"); err != nil {
		t.Fatal(err)
	}
}
func TestReviewChangesAndTelemetryConsentCannotBeBypassed(t *testing.T) {
	e, db, records := fixture(t)
	ctx := context.Background()
	review, err := e.Inventory(ctx, account, owner, "project")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Execute(ctx, statement("UPDATE projects SET name='Changed' WHERE id='project'")); err != nil {
		t.Fatal(err)
	}
	if _, err = e.Publish(ctx, account, review, approved(review)); !errors.Is(err, ErrChanged) || records.puts != 0 {
		t.Fatalf("changed review published: %v", err)
	}
	if _, err = db.Execute(ctx, statement("INSERT INTO request_logs(id,project_id,method,status) VALUES('log','project','tools/list','success')")); err != nil {
		t.Fatal(err)
	}
	review, err = e.Inventory(ctx, account, owner, "project")
	if err != nil {
		t.Fatal(err)
	}
	review.HasTelemetry = false
	if _, err = e.Publish(ctx, account, review, Consent{ReviewDigest: review.Digest, Public: true}); !errors.Is(err, ErrConsent) || records.puts != 0 {
		t.Fatalf("telemetry consent bypass: %v", err)
	}
}

func TestLegacyTelemetryOptInStillRequiresPublicConsentWithoutHistory(t *testing.T) {
	e, db, records := fixture(t)
	ctx := context.Background()
	if _, err := db.Execute(ctx, statement("INSERT INTO project_runtime_settings(id,project_id,telemetry_enabled) VALUES('settings','project',1)")); err != nil {
		t.Fatal(err)
	}
	review, err := e.Inventory(ctx, account, owner, "project")
	if err != nil {
		t.Fatal(err)
	}
	if !review.HasTelemetry {
		t.Fatal("legacy telemetry opt-in omitted from public review")
	}
	if _, err = e.Publish(ctx, account, review, Consent{ReviewDigest: review.Digest, Public: true}); !errors.Is(err, ErrConsent) || records.puts != 0 {
		t.Fatalf("legacy opt-in silently became public consent: %v", err)
	}
}
func TestProjectionRejectsCrossTenantIdentityCollision(t *testing.T) {
	e, db, _ := fixture(t)
	ctx := context.Background()
	review, err := e.Inventory(ctx, account, owner, "project")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.Publish(ctx, account, review, approved(review)); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Execute(ctx, statement("INSERT INTO projects(id,account_id,name,slug) VALUES('other-project','other','Other','other')")); err != nil {
		t.Fatal(err)
	}
	if _, err = e.Update(ctx, account, owner, "project", []Change{{Table: "projects", ID: "other-project", Values: map[string]turso.Value{"name": turso.Text("Stolen")}}}); err == nil {
		t.Fatal("cross-project root accepted")
	}
	if _, err = e.Update(ctx, account, owner, "project", []Change{{Table: "projects", ID: "project", Values: map[string]turso.Value{"custom_domain_verification_token": turso.Text("secret")}}}); err == nil {
		t.Fatal("private field accepted")
	}
	if _, err = e.Inventory(ctx, "other", owner, "project"); err == nil {
		t.Fatal("unlinked owner accepted")
	}
}

func TestExternalManifestRebuildIsBoundedAndReadOnlyOnPDS(t *testing.T) {
	e, _, records := fixture(t)
	ctx := context.Background()
	review, err := e.Inventory(ctx, account, owner, "project")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.Publish(ctx, account, review, approved(review)); err != nil {
		t.Fatal(err)
	}
	before := records.puts
	if _, err = e.Update(ctx, account, owner, "project", []Change{{Table: "projects", ID: "project", Values: map[string]turso.Value{"name": turso.Text("Remote")}}}); err != nil {
		t.Fatal(err)
	}
	if records.puts-before != 3 {
		t.Fatalf("rename rewrote unchanged records: %d writes", records.puts-before)
	}
	m, _, err := e.readManifest(ctx, owner, "project")
	if err != nil {
		t.Fatal(err)
	}
	copy, db, _ := fixture(t)
	copy.Records = records
	copy.WorkLimit = 1
	before = records.puts
	for i := 0; i < 10; i++ {
		err = copy.Import(ctx, account, owner, "project", Consent{ReviewDigest: m.Digest, Public: true, Telemetry: true})
		if err == nil {
			break
		}
		if !errors.Is(err, ErrPending) {
			t.Fatal(err)
		}
		if name(t, db) != "Project" {
			t.Fatal("partial external manifest activated")
		}
		if i == 9 {
			t.Fatal("reconciliation cursor stalled")
		}
	}
	if records.puts != before || name(t, db) != "Remote" {
		t.Fatal("rebuild wrote PDS or failed projection")
	}
}

func TestNativeRepositoryIdentityIsCanonicalAndScoped(t *testing.T) {
	e, db, _ := fixture(t)
	ctx := context.Background()
	if _, err := db.Execute(ctx, statement("INSERT INTO atproto_repo_bindings(connection_id,repository_did,owner_did,actor_did,is_private,branch) VALUES('repo','did:plc:bbbbbbbbbbbbbbbbbbbbbbbb',?,?,1,'main')", turso.Text(owner), turso.Text(owner))); err != nil {
		t.Fatal(err)
	}
	review, err := e.Inventory(ctx, account, owner, "project")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range review.Entries {
		if entry.Table == "atproto_repo_bindings" {
			found = true
			if entry.ID != "repo" {
				t.Fatal("native connection identity lost")
			}
		}
	}
	if !found {
		t.Fatal("native repository DID omitted")
	}
	if _, err = e.Publish(ctx, account, review, approved(review)); err != nil {
		t.Fatal(err)
	}
}

func TestRecordSchemasValidateWithProtocolSDK(t *testing.T) {
	for name, raw := range Schemas() {
		var schema lexicon.SchemaFile
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatal(err)
		}
		if err := schema.FinishParse(); err != nil {
			t.Fatal(err)
		}
		if err := schema.CheckSchema(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func TestReleaseActivationWaitsForSourceHydration(t *testing.T) {
	e, db, _ := fixture(t)
	ctx := context.Background()
	review, err := e.Inventory(ctx, account, owner, "project")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.Publish(ctx, account, review, approved(review)); err != nil {
		t.Fatal(err)
	}
	changes := []Change{
		{Table: "releases", ID: "release", Values: map[string]turso.Value{"commit_sha": turso.Text("revision"), "status": turso.Text("ready"), "skill_body_changes_count": turso.Integer(0)}},
		{Table: "skill_packages", ID: "package", Values: map[string]turso.Value{"release_id": turso.Text("release"), "path": turso.Text("skill/SKILL.md"), "name": turso.Text("skill"), "validation_status": turso.Text("valid")}},
		{Table: "compiled_skills", ID: "compiled", Values: map[string]turso.Value{"release_id": turso.Text("release"), "skill_package_id": turso.Text("package"), "path": turso.Text("skill/SKILL.md"), "name": turso.Text("skill"), "exposure_type": turso.Text("tool"), "risk_level": turso.Text("low"), "repo_specific": turso.Integer(0), "status": turso.Text("ready"), "yaml_frontmatter_present": turso.Integer(1), "canonical_schema_version": turso.Integer(1), "clarification_required": turso.Integer(0), "source_policy_stale": turso.Integer(0)}},
		{Table: "projects", ID: "project", Values: map[string]turso.Value{"active_release_id": turso.Text("release")}},
	}
	if _, err = e.Update(ctx, account, owner, "project", changes); !errors.Is(err, ErrHydrationRequired) {
		t.Fatalf("incomplete release activated: %v", err)
	}
	r, err := db.Execute(ctx, statement("SELECT active_release_id FROM projects WHERE id='project'"))
	if err != nil || r.Rows[0][0].Type != "null" {
		t.Fatal("active pointer changed before source body was ready")
	}
	e.Hydrate = func(ctx context.Context, _, _ string) error {
		_, err := db.Execute(ctx, statement("UPDATE compiled_skills SET skill_body='# Skill',canonical_json=json_set(COALESCE(canonical_json,'{}'),'$.instructions','# Skill') WHERE id='compiled'"))
		return err
	}
	if err = e.Reconcile(ctx, account, owner, "project"); err != nil {
		t.Fatal(err)
	}
	r, err = db.Execute(ctx, statement("SELECT active_release_id FROM projects WHERE id='project'"))
	if err != nil || r.Rows[0][0].Value != "release" {
		t.Fatal("ready hydrated release was not activated")
	}
}

func TestNativeIntentResumesLostProjectionWithoutDuplicatingSideEffects(t *testing.T) {
	e, db, records := fixture(t)
	ctx := context.Background()
	review, err := e.Inventory(ctx, account, owner, "project")
	if err != nil {
		t.Fatal(err)
	}
	source, err := e.Publish(ctx, account, review, approved(review))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Execute(ctx, statement("CREATE TABLE native_effects(id INTEGER PRIMARY KEY)")); err != nil {
		t.Fatal(err)
	}
	targetDigest := ""
	e.ExpectedPriorCID = source.CID
	e.BeforePublish = func(_ context.Context, r Review) error { targetDigest = r.Digest; return nil }
	failed := false
	e.NativeProjection = func(context.Context, string, Review, atproto.Reference) ([]turso.Statement, bool, error) {
		if !failed {
			failed = true
			return nil, true, errors.New("projection unavailable")
		}
		return []turso.Statement{statement("UPDATE projects SET name='Native' WHERE id='project'"), statement("INSERT INTO native_effects(id) VALUES(1)")}, true, nil
	}
	change := []Change{{Table: "projects", ID: "project", Values: map[string]turso.Value{"name": turso.Text("Native")}}}
	if _, err = e.Update(ctx, account, owner, "project", change); err == nil {
		t.Fatal("failed projection acknowledged")
	}
	if name(t, db) != "Project" {
		t.Fatal("SQL mutation committed early")
	}
	puts := records.puts
	e.ExpectedDigest = targetDigest
	if _, err = e.Update(ctx, account, owner, "project", change); err != nil {
		t.Fatal(err)
	}
	if records.puts != puts || name(t, db) != "Native" {
		t.Fatal("lost projection republished or failed native SQL")
	}
	if _, err = e.Update(ctx, account, owner, "project", change); err != nil {
		t.Fatal("idempotent retry", err)
	}
	r, err := db.Execute(ctx, statement("SELECT COUNT(*) FROM native_effects"))
	if err != nil || r.Rows[0][0].Value != "1" {
		t.Fatal("native SQL duplicated")
	}
}

func TestExpectedSourceRejectsRemoteDriftBeforePublication(t *testing.T) {
	e, _, records := fixture(t)
	ctx := context.Background()
	review, err := e.Inventory(ctx, account, owner, "project")
	if err != nil {
		t.Fatal(err)
	}
	source, err := e.Publish(ctx, account, review, approved(review))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.Update(ctx, account, owner, "project", []Change{{Table: "projects", ID: "project", Values: map[string]turso.Value{"name": turso.Text("Remote")}}}); err != nil {
		t.Fatal(err)
	}
	e.ExpectedPriorCID = source.CID
	before := records.puts
	if _, err = e.Update(ctx, account, owner, "project", []Change{{Table: "projects", ID: "project", Values: map[string]turso.Value{"name": turso.Text("Stale")}}}); !errors.Is(err, atproto.ErrConflict) || records.puts != before {
		t.Fatalf("stale intent published: %v", err)
	}
}

func TestOldActiveTreeSurvivesUntilReplacementHydration(t *testing.T) {
	e, db, _ := fixture(t)
	ctx := context.Background()
	_, err := db.Batch(ctx, []turso.Statement{{SQL: `INSERT INTO releases(id,project_id,commit_sha,status) VALUES('old-release','project','old-sha','ready')`}, {SQL: `INSERT INTO skill_packages(id,release_id,path,name,validation_status) VALUES('old-package','old-release','SKILL.md','old','valid')`}, {SQL: `INSERT INTO compiled_skills(id,release_id,skill_package_id,path,name,skill_body,exposure_type,risk_level,repo_specific,status) VALUES('old-compiled','old-release','old-package','SKILL.md','old','old body','tool','low',0,'ready')`}, {SQL: `UPDATE compiled_skills SET canonical_json='{"instructions":"old body","source":{"revision":"old-sha","path":"SKILL.md","checksum":"old"}}' WHERE id='old-compiled'`}, {SQL: `UPDATE projects SET active_release_id='old-release' WHERE id='project'`}})
	if err != nil {
		t.Fatal(err)
	}
	review, err := e.Inventory(ctx, account, owner, "project")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.Publish(ctx, account, review, approved(review)); err != nil {
		t.Fatal(err)
	}
	changes := []Change{{Table: "releases", ID: "old-release", Delete: true}, {Table: "skill_packages", ID: "old-package", Delete: true}, {Table: "compiled_skills", ID: "old-compiled", Delete: true}, {Table: "projects", ID: "project", Values: map[string]turso.Value{"active_release_id": turso.Text("new-release")}},
		{Table: "releases", ID: "new-release", Values: map[string]turso.Value{"commit_sha": turso.Text("new-sha"), "status": turso.Text("ready"), "skill_body_changes_count": turso.Integer(0)}},
		{Table: "skill_packages", ID: "new-package", Values: map[string]turso.Value{"release_id": turso.Text("new-release"), "path": turso.Text("SKILL.md"), "name": turso.Text("new"), "validation_status": turso.Text("valid")}},
		{Table: "compiled_skills", ID: "new-compiled", Values: map[string]turso.Value{"release_id": turso.Text("new-release"), "skill_package_id": turso.Text("new-package"), "path": turso.Text("SKILL.md"), "name": turso.Text("new"), "exposure_type": turso.Text("tool"), "risk_level": turso.Text("low"), "repo_specific": turso.Integer(0), "status": turso.Text("ready"), "yaml_frontmatter_present": turso.Integer(1), "canonical_schema_version": turso.Integer(1), "clarification_required": turso.Integer(0), "source_policy_stale": turso.Integer(0)}},
	}
	if _, err = e.Update(ctx, account, owner, "project", changes); !errors.Is(err, ErrHydrationRequired) {
		t.Fatal("replacement not held for hydration", err)
	}
	r, err := db.Execute(ctx, statement("SELECT active_release_id,(SELECT COUNT(*) FROM compiled_skills WHERE id='old-compiled') FROM projects WHERE id='project'"))
	if err != nil || r.Rows[0][0].Value != "old-release" || r.Rows[0][1].Value != "1" {
		t.Fatal("old active tree removed while hydration pending")
	}
	e.Hydrate = func(ctx context.Context, _, _ string) error {
		_, err := db.Execute(ctx, statement("UPDATE compiled_skills SET skill_body='new body',canonical_json=json_set(COALESCE(canonical_json,'{}'),'$.instructions','new body') WHERE id='new-compiled'"))
		return err
	}
	if err = e.Reconcile(ctx, account, owner, "project"); err != nil {
		t.Fatal(err)
	}
	r, err = db.Execute(ctx, statement("SELECT active_release_id,(SELECT COUNT(*) FROM releases WHERE id='old-release') FROM projects WHERE id='project'"))
	if err != nil || r.Rows[0][0].Value != "new-release" || r.Rows[0][1].Value != "0" {
		t.Fatal("old tree not retired with final activation")
	}
}
