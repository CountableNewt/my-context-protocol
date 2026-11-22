package main

import (
	"context"
	"errors"

	"github.com/CountableNewt/my-context-protocol/services/go/turso"
)

type readOnlyQuerier interface {
	QueryReadOnly(context.Context, turso.Statement) (*turso.Result, error)
}

// Provider-level BlockWrites also rejects foreign_keys=ON, even though it is a
// connection setting. Offline export/verification must issue only their trusted
// read queries; ordinary import/runtime operations retain FK-enforced clients.
type readOnlyDatabase struct{ query readOnlyQuerier }

func (d readOnlyDatabase) Execute(ctx context.Context, s turso.Statement) (*turso.Result, error) {
	return d.query.QueryReadOnly(ctx, s)
}
func (readOnlyDatabase) Batch(context.Context, []turso.Statement) ([]turso.Result, error) {
	return nil, errors.New("read-only snapshot database does not permit transactions")
}
