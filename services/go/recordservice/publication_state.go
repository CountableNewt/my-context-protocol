package recordservice

import (
	"context"
	"errors"

	"github.com/CountableNewt/my-context-protocol/services/go/turso"
)

// PublicationStates reads private completion receipts for one account in a
// single query. A receipt is not proof that remote records remain intact.
func (s *Service) PublicationStates(ctx context.Context, account string) (map[string]bool, error) {
	if s == nil || s.DB == nil || account == "" {
		return nil, errors.New("account and record database are required")
	}
	r, err := s.DB.Execute(ctx, turso.Statement{SQL: "SELECT project_id FROM domain_canonical_projects WHERE account_id=? AND public_consent=1", Args: []turso.Value{turso.Text(account)}, WantRows: true})
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, errors.New("invalid canonical publication state")
	}
	states := map[string]bool{}
	for _, row := range r.Rows {
		if len(row) != 1 || row[0].Type != "text" {
			return nil, errors.New("invalid canonical publication state")
		}
		project, ok := row[0].Value.(string)
		if !ok || project == "" {
			return nil, errors.New("invalid canonical publication state")
		}
		states[project] = true
	}
	return states, nil
}
