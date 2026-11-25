package atplatform

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/CountableNewt/my-context-protocol/services/go/ataccount"
	"github.com/CountableNewt/my-context-protocol/services/go/atproto"
	"github.com/CountableNewt/my-context-protocol/services/go/identity"
	"github.com/CountableNewt/my-context-protocol/services/go/internal/sqlitetest"
	"github.com/CountableNewt/my-context-protocol/services/go/lexiconpublish"
	"github.com/CountableNewt/my-context-protocol/services/go/migrations"
	"github.com/CountableNewt/my-context-protocol/services/go/providerhub"
	"github.com/CountableNewt/my-context-protocol/services/go/reposync"
	"github.com/CountableNewt/my-context-protocol/services/go/tangled"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
	"github.com/bluesky-social/indigo/atproto/atcrypto"
)

type noTouchDB struct{}

func (noTouchDB) Execute(context.Context, turso.Statement) (*turso.Result, error) {
	return nil, errors.New("constructor touched database")
}
func (noTouchDB) Batch(context.Context, []turso.Statement) ([]turso.Result, error) {
	return nil, errors.New("constructor touched database")
}
func platformEnv(t *testing.T) map[string]string {
	t.Helper()
	key, e := atcrypto.GeneratePrivateKeyP256()
	if e != nil {
		t.Fatal(e)
	}
	return map[string]string{"ATPROTO_ENABLED": "true", "ENCRYPTION_KEY": base64.StdEncoding.EncodeToString(make([]byte, 32)), "ATPROTO_OAUTH_PRIVATE_KEY": key.Multibase(), "ATPROTO_CLIENT_ID": "https://mycontextprotocol.dev/oauth-client-metadata.json", "ATPROTO_CALLBACK_URL": "https://mycontextprotocol.dev/auth/atproto/callback", "ATPROTO_RECORD_COLLECTIONS": "dev.mycontextprotocol.data.project", "ATPROTO_OAUTH_KEY_ID": "oauth-1", "TANGLED_ENABLED": "true", "TANGLED_API_URL": "https://tangled.example.com", "TANGLED_SERVICE_DID": "did:web:tangled.example.com#api", "TANGLED_ARCHIVE_MIRROR_URL": "https://archive.example.com"}
}
func TestPlatformConfigurationFailsClosedWithoutDatabaseWrites(t *testing.T) {
	if p, e := New(noTouchDB{}, func(string) string { return "" }); e != nil || p != nil {
		t.Fatal(p, e)
	}
	if _, e := New(noTouchDB{}, func(k string) string {
		if k == "TANGLED_ENABLED" {
			return "true"
		}
		return ""
	}); e == nil {
		t.Fatal("Tangled silently enabled without ATProto")
	}
	env := platformEnv(t)
	get := func(k string) string { return env[k] }
	if p, e := New(noTouchDB{}, get); e != nil || p == nil || p.Tangled == nil {
		t.Fatal(p, e)
	}
	if _, e := New(nil, get); e == nil {
		t.Fatal("nil DB accepted")
	}
	for _, key := range []string{"ATPROTO_OAUTH_PRIVATE_KEY", "ENCRYPTION_KEY", "TANGLED_API_URL", "TANGLED_SERVICE_DID", "TANGLED_ARCHIVE_MIRROR_URL"} {
		old := env[key]
		env[key] = ""
		if _, e := New(noTouchDB{}, get); e == nil {
			t.Fatalf("missing %s accepted", key)
		}
		env[key] = old
	}
	for _, origin := range []string{"http://example.com", "https://user:secret@example.com", "https://127.0.0.1", "https://localhost", "https://service.internal", "https://example.com/path", "https://example.com?token=secret", "https://example.com:8443"} {
		env["TANGLED_API_URL"] = origin
		if _, e := New(noTouchDB{}, get); e == nil {
			t.Fatal(origin)
		}
	}
}

type linkedIdentity struct{ did string }

func (s linkedIdentity) ATProtoIdentity(context.Context, string) (*identity.ATProtoIdentity, error) {
	return &identity.ATProtoIdentity{DID: s.did, AccountID: "account", SessionID: "session"}, nil
}

type attestationRecords struct {
	values map[string]atproto.Record
	puts   int
	lose   bool
}

