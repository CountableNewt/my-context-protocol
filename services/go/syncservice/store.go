package syncservice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/CountableNewt/my-context-protocol/services/go/compiler"
	"github.com/CountableNewt/my-context-protocol/services/go/identity"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
)

const MigrationSQL = `
CREATE TABLE IF NOT EXISTS sync_jobs(id TEXT PRIMARY KEY,project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,delivery_key TEXT NOT NULL,release_id TEXT NOT NULL,status TEXT NOT NULL,error_code TEXT,created_at INTEGER NOT NULL,updated_at INTEGER NOT NULL,lease_until INTEGER NOT NULL,UNIQUE(project_id,delivery_key));
CREATE TABLE IF NOT EXISTS sync_project_locks(project_id TEXT PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,job_id TEXT NOT NULL,lease_until INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS mcp_catalog_revisions(project_id TEXT PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,revision INTEGER NOT NULL DEFAULT 0);
`

type Database interface {
	Execute(context.Context, turso.Statement) (*turso.Result, error)
	Batch(context.Context, []turso.Statement) ([]turso.Result, error)
}
type SQLStore struct{ DB Database }

func value(row []turso.Value, i int) string {
	if i >= len(row) || row[i].Type == "null" {
		return ""
	}
	return fmt.Sprint(row[i].Value)
}
func stmt(sql string, args ...turso.Value) turso.Statement {
	return turso.Statement{SQL: sql, Args: args, WantRows: true}
}

const connectionSelect = `SELECT r.id,r.project_id,r.repo_owner,r.repo_name,r.default_branch,r.token_encrypted,CASE WHEN COALESCE(NULLIF(r.provider,''),'github')='github' THEN a.github_token_encrypted ELSE NULL END,r.webhook_secret,r.github_installation_id,p.active_release_id,r.provider FROM repo_connections r JOIN projects p ON p.id=r.project_id JOIN accounts a ON a.id=p.account_id `

func connection(row []turso.Value) (Connection, error) {
	if len(row) != 11 {
		return Connection{}, errors.New("invalid repository connection row")
	}
	return Connection{Provider: value(row, 10), ID: value(row, 0), ProjectID: value(row, 1), Owner: value(row, 2), Repository: value(row, 3), Branch: value(row, 4), EncryptedToken: value(row, 5), AccountEncryptedToken: value(row, 6), WebhookSecret: value(row, 7), InstallationID: value(row, 8), PriorReleaseID: value(row, 9)}, nil
}
func (s SQLStore) Connection(ctx context.Context, id string) (Connection, error) {
	r, err := s.DB.Execute(ctx, stmt(connectionSelect+"WHERE p.id=? ORDER BY r.id LIMIT 1", turso.Text(id)))
	if err != nil {
		return Connection{}, err
	}
	if len(r.Rows) == 0 {
		return Connection{}, ErrNotFound
	}
	return connection(r.Rows[0])
}
func (s SQLStore) Connections(ctx context.Context, owner, repo string) ([]Connection, error) {
	r, err := s.DB.Execute(ctx, stmt(connectionSelect+"WHERE COALESCE(NULLIF(r.provider,''),'github')='github' AND lower(r.repo_owner)=lower(?) AND lower(r.repo_name)=lower(?) ORDER BY r.project_id,r.id LIMIT 11", turso.Text(owner), turso.Text(repo)))
	if err != nil {
		return nil, err
	}
	out := []Connection{}
	for _, row := range r.Rows {
		c, e := connection(row)
		if e != nil {
			return nil, e
		}
		out = append(out, c)
	}
	return out, nil
}

