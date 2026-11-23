package domainrecords

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/CountableNewt/my-context-protocol/services/go/turso"
)

type readOnlyAuditDB struct{ Database }

func (d readOnlyAuditDB) Execute(ctx context.Context, s turso.Statement) (*turso.Result, error) {
	if !strings.HasPrefix(strings.TrimSpace(s.SQL), "SELECT ") {
		return nil, errors.New("audit attempted write")
	}
	return d.Database.Execute(ctx, s)
}
func (d readOnlyAuditDB) Batch(context.Context, []turso.Statement) ([]turso.Result, error) {
	return nil, errors.New("audit attempted batch")
}
func TestVerifyReconstructsWithoutAnyProjectionOrPDSWrites(t *testing.T) {
	e, db, pds := fixture(t)
	ctx := context.Background()
	review, err := e.Inventory(ctx, account, owner, "project")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.Publish(ctx, account, review, approved(review)); err != nil {
		t.Fatal(err)
	}
	puts := pds.puts
	e.DB = readOnlyAuditDB{db}
	verified, err := e.Verify(ctx, account, owner, "project")
	if err != nil || verified.Digest != review.Digest || pds.puts != puts {
		t.Fatal(verified.Digest, err, pds.puts)
	}
	if _, err = e.Verify(ctx, "other", owner, "project"); err == nil {
		t.Fatal("cross-account audit accepted")
	}
	_, err = db.Execute(ctx, statement("UPDATE projects SET name='external local drift' WHERE id='project'"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.Verify(ctx, account, owner, "project"); !errors.Is(err, ErrChanged) {
		t.Fatal("drift hidden", err)
	}
}
