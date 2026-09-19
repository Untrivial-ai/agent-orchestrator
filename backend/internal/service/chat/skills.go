package chat

import (
	"context"
	"errors"
	"fmt"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// ErrSkillsUnsupported reports a driver whose provider cannot enumerate skills.
// Distinct from an empty list: "this agent has no concept of skills" and "this
// agent has none installed" are the same thing to render but not the same thing
// to be wrong about, and only the first is permanent.
var ErrSkillsUnsupported = errors.New("chat driver cannot list skills")

// ErrSkillsUnavailable reports a driver that can enumerate skills in general but
// whose provider call failed this time (e.g. Codex app-server's `skills/list`
// returning a JSON-RPC error). Distinct from ErrSkillsUnsupported: this is a
// live, usually transient provider fault, not a permanent "this agent has no
// concept of skills". The wrapped cause is the raw error the provider returned,
// kept intact via %w so the caller can log or surface it rather than it being
// flattened into a generic 500.
var ErrSkillsUnavailable = errors.New("chat driver could not list skills")

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
		// The provider is reachable (feature-detection above succeeded) but this
		// particular call failed, so the raw cause is a provider fault worth
		// keeping, not an AO bug to hide behind "Internal server error".
		return nil, fmt.Errorf("%w: %w", ErrSkillsUnavailable, err)
	}
	return skills, nil
}
