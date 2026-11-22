package turso

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

func TestReadOnlyQueryWorksWhenProviderBlocksConnectionSettings(t *testing.T) {
	var settingCalls, readCalls atomic.Int64
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		var body pipeline
		if json.NewDecoder(r.Body).Decode(&body) != nil || len(body.Requests) != 2 || body.Requests[1].Type != "close" {
			t.Fatal("invalid pipeline")
		}
		operation := body.Requests[0]
		if operation.Type == "batch" {
			if operation.Batch.Steps[0].Statement.SQL != "PRAGMA foreign_keys=ON" {
				t.Fatal("runtime foreign keys changed")
			}
			settingCalls.Add(1)
			fmt.Fprint(w, `{"results":[{"type":"error","error":{"code":"BLOCKED","message":"private provider detail"}},{"type":"ok","response":{"type":"close"}}]}`)
			return
		}
		if operation.Type != "execute" || operation.Statement == nil {
			t.Fatal("inspection must use raw execute")
		}
		stmt := operation.Statement
		readCalls.Add(1)
		if strings.HasPrefix(strings.ToUpper(stmt.SQL), "SELECT") {
			if !stmt.WantRows || len(stmt.Args) != 1 || stmt.Args[0].Value != "9007199254740993" {
				t.Fatal("query arguments changed")
			}
			fmt.Fprint(w, `{"results":[{"type":"ok","response":{"type":"execute","result":{"cols":[{"name":"n"}],"rows":[[{"type":"integer","value":"9007199254740993"}]],"affected_row_count":0}}},{"type":"ok","response":{"type":"close"}}]}`)
		} else {
			if !strings.EqualFold(strings.Join(strings.Fields(stmt.SQL), " "), "PRAGMA foreign_key_check") {
				t.Fatal("setting pragma escaped")
			}
			fmt.Fprintf(w, `{"results":[{"type":"ok","response":{"type":"execute","result":%s}},{"type":"ok","response":{"type":"close"}}]}`, emptyResult)
		}
	})
	stmt := Statement{SQL: "SELECT ?", Args: []Value{Integer(9007199254740993)}, WantRows: true}
	if _, e := client.Execute(context.Background(), stmt); e == nil || strings.Contains(e.Error(), "private") {
		t.Fatal("runtime setting should remain blocked and redacted", e)
	}
	r, e := client.QueryReadOnly(context.Background(), stmt)
	if e != nil || r.Rows[0][0].Value != "9007199254740993" {
		t.Fatal("frozen read failed", e)
	}
	if _, e = client.QueryReadOnly(context.Background(), Statement{SQL: "pragma   FOREIGN_KEY_CHECK", WantRows: true}); e != nil {
		t.Fatal(e)
	}
	if settingCalls.Load() != 1 || readCalls.Load() != 2 {
		t.Fatal("unexpected settings during read")
	}
}

func TestReadOnlyQueryRejectsSettingsAndWritesBeforeNetwork(t *testing.T) {
	var calls atomic.Int64
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); t.Error("rejected query reached provider") })
	for _, sql := range []string{"", "PRAGMA foreign_keys=ON", "PRAGMA foreign_key_check=1", "PRAGMA wal_checkpoint", "UPDATE projects SET name='x'", "DELETE FROM projects", "BEGIN", "WITH x AS (SELECT 1) SELECT * FROM x", "SELECT 1; DELETE FROM projects", "SELECT 1 -- comment", "SELECT /* comment */ 1", "SELECT\x00 1"} {
		if _, e := client.QueryReadOnly(context.Background(), Statement{SQL: sql}); e == nil {
			t.Fatal("unsafe query accepted")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("rejected SQL made a request")
	}
}
