package syncservice

import (
	"context"
	"errors"
	"net/http"

	"github.com/CountableNewt/my-context-protocol/services/go/turso"
	"github.com/CountableNewt/my-context-protocol/services/go/xrpc"
)

type ProjectOwnerStore interface {
	OwnsProject(context.Context, string, string) (bool, error)
}

func (s SQLStore) OwnsProject(ctx context.Context, account, project string) (bool, error) {
	r, err := s.DB.Execute(ctx, stmt("SELECT id FROM projects WHERE id=? AND account_id=?", turso.Text(project), turso.Text(account)))
	if err != nil {
		return false, err
	}
	return len(r.Rows) == 1, nil
}

func (s *Service) XRPCHandler(verifier xrpc.Verifier) (http.Handler, error) {
	return xrpc.New(xrpc.Config{Verifier: verifier, Invoker: xrpc.InvokerFunc(s.invokeXRPC)})
}

func (s *Service) invokeXRPC(ctx context.Context, p xrpc.Principal, op xrpc.Operation, input xrpc.Input) (xrpc.Result, error) {
	if op.Service != "sync" || op.NSID != xrpc.Namespace+"repository.getSyncJob" {
		return xrpc.Result{}, &xrpc.Error{Status: 404, Code: "NotFound", Message: "Unknown sync operation"}
	}
	job, err := s.config.Store.Job(ctx, input.Parameters["job_id"])
	if errors.Is(err, ErrNotFound) {
		return xrpc.Result{}, &xrpc.Error{Status: 404, Code: "NotFound", Message: "Sync job not found"}
	}
	if err != nil {
		return xrpc.Result{}, err
	}
	store, ok := s.config.Store.(ProjectOwnerStore)
	if !ok {
		return xrpc.Result{}, errors.New("sync project ownership store unavailable")
	}
	owned, err := store.OwnsProject(ctx, p.AccountID, job.ProjectID)
	if err != nil {
		return xrpc.Result{}, err
	}
	if !owned {
		return xrpc.Result{}, &xrpc.Error{Status: 404, Code: "NotFound", Message: "Sync job not found"}
	}
	return xrpc.Result{Value: job}, nil
}
