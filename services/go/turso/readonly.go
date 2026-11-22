package turso

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
)

// QueryReadOnly executes one application-owned inspection query without changing
// connection settings. It is intended for exports while the provider blocks
// writes: even PRAGMA foreign_keys=ON can be rejected in that mode. Runtime
// Execute and Batch retain their foreign-key setup and transaction behavior.
//
// This narrow shape check is not an SQL sandbox: callers must own the SQL and
// must not expose it to user input or invoke functions with side effects. The
// provider's write block supplies enforcement during a frozen export.
func (c *Client) QueryReadOnly(ctx context.Context, stmt Statement) (*Result, error) {
	sql := strings.TrimSpace(stmt.SQL)
	words := strings.Fields(sql)
	if len(words) == 0 || strings.ContainsAny(sql, ";\x00") || strings.Contains(sql, "--") || strings.Contains(sql, "/*") || (strings.ToUpper(words[0]) != "SELECT" && !strings.EqualFold(strings.Join(words, " "), "PRAGMA foreign_key_check")) {
		return nil, errors.New("read-only inspection requires one owned SELECT or foreign_key_check")
	}
	raw, err := c.send(ctx, request{Type: "execute", Statement: &stmt})
	if err != nil {
		return nil, err
	}
	var result Result
	if json.Unmarshal(raw, &result) != nil {
		return nil, errors.New("invalid Turso read-only result")
	}
	return &result, nil
}
