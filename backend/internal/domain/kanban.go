package domain

import "github.com/aoagents/agent-orchestrator/backend/pkg/contract"

// KanbanColumn is the derived delivery-lifecycle placement of a session:
// building, the AO-driven validating loop, the review-feedback loop
// (needs_review), ready, or archive.
type KanbanColumn = contract.KanbanColumn

// Kanban columns.
const (
	KanbanBuilding    = contract.KanbanBuilding
	KanbanValidating  = contract.KanbanValidating
	KanbanNeedsReview = contract.KanbanNeedsReview
	KanbanReady       = contract.KanbanReady
	KanbanArchive     = contract.KanbanArchive
)

// DisplayStatus is the phrase shown inside a session's Kanban column: what is
// happening right now at the stage the column already placed it in.
type DisplayStatus = contract.DisplayStatus

// KanbanPresentation is a session's derived board presentation: its column plus
// the display status derived inside that column.
type KanbanPresentation = contract.KanbanPresentation
