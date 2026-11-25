package domainrecords

import (
	"context"
	"testing"

	"github.com/CountableNewt/my-context-protocol/services/go/turso"
)

func TestExternalProjectionKeepsProofOnlyForSameDomainAndRepository(t *testing.T) {
	ctx := context.Background()
	remote, _, records := fixture(t)
	if _, err := remote.DB.Execute(ctx, statement("UPDATE projects SET custom_domain='old.example.com' WHERE id='project'")); err != nil {
		t.Fatal(err)
	}
	review, err := remote.Inventory(ctx, account, owner, "project")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = remote.Publish(ctx, account, review, approved(review)); err != nil {
		t.Fatal(err)
	}
	local, db, _ := fixture(t)
	local.Records = records
	if err = local.Import(ctx, account, owner, "project", approved(review)); err != nil {
		t.Fatal(err)
	}
	_, err = db.Batch(ctx, []turso.Statement{
		statement("UPDATE projects SET custom_domain_verified_at=123,custom_domain_verification_token='old-proof' WHERE id='project'"),
		statement("UPDATE repo_connections SET token_ref='ref',token_encrypted='credential',github_installation_id=99,webhook_id='hook',webhook_secret='secret' WHERE id='repo'"),
	})
	if err != nil {
		t.Fatal(err)
	}
	// An unrelated remote metadata edit keeps existing authorization proofs.
	if _, err = remote.Update(ctx, account, owner, "project", []Change{{Table: "projects", ID: "project", Values: map[string]turso.Value{"name": turso.Text("Renamed")}}}); err != nil {
		t.Fatal(err)
	}
	if err = local.Reconcile(ctx, account, owner, "project"); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Execute(ctx, statement("SELECT custom_domain_verified_at,custom_domain_verification_token FROM projects WHERE id='project'"))
	if err != nil {
		t.Fatal(err)
	}
	if rows.Rows[0][0].Value == nil || rows.Rows[0][1].Value != "old-proof" {
		t.Fatal("same domain lost proof")
	}
	rows, err = db.Execute(ctx, statement("SELECT token_encrypted,github_installation_id FROM repo_connections WHERE id='repo'"))
	if err != nil {
		t.Fatal(err)
	}
	if rows.Rows[0][0].Value != "credential" || rows.Rows[0][1].Value == nil {
		t.Fatal("same repository lost credentials")
	}
	// The owner now changes public identities on the PDS. Their previous private
	// proofs cannot authorize a different tenant host or repository.
	if _, err = remote.Update(ctx, account, owner, "project", []Change{
		{Table: "projects", ID: "project", Values: map[string]turso.Value{"custom_domain": turso.Text("new.example.com")}},
		{Table: "repo_connections", ID: "repo", Values: map[string]turso.Value{"repo_owner": turso.Text("different-owner")}},
	}); err != nil {
		t.Fatal(err)
	}
	if err = local.Reconcile(ctx, account, owner, "project"); err != nil {
		t.Fatal(err)
	}
	rows, err = db.Execute(ctx, statement("SELECT custom_domain_verified_at,custom_domain_verification_token FROM projects WHERE id='project'"))
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range rows.Rows[0] {
		if v.Value != nil {
			t.Fatal("changed domain inherited verification proof")
		}
	}
	rows, err = db.Execute(ctx, statement("SELECT id FROM projects WHERE custom_domain='new.example.com' AND custom_domain_verified_at IS NOT NULL"))
	if err != nil || len(rows.Rows) != 0 {
		t.Fatal("changed host accepted as verified tenant")
	}
	rows, err = db.Execute(ctx, statement("SELECT token_ref,token_encrypted,github_installation_id,webhook_id,webhook_secret FROM repo_connections WHERE id='repo'"))
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range rows.Rows[0] {
		if v.Value != nil {
			t.Fatal("changed repository inherited credentials")
		}
	}
}
