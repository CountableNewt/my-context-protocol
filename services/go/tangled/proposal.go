package tangled

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/bluesky-social/indigo/atproto/syntax"
)

type ProposalReference struct{ TicketURI, TicketCID, PatchURI, PatchCID, TicketChange, PatchChange string }
type PatchProposal struct {
	ActorDID, RepositoryDID, SourceRepositoryDID        string
	Base, Head, SourceBranch, TargetBranch, Title, Body string
}

// ProposalError retains an already-created ticket when submitting a patch fails.
// Callers persist this reference and resume with SubmitPatch rather than creating
// another externally visible ticket. No failed proposal is reported as success.
type ProposalError struct {
	TicketURI, TicketCID string
	Cause                error
}

func (e *ProposalError) Error() string {
	return "Tangled patch submission failed after ticket creation"
}
func (e *ProposalError) Unwrap() error { return e.Cause }

func (c Client) postRepo(ctx context.Context, actor string, repo Repository, method string, body any, out any) error {
	if c.Tokens == nil {
		return errors.New("Tangled service authorization is not configured")
	}
	if _, err := syntax.ParseDID(actor); err != nil {
		return ErrInvalidRepository
	}
	u, err := validateURL(repo.KnotURL, true)
	if err != nil {
		return err
	}
	u.Path = "/xrpc/" + method
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	if len(data) > 1<<20 {
		return errors.New("Tangled proposal exceeds byte limit")
	}
	token, err := c.Tokens.Token(ctx, actor, repo.DID.String(), method)
	if err != nil {
		return err
	}
	if token == "" || strings.ContainsAny(token, "\r\n ") {
		return errors.New("invalid Tangled service authorization")
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(data))
	if err != nil {
		return err
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+token)
	response, err := c.httpClient().Do(r)
	if err != nil {
		return err
	}
	return decodeResponse(response, method, out)
}

func validateProposal(p PatchProposal) error {
	for _, did := range []string{p.ActorDID, p.RepositoryDID, p.SourceRepositoryDID} {
		if _, err := syntax.ParseDID(did); err != nil {
			return ErrInvalidRepository
		}
	}
	if !oidPattern.MatchString(p.Base) || !oidPattern.MatchString(p.Head) || strings.EqualFold(p.Base, p.Head) {
		return ErrInvalidRevision
	}
	if p.Title == "" || len(p.Title) > 1024 || len(p.Body) > 262144 || p.TargetBranch == "" || len(p.TargetBranch) > 244 || len(p.SourceBranch) > 244 || strings.ContainsAny(p.TargetBranch+p.SourceBranch, "\x00\r\n") {
		return errors.New("invalid Tangled proposal metadata")
	}
	return nil
}

type recordReference struct {
	URI    string `json:"uri"`
	CID    string `json:"cid"`
	Change string `json:"x-tngl-change"`
}

func validReference(ref recordReference, repo, collection string) bool {
	uri, err := syntax.ParseATURI(ref.URI)
	if err != nil {
		return false
	}
	_, cidErr := syntax.ParseCID(ref.CID)
	return cidErr == nil && uri.Authority().String() == repo && uri.Collection().String() == collection && uri.RecordKey().String() != ""
}

