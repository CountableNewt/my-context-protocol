package domainrecords

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/CountableNewt/my-context-protocol/services/go/atproto"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
)

const MigrationSQL = `
CREATE TABLE domain_canonical_projects(project_id TEXT PRIMARY KEY,account_id TEXT NOT NULL,owner_did TEXT NOT NULL,manifest_uri TEXT NOT NULL,manifest_cid TEXT NOT NULL,review_digest TEXT NOT NULL,public_consent INTEGER NOT NULL CHECK(public_consent=1),telemetry_consent INTEGER NOT NULL CHECK(telemetry_consent IN(0,1)),target_active_release_id TEXT,hydrated INTEGER NOT NULL DEFAULT 0,native_projection INTEGER NOT NULL DEFAULT 0,updated_at INTEGER NOT NULL);
CREATE TABLE domain_projection_rows(project_id TEXT NOT NULL,generation TEXT NOT NULL,table_name TEXT NOT NULL,row_id TEXT NOT NULL,fields_json TEXT NOT NULL,PRIMARY KEY(project_id,generation,table_name,row_id));
CREATE TABLE domain_record_versions(owner_did TEXT NOT NULL,project_id TEXT NOT NULL,collection TEXT NOT NULL,rkey TEXT NOT NULL,uri TEXT NOT NULL,cid TEXT NOT NULL,generation TEXT NOT NULL,PRIMARY KEY(owner_did,collection,rkey));
CREATE TABLE domain_publication_items(run_id TEXT NOT NULL,kind TEXT NOT NULL,item_index INTEGER NOT NULL,uri TEXT NOT NULL,cid TEXT NOT NULL,PRIMARY KEY(run_id,kind,item_index));
CREATE TABLE domain_reconciliation_runs(project_id TEXT NOT NULL,manifest_cid TEXT NOT NULL,owner_did TEXT NOT NULL,shard_index INTEGER NOT NULL DEFAULT 0,entry_index INTEGER NOT NULL DEFAULT 0,PRIMARY KEY(project_id,manifest_cid));
CREATE TABLE domain_reconciliation_entries(project_id TEXT NOT NULL,manifest_cid TEXT NOT NULL,table_name TEXT NOT NULL,row_id TEXT NOT NULL,entry_json TEXT NOT NULL,PRIMARY KEY(project_id,manifest_cid,table_name,row_id));
CREATE TABLE domain_projection_guards(token TEXT PRIMARY KEY,valid INTEGER NOT NULL CHECK(valid=1));
CREATE TABLE domain_projection_stage_runs(project_id TEXT NOT NULL,generation TEXT NOT NULL,entries_done INTEGER NOT NULL DEFAULT 0,PRIMARY KEY(project_id,generation));
CREATE TABLE domain_publication_runs(run_id TEXT PRIMARY KEY,project_id TEXT NOT NULL,owner_did TEXT NOT NULL,digest TEXT NOT NULL,prior_uri TEXT NOT NULL,prior_cid TEXT NOT NULL,entries_done INTEGER NOT NULL DEFAULT 0,shards_done INTEGER NOT NULL DEFAULT 0,completed INTEGER NOT NULL DEFAULT 0 CHECK(completed IN(0,1)));
CREATE TABLE domain_record_retirements(owner_did TEXT NOT NULL,project_id TEXT NOT NULL,collection TEXT NOT NULL,rkey TEXT NOT NULL,uri TEXT NOT NULL,cid TEXT NOT NULL,retired_at INTEGER NOT NULL,deleting INTEGER NOT NULL DEFAULT 0 CHECK(deleting IN(0,1)),PRIMARY KEY(owner_did,collection,rkey));
CREATE INDEX domain_record_retirements_due ON domain_record_retirements(owner_did,project_id,retired_at,rkey);
CREATE TABLE domain_project_audits(project_id TEXT PRIMARY KEY,owner_did TEXT NOT NULL,manifest_cid TEXT NOT NULL,repo_head TEXT NOT NULL,cycle_id TEXT NOT NULL,status TEXT NOT NULL CHECK(status IN('pending','valid','invalid')),invalid INTEGER NOT NULL DEFAULT 0 CHECK(invalid IN(0,1)),error_code TEXT,checked_at INTEGER NOT NULL);
`