// All existing runtime override fields are handled by RuntimeOverride. The
// capability remains on Store for adapters that cannot preserve their metadata.
func (s SQLStore) UnsupportedOverrides(context.Context, string) (bool, error) { return false, nil }
func (s SQLStore) RuntimeOverride(ctx context.Context, c Connection, skill compiler.Skill, preferred string) (map[string]any, error) {
	r, err := s.DB.Execute(ctx, stmt(`SELECT id,metadata_json,COALESCE(base_checksum,source_checksum),is_stale FROM skill_runtime_overrides WHERE project_id=? AND skill_id=? AND (repo_connection_id IS NULL OR repo_connection_id=?) ORDER BY CASE WHEN repo_connection_id=? THEN 0 ELSE 1 END,CASE WHEN scope=? THEN -1 WHEN scope='task' THEN 0 WHEN scope='repository' THEN 1 WHEN scope='workspace' THEN 2 WHEN scope='organization' THEN 3 WHEN scope='global' THEN 4 ELSE 99 END,COALESCE(updated_at,0) DESC,id ASC LIMIT 1`, turso.Text(c.ProjectID), turso.Text(skill.Name), turso.Text(c.ID), turso.Text(c.ID), turso.Text(preferred)))
	if err != nil {
		return nil, err
	}
	if len(r.Rows) == 0 {
		return nil, nil
	}
	row := r.Rows[0]
	if len(row) != 4 {
		return nil, errors.New("invalid runtime override row")
	}
	checksum := value(row, 2)
	stale := checksum != "" && checksum != skill.Hash
	if (value(row, 3) == "1") != stale {
		if _, err = s.DB.Execute(ctx, stmt("UPDATE skill_runtime_overrides SET is_stale=? WHERE id=?", flag(stale), turso.Text(value(row, 0)))); err != nil {
			return nil, err
		}
	}
	if stale {
		return nil, nil
	}
	var patch map[string]any
	if err = json.Unmarshal([]byte(value(row, 1)), &patch); err != nil {
		return nil, errors.New("invalid runtime override metadata")
	}
	return patch, nil
}

func decodeJob(row []turso.Value) (Job, error) {
	if len(row) != 6 {
		return Job{}, errors.New("invalid sync job row")
	}
	return Job{value(row, 0), value(row, 1), value(row, 2), value(row, 3), value(row, 4)}, nil
}

const jobCols = "id,project_id,release_id,status,error_code,lease_until"

