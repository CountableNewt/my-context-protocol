package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/CountableNewt/my-context-protocol/services/go/xrpc"
)

type xrpcTenantKey struct{}
type xrpcTenant struct {
	service *Service
	project Project
}

// XRPCHandler reuses native MCP dispatch and telemetry on the actual tenant host.
// A service account identity never selects a tenant through request parameters.
func (s *Service) XRPCHandler(verifier xrpc.Verifier) (http.Handler, error) {
	if verifier == nil {
		return nil, errors.New("XRPC verifier is required")
	}
	api, err := xrpc.New(xrpc.Config{Verifier: xrpc.VerifierFunc(func(ctx context.Context, r *http.Request) (xrpc.Principal, error) {
		p, err := verifier.Verify(ctx, r)
		if err != nil {
			return p, err
		}
		tenant, ok := ctx.Value(xrpcTenantKey{}).(xrpcTenant)
		if !ok || tenant.service != s || tenant.project.AccountID != p.AccountID || p.ProjectID != "" && p.ProjectID != tenant.project.ID {
			return xrpc.Principal{}, errors.New("credential does not match tenant owner")
		}
		p.ProjectID = tenant.project.ID
		return p, nil
	}), Invoker: xrpc.InvokerFunc(s.invokeXRPC)})
	if err != nil {
		return nil, err
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		project, err := s.ResolveTenant(r)
		if err != nil {
			mcpXRPCFailure(w, 503, "ServiceUnavailable", "Tenant routing unavailable")
			return
		}
		if project == nil {
			mcpXRPCFailure(w, 403, "Forbidden", "Use the project's MCP hostname")
			return
		}
		allowed, err := s.AllowIngress(r)
		if err != nil {
			mcpXRPCFailure(w, 503, "ServiceUnavailable", "Rate limiting unavailable")
			return
		}
		if !allowed {
			w.Header().Set("Retry-After", "60")
			mcpXRPCFailure(w, 429, "RateLimitExceeded", "Rate limited")
			return
		}
		ctx := context.WithValue(r.Context(), xrpcTenantKey{}, xrpcTenant{service: s, project: *project})
		api.ServeHTTP(w, r.WithContext(ctx))
	}), nil
}

func mcpXRPCFailure(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "message": message})
}

func (s *Service) invokeXRPC(ctx context.Context, verified xrpc.Principal, op xrpc.Operation, input xrpc.Input) (xrpc.Result, error) {
	if op.Service != "mcp" {
		return xrpc.Result{}, &xrpc.Error{Status: 404, Code: "NotFound", Message: "Unknown agent operation"}
	}
	tenant, ok := ctx.Value(xrpcTenantKey{}).(xrpcTenant)
	if !ok || tenant.service != s || tenant.project.ID != verified.ProjectID || tenant.project.AccountID != verified.AccountID {
		return xrpc.Result{}, &xrpc.Error{Status: 403, Code: "Forbidden", Message: "Credential does not match tenant"}
	}
	identity := verified.ClientID
	if identity == "" {
		identity = "atproto:" + verified.DID
	}
	principal := Principal{ID: verified.DID, ClientIdentity: identity, Kind: "atproto", Project: tenant.project}
	if s.config.ProjectContext != nil {
		var err error
		ctx, err = s.config.ProjectContext(ctx, tenant.project)
		if err != nil {
			return xrpc.Result{}, err
		}
	}
	snapshot, err := s.store.Snapshot(ctx, tenant.project.ID)
	if err != nil {
		return xrpc.Result{}, err
	}
	params := rpcParams{Arguments: input.Body}
	method := op.RESTPath
	if op.RESTMethod == "MCP_TOOL" {
		method = "tools/call"
		params.Name = op.RESTPath
	} else {
		for key, target := range map[string]*string{"uri": &params.URI, "name": &params.Name, "cursor": &params.Cursor} {
			if raw, ok := input.Body[key]; ok {
				if json.Unmarshal(raw, target) != nil {
					return xrpc.Result{}, &xrpc.Error{Status: 400, Code: "InvalidRequest", Message: "Invalid method argument"}
				}
			}
		}
		if raw, ok := input.Body["arguments"]; ok {
			var values []struct {
				Name  string `json:"name"`
				Value string `json:"value"`
			}
			if json.Unmarshal(raw, &values) != nil {
				return xrpc.Result{}, &xrpc.Error{Status: 400, Code: "InvalidRequest", Message: "Invalid prompt arguments"}
			}
			params.Arguments = map[string]json.RawMessage{}
			for _, value := range values {
				if _, exists := params.Arguments[value.Name]; exists {
					return xrpc.Result{}, &xrpc.Error{Status: 400, Code: "InvalidRequest", Message: "Duplicate prompt argument"}
				}
				params.Arguments[value.Name], _ = json.Marshal(value.Value)
			}
		}
	}
	start := time.Now()
	result, code, message, observations := s.dispatch(ctx, snapshot, principal, method, params, "2025-11-25")
	status := 200
	if code != 0 {
		status = rpcStatus(code)
	}
	if snapshot.Settings.TelemetryEnabled && len(observations) > 0 {
		if err := s.store.Record(ctx, snapshot.ProjectID, principal.ClientIdentity, observations); err != nil {
			return xrpc.Result{}, err
		}
	}
	_ = s.store.Log(ctx, RequestLog{ProjectID: snapshot.ProjectID, ReleaseID: snapshot.ReleaseID, ClientIdentity: principal.ClientIdentity, Method: method, LatencyMS: time.Since(start).Milliseconds(), Status: status, ErrorCode: code, ErrorMessage: message})
	if code != 0 {
		if code == -32602 {
			return xrpc.Result{}, &xrpc.Error{Status: 400, Code: "InvalidRequest", Message: message}
		}
		if code == -32601 {
			return xrpc.Result{}, &xrpc.Error{Status: 404, Code: "NotFound", Message: "Method not found"}
		}
		return xrpc.Result{}, &xrpc.Error{Status: 503, Code: "ServiceUnavailable", Message: "Agent operation unavailable"}
	}
	if method == "tools/call" {
		tool, ok := result.(map[string]any)
		if !ok {
			return xrpc.Result{}, errors.New("invalid native tool result")
		}
		if tool["isError"] == true {
			return xrpc.Result{}, &xrpc.Error{Status: 400, Code: "InvalidRequest", Message: "The requested skill operation could not be completed"}
		}
		result = tool["structuredContent"]
		if result == nil {
			return xrpc.Result{}, errors.New("native tool omitted structured content")
		}
		if op.NSID == xrpc.Namespace+"agent.getCatalog" {
			kind := "resolution"
			var doc map[string]any
			raw, _ := json.Marshal(result)
			_ = json.Unmarshal(raw, &doc)
			if doc["kind"] == "skill" || doc["kind"] == "file" {
				kind = "skill"
			}
			result = map[string]any{kind: result}
		}
	}
	if op.NSID == xrpc.Namespace+"agent.listTools" {
		if object, ok := result.(map[string]any); ok {
			if list, ok := object["tools"].([]map[string]any); ok {
				for _, tool := range list {
					for _, key := range []string{"inputSchema", "outputSchema"} {
						if value, exists := tool[key]; exists {
							raw, _ := json.Marshal(value)
							target := "input_schema_json"
							if key == "outputSchema" {
								target = "output_schema_json"
							}
							tool[target] = string(raw)
							delete(tool, key)
						}
					}
				}
			}
		}
	}
	return xrpc.Result{Value: result}, nil
}
