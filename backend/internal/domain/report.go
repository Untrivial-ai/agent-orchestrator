package domain

import (
	"errors"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

// MaxReportNoteCharacters is the hard limit for free-form and structured notes.
const MaxReportNoteCharacters = 1000

// ReportType identifies the worker progress fact being reported.
type ReportType string

// Supported report types.
const (
	ReportFreeForm   ReportType = "free_form"
	ReportPRCreated  ReportType = "pr_created"
	ReportArtifact   ReportType = "artifact"
	ReportCheckpoint ReportType = "checkpoint"
	ReportNeedsInput ReportType = "needs_input"
	ReportStuck      ReportType = "stuck"
	ReportDone       ReportType = "done"
)

// Valid reports whether t is a supported report type.
func (t ReportType) Valid() bool {
	switch t {
	case ReportFreeForm, ReportPRCreated, ReportArtifact, ReportCheckpoint, ReportNeedsInput, ReportStuck, ReportDone:
		return true
	default:
		return false
	}
}

// ReportDeliveryState is the durable outbox lifecycle of a report.
type ReportDeliveryState string

// Report delivery states.
const (
	ReportPending      ReportDeliveryState = "pending"
	ReportClaimed      ReportDeliveryState = "claimed"
	ReportAcknowledged ReportDeliveryState = "acknowledged"
)

// ReportRecord is the durable report and outbox persistence shape.
type ReportRecord struct {
	ID               string
	SessionID        SessionID
	ProjectID        ProjectID
	Type             ReportType
	Note             string
	CreatedAt        time.Time
	DeliveryState    ReportDeliveryState
	AvailableAt      time.Time
	ClaimToken       string
	ClaimedAt        time.Time
	DeliveryAttempts int64
	AcknowledgedAt   time.Time
	LastError        string
}

// ErrInvalidReport reports an invalid report or delivery transition input.
var ErrInvalidReport = errors.New("invalid report")

// Validate checks report content, ownership fields, and delivery-state invariants.
func (r ReportRecord) Validate() error {
	if r.ID == "" || r.SessionID == "" || r.ProjectID == "" || !r.Type.Valid() || r.CreatedAt.IsZero() || r.AvailableAt.IsZero() {
		return ErrInvalidReport
	}
	if err := ValidateReportNote(r.Type, r.Note); err != nil {
		return err
	}
	switch r.DeliveryState {
	case ReportPending:
		if r.ClaimToken != "" || !r.ClaimedAt.IsZero() || !r.AcknowledgedAt.IsZero() {
			return ErrInvalidReport
		}
	case ReportClaimed:
		if r.ClaimToken == "" || r.ClaimedAt.IsZero() || !r.AcknowledgedAt.IsZero() {
			return ErrInvalidReport
		}
	case ReportAcknowledged:
		if r.ClaimToken == "" || r.ClaimedAt.IsZero() || r.AcknowledgedAt.IsZero() {
			return ErrInvalidReport
		}
	default:
		return ErrInvalidReport
	}
	if r.DeliveryAttempts < 0 {
		return ErrInvalidReport
	}
	return nil
}

// ValidateReportNote checks the common note rules and type-specific PR URL rule.
func ValidateReportNote(typ ReportType, note string) error {
	if !typ.Valid() || strings.TrimSpace(note) == "" || utf8.RuneCountInString(note) > MaxReportNoteCharacters {
		return ErrInvalidReport
	}
	if typ == ReportPRCreated && !IsGitHubPullRequestURL(note) {
		return ErrInvalidReport
	}
	return nil
}

// IsGitHubPullRequestURL reports whether raw is an HTTP(S) github.com PR URL.
func IsGitHubPullRequestURL(raw string) bool {
	if strings.TrimSpace(raw) != raw {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || !strings.EqualFold(u.Hostname(), "github.com") || u.Port() != "" || u.User != nil {
		return false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 4 || parts[0] == "" || parts[1] == "" || parts[2] != "pull" || parts[3] == "" {
		return false
	}
	for _, c := range parts[3] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
