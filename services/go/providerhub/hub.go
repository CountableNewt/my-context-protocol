// Package providerhub dispatches account-owned repository operations without
// sharing provider credentials or interpreting mutable names as native IDs.
package providerhub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/CountableNewt/my-context-protocol/services/go/identity"
	"github.com/CountableNewt/my-context-protocol/services/go/reposync"
	"github.com/CountableNewt/my-context-protocol/services/go/syncservice"
	"github.com/CountableNewt/my-context-protocol/services/go/tangled"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
	"github.com/bluesky-social/indigo/atproto/syntax"
)

const MigrationSQL = `
CREATE TABLE atproto_repo_bindings(connection_id UUID PRIMARY KEY REFERENCES repo_connections(id) ON DELETE CASCADE,repository_did TEXT NOT NULL,owner_did TEXT NOT NULL,actor_did TEXT NOT NULL,is_private INTEGER NOT NULL CHECK(is_private IN (0,1)),branch TEXT NOT NULL,last_revision TEXT,last_checked_at INTEGER NOT NULL DEFAULT 0,next_check_at INTEGER NOT NULL DEFAULT 0);
CREATE INDEX atproto_repo_bindings_due ON atproto_repo_bindings(next_check_at,connection_id);
CREATE TABLE provider_operation_locks(project_id UUID PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,operation_id TEXT NOT NULL,lease_until INTEGER NOT NULL);
CREATE TABLE atproto_metadata_proposals(id TEXT PRIMARY KEY,project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,connection_id UUID NOT NULL REFERENCES repo_connections(id) ON DELETE CASCADE,release_id UUID NOT NULL REFERENCES releases(id) ON DELETE CASCADE,compiled_id UUID NOT NULL REFERENCES compiled_skills(id) ON DELETE CASCADE,document_hash TEXT NOT NULL,actor_did TEXT NOT NULL,repository_did TEXT NOT NULL,branch TEXT NOT NULL,base_branch TEXT NOT NULL,base_revision TEXT NOT NULL,commit_time INTEGER NOT NULL,head_revision TEXT,ticket_uri TEXT,ticket_cid TEXT,ticket_change TEXT,patch_uri TEXT,patch_cid TEXT,patch_change TEXT,status TEXT NOT NULL CHECK(status IN ('reserved','git_complete','proposal_started','ticket_created','patch_started','patch_complete','complete')),updated_at INTEGER NOT NULL,UNIQUE(project_id,release_id,compiled_id,document_hash));
`

type Database interface {
	Execute(context.Context, turso.Statement) (*turso.Result, error)
	Batch(context.Context, []turso.Statement) ([]turso.Result, error)
}
type TangledAPI interface {
	LookupRepository(context.Context, string, string, string) (tangled.RepositoryView, error)
	ListRepositories(context.Context, string, string, string, int) (tangled.RepositoryPage, error)
	Snapshot(context.Context, string, string) (reposync.Outcome, error)
	AuthenticatedSnapshot(context.Context, string, string, string) (reposync.Outcome, error)
	ResolveBranch(context.Context, string, string) (string, error)
	ResolveAuthenticatedBranch(context.Context, string, string, string) (string, error)
	WriteMetadataBranch(context.Context, tangled.MetadataChange) (tangled.MetadataBranch, error)
	WriteAuthenticatedMetadataBranch(context.Context, tangled.MetadataChange) (tangled.MetadataBranch, error)
	CreateProposal(context.Context, tangled.PatchProposal) (tangled.ProposalReference, error)
	SubmitPatch(context.Context, tangled.PatchProposal, string) (tangled.ProposalReference, error)
}
type Config struct {
	DB              Database
	Tangled         TangledAPI
	Identity        func(context.Context, string) (string, error)
	Attestor        func(context.Context, string) (tangled.Attestor, error)
	GitHubConnect   func(context.Context, string, string, map[string]json.RawMessage) (map[string]any, error)
	GitHubWriteback func(context.Context, string, string, string, string) (map[string]any, error)
	Sync            func(context.Context, string) (map[string]any, error)
	Now             func() time.Time
}
type Hub struct{ cfg Config }
type PublicError struct {
	Status  int
	Message string
}

