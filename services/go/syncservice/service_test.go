package syncservice

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CountableNewt/my-context-protocol/services/go/compiler"
	"github.com/CountableNewt/my-context-protocol/services/go/reposync"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
)

type memoryStore struct {
	connection                    Connection
	job                           Job
	claimed                       bool
	claimCalls, published, failed int
	lastKey                       string
	report                        compiler.Report
	compiled                      []compiler.Compiled
	unsupported                   bool
}

func (m *memoryStore) Connection(context.Context, string) (Connection, error) {
	return m.connection, nil
}
func (m *memoryStore) Connections(context.Context, string, string) ([]Connection, error) {
	return []Connection{m.connection}, nil
}
func (m *memoryStore) Claim(_ context.Context, _ Connection, key string, _ time.Time) (Job, bool, error) {
	m.claimCalls++
	m.lastKey = key
	return m.job, m.claimed, nil
}
func (m *memoryStore) Job(context.Context, string) (Job, error) { return m.job, nil }
func (m *memoryStore) UnsupportedOverrides(context.Context, string) (bool, error) {
	return m.unsupported, nil
}
func (m *memoryStore) Publish(_ context.Context, _ Connection, _ Job, _ string, skills []compiler.Compiled, r compiler.Report, _ time.Time) error {
	m.published++
	m.compiled = skills
	m.report = r
	return nil
}
func (m *memoryStore) Fail(context.Context, Job, string, time.Time) error { m.failed++; return nil }

type fakeFetcher struct {
	lastToken string
	root      string
	calls     int
	err       error
}

func (f *fakeFetcher) Fetch(_ context.Context, _ string, _ string, _ string, token string) (reposync.Outcome, error) {
	f.calls++
	f.lastToken = token
	if f.err != nil {
		return reposync.Outcome{}, f.err
	}
	return reposync.Outcome{TempRoot: f.root, RepositoryRoot: f.root, ExtractPath: f.root, ResolvedCommitSHA: strings.Repeat("a", 40)}, nil
}
func setup(t *testing.T) (*Service, *memoryStore, *fakeFetcher) {
	t.Helper()
	root := t.TempDir()
	os.Mkdir(filepath.Join(root, "a"), 0700)
	os.WriteFile(filepath.Join(root, "a", "SKILL.md"), []byte("---\nname: a\ndescription: Helpful skill\nuse_when: writing code\n---\nInstructions."), 0600)
	store := &memoryStore{connection: Connection{ID: "connection", ProjectID: "project", Owner: "owner", Repository: "repo", Branch: "main", WebhookSecret: "secret"}, job: Job{ID: "job", ProjectID: "project", ReleaseID: "release", Status: "running"}, claimed: true}
	fetcher := &fakeFetcher{root: root}
	service, err := New(Config{Store: store, Fetcher: fetcher, InternalToken: "internal"})
	if err != nil {
		t.Fatal(err)
	}
	return service, store, fetcher
}
func sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}
func TestWebhookSignatureUsesExactRawBytes(t *testing.T) {
	body := []byte(`{"value":"test"}`)
	sig := sign("secret", body)
	if !VerifySignature("secret", sig, body) {
		t.Fatal("valid signature rejected")
	}
	for _, tc := range []struct {
		secret, header string
		body           []byte
	}{{"secret", sig, append(body, ' ')}, {"other", sig, body}, {"", sig, body}, {"secret", "sha1=" + sig, body}, {"secret", "sha256=00", body}} {
		if VerifySignature(tc.secret, tc.header, tc.body) {
			t.Fatal("accepted forged webhook")
		}
	}
}
func TestWebhookRejectsBeforeSync(t *testing.T) {
	service, store, fetcher := setup(t)
	body := `{"ref":"refs/heads/main","repository":{"name":"repo","owner":{"login":"owner"}}}`
	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(body))
	req.Header.Set("X-GitHub-Event", "push")
	req.Header.Set("X-GitHub-Delivery", "delivery")
	req.Header.Set("X-Hub-Signature-256", sign("wrong", []byte(body)))
	w := httptest.NewRecorder()
	service.ServeHTTP(w, req)
	if w.Code != 401 || store.claimCalls != 0 || fetcher.calls != 0 {
		t.Fatalf("status=%d claims=%d fetch=%d", w.Code, store.claimCalls, fetcher.calls)
	}
}
func TestWebhookSyncAndIdempotency(t *testing.T) {
	service, store, fetcher := setup(t)
	body := `{ "ref":"refs/heads/main", "repository":{"name":"repo","owner":{"login":"owner"}}}`
	request := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/webhooks/github", strings.NewReader(body))
		req.Header.Set("X-GitHub-Event", "push")
		req.Header.Set("X-GitHub-Delivery", "delivery-123")
		req.Header.Set("X-Hub-Signature-256", sign("secret", []byte(body)))
		w := httptest.NewRecorder()
		service.ServeHTTP(w, req)
		return w
	}
	w := request()
	if w.Code != 200 || store.published != 1 || fetcher.calls != 1 || store.lastKey != "github:delivery-123" {
		t.Fatalf("%d %s %+v", w.Code, w.Body, store)
	}
	if len(store.compiled) != 1 || store.compiled[0].Status != "ready" {
		t.Fatal(store.compiled)
	}
	if _, err := os.Stat(fetcher.root); !os.IsNotExist(err) {
		t.Fatal("snapshot not cleaned")
	}
	store.claimed = false
	store.job.Status = "ready"
	w = request()
	if w.Code != 200 || fetcher.calls != 1 || store.published != 1 {
		t.Fatal("duplicate delivery repeated work")
	}
}
func TestWebhookOtherBranchDoesNotSync(t *testing.T) {
	service, store, fetcher := setup(t)
	body := `{"ref":"refs/heads/feature","repository":{"name":"repo","owner":{"login":"owner"}}}`
	r := httptest.NewRequest("POST", "/webhooks/github", strings.NewReader(body))
	r.Header.Set("X-GitHub-Event", "push")
	r.Header.Set("X-GitHub-Delivery", "delivery")
	r.Header.Set("X-Hub-Signature-256", sign("secret", []byte(body)))
	w := httptest.NewRecorder()
	service.ServeHTTP(w, r)
	if w.Code != 200 || fetcher.calls != 0 || store.claimCalls != 0 {
		t.Fatalf("%d %+v", w.Code, store)
	}
}
func TestManualSyncInternalAuth(t *testing.T) {
	service, store, fetcher := setup(t)
	r := httptest.NewRequest("POST", "/internal/sync", strings.NewReader(`{"project_id":"project"}`))
	w := httptest.NewRecorder()
	service.ServeHTTP(w, r)
	if w.Code != 401 || store.claimCalls != 0 {
		t.Fatal(w.Code)
	}
	r = httptest.NewRequest("POST", "/internal/sync", strings.NewReader(`{"project_id":"project","idempotency_key":"request"}`))
	r.Header.Set("Authorization", "Bearer internal")
	w = httptest.NewRecorder()
	service.ServeHTTP(w, r)
	if w.Code != 200 || fetcher.calls != 1 || store.lastKey != "request" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}
