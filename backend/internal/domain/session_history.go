package domain

import "time"

// SessionHistoryFilter selects a bounded keyset page of stopped sessions.
type SessionHistoryFilter struct {
	ProjectID   ProjectID
	Kind        SessionKind
	Delivery    string
	Query       string
	SinceEpoch  int64
	BeforeEpoch int64
	BeforeID    SessionID
	Limit       int
}

// SessionHistoryEntry carries the durable stop time separately from its stable
// ordering key. Pre-migration sessions have a nil StoppedAt and sort by their
// creation time; that ordering key is not represented as a stop time.
type SessionHistoryEntry struct {
	ID        SessionID
	StoppedAt *time.Time
	SortEpoch int64
}