func (s SQLStore) Job(ctx context.Context, id string) (Job, error) {
	r, err := s.DB.Execute(ctx, stmt("SELECT "+jobCols+" FROM sync_jobs WHERE id=?", turso.Text(id)))
	if err != nil {
		return Job{}, err
	}
	if len(r.Rows) == 0 {
		return Job{}, ErrNotFound
	}
	return decodeJob(r.Rows[0])
}
func (s SQLStore) Claim(ctx context.Context, c Connection, key string, now time.Time) (Job, bool, error) {
	// Retrying a failed delivery changes sync_jobs.id. Remove only its terminal
	// private plan first so its foreign key cannot prevent the fresh reservation.
	if s.hasPublicationPlans() && key != "" {
		_, err := s.DB.Batch(ctx, []turso.Statement{
			stmt("DELETE FROM sync_publication_chunks WHERE job_id IN(SELECT p.job_id FROM sync_publication_plans p JOIN sync_jobs j ON j.id=p.job_id WHERE j.project_id=? AND j.delivery_key=? AND j.status='failed' AND p.state IN ('complete','failed'))", turso.Text(c.ProjectID), turso.Text(key)),
			stmt("DELETE FROM sync_publication_plans WHERE job_id IN(SELECT id FROM sync_jobs WHERE project_id=? AND delivery_key=? AND status='failed') AND state IN ('complete','failed')", turso.Text(c.ProjectID), turso.Text(key)),
		})
		if err != nil {
			return Job{}, false, err
		}
	}

	id, err := identity.UUID()
	if err != nil {
		return Job{}, false, err
	}
	release, err := identity.UUID()
	if err != nil {
		return Job{}, false, err
	}
	if key == "" {
		key = "manual:" + id
	}
	// A duplicate delivery returns its durable state. An expired attempt is failed,
	// then reclaimed with a new release; it never reuses partially staged rows.
	results, err := s.DB.Batch(ctx, []turso.Statement{
		stmt("UPDATE sync_jobs SET status='failed',error_code='lease_expired',updated_at=? WHERE project_id=? AND delivery_key=? AND status='running' AND lease_until<=?", turso.Integer(now.Unix()), turso.Text(c.ProjectID), turso.Text(key), turso.Integer(now.Unix())),
		stmt("INSERT INTO sync_jobs(id,project_id,delivery_key,release_id,status,created_at,updated_at,lease_until) VALUES (?,?,?,?,'running',?,?,?) ON CONFLICT(project_id,delivery_key) DO UPDATE SET id=excluded.id,release_id=excluded.release_id,status='running',error_code=NULL,updated_at=excluded.updated_at,lease_until=excluded.lease_until WHERE sync_jobs.status='failed' RETURNING "+jobCols, turso.Text(id), turso.Text(c.ProjectID), turso.Text(key), turso.Text(release), turso.Integer(now.Unix()), turso.Integer(now.Unix()), turso.Integer(now.Add(120*time.Second).Unix())),
		stmt("SELECT "+jobCols+" FROM sync_jobs WHERE project_id=? AND delivery_key=?", turso.Text(c.ProjectID), turso.Text(key)),
	})
	if err != nil {
		return Job{}, false, err
	}
	if len(results) != 3 || len(results[2].Rows) != 1 {
		return Job{}, false, errors.New("invalid claim result")
	}
	job, err := decodeJob(results[2].Rows[0])
	if err != nil {
		return Job{}, false, err
	}
	if len(results[1].Rows) == 0 {
		return job, false, nil
	}
	locked, err := s.DB.Execute(ctx, stmt("INSERT INTO sync_project_locks(project_id,job_id,lease_until) VALUES (?,?,?) ON CONFLICT(project_id) DO UPDATE SET job_id=excluded.job_id,lease_until=excluded.lease_until WHERE sync_project_locks.lease_until<=? RETURNING job_id", turso.Text(c.ProjectID), turso.Text(job.ID), turso.Integer(now.Add(120*time.Second).Unix()), turso.Integer(now.Unix())))
	if err != nil {
		return job, false, err
	}
	if len(locked.Rows) != 1 {
		_ = s.Fail(ctx, job, "sync_in_progress", now)
		return job, false, ErrBusy
	}
	_, err = s.DB.Execute(ctx, stmt("INSERT INTO releases(id,project_id,commit_sha,status,created_at) VALUES (?,?,'pending','pending',?)", turso.Text(job.ReleaseID), turso.Text(c.ProjectID), turso.Integer(now.Unix())))
	if err != nil {
		if s.publicationEnabled(ctx) {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			_, _ = s.DB.Execute(cleanup, stmt("UPDATE sync_jobs SET status='publishing',updated_at=? WHERE id=? AND release_id=?", turso.Integer(now.Unix()), turso.Text(job.ID), turso.Text(job.ReleaseID)))
			job.Status = "publishing"
			return job, false, errors.Join(ErrPending, err)
		}
		_ = s.Fail(ctx, job, "release_create_failed", now)
		return job, false, err
	}
	return job, true, nil
}
func jsonValue(v any) turso.Value { data, _ := json.Marshal(v); return turso.Text(string(data)) }
func flag(v bool) turso.Value {
	if v {
		return turso.Integer(1)
	}
	return turso.Integer(0)
}
func nullableString(v string) turso.Value {
	if v == "" {
		return turso.Null()
	}
	return turso.Text(v)
}