func TestUnsupportedMetadataAndFetchFailureNeverPublish(t *testing.T) {
	for _, mode := range []string{"overrides", "installation", "decrypt", "fetch"} {
		t.Run(mode, func(t *testing.T) {
			service, store, fetcher := setup(t)
			switch mode {
			case "overrides":
				store.unsupported = true
			case "installation":
				store.connection.InstallationID = "123"
			case "decrypt":
				store.connection.EncryptedToken = "ciphertext"
			case "fetch":
				fetcher.err = errors.New("network unavailable")
			}
			job, err := service.Run(context.Background(), "project", "")
			if err == nil || job.Status != "failed" || store.failed != 1 || store.published != 0 {
				t.Fatalf("job=%+v err=%v store=%+v", job, err, store)
			}
		})
	}
}
func TestInvalidSkillsDoNotActivate(t *testing.T) {
	service, store, fetcher := setup(t)
	os.WriteFile(filepath.Join(fetcher.root, "a", "SKILL.md"), []byte("---\nname: wrong\ndescription: valid\n---\nBody"), 0600)
	job, err := service.Run(context.Background(), "project", "")
	if !errors.Is(err, ErrInvalid) || job.Status != "failed" || store.report.Valid || len(store.report.Errors) == 0 {
		t.Fatalf("%+v %v %+v", job, err, store.report)
	}
}

type recordingDB struct {
	batches  [][]turso.Statement
	activate bool
	err      error
}

