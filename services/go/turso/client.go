// Package turso implements bounded, stateless Hrana HTTP requests.
// No transaction or session state is retained in a function instance.
package turso

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const MaxStatements = 128
const MaxRequestBytes = 4 << 20
const MaxResponseBytes = 4 << 20

// Value preserves SQLite integers as decimal strings without float64 truncation.
// Types are null, integer, float, text or blob (base64 in Base64).
type Value struct {
	Type   string  `json:"type"`
	Value  any     `json:"value,omitempty"`
	Base64 *string `json:"base64,omitempty"`
}

func Text(value string) Value   { return Value{Type: "text", Value: value} }
func Integer(value int64) Value { return Value{Type: "integer", Value: fmt.Sprint(value)} }
func Blob(value []byte) Value {
	encoded := base64.StdEncoding.EncodeToString(value)
	return Value{Type: "blob", Base64: &encoded}
}
func Null() Value { return Value{Type: "null"} }

type Statement struct {
	SQL      string  `json:"sql"`
	Args     []Value `json:"args,omitempty"`
	WantRows bool    `json:"want_rows"`
}
type Column struct {
	Name     *string `json:"name"`
	DeclType *string `json:"decltype"`
}
type Result struct {
	Columns          []Column  `json:"cols"`
	Rows             [][]Value `json:"rows"`
	AffectedRowCount int64     `json:"affected_row_count"`
	LastInsertRowID  *string   `json:"last_insert_rowid"`
}
type DatabaseError struct {
	Message string `json:"message"`
	Code    string `json:"code"`
}

// Error deliberately excludes server error text, which may contain stored secrets.
func (e *DatabaseError) Error() string { return "Turso statement failed (" + e.Code + ")" }

type Client struct {
	endpoint string
	token    string
	http     *http.Client
}

// New requires TLS, except for loopback HTTP used by local tests/development.
// A private client prevents redirects from moving database authorization elsewhere.
func New(rawURL, token string) (*Client, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, errors.New("invalid Turso URL")
	}
	if parsed.Scheme == "libsql" {
		parsed.Scheme = "https"
	}
	local := parsed.Hostname() == "localhost" || parsed.Hostname() == "127.0.0.1" || parsed.Hostname() == "::1"
	if parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") || (parsed.Scheme != "https" && !(local && parsed.Scheme == "http")) {
		return nil, errors.New("Turso URL must be an HTTPS database origin")
	}
	if strings.TrimSpace(token) == "" || strings.ContainsAny(token, "\r\n") {
		return nil, errors.New("Turso auth token is required")
	}
	parsed.Path = "/v2/pipeline"
	return &Client{endpoint: parsed.String(), token: token, http: &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

type condition struct {
	Type string     `json:"type"`
	Step *int       `json:"step,omitempty"`
	Cond *condition `json:"cond,omitempty"`
}
type step struct {
	Condition *condition `json:"condition,omitempty"`
	Statement Statement  `json:"stmt"`
}

func ok(index int) *condition { return &condition{Type: "ok", Step: &index} }

type batch struct {
	Steps []step `json:"steps"`
}
type request struct {
	Type      string     `json:"type"`
	Statement *Statement `json:"stmt,omitempty"`
	Batch     *batch     `json:"batch,omitempty"`
}
type pipeline struct {
	Requests []request `json:"requests"`
}

// Execute runs one statement and closes its stream in the same HTTP request.
func (c *Client) Execute(ctx context.Context, stmt Statement) (*Result, error) {
	results, err := c.send(ctx, request{Type: "execute", Statement: &stmt})
	if err != nil {
		return nil, err
	}
	var result Result
	if err = json.Unmarshal(results, &result); err != nil {
		return nil, errors.New("invalid Turso statement result")
	}
	return &result, nil
}

// Batch executes all statements in one explicit transaction. Each step depends
// on its predecessor, COMMIT depends on the final statement, and ROLLBACK runs
// if COMMIT did not succeed. close also releases the stream on any error.
// SQL is application-owned; callers must not pass transaction-control SQL here.
func (c *Client) Batch(ctx context.Context, statements []Statement) ([]Result, error) {
	if len(statements) == 0 || len(statements) > MaxStatements {
		return nil, fmt.Errorf("batch requires 1 to %d statements", MaxStatements)
	}
	steps := []step{{Statement: Statement{SQL: "BEGIN IMMEDIATE"}}}
	for _, stmt := range statements {
		steps = append(steps, step{Condition: ok(len(steps) - 1), Statement: stmt})
	}
	commitIndex := len(steps)
	steps = append(steps, step{Condition: ok(commitIndex - 1), Statement: Statement{SQL: "COMMIT"}})
	steps = append(steps, step{Condition: &condition{Type: "not", Cond: ok(commitIndex)}, Statement: Statement{SQL: "ROLLBACK"}})
	raw, err := c.send(ctx, request{Type: "batch", Batch: &batch{Steps: steps}})
	if err != nil {
		return nil, err
	}
	var result struct {
		Results []*Result        `json:"step_results"`
		Errors  []*DatabaseError `json:"step_errors"`
	}
	if err = json.Unmarshal(raw, &result); err != nil {
		return nil, errors.New("invalid Turso batch result")
	}
	if len(result.Results) != len(steps) || len(result.Errors) != len(steps) {
		return nil, errors.New("incomplete Turso batch result")
	}
	for i := 0; i <= commitIndex; i++ {
		if result.Errors[i] != nil {
			return nil, result.Errors[i]
		}
		if result.Results[i] == nil {
			return nil, fmt.Errorf("Turso transaction step %d did not execute", i)
		}
	}
	output := make([]Result, len(statements))
	for i := range output {
		output[i] = *result.Results[i+1]
	}
	return output, nil
}

func (c *Client) send(ctx context.Context, operation request) (json.RawMessage, error) {
	encoded, err := json.Marshal(pipeline{Requests: []request{operation, {Type: "close"}}})
	if err != nil {
		return nil, err
	}
	if len(encoded) > MaxRequestBytes {
		return nil, errors.New("Turso request exceeds size limit")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Turso HTTP request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Turso HTTP status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > MaxResponseBytes {
		return nil, errors.New("Turso response exceeds size limit")
	}
	var decoded struct {
		Results []struct {
			Type     string         `json:"type"`
			Error    *DatabaseError `json:"error"`
			Response struct {
				Type   string          `json:"type"`
				Result json.RawMessage `json:"result"`
			} `json:"response"`
		} `json:"results"`
	}
	if err = json.Unmarshal(body, &decoded); err != nil {
		return nil, errors.New("invalid Turso response")
	}
	if len(decoded.Results) != 2 {
		return nil, errors.New("incomplete Turso pipeline response")
	}
	for _, item := range decoded.Results {
		if item.Type == "error" && item.Error != nil {
			return nil, item.Error
		}
		if item.Type != "ok" {
			return nil, errors.New("invalid Turso pipeline result")
		}
	}
	if decoded.Results[0].Response.Type != operation.Type || decoded.Results[1].Response.Type != "close" || len(decoded.Results[0].Response.Result) == 0 || string(decoded.Results[0].Response.Result) == "null" {
		return nil, errors.New("invalid Turso operation response")
	}
	return decoded.Results[0].Response.Result, nil
}
