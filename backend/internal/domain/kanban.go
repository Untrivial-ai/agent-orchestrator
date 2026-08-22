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