func (e *PublicError) Error() string           { return e.Message }
func (e *PublicError) HTTPStatus() int         { return e.Status }
func problem(status int, message string) error { return &PublicError{status, message} }
func New(cfg Config) (*Hub, error) {
	if cfg.DB == nil {
		return nil, errors.New("provider database is required")
	}
	return &Hub{cfg}, nil
}
func (h *Hub) now() time.Time {
	if h.cfg.Now != nil {
		return h.cfg.Now()
	}
	return time.Now().UTC()
}
func statement(sql string, args ...turso.Value) turso.Statement {
	return turso.Statement{SQL: sql, Args: args, WantRows: true}
}
func scalar(row []turso.Value, index int) string {
	if index >= len(row) || row[index].Type == "null" {
		return ""
	}
	return fmt.Sprint(row[index].Value)
}
func (h *Hub) query(ctx context.Context, sql string, args ...turso.Value) ([][]turso.Value, error) {
	r, err := h.cfg.DB.Execute(ctx, statement(sql, args...))
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, errors.New("invalid provider database response")
	}
	return r.Rows, nil
}
func (h *Hub) owned(ctx context.Context, account, project string) error {
	rows, err := h.query(ctx, "SELECT id FROM projects WHERE id=? AND account_id=?", turso.Text(project), turso.Text(account))
	if err != nil {
		return err
	}
	if len(rows) != 1 {
		return problem(404, "Project not found")
	}
	return nil
}
func (h *Hub) actor(ctx context.Context, account string) (string, error) {
	if h.cfg.Identity == nil {
		return "", problem(409, "Link an ATProto account before using Tangled")
	}
	did, err := h.cfg.Identity(ctx, account)
	if err != nil {
		return "", err
	}
	if _, err = syntax.ParseDID(did); err != nil {
		return "", problem(409, "Link an ATProto account before using Tangled")
	}
	return did, nil
}
func (h *Hub) lock(ctx context.Context, project string) (func(), error) {
	id, err := identity.UUID()
	if err != nil {
		return nil, err
	}
	now := h.now().Unix()
	rows, err := h.query(ctx, "INSERT INTO provider_operation_locks(project_id,operation_id,lease_until) VALUES(?,?,?) ON CONFLICT(project_id) DO UPDATE SET operation_id=excluded.operation_id,lease_until=excluded.lease_until WHERE provider_operation_locks.lease_until<=? RETURNING operation_id", turso.Text(project), turso.Text(id), turso.Integer(now+120), turso.Integer(now))
	if err != nil {
		return nil, err
	}
	if len(rows) != 1 {
		return nil, problem(409, "A repository operation is already running")
	}
	return func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		h.cfg.DB.Execute(cleanup, statement("DELETE FROM provider_operation_locks WHERE project_id=? AND operation_id=?", turso.Text(project), turso.Text(id)))
	}, nil
}

