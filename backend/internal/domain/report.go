package domain

import (
	"errors"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// MaxReportTextCharacters is the hard limit for free-form messages and structured notes.
	MaxReportTextCharacters = 1000
	// ReportBatchFallback is the fixed informational-report fallback deadline.
	ReportBatchFallback = time.Hour
	// ReportSettlementWindow is the fixed window opened by a done report.
	ReportSettlementWindow = 5 * time.Minute
)

// ReportState is an optional worker-originated coordination claim.
type ReportState string

// Supported report states.
const (
	ReportCheckpoint ReportState = "checkpoint"
	ReportNeedsInput ReportState = "needs_input"
	ReportStuck      ReportState = "stuck"
	ReportDone       ReportState = "done"
)

// Valid reports whether s is a supported structured state.
func (s ReportState) Valid() bool {
	switch s {
	case ReportCheckpoint, ReportNeedsInput, ReportStuck, ReportDone:
		return true
	default:
		return false
	}
}

// ReportOutputKind identifies a durable output reference attached to a report.
type ReportOutputKind string

// Supported report output kinds.
const (
	ReportOutputArtifact   ReportOutputKind = "artifact"
	ReportOutputPRCreated  ReportOutputKind = "pr_created"
	ReportOutputPRReviewed ReportOutputKind = "pr_reviewed"
)

// Valid reports whether k is a supported output kind.
func (k ReportOutputKind) Valid() bool {
	switch k {
	case ReportOutputArtifact, ReportOutputPRCreated, ReportOutputPRReviewed:
		return true
	default:
		return false
	}
}

// ReportOutput is an ordered reference to work produced by a worker.
type ReportOutput struct {
	Kind      ReportOutputKind
	Reference string
	Label     string
}

// ReportDeliveryState is the durable outbox lifecycle of a report.
type ReportDeliveryState string

// Report delivery states.
const (
	ReportPending      ReportDeliveryState = "pending"
	ReportClaimed      ReportDeliveryState = "claimed"
	ReportAcknowledged ReportDeliveryState = "acknowledged"
)

// ReportRecord is the durable report and outbox persistence shape. State and
// outputs are independent, and neither changes authoritative session state.
type ReportRecord struct {
	ID                 string
	SessionID          SessionID
	ProjectID          ProjectID
	State              ReportState
	Note               string
	Message            string
	Outputs            []ReportOutput
	CreatedAt          time.Time
	DeliveryState      ReportDeliveryState
	AvailableAt        time.Time
	SettlementDeadline time.Time
	RepeatCount        int64
	ClaimToken         string
	ClaimedAt          time.Time
	DeliveryAttempts   int64
	AcknowledgedAt     time.Time
	LastError          string
}

// ErrInvalidReport reports an invalid report or delivery transition input.
var ErrInvalidReport = errors.New("invalid report")

// Validate checks content, ownership, output, and delivery-state invariants.
func (r ReportRecord) Validate() error {
	if r.ID == "" || r.SessionID == "" || r.ProjectID == "" || r.CreatedAt.IsZero() || r.AvailableAt.IsZero() || r.RepeatCount < 1 {
		return ErrInvalidReport
	}
	if err := ValidateReportContent(r.State, r.Note, r.Message, r.Outputs); err != nil {
		return err
	}
	if r.State == ReportDone {
		if r.SettlementDeadline.IsZero() {
			return ErrInvalidReport
		}
	} else if !r.SettlementDeadline.IsZero() {
		return ErrInvalidReport
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

// ValidateReportContent checks the authoritative report payload contract.
func ValidateReportContent(state ReportState, note, message string, outputs []ReportOutput) error {
	if message != "" {
		if state != "" || note != "" || len(outputs) != 0 || strings.TrimSpace(message) == "" || utf8.RuneCountInString(message) > MaxReportTextCharacters {
			return ErrInvalidReport
		}
		return nil
	}
	if state == "" {
		if note != "" || len(outputs) == 0 {
			return ErrInvalidReport
		}
	} else if !state.Valid() || strings.TrimSpace(note) == "" || utf8.RuneCountInString(note) > MaxReportTextCharacters {
		return ErrInvalidReport
	}
	for _, output := range outputs {
		if !output.Kind.Valid() || strings.TrimSpace(output.Reference) == "" {
			return ErrInvalidReport
		}
		if (output.Kind == ReportOutputPRCreated || output.Kind == ReportOutputPRReviewed) && !IsGitHubPullRequestURL(output.Reference) {
			return ErrInvalidReport
		}
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
