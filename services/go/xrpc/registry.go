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
	kind      string
	schema    *contractNode
}

// Operation identifies an application operation independently of its transport.
// RESTPath is a template for the application adapter, never an authorization rule.
type Operation struct {
	NSID         string
	Service      string
	Envelope     string
	inputSchema  *contractNode
	outputSchema *contractNode
	Method       string
	RESTMethod   string
	RESTPath     string
	Admin        bool
	NoOutput     bool
	Fields       []Field
}

// Scope is a normalized verified permission, not a wire OAuth scope parser.
// The verifier must establish the service audience and requested method before
// granting this permission. Wildcard strings never imply access here.
func (o Operation) Scope() string { return "rpc:" + o.NSID }

var operations = loadOperations()

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

// Lookup returns the reviewed operation for a fully qualified method NSID.
func Lookup(nsid string) (Operation, bool) { return lookup("/xrpc/" + nsid) }