func (e Engine) state(ctx context.Context, account, project string) (Consent, atproto.Reference, error) {
	r, err := e.DB.Execute(ctx, statement("SELECT review_digest,public_consent,telemetry_consent,manifest_uri,manifest_cid FROM domain_canonical_projects WHERE project_id=? AND account_id=?", turso.Text(project), turso.Text(account)))
	if err != nil {
		return Consent{}, atproto.Reference{}, err
	}
	if len(r.Rows) == 0 {
		return Consent{}, atproto.Reference{}, nil
	}
	row := r.Rows[0]
	if len(row) != 5 {
		return Consent{}, atproto.Reference{}, errors.New("invalid canonical project state")
	}
	return Consent{fmt.Sprint(row[0].Value), fmt.Sprint(row[1].Value) == "1", fmt.Sprint(row[2].Value) == "1"}, atproto.Reference{URI: fmt.Sprint(row[3].Value), CID: fmt.Sprint(row[4].Value)}, nil
}

func (e Engine) project(ctx context.Context, account string, review Review, consent Consent, ref atproto.Reference) error {
	if err := e.owner(ctx, account, review.OwnerDID); err != nil {
		return err
	}
	if !consent.Public || review.HasTelemetry && !consent.Telemetry {
		return ErrConsent
	}
	native := []turso.Statement{}
	handled := false
	if e.NativeProjection != nil {
		var err error
		native, handled, err = e.NativeProjection(ctx, account, review, ref)
		if err != nil {
			return err
		}
	}
	if !handled {
		if err := e.stageProjection(ctx, review); err != nil {
			return err
		}
	}
	// One SQL transaction projects the whole generation using SELECT from staged
	// JSON. Its statement count is bounded by the registry, not record count.
	token := atproto.StableKey("projection", review.OwnerDID+review.ProjectID+review.Digest)
	batch := []turso.Statement{statement("INSERT INTO domain_projection_guards(token,valid) SELECT ?,CASE WHEN (NOT EXISTS(SELECT 1 FROM projects WHERE id=?) OR EXISTS(SELECT 1 FROM projects WHERE id=? AND account_id=?)) AND NOT EXISTS(SELECT 1 FROM domain_canonical_projects WHERE project_id=? AND (account_id<>? OR owner_did<>?)) THEN 1 ELSE 0 END", turso.Text(token), turso.Text(review.ProjectID), turso.Text(review.ProjectID), turso.Text(account), turso.Text(review.ProjectID), turso.Text(account), turso.Text(review.OwnerDID))}
	if handled {
		batch = append(batch, native...)
	} else {
		// Upsert parents before children, preserving every private column on updates.
		for _, d := range definitions {
			columns := []string{d.IDColumn()}
			expressions := []string{"row_id"}
			updates := []string{}
			if d.Table == "projects" {
				columns = append(columns, "account_id")
				expressions = append(expressions, "?")
			}
			if d.Selector == "project_id=?" {
				columns = append(columns, "project_id")
				expressions = append(expressions, "?")
			}
			for _, field := range d.Fields {
				columns = append(columns, field.Name)
				expression := "json_extract(fields_json,'$." + field.Name + "')"
				if field.Type == "decimal" {
					expression = "CAST(" + expression + " AS REAL)"
				}
				if d.Table == "projects" && field.Name == "active_release_id" {
					expression = "(SELECT active_release_id FROM projects WHERE id=json_extract(fields_json,'$.legacyId'))"
				}
				expressions = append(expressions, expression)
				updates = append(updates, field.Name+"=excluded."+field.Name)
				if d.Table == "compiled_skills" && field.Name == "canonical_json" {
					updates[len(updates)-1] = "canonical_json=CASE WHEN json_valid(compiled_skills.canonical_json) AND json_valid(excluded.canonical_json) AND COALESCE(compiled_skills.source_checksum,'')=COALESCE(excluded.source_checksum,'') AND json_extract(compiled_skills.canonical_json,'$.source')=json_extract(excluded.canonical_json,'$.source') AND compiled_skills.skill_body IS NOT NULL THEN json_set(excluded.canonical_json,'$.instructions',compiled_skills.skill_body) ELSE excluded.canonical_json END"
				}
			}
			args := []turso.Value{}
			if d.Table == "projects" {
				args = append(args, turso.Text(account))
			}
			if d.Selector == "project_id=?" {
				args = append(args, turso.Text(review.ProjectID))
			}
			args = append(args, turso.Text(review.ProjectID), turso.Text(review.Digest), turso.Text(d.Table))
			// Never update a row with a legacy ID belonging to another project. The
			// preflight below also rejects IDs colliding with rows outside this scope.
			batch = append(batch, statement("INSERT INTO domain_projection_guards(token,valid) SELECT ?||':'||?,CASE WHEN NOT EXISTS(SELECT 1 FROM "+d.Table+" t JOIN domain_projection_rows s ON s.row_id=t."+d.IDColumn()+" WHERE s.project_id=? AND s.generation=? AND s.table_name=? AND NOT(t."+d.Selector+")) THEN 1 ELSE 0 END", turso.Text(token), turso.Text(d.Table), turso.Text(review.ProjectID), turso.Text(review.Digest), turso.Text(d.Table), turso.Text(review.ProjectID)))
			batch = append(batch, statement("INSERT INTO "+d.Table+"("+strings.Join(columns, ",")+") SELECT "+strings.Join(expressions, ",")+" FROM domain_projection_rows WHERE project_id=? AND generation=? AND table_name=? ON CONFLICT("+d.IDColumn()+") DO UPDATE SET "+strings.Join(updates, ","), args...))
		}
		batch = append(batch, pruneStatements(review.ProjectID, review.Digest, true)...)

		batch = append(batch, statement("INSERT INTO mcp_catalog_revisions(project_id,revision) VALUES(?,1) ON CONFLICT(project_id) DO UPDATE SET revision=revision+1", turso.Text(review.ProjectID)))
	}
	telemetry := int64(0)
	if consent.Telemetry {
		telemetry = 1
	}
	batch = append(batch, statement("UPDATE domain_record_versions SET generation=? WHERE owner_did=? AND project_id=? AND uri IN(SELECT i.uri FROM domain_publication_items i JOIN domain_publication_runs r ON r.run_id=i.run_id WHERE r.project_id=? AND r.digest=?)", turso.Text(review.Digest), turso.Text(review.OwnerDID), turso.Text(review.ProjectID), turso.Text(review.ProjectID), turso.Text(review.Digest)))
	batch = append(batch,
		statement("UPDATE domain_publication_runs SET completed=1 WHERE project_id=? AND owner_did=? AND digest=?", turso.Text(review.ProjectID), turso.Text(review.OwnerDID), turso.Text(review.Digest)),
		statement("INSERT INTO domain_record_retirements(owner_did,project_id,collection,rkey,uri,cid,retired_at) SELECT owner_did,project_id,collection,rkey,uri,cid,? FROM domain_record_versions WHERE owner_did=? AND project_id=? AND generation<>? ON CONFLICT(owner_did,collection,rkey) DO NOTHING", turso.Integer(time.Now().Unix()), turso.Text(review.OwnerDID), turso.Text(review.ProjectID), turso.Text(review.Digest)),
		statement("DELETE FROM domain_record_retirements WHERE owner_did=? AND project_id=? AND uri IN(SELECT uri FROM domain_record_versions WHERE owner_did=? AND project_id=? AND generation=?)", turso.Text(review.OwnerDID), turso.Text(review.ProjectID), turso.Text(review.OwnerDID), turso.Text(review.ProjectID), turso.Text(review.Digest)),
	)
	active := turso.Null()
	for _, entry := range review.Entries {
		if entry.Table == "projects" {
			if v, ok := entry.Values["active_release_id"]; ok {
				active = v
			}
		}
	}
	batch = append(batch, statement("INSERT INTO domain_canonical_projects(project_id,account_id,owner_did,manifest_uri,manifest_cid,review_digest,public_consent,telemetry_consent,target_active_release_id,hydrated,native_projection,updated_at) VALUES(?,?,?,?,?,?,1,?,?,0,?,?) ON CONFLICT(project_id) DO UPDATE SET manifest_uri=excluded.manifest_uri,manifest_cid=excluded.manifest_cid,review_digest=excluded.review_digest,public_consent=1,telemetry_consent=excluded.telemetry_consent,target_active_release_id=excluded.target_active_release_id,hydrated=0,native_projection=excluded.native_projection,updated_at=excluded.updated_at", turso.Text(review.ProjectID), turso.Text(account), turso.Text(review.OwnerDID), turso.Text(ref.URI), turso.Text(ref.CID), turso.Text(review.Digest), turso.Integer(telemetry), active, turso.Integer(nativeFlag(handled)), turso.Integer(time.Now().Unix())), statement("DELETE FROM domain_projection_guards WHERE token=? OR token LIKE ?", turso.Text(token), turso.Text(token+":%")))
	if len(batch) > turso.MaxStatements {
		return errors.New("projection exceeds bounded transaction")
	}
	if _, err := e.DB.Batch(ctx, batch); err != nil {
		return err
	}
	return e.finishActivation(ctx, account, review.ProjectID, ref)
}

