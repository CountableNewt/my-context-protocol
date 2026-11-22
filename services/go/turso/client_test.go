package turso

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const emptyResult = `{"cols":[],"rows":[],"affected_row_count":0,"last_insert_rowid":null}`

func wireResult(kind, result string) string {
	return fmt.Sprintf(`{"results":[{"type":"ok","response":{"type":%q,"result":%s}},{"type":"ok","response":{"type":"close"}}]}`, kind, result)
}
func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := New(server.URL, "secret")
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestExecuteBindsValuesAndClosesStream(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/pipeline" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Error("incorrect endpoint or authorization")
		}
		var body pipeline
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if len(body.Requests) != 2 || body.Requests[1].Type != "close" {
			t.Fatal("stream not closed")
		}
		stmt := body.Requests[0].Statement
		if stmt.SQL != "SELECT ?" || stmt.Args[0].Value != "9007199254740993" {
			t.Fatal("integer precision lost")
		}
		fmt.Fprint(w, wireResult("execute", `{"cols":[{"name":"n","decltype":"INTEGER"}],"rows":[[{"type":"integer","value":"9007199254740993"}]],"affected_row_count":0}`))
	})
	result, err := client.Execute(context.Background(), Statement{SQL: "SELECT ?", Args: []Value{Integer(9007199254740993)}, WantRows: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.Rows[0][0].Value != "9007199254740993" {
		t.Fatal("response integer precision lost")
	}
}
func TestBatchTransactionConditions(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		var body pipeline
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		steps := body.Requests[0].Batch.Steps
		if len(steps) != 5 || steps[0].Statement.SQL != "BEGIN IMMEDIATE" || steps[3].Statement.SQL != "COMMIT" || steps[4].Statement.SQL != "ROLLBACK" {
			t.Fatal("missing transaction boundaries")
		}
		for i := 1; i <= 3; i++ {
			if steps[i].Condition.Type != "ok" || *steps[i].Condition.Step != i-1 {
				t.Fatal("statement can execute after failed predecessor")
			}
		}
		if steps[4].Condition.Type != "not" || *steps[4].Condition.Cond.Step != 3 {
			t.Fatal("rollback must depend on commit failure")
		}
		fmt.Fprint(w, wireResult("batch", fmt.Sprintf(`{"step_results":[%s,%s,%s,%s,null],"step_errors":[null,null,null,null,null]}`, emptyResult, emptyResult, emptyResult, emptyResult)))
	})
	results, err := client.Batch(context.Background(), []Statement{{SQL: "INSERT INTO accounts VALUES (?)", Args: []Value{Text("id")}}, {SQL: "UPDATE accounts SET name = ?", Args: []Value{Text("name")}}})
	if err != nil || len(results) != 2 {
		t.Fatalf("batch failed: %v", err)
	}
}
func TestBatchRejectsSQLErrorInsideHTTP200(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, wireResult("batch", fmt.Sprintf(`{"step_results":[%s,null,null,%s],"step_errors":[null,{"code":"SQLITE_CONSTRAINT","message":"secret token"},null,null]}`, emptyResult, emptyResult)))
	})
	_, err := client.Batch(context.Background(), []Statement{{SQL: "INSERT INTO x VALUES (1)"}})
	var dbErr *DatabaseError
	if !errors.As(err, &dbErr) || dbErr.Code != "SQLITE_CONSTRAINT" {
		t.Fatalf("missing SQL error: %v", err)
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatal("error exposes statement data")
	}
}
func TestRejectsMalformedAndOversizedResponses(t *testing.T) {
	for _, body := range []string{`{}`, `{"results":[]}`, `{"results":[{"type":"error","error":{"code":"AUTH","message":"private"}}]}`, strings.Repeat(" ", MaxResponseBytes+1), wireResult("batch", `{}`)} {
		t.Run(fmt.Sprint(len(body)), func(t *testing.T) {
			client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) })
			if _, err := client.Execute(context.Background(), Statement{SQL: "SELECT 1"}); err == nil {
				t.Fatal("invalid response accepted")
			}
		})
	}
}
func TestRequestLimitsAndCancellation(t *testing.T) {
	calls := 0
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		fmt.Fprint(w, wireResult("execute", emptyResult))
	})
	if _, err := client.Batch(context.Background(), nil); err == nil {
		t.Fatal("empty batch accepted")
	}
	if _, err := client.Batch(context.Background(), make([]Statement, MaxStatements+1)); err == nil {
		t.Fatal("oversized batch accepted")
	}
	if _, err := client.Execute(context.Background(), Statement{SQL: strings.Repeat("x", MaxRequestBytes+1)}); err == nil {
		t.Fatal("oversized SQL accepted")
	}
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if _, err := client.Execute(ctx, Statement{SQL: "SELECT 1"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("context deadline lost: %v", err)
	}
	if calls != 0 {
		t.Fatal("invalid requests reached database")
	}
}
func TestDoesNotFollowRedirects(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("followed database redirect") }))
	defer target.Close()
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	})
	if _, err := client.Execute(context.Background(), Statement{SQL: "SELECT 1"}); err == nil {
		t.Fatal("redirect accepted")
	}
}
func TestDatabaseURLValidation(t *testing.T) {
	for _, raw := range []string{"http://db.example.com", "https://user:pass@db.example.com", "https://db.example.com/path", "https://db.example.com?token=x", "ftp://db.example.com", ""} {
		if _, err := New(raw, "secret"); err == nil {
			t.Errorf("accepted URL %q", raw)
		}
	}
	c, err := New("libsql://database.example.com", "secret")
	if err != nil || c.endpoint != "https://database.example.com/v2/pipeline" {
		t.Fatalf("libsql conversion: %v", err)
	}
	if _, err = New("https://database.example.com", ""); err == nil {
		t.Fatal("empty token accepted")
	}
}

func TestEmptyBlobAndTextAreNotNull(t *testing.T) {
	for _, tc := range []struct {
		value    Value
		expected string
	}{{Blob(nil), `{"type":"blob","base64":""}`}, {Text(""), `{"type":"text","value":""}`}, {Null(), `{"type":"null"}`}} {
		actual, err := json.Marshal(tc.value)
		if err != nil || string(actual) != tc.expected {
			t.Fatalf("value encoded as %s, expected %s (%v)", actual, tc.expected, err)
		}
	}
}
