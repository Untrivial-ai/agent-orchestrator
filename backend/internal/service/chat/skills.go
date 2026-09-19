package chat

import (
	"context"
	"errors"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// ErrSkillsUnsupported reports a driver whose provider cannot enumerate skills.
// Distinct from an empty list: "this agent has no concept of skills" and "this
// agent has none installed" are the same thing to render but not the same thing
// to be wrong about, and only the first is permanent.
var ErrSkillsUnsupported = errors.New("chat driver cannot list skills")

// Skills reports the named skills the provider will let this session invoke.
//
// Read from the live conversation for the same reason models are: skills come from
// the user's own Codex config and the repo's own files, both of which change
// without AO being told. A list AO cached at build time would offer commands that
// no longer exist and hide ones the user just wrote.
func (s *Service) Skills(ctx context.Context, id domain.SessionID) ([]ports.ChatSkill, error) {
	if _, err := s.requireChatSession(ctx, id); err != nil {
		return nil, err
	}
	controller, err := s.Controller(id)
	if err != nil {
		return nil, err
	}
	lister, ok := controller.conv.(ports.ChatSkillLister)
	if !ok {
		return nil, ErrSkillsUnsupported
	}
	skills, err := lister.ListSkills(ctx)
	if err != nil {
		return nil, err
	}
	if len(skills) > 0 {
		return skills, nil
	}
	// Empty means one of two things and the live conversation cannot tell them
	// apart: the provider said "none", or it has not said anything yet. ACP only
	// ever pushes its catalog -- on session/new and on commands_changed, never on
	// reattach -- so a controller that took over a surviving provider answers empty
	// for the rest of the session with no way to ask again. The last catalog AO
	// wrote down is the better answer, superseded the moment the provider pushes.
	// A provider that genuinely has none wrote an empty list, so this stays empty.
	record, err := s.store.ConversationForSession(ctx, id)
	if err != nil {
		// Reported rather than swallowed into an empty list: "AO could not read its
		// own row" and "this agent has no skills" render identically, and only one
		// of them is worth retrying.
		return nil, err
	}
	return persistedSkills(record), nil
}

// persistedSkills converts the stored catalog back to the driver's shape.
func persistedSkills(record domain.ConversationRecord) []ports.ChatSkill {
	out := make([]ports.ChatSkill, 0, len(record.Skills))
	for _, skill := range record.Skills {
		out = append(out, ports.ChatSkill{
			Name:        skill.Name,
			DisplayName: skill.DisplayName,
			Description: skill.Description,
			InputHint:   skill.InputHint,
			Source:      skill.Source,
		})
	}
	return out
}
