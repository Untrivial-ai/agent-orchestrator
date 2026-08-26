package domain

import (
	"errors"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

const MaxReportNoteCharacters = 1000

type ReportType string

const (
	ReportFreeForm   ReportType = "free_form"
	ReportPRCreated  ReportType = "pr_created"
	ReportArtifact   ReportType = "artifact"
	ReportCheckpoint ReportType = "checkpoint"
	ReportNeedsInput ReportType = "needs_input"
	ReportStuck      ReportType = "stuck"
	ReportDone       ReportType = "done"
)

func (t ReportType) Valid() bool {
	switch t {
	case ReportFreeForm, ReportPRCreated, ReportArtifact, ReportCheckpoint, ReportNeedsInput, ReportStuck, ReportDone:
		return true
	default:
		return false
	}
}

type ReportDeliveryState string

const (
	ReportPending      ReportDeliveryState = "pending"
	ReportClaimed      ReportDeliveryState = "claimed"
	ReportAcknowledged ReportDeliveryState = "acknowledged"
)

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

var ErrInvalidReport = errors.New("invalid report")

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

func ValidateReportNote(typ ReportType, note string) error {
	if !typ.Valid() || strings.TrimSpace(note) == "" || utf8.RuneCountInString(note) > MaxReportNoteCharacters {
		return ErrInvalidReport
	}
	if typ == ReportPRCreated && !IsGitHubPullRequestURL(note) {
		return ErrInvalidReport
	}
	return nil
}

func IsGitHubPullRequestURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || !strings.EqualFold(u.Hostname(), "github.com") || u.User != nil {
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
