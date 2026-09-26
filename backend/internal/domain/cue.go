package domain

import (
	"errors"
	"strings"
	"time"
)

// CueID identifies a cue.
type CueID string

// CueType distinguishes a command cue from an agent prompt cue.
type CueType string

// Cue types.
const (
	// CueTypeCommand runs a shell command in a dedicated terminal without an agent.
	CueTypeCommand CueType = "command"
	// CueTypeAgent sends a prompt through an agent session.
	CueTypeAgent CueType = "agent"
)

// Valid reports whether t is a supported cue type.
func (t CueType) Valid() bool {
	return t == CueTypeCommand || t == CueTypeAgent
}

// Cue definition size bounds. Byte lengths are used consistently with the
// rest of the codebase; prompts reuse the spawn prompt budget so any agent cue
// can always be delivered as a session seed without further truncation.
const (
	// MaxCueNameLength bounds the user-visible cue name.
	MaxCueNameLength = 64
	// MaxCueDescriptionLength bounds the optional cue description.
	MaxCueDescriptionLength = 240
	// MaxCueCommandLength bounds a command cue's shell command.
	MaxCueCommandLength = 4 << 10
	// MaxCuePromptLength bounds an agent cue's prompt. It matches the spawn
	// prompt cap so a valid prompt is always spawnable as a worker seed.
	MaxCuePromptLength = 16 << 10
)

var (
	// ErrInvalidCueType reports an unknown cue type.
	ErrInvalidCueType = errors.New("invalid cue type")
	// ErrInvalidCueName reports a missing or oversized cue name.
	ErrInvalidCueName = errors.New("invalid cue name")
	// ErrInvalidCueDescription reports an oversized cue description.
	ErrInvalidCueDescription = errors.New("invalid cue description")
	// ErrInvalidCueCommand reports a command cue without a command or with an
	// oversized command.
	ErrInvalidCueCommand = errors.New("invalid cue command")
	// ErrInvalidCuePrompt reports an agent cue without a prompt or with an
	// oversized prompt.
	ErrInvalidCuePrompt = errors.New("invalid cue prompt")
	// ErrCueNameExists reports a cue name already taken within the same project.
	ErrCueNameExists = errors.New("cue name already exists")
	// ErrProjectUnknown reports a cue operation against a project that is not
	// registered.
	ErrProjectUnknown = errors.New("project not found")
)

// Cue is a reusable, project-associated quick action owned and managed by the
// user. Definitions live in AO-managed application data keyed by project; the
// project repository is never modified by cue operations.
type Cue struct {
	ID          CueID
	ProjectID   ProjectID
	Name        string
	Description string
	Type        CueType
	// Command is the shell command for a command cue. It is unused for agent cues.
	Command string
	// Prompt is the agent instruction for an agent cue. It is unused for
	// command cues.
	Prompt    string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Validate checks the required fields, enum values, and size bounds of a cue
// definition. It does not check name uniqueness, which requires store context
// and is enforced by the service layer.
func (c Cue) Validate() error {
	if strings.TrimSpace(c.Name) == "" || len(c.Name) > MaxCueNameLength {
		return ErrInvalidCueName
	}
	if len(c.Description) > MaxCueDescriptionLength {
		return ErrInvalidCueDescription
	}
	if !c.Type.Valid() {
		return ErrInvalidCueType
	}
	if len(c.Command) > MaxCueCommandLength {
		return ErrInvalidCueCommand
	}
	if len(c.Prompt) > MaxCuePromptLength {
		return ErrInvalidCuePrompt
	}
	switch c.Type {
	case CueTypeCommand:
		if strings.TrimSpace(c.Command) == "" || len(c.Command) > MaxCueCommandLength {
			return ErrInvalidCueCommand
		}
	case CueTypeAgent:
		if strings.TrimSpace(c.Prompt) == "" || len(c.Prompt) > MaxCuePromptLength {
			return ErrInvalidCuePrompt
		}
	}
	return nil
}
