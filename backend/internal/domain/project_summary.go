package domain

import "time"

// ProjectSummary is the replaceable, daemon-owned briefing for one project.
// SourceWatermark belongs to this consumer and never changes report delivery state.
type ProjectSummary struct {
	ProjectID        ProjectID              `json:"projectId"`
	Narrative        string                 `json:"narrative"`
	ActiveWorkers    int                    `json:"activeWorkers"`
	CompletedWorkers int                    `json:"completedWorkers"`
	NeedsAttention   []ProjectAttentionItem `json:"needsAttention"`
	SourceWatermark  string                 `json:"sourceWatermark"`
	GeneratedAt      time.Time              `json:"generatedAt"`
	GenerationError  string                 `json:"generationError,omitempty"`
}

// ProjectAttentionItem is a worker question that needs a user decision.
type ProjectAttentionItem struct {
	SessionID   SessionID `json:"sessionId"`
	SessionName string    `json:"sessionName"`
	Question    string    `json:"question"`
}
