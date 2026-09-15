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
	Outputs          []ProjectSummaryOutput `json:"outputs"`
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

// ProjectSummaryOutput is a meaningful persisted link surfaced by the project briefing.
type ProjectSummaryOutput struct {
	SessionID   SessionID `json:"sessionId"`
	SessionName string    `json:"sessionName"`
	Kind        string    `json:"kind"`
	Reference   string    `json:"reference,omitempty"`
	Label       string    `json:"label,omitempty"`
	URL         string    `json:"url,omitempty"`
	Number      int       `json:"number,omitempty"`
	State       string    `json:"state,omitempty"`
}
