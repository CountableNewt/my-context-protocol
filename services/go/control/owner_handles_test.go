package control

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestProjectOwnerHandleDisplayIsRequestDeduplicatedAndFailSoft(t *testing.T) {
	s, _ := fixture(t)
	did := "did:plc:aaaaaaaaaaaaaaaaaaaaaaaa"
	mustExec(t, s, "INSERT INTO projects(id,account_id,name,slug,subdomain) VALUES('second',?,'Second','second','second')", ownerID)
	mustExec(t, s, "INSERT INTO repo_connections(id,project_id,provider,repo_owner,repo_name,default_branch,auth_type) VALUES('one',?,'tangled',?,'skills','main','atproto'),('two','second','tangled',?,'other','main','atproto')", projectID, did, did)
	lookups := 0
	s.cfg.ResolveOwnerHandle = func(ctx context.Context, owner string) (string, error) {
		lookups++
		if owner != did {
			t.Fatalf("unexpected owner %q", owner)
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 2*time.Second {
			t.Fatal("lookup has no bounded deadline")
		}
		return "owner.example.com", nil
	}
	list := call(t, s, "GET", "/projects", ownerCookie, "", "")
	expectStatus(t, list, 200)
	var projects []map[string]any
	if err := json.Unmarshal(list.Body.Bytes(), &projects); err != nil {
		t.Fatal(err)
	}
	if lookups != 1 || len(projects) != 2 {
		t.Fatalf("lookups %d projects %d", lookups, len(projects))
	}
	for _, p := range projects {
		c := p["repo_connection"].(map[string]any)
		if c["owner_handle"] != "owner.example.com" || c["repo_owner"] != did {
			t.Fatalf("display changed identity %+v", c)
		}
	}
	detail := call(t, s, "GET", "/projects/"+projectID+"/repo-connection", ownerCookie, "", "")
	expectStatus(t, detail, 200)
	if object(t, detail)["owner_handle"] != "owner.example.com" || object(t, detail)["repo_owner"] != did {
		t.Fatal("connection display missing")
	}
	s.cfg.ResolveOwnerHandle = func(context.Context, string) (string, error) { return "", errors.New("offline") }
	detail = call(t, s, "GET", "/projects/"+projectID, ownerCookie, "", "")
	expectStatus(t, detail, 200)
	c := object(t, detail)["repo_connection"].(map[string]any)
	if _, ok := c["owner_handle"]; ok || c["repo_owner"] != did {
		t.Fatalf("resolution failure lost DID fallback %+v", c)
	}
}
func TestOwnerHandleLookupCachesFailureAndStopsAfterBudget(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	h := &ownerHandleLookup{ctx: ctx, values: map[string]string{}, lookup: func(context.Context, string) (string, error) { calls++; return "", errors.New("offline") }}
	defer h.close()
	if h.get("first") != "" || h.get("first") != "" || calls != 1 {
		t.Fatal("failed lookup was repeated")
	}
	cancel()
	if h.get("second") != "" || calls != 1 {
		t.Fatal("lookup continued after cancellation")
	}
}