var ErrHydrationRequired = errors.New("canonical source bodies must be hydrated before release activation")

func (e Engine) finishActivation(ctx context.Context, account, project string, ref atproto.Reference) error {
	if e.Hydrate != nil {
		if err := e.Hydrate(ctx, account, project); err != nil {
			return err
		}
	}
	r, err := e.DB.Execute(ctx, statement("SELECT target_active_release_id,hydrated,review_digest,native_projection FROM domain_canonical_projects WHERE project_id=? AND account_id=? AND manifest_cid=?", turso.Text(project), turso.Text(account), turso.Text(ref.CID)))
	if err != nil {
		return err
	}
	if len(r.Rows) != 1 {
		return atproto.ErrConflict
	}
	if fmt.Sprint(r.Rows[0][1].Value) == "1" {
		return nil
	}
	active := r.Rows[0][0]
	if active.Type != "null" {
		check, err := e.DB.Execute(ctx, statement("SELECT COUNT(*) FROM compiled_skills WHERE release_id=? AND (skill_body IS NULL OR CASE WHEN json_valid(canonical_json) THEN json_type(canonical_json,'$.instructions') IS NULL OR json_extract(canonical_json,'$.instructions')<>skill_body ELSE 1 END)", active))
		if err != nil {
			return err
		}
		if fmt.Sprint(check.Rows[0][0].Value) != "0" {
			return ErrHydrationRequired
		}
	}
	batch := []turso.Statement{statement("INSERT INTO mcp_catalog_revisions(project_id,revision) SELECT ?,1 WHERE COALESCE((SELECT active_release_id FROM projects WHERE id=?),'')<>COALESCE(?,'') ON CONFLICT(project_id) DO UPDATE SET revision=revision+1", turso.Text(project), turso.Text(project), active), statement("UPDATE projects SET active_release_id=? WHERE id=? AND account_id=? AND EXISTS(SELECT 1 FROM domain_canonical_projects WHERE project_id=? AND manifest_cid=?)", active, turso.Text(project), turso.Text(account), turso.Text(project), turso.Text(ref.CID))}
	if fmt.Sprint(r.Rows[0][3].Value) == "0" {
		batch = append(batch, pruneStatements(project, fmt.Sprint(r.Rows[0][2].Value), false)...)
	}
	batch = append(batch, statement("UPDATE domain_canonical_projects SET hydrated=1 WHERE project_id=? AND account_id=? AND manifest_cid=?", turso.Text(project), turso.Text(account), turso.Text(ref.CID)))
	_, err = e.DB.Batch(ctx, batch)

	return err
}