func (d *recordingDB) Execute(context.Context, turso.Statement) (*turso.Result, error) {
	return &turso.Result{}, d.err
}
func (d *recordingDB) Batch(_ context.Context, statements []turso.Statement) ([]turso.Result, error) {
	d.batches = append(d.batches, statements)
	if d.err != nil {
		return nil, d.err
	}
	results := make([]turso.Result, len(statements))
	for i, s := range statements {
		if strings.HasPrefix(s.SQL, "UPDATE projects SET active_release_id") && d.activate {
			results[i].Rows = [][]turso.Value{{turso.Text("project")}}
		}
	}
	return results, nil
}
func TestPublicationAtomicCatalogSwitch(t *testing.T) {
	skill, _ := compiler.Parse("a/SKILL.md", []byte("---\nname: a\ndescription: useful\nuse_when: writing\n---\nbody"))
	compiled, _ := compiler.Compile(skill, "owner/repo", "sha", nil)
	db := &recordingDB{activate: true}
	store := SQLStore{DB: db}
	err := store.Publish(context.Background(), Connection{ProjectID: "project", PriorReleaseID: "old"}, Job{ID: "job", ProjectID: "project", ReleaseID: "new"}, "sha", []compiler.Compiled{compiled}, compiler.Report{Valid: true, Errors: []compiler.Issue{}, Warnings: []compiler.Issue{}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(db.batches) != 2 {
		t.Fatalf("batches %d", len(db.batches))
	}
	for _, stmt := range db.batches[0] {
		if strings.Contains(stmt.SQL, "active_release_id") {
			t.Fatal("catalog switched before final transaction")
		}
	}
	final := db.batches[1]
	if !strings.Contains(final[2].SQL, "COALESCE(active_release_id,'')=?") || !strings.Contains(final[2].SQL, "lease_until>?") || !strings.Contains(final[3].SQL, "mcp_catalog_revisions") {
		t.Fatal("publication missing concurrency guards or transactional revision bump")
	}
	db.activate = false
	if err = store.Publish(context.Background(), Connection{ProjectID: "project"}, Job{ID: "job", ReleaseID: "new"}, "sha", []compiler.Compiled{compiled}, compiler.Report{Valid: true}, time.Now()); !errors.Is(err, ErrBusy) {
		t.Fatalf("stale publication reported success: %v", err)
	}
}
func TestInvalidReleaseNeverSwitchesCatalog(t *testing.T) {
	db := &recordingDB{}
	err := (SQLStore{DB: db}).Publish(context.Background(), Connection{ProjectID: "project"}, Job{ID: "job", ReleaseID: "release"}, "sha", nil, compiler.Report{Valid: false}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, batch := range db.batches {
		for _, s := range batch {
			if strings.Contains(s.SQL, "UPDATE projects SET active_release_id") {
				t.Fatal("invalid release can activate")
			}
		}
	}
}

func TestRepositorySecretWorksAlongsideAppWebhookSecret(t *testing.T) {
	s, store, _ := setup(t)
	s.config.WebhookSecret = "global-app-secret"
	body := `{"ref":"refs/heads/feature","repository":{"name":"repo","owner":{"login":"owner"}}}`
	r := httptest.NewRequest("POST", "/webhooks/github", strings.NewReader(body))
	r.Header.Set("X-GitHub-Event", "push")
	r.Header.Set("X-GitHub-Delivery", "delivery")
	r.Header.Set("X-Hub-Signature-256", sign("secret", []byte(body)))
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 200 || store.claimCalls != 0 {
		t.Fatalf("repository secret was blocked by App secret %d", w.Code)
	}
}

func TestRepoCredentialDecryptFailureFallsBackToAccount(t *testing.T) {
	s, store, fetcher := setup(t)
	store.connection.EncryptedToken = "invalid-repo-token"
	store.connection.AccountEncryptedToken = "account-token"
	s.config.DecryptToken = func(encrypted string) (string, error) {
		if encrypted == "account-token" {
			return "valid-account-oauth", nil
		}
		return "", errors.New("invalid ciphertext")
	}
	if _, err := s.Run(context.Background(), "project", ""); err != nil {
		t.Fatal(err)
	}
	if fetcher.lastToken != "valid-account-oauth" {
		t.Fatalf("fallback credential %q", fetcher.lastToken)
	}
}
func TestRetainedCompilationBudgetDoesNotPublishPartialCatalog(t *testing.T) {
	s, store, _ := setup(t)
	s.config.MaxCompiledBytes = 1
	job, err := s.Run(context.Background(), "project", "")
	if err == nil || job.Status != "failed" || store.published != 0 || store.failed != 1 {
		t.Fatalf("job %+v err%v store%+v", job, err, store)
	}
}
