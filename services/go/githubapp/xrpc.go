package githubapp

import (
	"context"
	"errors"
	"net/http"

	"github.com/CountableNewt/my-context-protocol/services/go/xrpc"
)

func (s *Service) XRPCHandler(verifier xrpc.Verifier) (http.Handler, error) {
	return xrpc.New(xrpc.Config{Verifier: verifier, Invoker: xrpc.InvokerFunc(s.invokeXRPC)})
}

func (s *Service) invokeXRPC(ctx context.Context, p xrpc.Principal, op xrpc.Operation, input xrpc.Input) (xrpc.Result, error) {
	if op.Service != "githubapp" || op.NSID != xrpc.Namespace+"repository.listRepositories" {
		return xrpc.Result{}, &xrpc.Error{Status: 404, Code: "NotFound", Message: "Unknown repository operation"}
	}
	if provider := input.Parameters["provider"]; provider != "" && provider != "github" {
		return xrpc.Result{}, &xrpc.Error{Status: 400, Code: "InvalidRequest", Message: "Use the native repository provider for this identity"}
	}
	if s.config.Sessions == nil {
		return xrpc.Result{}, errors.New("account store unavailable")
	}
	account, err := s.config.Sessions.Account(ctx, p.AccountID)
	if err != nil {
		return xrpc.Result{}, err
	}
	if account == nil {
		return xrpc.Result{}, &xrpc.Error{Status: 401, Code: "AuthenticationRequired", Message: "Account no longer exists"}
	}
	token, _, err := s.accountToken(ctx, account.ID)
	if err != nil {
		return xrpc.Result{}, repositoryXRPCError(err)
	}
	repos, err := s.config.Client.ListRepositories(ctx, token)
	if err != nil {
		return xrpc.Result{}, repositoryXRPCError(err)
	}
	items := make([]map[string]any, 0, len(repos))
	for _, repo := range repos {
		branch := repo.DefaultBranch
		if branch == "" {
			branch = "main"
		}
		items = append(items, map[string]any{"provider": "github", "full_name": repo.FullName, "owner_login": repo.Owner.Login, "owner": repo.Owner.Login, "name": repo.Name, "default_branch": branch, "is_private": repo.Private, "private": repo.Private, "can_write": repo.Permissions["push"] || repo.Permissions["admin"] || repo.Permissions["maintain"]})
	}
	return xrpc.Result{Value: map[string]any{"repositories": items}}, nil
}

func repositoryXRPCError(err error) error {
	var public interface{ HTTPStatus() int }
	if errors.As(err, &public) {
		codes := map[int]string{400: "InvalidRequest", 401: "AuthenticationRequired", 403: "Forbidden", 404: "NotFound", 409: "Conflict"}
		if code := codes[public.HTTPStatus()]; code != "" {
			return &xrpc.Error{Status: public.HTTPStatus(), Code: code, Message: err.Error()}
		}
	}
	return err
}
