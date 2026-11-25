package control

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/CountableNewt/my-context-protocol/services/go/turso"
)

func TestProjectConnectionSummary(t *testing.T) {
	for _, provider := range []string{"github", "tangled"} {
		t.Run(provider, func(t *testing.T) {
			s, db := fixture(t)
			// The other account's connection must never appear in the owner's list.
			for _, stmt := range []turso.Statement{
				{SQL: "INSERT INTO projects(id,account_id,name,slug,subdomain) VALUES('other-project',?,'Other','other','other')", Args: values(otherID)},
				{SQL: "INSERT INTO repo_connections(id,project_id,provider,repo_owner,repo_name,default_branch,auth_type,token_encrypted) VALUES('private-other','other-project','github','private','secret','main','oauth','secret-token')"},
			} {
				if _, err := db.Execute(context.Background(), stmt); err != nil {
					t.Fatal(err)
				}
			}
			assertSummary := func(want any) {
				t.Helper()
				detail := call(t, s, "GET", "/projects/"+projectID, ownerCookie, "", "")
				expectStatus(t, detail, 200)
				list := call(t, s, "GET", "/projects", ownerCookie, "", "")
				expectStatus(t, list, 200)
				var projects []map[string]any
				if err := json.Unmarshal(list.Body.Bytes(), &projects); err != nil {
					t.Fatal(err)
				}
				if len(projects) != 1 {
					t.Fatalf("unexpected project list: %s", list.Body)
				}
				for _, p := range []map[string]any{object(t, detail), projects[0]} {
					summary, exists := p["repo_connection"]
					if !exists || !reflect.DeepEqual(summary, want) {
						t.Fatalf("connection = %#v, want %#v", summary, want)
					}
				}
			}
			assertSummary(nil)
			owner := "owner"
			auth := "oauth"
			if provider == "tangled" {
				owner = "did:plc:abcdefghijklmnopqrstuvwx"
				auth = "atproto"
			}
			if _, err := db.Execute(context.Background(), turso.Statement{SQL: "INSERT INTO repo_connections(id,project_id,provider,repo_owner,repo_name,default_branch,auth_type,created_at,token_encrypted,webhook_secret) VALUES('a',?,?,?,?,?,?,1,'secret-token','secret-webhook'),('z',?,'github','later','ignored','dev','oauth',1,NULL,NULL)", Args: values(projectID, provider, owner, "skills", "main", auth, projectID)}); err != nil {
				t.Fatal(err)
			}
			// A timestamp tie must produce one project and the same stable connection.
			assertSummary(map[string]any{"provider": provider, "repo_owner": owner, "repo_name": "skills", "default_branch": "main"})
			if _, err := db.Execute(context.Background(), turso.Statement{SQL: "DELETE FROM repo_connections WHERE project_id=?", Args: values(projectID)}); err != nil {
				t.Fatal(err)
			}
			assertSummary(nil)
		})
	}
}