func (s *attestationRecords) Get(_ context.Context, m atproto.Mutation) (atproto.Record, error) {
	r, ok := s.values[m.URI()]
	if !ok {
		return r, atproto.ErrNotFound
	}
	return r, nil
}
func (s *attestationRecords) Put(_ context.Context, m atproto.Mutation) (atproto.Reference, error) {
	cid, e := lexiconpublish.RecordCID(m.Record)
	if e != nil {
		return atproto.Reference{}, e
	}
	ref := atproto.Reference{URI: m.URI(), CID: cid}
	s.values[m.URI()] = atproto.Record{Reference: ref, Value: m.Record}
	s.puts++
	if s.lose {
		s.lose = false
		return atproto.Reference{}, errors.New("lost ack")
	}
	return ref, nil
}
func TestAttestationPinsIdentityAndRecoversStableRecordAfterLostAcknowledgment(t *testing.T) {
	ctx := context.Background()
	actor := "did:plc:aaaaaaaaaaaaaaaaaaaaaaaa"
	db := sqlitetest.New(t, MigrationSQL+atproto.MigrationSQL)
	pds := &attestationRecords{values: map[string]atproto.Record{}, lose: true}
	sessions := 0
	p := &Platform{Raw: db, Accounts: &ataccount.Manager{Identities: linkedIdentity{actor}}, recordSession: func(ctx context.Context, _ string, apply func(context.Context, atproto.Records) error) error {
		sessions++
		return apply(ctx, pds)
	}}
	a := attestor{platform: p, account: "account"}
	cid, e := lexiconpublish.RecordCID([]byte(`{"$type":"org.tangled.repo","name":"repo"}`))
	if e != nil {
		t.Fatal(e)
	}
	uri := "at://" + actor + "/org.tangled.repo/repo"
	if e = a.Attest(ctx, "did:plc:bbbbbbbbbbbbbbbbbbbbbbbb", uri, cid, ""); e == nil || sessions != 0 {
		t.Fatal("wrong actor authorized", e)
	}
	if e = a.Attest(ctx, actor, uri, cid, "patch"); e == nil {
		t.Fatal("lost ack hidden")
	}
	if e = a.Attest(ctx, actor, uri, cid, "patch"); e != nil {
		t.Fatal(e)
	}
	if e = a.Attest(ctx, actor, uri, cid, "patch"); e != nil || pds.puts != 1 || len(pds.values) != 1 {
		t.Fatal(e, pds.puts)
	}
	if e = a.Attest(ctx, actor, uri, cid, "different patch"); !errors.Is(e, atproto.ErrConflict) {
		t.Fatal(e)
	}
}
func TestSourceRecoveryRejectsMutableRevisionAndChangedActorBeforeNetwork(t *testing.T) {
	ctx := context.Background()
	p := &Platform{Raw: noTouchDB{}}
	for _, rev := range []string{"main", "HEAD", strings.Repeat("z", 40), ""} {
		if _, e := p.FetchRevision(ctx, "repo", rev); e == nil || !strings.Contains(e.Error(), "immutable") {
			t.Fatal(rev, e)
		}
	}
	db := sqlitetest.New(t, migrations.BaselineSQL+providerhub.MigrationSQL+`INSERT INTO accounts(id,github_id,login) VALUES('account',1,'owner');INSERT INTO projects(id,account_id,name,slug) VALUES('project','account','project','project');INSERT INTO repo_connections(id,project_id,provider,repo_owner,repo_name,default_branch,auth_type) VALUES('repo','project','tangled','did:plc:owner','repo','main','atproto');INSERT INTO atproto_repo_bindings(connection_id,repository_did,owner_did,actor_did,is_private,branch) VALUES('repo','did:plc:repo','did:plc:owner','did:plc:prior',0,'main');`)
	p = &Platform{Raw: db, Tangled: &tangled.Client{}, Accounts: &ataccount.Manager{Identities: linkedIdentity{"did:plc:changed"}}}
	if _, e := p.FetchRevision(ctx, "repo", strings.Repeat("a", 40)); e == nil || !strings.Contains(e.Error(), "reauthorized") {
		t.Fatal(e)
	}
}

type sourceFixture struct {
	resolved, ref string
	fetches       int
}

func (s *sourceFixture) ResolveCommitSHA(_ context.Context, _, _, ref, _ string) (string, error) {
	s.ref = ref
	return s.resolved, nil
}
func (s *sourceFixture) Fetch(_ context.Context, _, _, ref, _ string) (reposync.Outcome, error) {
	s.ref = ref
	s.fetches++
	return reposync.Outcome{RepositoryRoot: "pinned-source"}, nil
}
func TestGitHubSourceRecoveryPinsRequestedImmutableCommit(t *testing.T) {
	ctx := context.Background()
	db := sqlitetest.New(t, migrations.BaselineSQL+`INSERT INTO accounts(id,github_id,login) VALUES('account',1,'owner');INSERT INTO projects(id,account_id,name,slug) VALUES('project','account','project','project');INSERT INTO repo_connections(id,project_id,provider,repo_owner,repo_name,default_branch,auth_type) VALUES('repo','project','github','owner','repo','moving-main','oauth');`)
	revision := strings.Repeat("a", 40)
	source := &sourceFixture{resolved: strings.Repeat("b", 40)}
	p := Platform{Raw: db, githubSource: source}
	if _, e := p.FetchRevision(ctx, "repo", revision); e == nil || source.fetches != 0 || source.ref != revision {
		t.Fatal("mismatched identity fetched", e, source)
	}
	source.resolved = revision
	result, e := p.FetchRevision(ctx, "repo", revision)
	if e != nil || source.fetches != 1 || source.ref != revision || result.ResolvedCommitSHA != revision {
		t.Fatal(result, e, source)
	}
}
