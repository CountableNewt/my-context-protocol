// Package xrpc provides an authenticated XRPC transport for reviewed product
// contracts. Registering a contract does not enable it in a production service.
package xrpc

import "strings"

const Namespace = "dev.mycontextprotocol."

type Field struct {
	Name      string
	Required  bool
	Boolean   bool
	MinLength int
	MaxLength int
}

// Operation identifies an application operation independently of its transport.
// RESTPath is a template for the application adapter, never an authorization rule.
type Operation struct {
	NSID       string
	Method     string
	RESTMethod string
	RESTPath   string
	Admin      bool
	NoOutput   bool
	Fields     []Field
}

// Scope is a normalized verified permission, not a wire OAuth scope parser.
// The verifier must establish the service audience and requested method before
// granting this permission. Wildcard strings never imply access here.
func (o Operation) Scope() string { return "rpc:" + o.NSID }

var operations = []Operation{
	{NSID: Namespace + "project.listProjects", Method: "GET", RESTMethod: "GET", RESTPath: "/projects"},
	{NSID: Namespace + "project.getProject", Method: "GET", RESTMethod: "GET", RESTPath: "/projects/{project_id}", Fields: []Field{{Name: "project_id", Required: true}}},
	{NSID: Namespace + "project.createProject", Method: "POST", RESTMethod: "POST", RESTPath: "/projects", Fields: []Field{{Name: "name", Required: true, MinLength: 1, MaxLength: 256}, {Name: "slug", Required: true, MinLength: 1, MaxLength: 128}}},
	{NSID: Namespace + "project.updateProject", Method: "POST", RESTMethod: "PATCH", RESTPath: "/projects/{project_id}", Fields: []Field{{Name: "project_id", Required: true}, {Name: "name", Required: true, MinLength: 1, MaxLength: 256}}},
	{NSID: Namespace + "project.setActive", Method: "POST", RESTMethod: "POST", RESTPath: "/projects/{project_id}/set-active", Fields: []Field{{Name: "project_id", Required: true}}},
	{NSID: Namespace + "admin.getMetrics", Method: "GET", RESTMethod: "GET", RESTPath: "/admin/metrics", Admin: true},
	{NSID: Namespace + "admin.refreshRollups", Method: "POST", RESTMethod: "POST", RESTPath: "/admin/analytics/rollup-refresh", Admin: true, NoOutput: true},
	{NSID: Namespace + "admin.lookupAccount", Method: "POST", RESTMethod: "POST", RESTPath: "/admin/lookup", Admin: true, Fields: []Field{{Name: "github_login"}, {Name: "github_id"}, {Name: "email"}}},
	{NSID: Namespace + "admin.updateAccountFlags", Method: "POST", RESTMethod: "POST", RESTPath: "/admin/account-flags", Admin: true, Fields: []Field{{Name: "account_id", Required: true}, {Name: "is_admin", Required: true, Boolean: true}}},
	{NSID: Namespace + "account.logout", Method: "POST", RESTMethod: "POST", RESTPath: "/auth/logout", NoOutput: true},
}

// Operations returns detached registry entries so callers cannot mutate routing.
func Operations() []Operation {
	out := make([]Operation, len(operations))
	for i, operation := range operations {
		operation.Fields = append([]Field(nil), operation.Fields...)
		out[i] = operation
	}
	return out
}

func lookup(path string) (Operation, bool) {
	if !strings.HasPrefix(path, "/xrpc/") {
		return Operation{}, false
	}
	for _, operation := range operations {
		if path == "/xrpc/"+operation.NSID {
			operation.Fields = append([]Field(nil), operation.Fields...)
			return operation, true
		}
	}
	return Operation{}, false
}
