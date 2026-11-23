// Package recordservice connects user-owned canonical records to hosted services.
package recordservice

import (
	"context"
	"errors"

	"github.com/CountableNewt/my-context-protocol/services/go/ataccount"
	"github.com/CountableNewt/my-context-protocol/services/go/atproto"
	"github.com/CountableNewt/my-context-protocol/services/go/domainrecords"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
)

type Database interface{ ataccount.FullDatabase }
type Service struct {
	DB        Database
	Accounts  *ataccount.Manager
	Hydrate   func(context.Context, string, string) error
	WorkLimit int
}

func (s *Service) WithEngine(ctx context.Context, account string, apply func(context.Context, string, *domainrecords.Engine) error) error {
	if s == nil || s.Accounts == nil || s.DB == nil {
		return errors.New("canonical record service is not configured")
	}
	linked, err := s.Accounts.Identities.ATProtoIdentity(ctx, account)
	if err != nil {
		return err
	}
	if linked == nil || linked.AccountID != account {
		return ataccount.ErrReconnect
	}
	return s.Accounts.WithRecords(ctx, account, func(ctx context.Context, records atproto.Records) error {
		engine := &domainrecords.Engine{DB: s.DB, Publisher: atproto.Publisher{Records: records, Journal: atproto.SQLJournal{DB: s.DB}}, Records: records, Hydrate: s.Hydrate, WorkLimit: s.WorkLimit}
		return apply(ctx, linked.DID, engine)
	})
}

func (s *Service) IsCanonical(ctx context.Context, account, project string) (bool, error) {
	r, err := s.DB.Execute(ctx, turso.Statement{SQL: `SELECT 1 FROM domain_canonical_projects WHERE project_id=? AND account_id=? AND public_consent=1`, Args: []turso.Value{turso.Text(project), turso.Text(account)}, WantRows: true})
	if err != nil {
		return false, err
	}
	return r != nil && len(r.Rows) == 1, nil
}

func (s *Service) Reconcile(ctx context.Context, account, project string) error {
	if err := s.resumeNative(ctx, account, project); err != nil {
		return err
	}
	return s.WithEngine(ctx, account, func(ctx context.Context, owner string, e *domainrecords.Engine) error {
		return e.Reconcile(ctx, account, owner, project)
	})
}

func (s *Service) Update(ctx context.Context, account, project string, changes []domainrecords.Change) error {
	return s.WithEngine(ctx, account, func(ctx context.Context, owner string, e *domainrecords.Engine) error {
		_, err := e.Update(ctx, account, owner, project, changes)
		return err
	})
}
