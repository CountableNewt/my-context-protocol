package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestOptionsAndRequestBody(t *testing.T) {
	o, e := ParseOptions([]string{"--compact", "projects", "--id=p", "get", "--query", "q=1", "--query=q=2"})
	if e != nil || strings.Join(o.Words, " ") != "projects get" || o.Get("id") != "p" || len(o.Flags["query"]) != 2 {
		t.Fatalf("%+v %v", o, e)
	}
	for _, args := range [][]string{{"--unknown"}, {"--url"}, {"--url=a", "--url=b"}, {"--json=true"}} {
		if _, e := ParseOptions(args); e == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	if !jsonErrors([]string{"--oops", "--json"}) || jsonErrors([]string{"--data", "--json"}) {
		t.Fatal("json error mode flag detection")
	}
	o, _ = ParseOptions([]string{"--data", `{"n":123456789012345678901234567890,"name":"old"}`, "--set", `name="new"`, "--set", "nested=[true,null]"})
	body, e := requestBody(o, strings.NewReader(""))
	if e != nil || !bytes.Contains(body, []byte("123456789012345678901234567890")) || !bytes.Contains(body, []byte(`"name":"new"`)) {
		t.Fatalf("%s %v", body, e)
	}
	for _, raw := range []string{"", "[1,]", `{"a":}`, "01", "true false", `"\x"`, strings.Repeat("[", 130) + "0" + strings.Repeat("]", 130)} {
		if _, e := decodeJSON([]byte(raw)); e == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	for _, raw := range []string{"0s", "-1s", "30", "nan", "1sx", "+1s"} {
		if _, e := duration(raw); e == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	d, e := duration("1m30s")
	if e != nil || d.Seconds() != 90 {
		t.Fatal(d, e)
	}
}
func TestOperationRegistryParity(t *testing.T) {
	source, e := os.ReadFile("../../../../packages/mycontext-web-client/src/xrpc-types.ts")
	if e != nil {
		t.Fatal(e)
	}
	methods := map[string]string{}
	for _, m := range regexp.MustCompile(`"(dev\.mycontextprotocol\.[^"]+)": "(GET|POST)"`).FindAllStringSubmatch(string(source), -1) {
		methods[m[1]] = m[2]
	}
	names := map[string]bool{}
	nsids := map[string]bool{}
	for _, op := range Operations {
		if names[op.Name] {
			t.Fatal("duplicate", op.Name)
		}
		names[op.Name] = true
		if op.NSID != "" {
			if methods[op.NSID] != op.Method || op.Path != "/xrpc/"+op.NSID {
				t.Fatalf("invalid mapping %+v", op)
			}
			nsids[op.NSID] = true
		}
		if op.RESTMethod == "" || op.RESTPath == "" {
			t.Fatalf("missing REST parity %+v", op)
		}
	}
	for nsid := range methods {
		if !strings.Contains(nsid, ".agent.") && nsid != "dev.mycontextprotocol.repository.getSyncJob" && !nsids[nsid] {
			t.Fatal("missing account XRPC", nsid)
		}
	}
	if len(Operations) != 49 {
		t.Fatalf("unexpected registry size %d", len(Operations))
	}
}
func TestDiscoveryAndErrors(t *testing.T) {
	var out, diag bytes.Buffer
	if code := Run(context.Background(), []string{"commands", "--compact"}, nil, strings.NewReader(""), &out, &diag, "test"); code != 0 {
		t.Fatal(code, diag.String())
	}
	var ops []Operation
	if json.Unmarshal(out.Bytes(), &ops) != nil || len(ops) != 49 {
		t.Fatal(out.String())
	}
	out.Reset()
	diag.Reset()
	if code := Run(context.Background(), []string{"--unknown", "--json"}, nil, strings.NewReader(""), &out, &diag, "test"); code != 2 || !strings.Contains(diag.String(), `"code":"invalid_arguments"`) {
		t.Fatal(code, diag.String())
	}
}
