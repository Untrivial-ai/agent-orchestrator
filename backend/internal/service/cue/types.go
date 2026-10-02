// Package cue exposes the project-scoped quick action (Cue) lifecycle to REST
// controllers.
package cue

import (
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/shellterm"
)

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

// InvokeInput identifies the optional session target and local shell selection.
type InvokeInput struct {
	SessionID          domain.SessionID
	Shell              string
	AllowDirectCommand bool
}

// InvokeResult describes either the agent destination or opened command terminal.
type InvokeResult struct {
	Kind      domain.CueType
	SessionID domain.SessionID
	Terminal  *shellterm.ShellTerminal
}
