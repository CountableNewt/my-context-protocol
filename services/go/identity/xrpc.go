package identity

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/CountableNewt/my-context-protocol/services/go/session"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
	"github.com/CountableNewt/my-context-protocol/services/go/xrpc"
)

// OwnedSessionStore exposes identifiers, never raw cookie credentials, to API
// session management. Ownership is checked atomically when revoking a session.
type OwnedSessionStore interface {
	SessionIdentifier(context.Context, string, string) (string, error)
	RevokeOwnedSession(context.Context, string, string) (bool, error)
}

func (s SQLStore) SessionIdentifier(ctx context.Context, key, account string) (string, error) {
	r, err := s.DB.Execute(ctx, turso.Statement{SQL: "SELECT id,payload FROM app_sessions WHERE session_key=?", Args: []turso.Value{turso.Text(key)}, WantRows: true})
	if err != nil {
		return "", err
	}
	if len(r.Rows) != 1 || len(r.Rows[0]) != 2 {
		return "", nil
	}
	id, err := scalar(r.Rows[0], 0)
	if err != nil {
		return "", err
	}
	raw, err := scalar(r.Rows[0], 1)
	if err != nil {
		return "", err
	}
	payload, err := session.Decode(raw)
	if err != nil {
		return "", err
	}
	if !strings.EqualFold(payload["accountId"], account) {
		return "", nil
	}
	return id, nil
}

func (s SQLStore) RevokeOwnedSession(ctx context.Context, account, id string) (bool, error) {
	r, err := s.DB.Execute(ctx, turso.Statement{SQL: "DELETE FROM app_sessions WHERE id=? AND upper(json_extract(payload,'$.accountId'))=upper(?) RETURNING id", Args: []turso.Value{turso.Text(id), turso.Text(account)}, WantRows: true})
	if err != nil {
		return false, err
	}
	return len(r.Rows) == 1, nil
}

// XRPCHandler serves account APIs with independently verified service credentials.
// REST logout still targets the current browser cookie. XRPC logout explicitly
// targets an owned app session UUID; it never revokes unrelated sessions.
func (s *Service) XRPCHandler(verifier xrpc.Verifier) (http.Handler, error) {
	return xrpc.New(xrpc.Config{Verifier: verifier, Invoker: xrpc.InvokerFunc(s.invokeXRPC)})
}

var sessionIDPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func (s *Service) invokeXRPC(ctx context.Context, p xrpc.Principal, op xrpc.Operation, input xrpc.Input) (xrpc.Result, error) {
	if op.Service != "identity" {
		return xrpc.Result{}, &xrpc.Error{Status: 404, Code: "NotFound", Message: "Unknown identity operation"}
	}
	account, err := s.store.Account(ctx, p.AccountID)
	if err != nil {
		return xrpc.Result{}, err
	}
	if account == nil {
		return xrpc.Result{}, &xrpc.Error{Status: 401, Code: "AuthenticationRequired", Message: "Account no longer exists"}
	}
	switch op.NSID {
	case xrpc.Namespace + "account.getAccount":
		view, err := s.accountView(ctx, account)
		return xrpc.Result{Value: view}, err
	case xrpc.Namespace + "account.logout":
		var id string
		if json.Unmarshal(input.Body["session_id"], &id) != nil || !sessionIDPattern.MatchString(id) {
			return xrpc.Result{}, &xrpc.Error{Status: 400, Code: "InvalidRequest", Message: "A session UUID is required"}
		}
		store, ok := s.store.(OwnedSessionStore)
		if !ok {
			return xrpc.Result{}, errors.New("owned session revocation is unavailable")
		}
		revoked, err := store.RevokeOwnedSession(ctx, account.ID, id)
		if err != nil {
			return xrpc.Result{}, err
		}
		if !revoked {
			return xrpc.Result{}, &xrpc.Error{Status: 404, Code: "NotFound", Message: "Session not found"}
		}
		return xrpc.Result{}, nil
	}
	return xrpc.Result{}, &xrpc.Error{Status: 404, Code: "NotFound", Message: "Unknown identity operation"}
}

func (s *Service) accountView(ctx context.Context, account *Account) (map[string]any, error) {
	suggest := false
	if s.config.GitHubAppConfigured && account.GitHubID > 0 {
		var err error
		suggest, err = s.store.SuggestedGitHubApp(ctx, account.ID)
		if err != nil {
			return nil, err
		}
	}
	result := map[string]any{"id": account.ID, "email": account.Email, "login": account.Login, "avatar_url": account.AvatarURL, "plan": "free", "is_admin": account.IsAdmin, "internal_pro_bypass": false, "can_manage_subscription": false, "suggested_github_app_install": suggest, "app_env": s.config.AppEnvironment, "non_production_bypasses": false, "needs_project_selection": false, "github_connected": account.GitHubID > 0, "atproto_enabled": s.config.ATProto != nil}
	if s.config.ATProto != nil {
		ident, err := s.config.ATProto.Accounts.ATProtoIdentity(ctx, account.ID)
		if err != nil {
			return nil, err
		}
		if ident != nil {
			result["atproto_did"] = ident.DID
			result["atproto_handle"] = ident.Handle
		}
	}
	return result, nil
}
