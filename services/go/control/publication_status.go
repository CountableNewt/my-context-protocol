package control

import "context"

// addPublicationCompletion exposes only the private publication receipt. It does
// not reconcile, resume publication, or imply that remote records were verified.
func (s *Service) addPublicationCompletion(ctx context.Context, account, project string, result row) error {
	if s.cfg.Records == nil {
		return nil
	}
	completed, err := s.cfg.Records.IsCanonical(ctx, account, project)
	if err != nil {
		return err
	}
	result["atproto_publication_completed"] = completed
	return nil
}

// Hosted record storage provides the batch path to avoid per-project Turso HTTP
// calls. Alternate record implementations retain the existing IsCanonical API.
type publicationStatesReader interface {
	PublicationStates(context.Context, string) (map[string]bool, error)
}

func (s *Service) addPublicationCompletions(ctx context.Context, account string, projects []row) error {
	if s.cfg.Records == nil || len(projects) == 0 {
		return nil
	}
	if reader, ok := s.cfg.Records.(publicationStatesReader); ok {
		states, err := reader.PublicationStates(ctx, account)
		if err != nil {
			return err
		}
		for _, project := range projects {
			project["atproto_publication_completed"] = states[str(project["id"])]
		}
		return nil
	}
	for _, project := range projects {
		if err := s.addPublicationCompletion(ctx, account, str(project["id"]), project); err != nil {
			return err
		}
	}
	return nil
}
