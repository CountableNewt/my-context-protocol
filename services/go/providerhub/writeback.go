package providerhub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/CountableNewt/my-context-protocol/services/go/identity"
	"github.com/CountableNewt/my-context-protocol/services/go/skillpolicy"
	"github.com/CountableNewt/my-context-protocol/services/go/tangled"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
	"github.com/bluesky-social/indigo/atproto/syntax"
)

type proposalState struct {
	ID, Actor, Repo, Branch, BaseBranch, Base, Head, Status string
	When                                                    time.Time
	Ref                                                     tangled.ProposalReference
}

func (h *Hub) Writeback(ctx context.Context, account, project, release, compiled string) (map[string]any, error) {
	if err := h.owned(ctx, account, project); err != nil {
		return nil, err
	}
	connections, err := h.query(ctx, "SELECT id,provider,repo_owner,repo_name FROM repo_connections WHERE project_id=? ORDER BY id", turso.Text(project))
	if err != nil {
		return nil, err
	}
	if len(connections) != 1 {
		return nil, problem(409, "A single repository connection is required")
	}
	connection := connections[0]
	provider := scalar(connection, 1)
	if provider == "" || provider == "github" {
		if h.cfg.GitHubWriteback == nil {
			return nil, problem(501, "GitHub metadata writeback is not configured")
		}
		return h.cfg.GitHubWriteback(ctx, account, project, release, compiled)
	}
	if provider != "tangled" || h.cfg.Tangled == nil {
		return nil, problem(501, "Repository provider is not configured")
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	unlock, err := h.lock(ctx, project)
	if err != nil {
		return nil, err
	}
	defer unlock()
	binding, err := h.ResolveTangledConnection(ctx, scalar(connection, 0))
	if err != nil {
		return nil, err
	}
	// Establish the PDS attestor before creating any external branch or ticket.
	if h.cfg.Attestor == nil {
		return nil, problem(409, "ATProto author attestation is not configured")
	}
	attestor, err := h.cfg.Attestor(ctx, account)
	if err != nil {
		return nil, err
	}
	if attestor == nil {
		return nil, problem(409, "ATProto author attestation is not configured")
	}
	rows, err := h.query(ctx, "SELECT c.canonical_json,c.exposure_type,r.commit_sha FROM compiled_skills c JOIN releases r ON r.id=c.release_id WHERE c.id=? AND c.release_id=? AND r.project_id=?", turso.Text(compiled), turso.Text(release), turso.Text(project))
	if err != nil {
		return nil, err
	}
	if len(rows) != 1 {
		return nil, problem(404, "Compiled skill not found")
	}
	var document map[string]any
	if json.Unmarshal([]byte(scalar(rows[0], 0)), &document) != nil || document == nil {
		return nil, problem(409, "Canonical skill metadata is unavailable")
	}
	exposure := scalar(rows[0], 1)
	if _, err = skillpolicy.MergeSidecar(nil, document, exposure); err != nil {
		return nil, problem(409, "Resolve canonical metadata validation before writeback")
	}
	state, err := h.reserveProposal(ctx, project, scalar(connection, 0), release, compiled, binding, document, exposure, scalar(rows[0], 2))
	if err != nil {
		return nil, err
	}
	if state.Actor != binding.ActorDID || state.Repo != binding.RepositoryDID || state.BaseBranch != binding.Branch {
		return nil, problem(409, "Repository identity changed since the metadata operation began")
	}
	if state.Status == "complete" {
		return proposalResult(state, scalar(connection, 2), scalar(connection, 3)), nil
	}
	if state.Status == "proposal_started" || state.Status == "patch_started" {
		return nil, problem(409, "The prior Tangled proposal outcome is uncertain; reconcile its external records before retrying")
	}
	if state.Status == "reserved" {
		branch, err := h.cfg.Tangled.WriteAuthenticatedMetadataBranch(ctx, tangled.MetadataChange{ActorDID: state.Actor, RepositoryDID: state.Repo, BaseBranch: state.BaseBranch, Branch: state.Branch, ExpectedBase: state.Base, CommitTime: state.When, Document: document, Exposure: exposure})
		if err != nil {
			return nil, err
		}
		if branch.Base != state.Base || branch.Branch != state.Branch || branch.SourceRepositoryDID != state.Repo || branch.Head == "" {
			return nil, errors.New("Tangled metadata branch identity was not confirmed")
		}
		state.Head = branch.Head
		state.Status = "git_complete"
		if err = h.saveProposal(ctx, state); err != nil {
			return nil, err
		}
	}
	proposal := tangled.PatchProposal{ActorDID: state.Actor, RepositoryDID: state.Repo, SourceRepositoryDID: state.Repo, Base: state.Base, Head: state.Head, SourceBranch: state.Branch, TargetBranch: state.BaseBranch, Title: "Update skill runtime metadata", Body: "Apply checksum-bound skill runtime policy.\n\nMyContextProtocol operation: " + state.ID}
	if state.Status == "git_complete" {
		state.Status = "proposal_started"
		if err = h.saveProposal(ctx, state); err != nil {
			return nil, err
		}
		ref, writeErr := h.cfg.Tangled.CreateProposal(ctx, proposal)
		if ref.TicketURI != "" {
			state.Ref = ref
			if ref.PatchURI != "" {
				state.Status = "patch_complete"
			} else {
				state.Status = "patch_started"
				var status *tangled.StatusError
				if errors.As(writeErr, &status) && status.Status >= 400 && status.Status < 500 {
					state.Status = "ticket_created"
				}
			}
			if err = h.saveProposal(ctx, state); err != nil {
				return nil, err
			}
		}
		if writeErr != nil {
			return nil, writeErr
		}
		if state.Status != "patch_complete" {
			return nil, errors.New("Tangled proposal references were not confirmed")
		}
	}
	if state.Status == "ticket_created" {
		state.Status = "patch_started"
		if err = h.saveProposal(ctx, state); err != nil {
			return nil, err
		}
		ref, writeErr := h.cfg.Tangled.SubmitPatch(ctx, proposal, state.Ref.TicketURI)
		if writeErr != nil {
			return nil, writeErr
		}
		state.Ref.PatchURI = ref.PatchURI
		state.Ref.PatchCID = ref.PatchCID
		state.Ref.PatchChange = ref.PatchChange
		state.Status = "patch_complete"
		if err = h.saveProposal(ctx, state); err != nil {
			return nil, err
		}
	}
	if state.Status == "patch_complete" {
		if err = tangled.AttestProposal(ctx, attestor, state.Actor, state.Ref); err != nil {
			return nil, err
		}
		state.Status = "complete"
		if err = h.saveProposal(ctx, state); err != nil {
			return nil, err
		}
	}
	if state.Status != "complete" {
		return nil, errors.New("invalid Tangled metadata operation state")
	}
	result := proposalResult(state, scalar(connection, 2), scalar(connection, 3))
	_, err = h.cfg.DB.Execute(ctx, statement("UPDATE skill_runtime_overrides SET writeback_pr_url=? WHERE project_id=? AND skill_id=? AND (repo_connection_id IS NULL OR repo_connection_id=?)", turso.Text(result["pull_request_url"].(string)), turso.Text(project), turso.Text(fmtString(document["id"])), turso.Text(scalar(connection, 0))))
	return result, err
}
func fmtString(value any) string {
	if s, ok := value.(string); ok {
		return s
	}
	return ""
}

func (h *Hub) reserveProposal(ctx context.Context, project, connection, release, compiled string, binding tangled.Binding, document map[string]any, exposure, base string) (proposalState, error) {
	data, _ := json.Marshal(map[string]any{"document": document, "exposure": exposure})
	hash := sha256.Sum256(data)
	digest := hex.EncodeToString(hash[:])
	id, err := identity.UUID()
	if err != nil {
		return proposalState{}, err
	}
	now := h.now().UTC().Truncate(time.Second)
	branch := "mycontext/metadata/" + strings.ToLower(id)
	_, err = h.cfg.DB.Execute(ctx, statement("INSERT INTO atproto_metadata_proposals(id,project_id,connection_id,release_id,compiled_id,document_hash,actor_did,repository_did,branch,base_branch,base_revision,commit_time,status,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,'reserved',?) ON CONFLICT(project_id,release_id,compiled_id,document_hash) DO NOTHING", turso.Text(id), turso.Text(project), turso.Text(connection), turso.Text(release), turso.Text(compiled), turso.Text(digest), turso.Text(binding.ActorDID), turso.Text(binding.RepositoryDID), turso.Text(branch), turso.Text(binding.Branch), turso.Text(base), turso.Integer(now.Unix()), turso.Integer(now.Unix())))
	if err != nil {
		return proposalState{}, err
	}
	rows, err := h.query(ctx, "SELECT id,actor_did,repository_did,branch,base_branch,base_revision,head_revision,status,commit_time,ticket_uri,ticket_cid,ticket_change,patch_uri,patch_cid,patch_change FROM atproto_metadata_proposals WHERE project_id=? AND release_id=? AND compiled_id=? AND document_hash=?", turso.Text(project), turso.Text(release), turso.Text(compiled), turso.Text(digest))
	if err != nil {
		return proposalState{}, err
	}
	if len(rows) != 1 || len(rows[0]) != 15 {
		return proposalState{}, errors.New("invalid durable Tangled proposal row")
	}
	r := rows[0]
	when, err := strconv.ParseInt(scalar(r, 8), 10, 64)
	if err != nil {
		return proposalState{}, err
	}
	return proposalState{ID: scalar(r, 0), Actor: scalar(r, 1), Repo: scalar(r, 2), Branch: scalar(r, 3), BaseBranch: scalar(r, 4), Base: scalar(r, 5), Head: scalar(r, 6), Status: scalar(r, 7), When: time.Unix(when, 0), Ref: tangled.ProposalReference{TicketURI: scalar(r, 9), TicketCID: scalar(r, 10), TicketChange: scalar(r, 11), PatchURI: scalar(r, 12), PatchCID: scalar(r, 13), PatchChange: scalar(r, 14)}}, nil
}
func (h *Hub) saveProposal(ctx context.Context, s proposalState) error {
	rows, err := h.query(ctx, "UPDATE atproto_metadata_proposals SET head_revision=?,ticket_uri=?,ticket_cid=?,ticket_change=?,patch_uri=?,patch_cid=?,patch_change=?,status=?,updated_at=? WHERE id=? AND actor_did=? AND repository_did=? RETURNING id", nullable(s.Head), nullable(s.Ref.TicketURI), nullable(s.Ref.TicketCID), nullable(s.Ref.TicketChange), nullable(s.Ref.PatchURI), nullable(s.Ref.PatchCID), nullable(s.Ref.PatchChange), turso.Text(s.Status), turso.Integer(h.now().Unix()), turso.Text(s.ID), turso.Text(s.Actor), turso.Text(s.Repo))
	if err != nil {
		return err
	}
	if len(rows) != 1 {
		return errors.New("Tangled operation checkpoint was not confirmed")
	}
	return nil
}
func nullable(value string) turso.Value {
	if value == "" {
		return turso.Null()
	}
	return turso.Text(value)
}
func proposalResult(s proposalState, owner, name string) map[string]any {
	uri, _ := syntax.ParseATURI(s.Ref.TicketURI)
	return map[string]any{"provider": "tangled", "operation_id": s.ID, "status": s.Status, "branch": s.Branch, "source_path": ".mycontext/skills.yaml", "pull_request_url": "https://tangled.org/" + url.PathEscape(owner) + "/" + url.PathEscape(name) + "/pulls/" + url.PathEscape(uri.RecordKey().String()), "ticket_uri": s.Ref.TicketURI, "patch_uri": s.Ref.PatchURI}
}