// CreateProposal creates a native open ticket and submits a patch referencing an
// already-pushed source commit. The caller must finish and verify the Git write
// first; Tangled fetches the head from SourceRepositoryDID during submitPatch.
func (c Client) CreateProposal(ctx context.Context, p PatchProposal) (ProposalReference, error) {
	if err := validateProposal(p); err != nil {
		return ProposalReference{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	repo, err := c.ResolveRepository(ctx, p.RepositoryDID)
	if err != nil {
		return ProposalReference{}, err
	}
	ticket := map[string]any{"$type": "org.tangled.track.ticket", "title": map[string]any{"$type": "org.tangled.markup.markdown#inline", "text": p.Title}, "body": map[string]any{"$type": "org.tangled.markup.markdown", "text": p.Body}, "state": "open", "targetBranch": p.TargetBranch, "createdAt": time.Now().UTC().Format(time.RFC3339Nano)}
	var created recordReference
	err = c.postRepo(ctx, p.ActorDID, repo, "com.atproto.repo.createRecord", map[string]any{"repo": p.RepositoryDID, "collection": "org.tangled.track.ticket", "record": ticket}, &created)
	if err != nil {
		return ProposalReference{}, err
	}
	if !validReference(created, p.RepositoryDID, "org.tangled.track.ticket") {
		return ProposalReference{}, errors.New("invalid Tangled ticket reference")
	}
	result := ProposalReference{TicketURI: created.URI, TicketCID: created.CID, TicketChange: created.Change}
	patch, err := c.SubmitPatch(ctx, p, created.URI)
	if err != nil {
		return result, &ProposalError{created.URI, created.CID, err}
	}
	result.PatchURI = patch.PatchURI
	result.PatchCID = patch.PatchCID
	result.PatchChange = patch.PatchChange
	return result, nil
}

func (c Client) SubmitPatch(ctx context.Context, p PatchProposal, ticketURI string) (ProposalReference, error) {
	if err := validateProposal(p); err != nil {
		return ProposalReference{}, err
	}
	uri, err := syntax.ParseATURI(ticketURI)
	if err != nil || uri.Authority().String() != p.RepositoryDID || uri.Collection().String() != "org.tangled.track.ticket" || uri.RecordKey().String() == "" {
		return ProposalReference{}, errors.New("invalid Tangled ticket")
	}
	repo, err := c.ResolveRepository(ctx, p.RepositoryDID)
	if err != nil {
		return ProposalReference{}, err
	}
	patch := map[string]any{"ticket": uri.RecordKey().String(), "base": map[string]string{"oid": strings.ToLower(p.Base)}, "head": map[string]string{"oid": strings.ToLower(p.Head)}, "sourceRepo": p.SourceRepositoryDID}
	if p.SourceBranch != "" {
		patch["sourceBranch"] = p.SourceBranch
	}
	var result recordReference
	if err = c.postRepo(ctx, p.ActorDID, repo, "org.tangled.review.submitPatch", map[string]any{"repo": p.RepositoryDID, "patch": patch}, &result); err != nil {
		return ProposalReference{}, err
	}
	if !validReference(result, p.RepositoryDID, "org.tangled.review.patch") {
		return ProposalReference{}, errors.New("invalid Tangled patch reference")
	}
	return ProposalReference{TicketURI: ticketURI, PatchURI: result.URI, PatchCID: result.CID, PatchChange: result.Change}, nil
}

// Attestor publishes org.tangled.graph.attestation records on the actor's PDS.
// Its implementation owns stable TID keys, durable retry intents and OAuth.
type Attestor interface {
	Attest(context.Context, string, string, string, string) error
}

// AttestProposal ties knot-authored records back to their authenticated actor.
// This is a separate resumable step: callers retain the proposal references if
// the PDS is unavailable and do not recreate the already-written knot records.
func AttestProposal(ctx context.Context, writer Attestor, actor string, ref ProposalReference) error {
	if writer == nil {
		return errors.New("Tangled author attestation is not configured")
	}
	if _, err := syntax.ParseDID(actor); err != nil {
		return ErrInvalidRepository
	}
	subjects := []struct{ uri, cid, change string }{{ref.TicketURI, ref.TicketCID, ref.TicketChange}, {ref.PatchURI, ref.PatchCID, ref.PatchChange}}
	for _, subject := range subjects {
		if _, err := syntax.ParseATURI(subject.uri); err != nil {
			return errors.New("invalid Tangled attestation subject")
		}
		if _, err := syntax.ParseCID(subject.cid); err != nil {
			return errors.New("invalid Tangled attestation CID")
		}
		if subject.change != "" && !oidPattern.MatchString(subject.change) {
			return ErrInvalidRevision
		}
	}
	for _, subject := range subjects {
		if err := writer.Attest(ctx, actor, subject.uri, subject.cid, subject.change); err != nil {
			return err
		}
	}
	return nil
}
