package recordservice

import (
	"context"
	"errors"
	"testing"

	"github.com/CountableNewt/my-context-protocol/services/go/domainrecords"
	"github.com/CountableNewt/my-context-protocol/services/go/turso"
)

func TestScopeBlocksInvalidAuditBeforeOAuthOrNativeResume(t *testing.T) {
	f := fixtureSQL(t, false)
	_, err := f.db.Execute(context.Background(), turso.Statement{SQL: `INSERT INTO domain_project_audits(project_id,owner_did,manifest_cid,repo_head,cycle_id,status,invalid,checked_at) SELECT project_id,owner_did,manifest_cid,'head','cycle','invalid',1,0 FROM domain_canonical_projects WHERE project_id='project'`})
	if err != nil {
		t.Fatal(err)
	}
	// No OAuth manager is configured. The private audit gate must reject this
	// before attempting account credentials or resuming a durable mutation.
	if _, err = f.service.Scope(context.Background(), "account", "project"); !errors.Is(err, domainrecords.ErrAuditInvalid) {
		t.Fatalf("scope did not honor audit gate: %v", err)
	}
}
