package managedagents

import (
	"context"
)

func (s *Service) listAllSkills(ctx context.Context, principal Principal, limit int, page string) ([]Skill, *string, bool, error) {
	return s.repo.ListSkills(ctx, principal.TeamID, limit, page, "custom")
}
