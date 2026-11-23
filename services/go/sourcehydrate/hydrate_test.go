package sourcehydrate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CountableNewt/my-context-protocol/services/go/compiler"
	"github.com/CountableNewt/my-context-protocol/services/go/domainrecords"
	"github.com/CountableNewt/my-context-protocol/services/go/internal/sqlitetest"
	"github.com/CountableNewt/my-context-protocol/services/go/migrations"
	"github.com/CountableNewt/my-context-protocol/services/go/reposync"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
)

const revision = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func fixture(t *testing.T, count int) (Hydrator, *sqlitetest.DB, *int) {
	t.Helper()
	db := sqlitetest.New(t, migrations.BaselineSQL+domainrecords.MigrationSQL+`CREATE TABLE atproto_repo_bindings(connection_id TEXT PRIMARY KEY,repository_did TEXT);`)
	_, err := db.Batch(context.Background(), []turso.Statement{{SQL: `INSERT INTO accounts(id,github_id,login) VALUES('owner',1,'owner'),('other',2,'other')`}, {SQL: `INSERT INTO projects(id,account_id,name,slug) VALUES('project','owner','Project','project')`}, {SQL: `INSERT INTO repo_connections(id,project_id,provider,repo_owner,repo_name,default_branch,auth_type) VALUES('connection','project','github','owner','repo','main','oauth')`}, {SQL: `INSERT INTO releases(id,project_id,commit_sha,status) VALUES('release','project','` + revision + `','ready')`}, {SQL: `INSERT INTO domain_canonical_projects(project_id,account_id,owner_did,manifest_uri,manifest_cid,review_digest,public_consent,telemetry_consent,target_active_release_id,updated_at) VALUES('project','owner','did:plc:aaaaaaaaaaaaaaaaaaaaaaaa','at://manifest','manifest','digest',1,0,'release',1)`}})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < count; i++ {
		path := fmt.Sprintf("skill%d/SKILL.md", i)
		source := []byte(fmt.Sprintf("---\nname: skill%d\ndescription: test\nunknown: keep\n---\nBody %d\n", i, i))
		canonical, _ := json.Marshal(map[string]any{"source": provenance{Repository: "owner/repo", Path: path, Revision: revision, Checksum: compiler.Hash(source)}, "standardFrontmatterJson": "{\"unknown\":\"keep\"}", "futureUnknown": map[string]any{"nested": true}})
		_, err = db.Batch(context.Background(), []turso.Statement{statement("INSERT INTO skill_packages(id,release_id,path,name,hash,validation_status) VALUES(?,'release',?,?,?,'valid')", turso.Text(fmt.Sprintf("package%d", i)), turso.Text(path), turso.Text(fmt.Sprintf("skill%d", i)), turso.Text(compiler.Hash(source))), statement("INSERT INTO compiled_skills(id,release_id,skill_package_id,path,name,exposure_type,risk_level,repo_specific,status,source_checksum,canonical_json) VALUES(?,'release',?,?,?,'tool','low',0,'ready',?,?)", turso.Text(fmt.Sprintf("compiled%d", i)), turso.Text(fmt.Sprintf("package%d", i)), turso.Text(path), turso.Text(fmt.Sprintf("skill%d", i)), turso.Text(compiler.Hash(source)), turso.Text(string(canonical)))})
		if err != nil {
			t.Fatal(err)
		}
	}
	calls := new(int)
	h := Hydrator{DB: db, Fetch: func(_ context.Context, connectionID, sha string) (reposync.Outcome, error) {
		*calls++
		if connectionID != "connection" || sha != revision {
			t.Fatalf("unverified fetch arguments %s %s", connectionID, sha)
		}
		root := t.TempDir()
		for i := 0; i < count; i++ {
			dir := filepath.Join(root, fmt.Sprintf("skill%d", i))
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			source := []byte(fmt.Sprintf("---\nname: skill%d\ndescription: test\nunknown: keep\n---\nBody %d\n", i, i))
			if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), source, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "asset.txt"), []byte("asset"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		return reposync.Outcome{RepositoryRoot: root, TempRoot: root, ResolvedCommitSHA: revision}, nil
	}}
	return h, db, calls
}
func TestVerifiedBodyAndFilesRebuildPreservesMetadataAndActivePointer(t *testing.T) {
	h, db, calls := fixture(t, 2)
	ctx := context.Background()
	if err := h.Hydrate(ctx, "owner", "project"); err != nil {
		t.Fatal(err)
	}
	if *calls != 1 {
		t.Fatal("snapshot was not reused across same-revision skills")
	}
	r, err := db.Execute(ctx, statement("SELECT skill_body,canonical_json FROM compiled_skills WHERE id='compiled0'"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(scalar(r.Rows[0], 0), "Body 0") {
		t.Fatal("body missing")
	}
	var metadata map[string]any
	if json.Unmarshal([]byte(scalar(r.Rows[0], 1)), &metadata) != nil || metadata["instructions"] != scalar(r.Rows[0], 0) || metadata["standardFrontmatterJson"] != "{\"unknown\":\"keep\"}" || metadata["futureUnknown"] == nil {
		t.Fatal("canonical unknown metadata was replaced")
	}
	r, err = db.Execute(ctx, statement("SELECT COUNT(*),SUM(byte_count) FROM skill_package_files"))
	if err != nil || scalar(r.Rows[0], 0) != "2" || scalar(r.Rows[0], 1) != "10" {
		t.Fatal("package files not rebuilt")
	}
	r, err = db.Execute(ctx, statement("SELECT active_release_id FROM projects WHERE id='project'"))
	if err != nil || r.Rows[0][0].Type != "null" {
		t.Fatal("hydrator activated a release itself")
	}
	if err = h.Hydrate(ctx, "owner", "project"); err != nil || *calls != 1 {
		t.Fatal("completed source work repeated")
	}
}
func TestHydrationIsBoundedAndResumesMissingPackages(t *testing.T) {
	h, db, calls := fixture(t, 3)
	h.MaxSkills = 1
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		err := h.Hydrate(ctx, "owner", "project")
		if i < 2 && !errors.Is(err, domainrecords.ErrPending) {
			t.Fatalf("missing pending status: %v", err)
		}
		if i == 2 && err != nil {
			t.Fatal(err)
		}
		r, e := db.Execute(ctx, statement("SELECT COUNT(*) FROM compiled_skills WHERE skill_body IS NOT NULL"))
		if e != nil || scalar(r.Rows[0], 0) != fmt.Sprint(i+1) {
			t.Fatal("hydration limit ignored")
		}
	}
	if *calls != 3 {
		t.Fatal("unexpected snapshot lifetime")
	}
}
func TestSourceMismatchAndCrossTenantNeverPopulateBodies(t *testing.T) {
	for _, mode := range []string{"revision", "checksum", "repository", "path"} {
		t.Run(mode, func(t *testing.T) {
			h, db, _ := fixture(t, 1)
			ctx := context.Background()
			switch mode {
			case "revision":
				fetch := h.Fetch
				h.Fetch = func(ctx context.Context, id, sha string) (reposync.Outcome, error) {
					out, err := fetch(ctx, id, sha)
					out.ResolvedCommitSHA = strings.Repeat("b", 40)
					return out, err
				}
			case "checksum":
				_, _ = db.Execute(ctx, statement("UPDATE compiled_skills SET source_checksum=?", turso.Text(strings.Repeat("b", 64))))
			case "repository":
				_, _ = db.Execute(ctx, statement("UPDATE repo_connections SET repo_name='other'"))
			case "path":
				_, _ = db.Execute(ctx, statement("UPDATE compiled_skills SET path='../outside/SKILL.md'"))
			}
			if err := h.Hydrate(ctx, "owner", "project"); !errors.Is(err, ErrSourceMismatch) {
				t.Fatalf("mismatch accepted: %v", err)
			}
			r, _ := db.Execute(ctx, statement("SELECT skill_body FROM compiled_skills"))
			if r.Rows[0][0].Type != "null" {
				t.Fatal("unverified body cached")
			}
		})
	}
	h, _, calls := fixture(t, 1)
	if err := h.Hydrate(context.Background(), "other", "project"); err == nil || *calls != 0 {
		t.Fatal("cross-account hydration fetched source")
	}
}
func TestSafeSkillPathRejectsParentSymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "SKILL.md"), []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "skill")); err != nil {
		t.Fatal(err)
	}
	if _, err := safeSkillPath(root, "skill/SKILL.md"); !errors.Is(err, ErrSourceMismatch) {
		t.Fatal("parent symlink followed")
	}
	for _, path := range []string{"../SKILL.md", "/SKILL.md", "skill\\SKILL.md", "skill/../SKILL.md", "skill/README.md"} {
		if _, err := safeSkillPath(root, path); err == nil {
			t.Fatalf("unsafe source path accepted: %s", path)
		}
	}
}
