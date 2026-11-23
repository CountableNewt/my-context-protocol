package tangled

import (
	"context"
	"errors"
	"github.com/CountableNewt/my-context-protocol/services/go/reposync"
	"github.com/CountableNewt/my-context-protocol/services/go/syncservice"
	"github.com/bluesky-social/indigo/atproto/syntax"
)

type Binding struct {
	RepositoryDID, OwnerDID, Branch, ActorDID string
	Private                                   bool
}
type ConnectionResolver interface {
	ResolveTangledConnection(context.Context, string) (Binding, error)
}
type Provider struct {
	Client      Client
	Connections ConnectionResolver
}

var _ syncservice.RepositoryProvider = Provider{}
var _ syncservice.RepositoryCommitResolver = Provider{}

func (p Provider) binding(ctx context.Context, c syncservice.Connection) (Binding, error) {
	if c.Provider != "tangled" || c.ID == "" || p.Connections == nil {
		return Binding{}, ErrInvalidRepository
	}
	b, err := p.Connections.ResolveTangledConnection(ctx, c.ID)
	if err != nil {
		return Binding{}, err
	}
	for _, did := range []string{b.RepositoryDID, b.OwnerDID, b.ActorDID} {
		if _, err := syntax.ParseDID(did); err != nil {
			return Binding{}, ErrInvalidRepository
		}
	}
	if b.Branch == "" {
		return Binding{}, ErrInvalidRevision
	}
	return b, nil
}
func (p Provider) FetchRepository(ctx context.Context, c syncservice.Connection) (reposync.Outcome, error) {
	b, err := p.binding(ctx, c)
	if err != nil {
		return reposync.Outcome{}, err
	}
	if b.Private {
		return p.Client.AuthenticatedSnapshot(ctx, b.ActorDID, b.RepositoryDID, b.Branch)
	}
	return p.Client.Snapshot(ctx, b.RepositoryDID, b.Branch)
}
func (p Provider) ResolveRepositoryCommit(ctx context.Context, c syncservice.Connection) (string, error) {
	b, err := p.binding(ctx, c)
	if err != nil {
		return "", err
	}
	if b.Private {
		return p.Client.authenticatedBranch(ctx, b.ActorDID, b.RepositoryDID, b.Branch)
	}
	return p.Client.ResolveBranch(ctx, b.RepositoryDID, b.Branch)
}

// Changed is a bounded reconciliation step. Its caller persists checkpoints and
// schedules the existing bounded sync when the immutable branch head differs.
func (p Provider) Changed(ctx context.Context, c syncservice.Connection, priorRevision string) (bool, string, error) {
	if priorRevision != "" && !oidPattern.MatchString(priorRevision) {
		return false, "", errors.New("invalid prior Tangled revision")
	}
	head, err := p.ResolveRepositoryCommit(ctx, c)
	return err == nil && head != priorRevision, head, err
}