func (h *Hub) ConnectRepository(ctx context.Context, account, project string, input map[string]json.RawMessage) (map[string]any, error) {
	if err := h.owned(ctx, account, project); err != nil {
		return nil, err
	}
	provider := "github"
	if raw, ok := input["provider"]; ok {
		if json.Unmarshal(raw, &provider) != nil {
			return nil, problem(400, "Invalid repository provider")
		}
	}
	if provider == "github" {
		if h.cfg.GitHubConnect == nil {
			return nil, problem(501, "GitHub repository integration is not configured")
		}
		return h.cfg.GitHubConnect(ctx, account, project, input)
	}
	if provider != "tangled" {
		return nil, problem(400, "Unsupported repository provider")
	}
	if h.cfg.Tangled == nil {
		return nil, problem(501, "Tangled repository integration is not configured")
	}
	ctx, cancel := context.WithTimeout(ctx, 80*time.Second)
	defer cancel()
	actor, err := h.actor(ctx, account)
	if err != nil {
		return nil, err
	}
	var owner, name, branch string
	private := false
	for key, target := range map[string]*string{"owner": &owner, "repo": &name, "branch": &branch} {
		if raw, ok := input[key]; ok && json.Unmarshal(raw, target) != nil {
			return nil, problem(400, "Invalid Tangled repository fields")
		}
	}
	if raw, ok := input["private"]; ok && json.Unmarshal(raw, &private) != nil {
		return nil, problem(400, "Invalid Tangled repository visibility")
	}
	if owner == "" {
		owner = actor
	}
	unlock, err := h.lock(ctx, project)
	if err != nil {
		return nil, err
	}
	defer unlock()
	current, err := h.query(ctx, "SELECT id,provider FROM repo_connections WHERE project_id=? ORDER BY id", turso.Text(project))
	if err != nil {
		return nil, err
	}
	if len(current) > 1 || len(current) == 1 && scalar(current[0], 1) != "tangled" {
		return nil, problem(409, "Project is connected to another repository provider")
	}
	view, err := h.cfg.Tangled.LookupRepository(ctx, actor, owner, name)
	if err != nil {
		return nil, problem(409, "Tangled repository lookup or access verification failed")
	}
	if view.OwnerDID != owner || view.RepositoryDID == "" {
		return nil, problem(409, "Tangled repository identity did not match")
	}
	if !private {
		private = true
		if verifier, ok := h.cfg.Tangled.(interface {
			IsPublicRepository(context.Context, string, string, string) (bool, error)
		}); ok {
			public, checkErr := verifier.IsPublicRepository(ctx, owner, name, view.RepositoryDID)
			private = checkErr != nil || !public
		}
	}
	if branch == "" {
		branch = view.DefaultBranch
	}
	if branch == "" || len(branch) > 244 || strings.ContainsAny(branch, "\x00\r\n") {
		return nil, problem(400, "Invalid Tangled branch")
	}
	// Always probe through Bobbin with the actor, including public projects, so
	// a private repository can never be connected on anonymous reachability.
	head, err := h.cfg.Tangled.ResolveAuthenticatedBranch(ctx, actor, view.RepositoryDID, branch)
	if err != nil {
		return nil, problem(409, "Tangled branch access could not be verified")
	}
	id, err := identity.UUID()
	if err != nil {
		return nil, err
	}
	if len(current) == 1 {
		id = scalar(current[0], 0)
	}
	flag := int64(0)
	if private {
		flag = 1
	}
	_, err = h.cfg.DB.Batch(ctx, []turso.Statement{
		statement("INSERT INTO repo_connections(id,project_id,provider,repo_owner,repo_name,default_branch,auth_type,created_at) VALUES(?,?,'tangled',?,?,?,'atproto',?) ON CONFLICT(id) DO UPDATE SET repo_owner=excluded.repo_owner,repo_name=excluded.repo_name,default_branch=excluded.default_branch WHERE repo_connections.provider='tangled' AND repo_connections.project_id=excluded.project_id", turso.Text(id), turso.Text(project), turso.Text(owner), turso.Text(view.Name), turso.Text(branch), turso.Integer(h.now().Unix())),
		statement("INSERT INTO atproto_repo_bindings(connection_id,repository_did,owner_did,actor_did,is_private,branch,last_revision,last_checked_at,next_check_at) VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(connection_id) DO UPDATE SET repository_did=excluded.repository_did,owner_did=excluded.owner_did,actor_did=excluded.actor_did,is_private=excluded.is_private,branch=excluded.branch,last_revision=excluded.last_revision,last_checked_at=excluded.last_checked_at,next_check_at=excluded.next_check_at", turso.Text(id), turso.Text(view.RepositoryDID), turso.Text(owner), turso.Text(actor), turso.Integer(flag), turso.Text(branch), turso.Text(head), turso.Integer(h.now().Unix()), turso.Integer(h.now().Add(5*time.Minute).Unix())),
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"provider": "tangled", "project_id": project, "repo_owner": owner, "repo_name": view.Name, "repository_did": view.RepositoryDID, "default_branch": branch, "auth_type": "atproto", "private": private}, nil
}

func (h *Hub) ResolveTangledConnection(ctx context.Context, id string) (tangled.Binding, error) {
	rows, err := h.query(ctx, "SELECT b.repository_did,b.owner_did,b.actor_did,b.branch,b.is_private,p.account_id FROM atproto_repo_bindings b JOIN repo_connections r ON r.id=b.connection_id JOIN projects p ON p.id=r.project_id WHERE b.connection_id=? AND r.provider='tangled'", turso.Text(id))
	if err != nil {
		return tangled.Binding{}, err
	}
	if len(rows) != 1 {
		return tangled.Binding{}, problem(404, "Tangled repository connection not found")
	}
	row := rows[0]
	actor, err := h.actor(ctx, scalar(row, 5))
	if err != nil {
		return tangled.Binding{}, err
	}
	if actor != scalar(row, 2) {
		return tangled.Binding{}, problem(409, "Tangled connection identity must be reauthorized")
	}
	return tangled.Binding{RepositoryDID: scalar(row, 0), OwnerDID: scalar(row, 1), ActorDID: actor, Branch: scalar(row, 3), Private: scalar(row, 4) == "1"}, nil
}
func (h *Hub) Provider() syncservice.RepositoryProvider { return h }
func (h *Hub) FetchRepository(ctx context.Context, c syncservice.Connection) (reposync.Outcome, error) {
	if c.Provider != "tangled" || h.cfg.Tangled == nil {
		return reposync.Outcome{}, syncservice.ErrProviderUnsupported
	}
	b, err := h.ResolveTangledConnection(ctx, c.ID)
	if err != nil {
		return reposync.Outcome{}, err
	}
	// Actor-authenticated Bobbin reads are used for every Tangled binding. A
	// caller-supplied visibility flag must never choose an anonymous bypass.
	return h.cfg.Tangled.AuthenticatedSnapshot(ctx, b.ActorDID, b.RepositoryDID, b.Branch)
}
func (h *Hub) ResolveRepositoryCommit(ctx context.Context, c syncservice.Connection) (string, error) {
	if c.Provider != "tangled" || h.cfg.Tangled == nil {
		return "", syncservice.ErrProviderUnsupported
	}
	b, err := h.ResolveTangledConnection(ctx, c.ID)
	if err != nil {
		return "", err
	}
	return h.cfg.Tangled.ResolveAuthenticatedBranch(ctx, b.ActorDID, b.RepositoryDID, b.Branch)
}
func (h *Hub) ListRepositories(ctx context.Context, account string) (map[string]any, error) {
	if h.cfg.Tangled == nil {
		return nil, problem(501, "Tangled repository integration is not configured")
	}
	actor, err := h.actor(ctx, account)
	if err != nil {
		return nil, err
	}
	page, err := h.cfg.Tangled.ListRepositories(ctx, actor, actor, "", 100)
	if err != nil {
		return nil, err
	}
	return map[string]any{"provider": "tangled", "repositories": page.Repositories, "cursor": page.Cursor, "private_repository_selection": "owner_and_name"}, nil
}
