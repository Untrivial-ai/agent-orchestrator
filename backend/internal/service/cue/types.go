// Package cue exposes the project-scoped quick action (Cue) lifecycle to REST
// controllers.
package cue

import "github.com/aoagents/agent-orchestrator/backend/internal/domain"

// Input is the editable cue definition shared by create and update. Update
// replaces the whole definition, including the type switch between command and
// agent, so every field is carried in both directions.
type Input struct {
	Name        string
	Description string
	Type        domain.CueType
	Command     string
	Prompt      string
}
