package domain

import "fmt"

// WorkflowMode is the user-controlled delivery stage of a session: where its
// work sits between task creation and merge. Unlike SessionMode (which
// conversation controller runs the session), WorkflowMode is a board
// placement: a session is "planning" until a user (or a build-mode
// orchestrator) pushes it into "building".
//
//   - WorkflowModePlanning: the session is being scoped/designed before work
//     starts. This is the default for every new task; the board shows it in the
//     Planning lane.
//   - WorkflowModeBuilding: the session is executing — writing code, opening
//     PRs, and moving through review to merge. The board shows it in the
//     Building lane (then Review and Ready as PR facts arrive).
type WorkflowMode string

// The workflow modes.
const (
	WorkflowModePlanning WorkflowMode = "planning"
	WorkflowModeBuilding WorkflowMode = "building"
)

// DefaultWorkflowMode is what a new session gets unless a build-mode
// orchestrator spawns it straight into building. Planning is always the
// starting point: work is scoped before it is executed.
const DefaultWorkflowMode = WorkflowModePlanning

// Valid reports whether mode is one AO knows how to place on the board.
func (m WorkflowMode) Valid() bool {
	switch m {
	case WorkflowModePlanning, WorkflowModeBuilding:
		return true
	default:
		return false
	}
}

// NormalizeWorkflowMode collapses an empty or unrecognized mode to the
// default. Use it when reading durable state: a row written before this
// feature, or by a newer build that knows a mode this one does not, must
// still land somewhere safe (Planning).
func NormalizeWorkflowMode(mode WorkflowMode) WorkflowMode {
	if mode.Valid() {
		return mode
	}
	return DefaultWorkflowMode
}

// ParseWorkflowMode converts caller-supplied input into a mode, strictly. An
// empty string means "no mode requested" and yields the zero value with no
// error, so callers can distinguish absent from invalid and apply their own
// precedence. Anything else unrecognized is an error: a request that named a
// mode AO cannot place must fail loudly rather than downgrade.
func ParseWorkflowMode(raw string) (WorkflowMode, error) {
	if raw == "" {
		return "", nil
	}
	mode := WorkflowMode(raw)
	if !mode.Valid() {
		return "", fmt.Errorf("unknown workflow mode %q: want %q or %q", raw, WorkflowModePlanning, WorkflowModeBuilding)
	}
	return mode, nil
}
