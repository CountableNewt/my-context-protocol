package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/CountableNewt/my-context-protocol/services/go/xrpc"
)

// Only this adapter can establish the service-bound trusted context. HTTP
// headers and cookies cannot manufacture it, and another Service cannot reuse it.
type xrpcContextKey struct{}
type xrpcAuthorization struct {
	service   *Service
	accountID string
}

func (s *Service) verifiedXRPC(ctx context.Context) (xrpcAuthorization, bool) {
	authorization, ok := ctx.Value(xrpcContextKey{}).(xrpcAuthorization)
	return authorization, ok && authorization.service == s && authorization.accountID != ""
}

type controlXRPCInvoker struct{ service *Service }

func (a controlXRPCInvoker) Invoke(ctx context.Context, principal xrpc.Principal, operation xrpc.Operation, input xrpc.Input) (xrpc.Result, error) {
	// The reviewed control adapter deliberately excludes identity.logout and all
	// pending operations; the registry alone never enables another service's API.
	if operation.Service != "control" {
		return xrpc.Result{}, &xrpc.Error{Status: 404, Code: "NotFound", Message: "XRPC method is not served by control"}
	}
	accountID, err := uuid(principal.AccountID)
	if err != nil {
		return xrpc.Result{}, &xrpc.Error{Status: 401, Code: "AuthenticationRequired", Message: "Invalid API account"}
	}
	path := operation.RESTPath
	body := make(map[string]json.RawMessage, len(input.Body))
	for key, value := range input.Body {
		body[key] = value
	}
	parameters := url.Values{}
	for key, value := range input.Parameters {
		parameters.Set(key, value)
	}
	for _, match := range regexp.MustCompile(`\{([a-z_]+)\}`).FindAllStringSubmatch(path, -1) {
		key := match[1]
		id := input.Parameters[key]
		if operation.Method == "POST" {
			if json.Unmarshal(body[key], &id) != nil {
				return xrpc.Result{}, badXRPC("Invalid resource ID")
			}
			delete(body, key)
		}
		id, err = uuid(id)
		if err != nil {
			return xrpc.Result{}, badXRPC("Invalid resource ID")
		}
		path = strings.ReplaceAll(path, match[0], url.PathEscape(id))
		parameters.Del(key)
	}
	if len(parameters) > 0 {
		path += "?" + parameters.Encode()
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return xrpc.Result{}, err
	}
	trusted := context.WithValue(ctx, xrpcContextKey{}, xrpcAuthorization{service: a.service, accountID: accountID})
	request, err := http.NewRequestWithContext(trusted, operation.RESTMethod, path, bytes.NewReader(raw))
	if err != nil {
		return xrpc.Result{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	value, status, err := a.service.serve(request)
	if err != nil {
		return xrpc.Result{}, a.applicationError(err)
	}
	if operation.Envelope != "" {
		value = row{operation.Envelope: value}
	}

	return xrpc.Result{Value: value, Status: status}, nil
}

func omitNulls(input row) row {
	out := make(row, len(input))
	for key, value := range input {
		if value != nil {
			out[key] = value
		}
	}
	return out
}
func badXRPC(message string) error {
	return &xrpc.Error{Status: 400, Code: "InvalidRequest", Message: message}
}
func (a controlXRPCInvoker) applicationError(err error) error {
	err = recordError(err)
	status, message := 503, "Control service temporarily unavailable"
	var application apiError
	if errors.As(err, &application) {
		status, message = application.status, application.message
	}
	if public, ok := err.(interface{ HTTPStatus() int }); ok && public.HTTPStatus() >= 400 && public.HTTPStatus() < 500 {
		status, message = public.HTTPStatus(), err.Error()
	}
	codes := map[int]string{400: "InvalidRequest", 401: "AuthenticationRequired", 403: "Forbidden", 404: "NotFound", 409: "Conflict", 429: "RateLimitExceeded", 503: "ServiceUnavailable"}
	code, ok := codes[status]
	if !ok {
		status, code, message = 503, "ServiceUnavailable", "Control service temporarily unavailable"
	}
	public := &xrpc.Error{Status: status, Code: code, Message: message}
	if status == 429 {
		window := a.service.cfg.SyncWindowSeconds
		if window < 1 {
			window = 3600
		}
		public.RetryAfterSeconds = int(window)
	}
	return public
}
