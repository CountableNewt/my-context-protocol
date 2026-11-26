package recordservice

import (
	"context"
	"testing"

	"github.com/CountableNewt/my-context-protocol/services/go/turso"
)

func TestPublicationStatesUsesPrivateAccountReceipts(t *testing.T) {
	f := fixtureSQL(t, false)
	_, err := f.db.Execute(context.Background(), turso.Statement{SQL: `INSERT INTO domain_canonical_projects(project_id,account_id,owner_did,manifest_uri,manifest_cid,review_digest,public_consent,telemetry_consent,hydrated,updated_at) SELECT 'foreign','other',owner_did,manifest_uri,manifest_cid,review_digest,public_consent,telemetry_consent,hydrated,updated_at FROM domain_canonical_projects WHERE project_id='project'`})
	if err != nil {
		t.Fatal(err)
	}
	puts := f.pds.puts
	states, err := f.service.PublicationStates(context.Background(), "account")
	if err != nil || len(states) != 1 || !states["project"] || states["foreign"] || states["not-published"] {
		t.Fatalf("states=%v error=%v", states, err)
	}
	other, err := f.service.PublicationStates(context.Background(), "other")
	if err != nil || len(other) != 1 || !other["foreign"] || other["project"] || f.pds.puts != puts {
		t.Fatalf("foreign states=%v error=%v", other, err)
	}
}