// carryForward preserves dashboard-edited presentation/routing from the active
// release, matching Swift's path+name key while retaining freshly compiled bodies.
func (s SQLStore) carryForward(ctx context.Context, prior string, c *compiler.Compiled) (string, error) {
	if prior == "" {
		return "", nil
	}
	r, err := s.DB.Execute(ctx, stmt(`SELECT c.summary,c.risk_level,c.status,c.repo_specific,c.exposure_type,c.skill_body,r.use_when_json,r.avoid_when_json,r.failure_modes_json,r.invoke_first,d.schema_json,d.side_effect_level FROM compiled_skills c LEFT JOIN routing_rules r ON r.compiled_skill_id=c.id LEFT JOIN capability_defs d ON d.compiled_skill_id=c.id WHERE c.release_id=? AND c.path=? AND c.name=? ORDER BY c.id,r.id,d.id LIMIT 1`, turso.Text(prior), turso.Text(c.Skill.Path), turso.Text(c.Skill.Name)))
	if err != nil {
		return "", err
	}
	if len(r.Rows) == 0 {
		return "", nil
	}
	row := r.Rows[0]
	if len(row) != 12 {
		return "", errors.New("invalid prior metadata row")
	}
	c.Summary = value(row, 0)
	c.Risk = value(row, 1)
	c.Status = value(row, 2)
	c.RepoSpecific = value(row, 3) == "1"
	c.Exposure = value(row, 4)
	if c.Exposure == "guidance" {
		c.Exposure = "prompt"
	}
	for i, target := range map[int]*[]string{6: &c.UseWhen, 7: &c.AvoidWhen, 8: &c.FailureModes} {
		if row[i].Type != "null" {
			if err = json.Unmarshal([]byte(value(row, i)), target); err != nil {
				return "", errors.New("invalid prior routing metadata")
			}
		}
	}
	if row[9].Type != "null" {
		v := value(row, 9) == "1"
		c.InvokeFirst = &v
	}
	if row[10].Type != "null" {
		if err = json.Unmarshal([]byte(value(row, 10)), &c.Schema); err != nil {
			return "", errors.New("invalid prior capability metadata")
		}
		if c.Schema == nil {
			return "", errors.New("invalid prior capability metadata")
		}
		if c.Exposure == "resource" {
			c.Schema["uri"] = "ctx://skill/" + c.Skill.Name
		}
	}
	if row[11].Type != "null" {
		c.SideEffects = value(row, 11)
	}
	return compiler.BodyDiff(value(row, 5), c.Skill.Body), nil
}

// Publication statement templates are also the recovery allowlist.
const (
	publicationPackageSQL    = "INSERT INTO skill_packages(id,release_id,path,name,description,hash,validation_status,created_at) VALUES (?,?,?,?,?,?,?,?)"
	publicationCompiledSQL   = "INSERT INTO compiled_skills(id,release_id,skill_package_id,path,name,summary,skill_body,exposure_type,risk_level,repo_specific,status,yaml_frontmatter_present,canonical_schema_version,skill_id,kind,scope,activation_mode,enforcement,priority,version,source_checksum,source_policy_json,source_policy_base_checksum,source_policy_stale,canonical_json,clarification_json,clarification_required,created_at,body_diff_unified,body_diff_prior_release_id) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)"
	publicationRoutingSQL    = "INSERT INTO routing_rules(id,compiled_skill_id,use_when_json,avoid_when_json,failure_modes_json,invoke_first,created_at) VALUES (?,?,?,?,?,?,?)"
	publicationCapabilitySQL = "INSERT INTO capability_defs(id,compiled_skill_id,capability_name,type,schema_json,side_effect_level,created_at) VALUES (?,?,?,?,?,?,?)"
	publicationFileSQL       = "INSERT INTO skill_package_files(id,skill_package_id,path,content,content_type,byte_count,checksum,created_at) VALUES (?,?,?,?,?,?,?,?)"
	publicationValidationSQL = "INSERT INTO validation_reports(id,release_id,report_json,created_at) VALUES (?,?,?,?)"
	publicationReleaseSQL    = "UPDATE releases SET commit_sha=?,status=?,error_summary=?,skill_body_changes_count=? WHERE id=?"
	publicationActivateSQL   = "UPDATE projects SET active_release_id=? WHERE id=? AND COALESCE(active_release_id,'')=? AND EXISTS(SELECT 1 FROM sync_project_locks WHERE project_id=? AND job_id=? AND lease_until>?) RETURNING id"
	publicationRevisionSQL   = "INSERT INTO mcp_catalog_revisions(project_id,revision) SELECT ?,1 WHERE EXISTS(SELECT 1 FROM projects WHERE id=? AND active_release_id=?) ON CONFLICT(project_id) DO UPDATE SET revision=mcp_catalog_revisions.revision+1"
	publicationCompleteSQL   = "UPDATE sync_jobs SET status=?,error_code=?,updated_at=? WHERE id=? AND release_id=?"
	publicationUnlockSQL     = "DELETE FROM sync_project_locks WHERE project_id=? AND job_id=?"
)

