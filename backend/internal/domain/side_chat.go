package domain

import (
	"encoding/json"
	"time"
)

// SideConversation is one launch-scoped /btw provider conversation. Its
// provider host and transcript are independent of the main conversation.
type SideConversation struct {
	ID                 string       `json:"id"`
	SessionID          SessionID    `json:"sessionId"`
	MainConversationID string       `json:"mainConversationId"`
	AppRunID           string       `json:"-"`
	CreateKey          string       `json:"-"`
	ProviderHostID     string       `json:"-"`
	ProviderForkID     string       `json:"-"`
	AnchorTurnID       string       `json:"anchorTurnId,omitempty"`
	ContextMode        string       `json:"contextMode"`
	NativeAnchorID     string       `json:"-"`
	SourceMessageID    string       `json:"sourceMessageId,omitempty"`
	SourceRevision     int64        `json:"sourceRevision,omitempty"`
	SelectedText       string       `json:"selectedText,omitempty"`
	ReferencePending   bool         `json:"-"`
	ReferenceContext   string       `json:"-"`
	SeedHistory        string       `json:"-"`
	Harness            AgentHarness `json:"-"`
	Model              string       `json:"model,omitempty"`
	Effort             string       `json:"effort,omitempty"`
	Generation         string       `json:"-"`
	Label              string       `json:"label"`
	State              string       `json:"state"`
	ErrorMessage       string       `json:"errorMessage,omitempty"`
	CleanupPending     bool         `json:"cleanupPending,omitempty"`
	CreatedAt          time.Time    `json:"createdAt"`
	UpdatedAt          time.Time    `json:"updatedAt"`
	ClosedAt           *time.Time   `json:"-"`
}

// SideTurn records one question and its execution state in a side conversation.
type SideTurn struct {
	ID               string          `json:"id"`
	SideID           string          `json:"sideId"`
	ClientMessageID  string          `json:"clientMessageId"`
	Text             string          `json:"text"`
	Content          []SideContent   `json:"-"`
	References       []SideReference `json:"references,omitempty"`
	SelectionText    string          `json:"selectionText,omitempty"`
	ReferenceContext string          `json:"referenceContext,omitempty"`
	ProviderTurnID   string          `json:"providerTurnId,omitempty"`
	RetryOfTurnID    string          `json:"retryOfTurnId,omitempty"`
	State            string          `json:"state"`
	ErrorMessage     string          `json:"errorMessage,omitempty"`
	CreatedAt        time.Time       `json:"createdAt"`
	StartedAt        *time.Time      `json:"startedAt,omitempty"`
	CompletedAt      *time.Time      `json:"completedAt,omitempty"`
}

// SideReference is verified and frozen when its question is accepted.
type SideReference struct {
	ConversationID string `json:"conversationId"`
	MessageID      string `json:"messageId"`
	Revision       int64  `json:"revision"`
	Selection      string `json:"selection"`
	SourceRole     string `json:"sourceRole"`
	Context        string `json:"context,omitempty"`
}

// SideContent records structured input attached to a side turn.
type SideContent struct {
	Type     string `json:"type"`
	MIMEType string `json:"mimeType,omitempty"`
	Data     string `json:"data,omitempty"`
	URI      string `json:"uri,omitempty"`
	Name     string `json:"name,omitempty"`
	Text     string `json:"text,omitempty"`
}

// SideMessage records a visible message produced in a side conversation.
type SideMessage struct {
	ID             string    `json:"id"`
	SideID         string    `json:"sideId"`
	TurnID         string    `json:"turnId"`
	ProviderItemID string    `json:"-"`
	Role           string    `json:"role"`
	Text           string    `json:"text"`
	Sequence       int64     `json:"sequence"`
	Revision       int64     `json:"revision"`
	Streaming      bool      `json:"streaming"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

// SideSnapshot contains a page of side turns and their visible messages.
type SideSnapshot struct {
	Side       SideConversation `json:"side"`
	Turns      []SideTurn       `json:"turns"`
	Messages   []SideMessage    `json:"messages"`
	Activities []SideActivity   `json:"activities"`
	HasMore    bool             `json:"hasMore"`
}

// SideDecision describes an available response to a side approval request.
type SideDecision struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Kind  string `json:"kind"`
	Raw   []byte `json:"-"`
}

// SideActivity records a side turn activity such as a command or approval.
type SideActivity struct {
	ID             string          `json:"id"`
	SideID         string          `json:"sideId"`
	TurnID         string          `json:"turnId"`
	ProviderItemID string          `json:"-"`
	Kind           string          `json:"kind"`
	Status         string          `json:"status"`
	Summary        string          `json:"summary"`
	Text           string          `json:"text,omitempty"`
	Detail         json.RawMessage `json:"detail,omitempty"`
	RequestID      string          `json:"requestId,omitempty"`
	Decisions      []SideDecision  `json:"decisions,omitempty"`
	Input          *SideInput      `json:"input,omitempty"`
	CreatedAt      time.Time       `json:"createdAt"`
}

// SideInput describes input requested by a side provider.
type SideInput struct {
	Mode    string         `json:"mode"`
	Message string         `json:"message"`
	URL     string         `json:"url,omitempty"`
	Schema  map[string]any `json:"schema,omitempty"`
}

// SideProviderCleanup identifies a registered provider fork pending deletion.
type SideProviderCleanup struct {
	ForkID    string
	SessionID SessionID
}