func nativeFlag(v bool) int64 {
	if v {
		return 1
	}
	return 0
}
func pruneStatements(project, generation string, preserveActive bool) []turso.Statement {
	out := []turso.Statement{}
	for i := len(definitions) - 1; i >= 0; i-- {
		d := definitions[i]
		if d.Table == "projects" {
			continue
		}
		extra := ""
		args := []turso.Value{turso.Text(project)}
		if preserveActive {
			switch d.Table {
			case "releases":
				extra = " AND id<>COALESCE((SELECT active_release_id FROM projects WHERE id=?),'')"
			case "skill_packages", "compiled_skills", "validation_reports":
				extra = " AND release_id<>COALESCE((SELECT active_release_id FROM projects WHERE id=?),'')"
			case "capability_defs", "routing_rules":
				extra = " AND compiled_skill_id NOT IN(SELECT id FROM compiled_skills WHERE release_id=(SELECT active_release_id FROM projects WHERE id=?))"
			case "tools_index":
				extra = " AND skill_package_id NOT IN(SELECT id FROM skill_packages WHERE release_id=(SELECT active_release_id FROM projects WHERE id=?))"
			}
			if extra != "" {
				args = append(args, turso.Text(project))
			}
		}
		args = append(args, turso.Text(project), turso.Text(generation), turso.Text(d.Table))
		out = append(out, statement("DELETE FROM "+d.Table+" WHERE "+d.Selector+extra+" AND "+d.IDColumn()+" NOT IN(SELECT row_id FROM domain_projection_rows WHERE project_id=? AND generation=? AND table_name=?)", args...))
	}
	return out
}