func (s SQLStore) buildPublication(ctx context.Context, c Connection, job Job, revision string, skills []compiler.Compiled, report compiler.Report, now time.Time) ([][]turso.Statement, bool, error) {
	// Staged rows are unreachable until the final active_release_id transaction.
	// Batches stay below Hrana limits; a failed stage leaves a failed release only.
	batches := [][]turso.Statement{}
	statements := []turso.Statement{}
	flush := func() error {
		if len(statements) == 0 {
			return nil
		}
		batches = append(batches, append([]turso.Statement(nil), statements...))
		statements = nil
		return nil
	}
	allReady := report.Valid
	bodyChanges := 0
	for _, compiled := range skills {
		diff, err := s.carryForward(ctx, c.PriorReleaseID, &compiled)
		if err != nil {
			return nil, false, err
		}
		if diff != "" {
			bodyChanges++
		}
		packageID, err := identity.UUID()
		if err != nil {
			return nil, false, err
		}
		compiledID, err := identity.UUID()
		if err != nil {
			return nil, false, err
		}
		ruleID, err := identity.UUID()
		if err != nil {
			return nil, false, err
		}
		capID, err := identity.UUID()
		if err != nil {
			return nil, false, err
		}
		skill := compiled.Skill
		status := "valid"
		if !report.Valid {
			status = "invalid"
		}
		allReady = allReady && compiled.Status == "ready"
		canonical := compiled.Canonical
		activation, _ := canonical["activation"].(map[string]any)
		validation, _ := canonical["validation"].(map[string]any)
		priority, _ := canonical["priority"].(int)
		statements = append(statements,
			stmt(publicationPackageSQL, turso.Text(packageID), turso.Text(job.ReleaseID), turso.Text(skill.Path), turso.Text(skill.Name), nullableString(skill.Description), turso.Text(skill.Hash), turso.Text(status), turso.Integer(now.Unix())),
			stmt(publicationCompiledSQL, turso.Text(compiledID), turso.Text(job.ReleaseID), turso.Text(packageID), turso.Text(skill.Path), turso.Text(skill.Name), turso.Text(compiled.Summary), turso.Text(skill.Body), turso.Text(compiled.Exposure), turso.Text(compiled.Risk), flag(compiled.RepoSpecific), turso.Text(compiled.Status), flag(skill.Frontmatter), turso.Integer(1), turso.Text(skill.Name), turso.Text(fmt.Sprint(canonical["kind"])), turso.Text(fmt.Sprint(canonical["scope"])), turso.Text(fmt.Sprint(activation["mode"])), turso.Text(fmt.Sprint(canonical["enforcement"])), turso.Integer(int64(priority)), turso.Text(fmt.Sprint(canonical["version"])), turso.Text(skill.Hash), nullableString(compiled.SourcePolicyJSON), nullableString(compiled.SourcePolicyChecksum), flag(compiled.SourcePolicyStale), jsonValue(canonical), jsonValue(compiled.Clarifications), flag(validation["clarificationRequired"] == true), turso.Integer(now.Unix()), nullableString(diff), priorDiffRelease(diff, c.PriorReleaseID)),
			stmt(publicationRoutingSQL, turso.Text(ruleID), turso.Text(compiledID), jsonValue(compiled.UseWhen), jsonValue(compiled.AvoidWhen), jsonValue(compiled.FailureModes), boolValue(compiled.InvokeFirst), turso.Integer(now.Unix())),
			stmt(publicationCapabilitySQL, turso.Text(capID), turso.Text(compiledID), turso.Text(skill.Name), turso.Text(compiled.Exposure), jsonValue(compiled.Schema), turso.Text(compiled.SideEffects), turso.Integer(now.Unix())),
		)
		for _, file := range compiled.Files {
			id, err := identity.UUID()
			if err != nil {
				return nil, false, err
			}
			statements = append(statements, stmt(publicationFileSQL, turso.Text(id), turso.Text(packageID), turso.Text(file.Path), turso.Blob(file.Content), nullableString(file.ContentType), turso.Integer(int64(len(file.Content))), turso.Text(file.Checksum), turso.Integer(now.Unix())))
			// Each blob can approach 256 KiB; flush before serialized Hrana
			// requests can exceed its 4 MiB bound.
			if len(statements) >= 8 {
				if err = flush(); err != nil {
					return nil, false, err
				}
			}
		}

		if len(statements) >= 8 {
			if err = flush(); err != nil {
				return nil, false, err
			}
		}
	}
	if err := flush(); err != nil {
		return nil, false, err
	}
	reportID, err := identity.UUID()
	if err != nil {
		return nil, false, err
	}
	status := "ready"
	if !report.Valid {
		status = "failed"
	}
	statements = []turso.Statement{stmt(publicationValidationSQL, turso.Text(reportID), turso.Text(job.ReleaseID), jsonValue(report), turso.Integer(now.Unix())), stmt(publicationReleaseSQL, turso.Text(revision), turso.Text(status), errorSummary(report), turso.Integer(int64(bodyChanges)), turso.Text(job.ReleaseID))}
	if allReady {
		statements = append(statements, stmt(publicationActivateSQL, turso.Text(job.ReleaseID), turso.Text(c.ProjectID), turso.Text(c.PriorReleaseID), turso.Text(c.ProjectID), turso.Text(job.ID), turso.Integer(now.Unix())), stmt(publicationRevisionSQL, turso.Text(c.ProjectID), turso.Text(c.ProjectID), turso.Text(job.ReleaseID)))
	}
	statements = append(statements, stmt(publicationCompleteSQL, turso.Text(status), errorCode(report), turso.Integer(now.Unix()), turso.Text(job.ID), turso.Text(job.ReleaseID)), stmt(publicationUnlockSQL, turso.Text(c.ProjectID), turso.Text(job.ID)))
	batches = append(batches, statements)
	return batches, allReady, nil
}
func boolValue(b *bool) turso.Value {
	if b == nil {
		return turso.Null()
	}
	return flag(*b)
}
func errorSummary(r compiler.Report) turso.Value {
	if r.Valid {
		return turso.Null()
	}
	encoded, _ := json.Marshal(r.Errors)
	return turso.Text(string(encoded))
}
func errorCode(r compiler.Report) turso.Value {
	if r.Valid {
		return turso.Null()
	}
	return turso.Text("validation_failed")
}
func (s SQLStore) Fail(ctx context.Context, job Job, code string, now time.Time) error {
	_, err := s.DB.Batch(ctx, []turso.Statement{stmt("UPDATE sync_jobs SET status='failed',error_code=?,updated_at=? WHERE id=? AND release_id=?", turso.Text(code), turso.Integer(now.Unix()), turso.Text(job.ID), turso.Text(job.ReleaseID)), stmt("UPDATE releases SET status='failed',error_summary=? WHERE id=?", turso.Text(code), turso.Text(job.ReleaseID)), stmt("DELETE FROM sync_project_locks WHERE project_id=? AND job_id=?", turso.Text(job.ProjectID), turso.Text(job.ID))})
	return err
}

func priorDiffRelease(diff, prior string) turso.Value {
	if diff == "" {
		return turso.Null()
	}
	return turso.Text(prior)
}
