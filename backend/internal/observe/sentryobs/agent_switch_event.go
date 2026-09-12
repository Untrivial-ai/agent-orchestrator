package sentryobs

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const (
	// AgentSwitchCanonicalEventMaxBytes bounds the provider payload persisted in the outbox.
	AgentSwitchCanonicalEventMaxBytes = 60 << 10
	// AgentSwitchEnvelopeEncodingV1 identifies the initial immutable Sentry envelope encoding.
	AgentSwitchEnvelopeEncodingV1 = 1
)

var canonicalEventIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// AgentSwitchEventEncoder constructs the immutable Sentry event bytes stored
// in the outbox. Provider field names and encoding versions live here.
type AgentSwitchEventEncoder struct{}

// EncodeAgentSwitchFailureEvent validates and encodes one normalized failure event.
func (AgentSwitchEventEncoder) EncodeAgentSwitchFailureEvent(input domain.AgentSwitchEventBuildInput) (ports.AgentSwitchFailureEncodedEvent, error) {
	raw, err := BuildAgentSwitchCanonicalEvent(input)
	if err != nil {
		return ports.AgentSwitchFailureEncodedEvent{}, err
	}
	return ports.AgentSwitchFailureEncodedEvent{EnvelopeEncodingVersion: AgentSwitchEnvelopeEncodingV1, Payload: raw}, nil
}

// BuildAgentSwitchCanonicalEvent validates input and returns a bounded,
// deterministic Sentry event payload.
func BuildAgentSwitchCanonicalEvent(input domain.AgentSwitchEventBuildInput) ([]byte, error) {
	if !canonicalEventIDPattern.MatchString(input.EventID) {
		return nil, errors.New("event ID must be exactly 32 lowercase hexadecimal characters")
	}
	if err := domain.ValidateAgentSwitchFault(input.Fault); err != nil {
		return nil, err
	}
	if err := domain.ValidateAgentSwitchEventMetadata(domain.AgentSwitchEventMetadata{
		Release: input.Release, Environment: input.Environment, Channel: input.Channel,
		Platform: input.Platform, OS: input.OS, ElapsedTimeBucket: input.ElapsedTimeBucket,
	}); err != nil {
		return nil, err
	}

	entry, _ := domain.AgentSwitchFailureTaxonomy(input.Fault.FailurePoint)
	frames := make([]canonicalAgentSwitchFrame, len(input.Fault.Frames))
	for i, frame := range input.Fault.Frames {
		frames[i] = canonicalAgentSwitchFrame{
			Module: frame.Package, Function: frame.Function,
			Filename: frame.Filename, Lineno: frame.Line, InApp: true,
		}
	}
	event := canonicalAgentSwitchEvent{
		EventID: input.EventID, Timestamp: input.Fault.OccurredAt.UTC().Format(time.RFC3339Nano),
		Message:  "Agent switch failed: " + string(input.Fault.Mode) + " / " + string(input.Fault.Phase) + " / " + string(input.Fault.FailurePoint),
		Level:    string(domain.AgentSwitchSeverityForFault(input.Fault, entry.DefaultSeverity)),
		Platform: input.Platform, Environment: input.Environment, Release: input.Release,
		Exception: canonicalAgentSwitchExceptions{Values: []canonicalAgentSwitchException{{
			Type: "AgentSwitchFailure", Value: "agent switch failure: " + domain.AgentSwitchFailureCode(input.Fault) + " at " + string(input.Fault.FailurePoint),
			Stacktrace: canonicalAgentSwitchStacktrace{Frames: frames},
		}}},
		Fingerprint: domain.AgentSwitchIssueFingerprint(input.Fault),
		Tags: canonicalAgentSwitchTags{
			Feature: "agent_switching", Platform: input.Platform, ReportKind: input.Fault.ReportKind,
			Subsystem: entry.Subsystem, Mode: input.Fault.Mode, Phase: input.Fault.Phase,
			FailurePoint: input.Fault.FailurePoint, ErrorCode: input.Fault.ErrorCode,
			FaultCode: input.Fault.FaultCode, Execution: input.Fault.Execution,
			FromHarness: input.Fault.FromHarness, TargetHarness: input.Fault.TargetHarness,
			TargetStartMode: input.Fault.TargetStartMode, RuntimeBackend: input.Fault.RuntimeBackend,
			CallOutcome: input.Fault.CallOutcome, Ownership: input.Fault.Ownership,
			Compensation: input.Fault.Compensation, UserImpact: input.Fault.UserImpact,
			Release: input.Release, ClassifierCallsite: input.Fault.ClassifierCallsite,
			Channel: input.Channel, OS: input.OS,
		},
		Contexts: canonicalAgentSwitchContexts{AgentSwitch: canonicalAgentSwitchContext{
			SourceStopConfirmed: input.Fault.SourceStopConfirmed, TargetOwnerCommitted: input.Fault.TargetOwnerCommitted,
			GateRetained: input.Fault.GateRetained, ElapsedTimeBucket: input.ElapsedTimeBucket,
		}},
	}
	raw, err := json.Marshal(event)
	if err != nil {
		return nil, fmt.Errorf("marshal canonical agent switch event: %w", err)
	}
	if len(raw) > AgentSwitchCanonicalEventMaxBytes {
		return nil, errors.New("canonical agent switch event exceeds 60 KiB")
	}
	return raw, nil
}

type canonicalAgentSwitchEvent struct {
	EventID     string                         `json:"event_id"`
	Timestamp   string                         `json:"timestamp"`
	Message     string                         `json:"message"`
	Level       string                         `json:"level"`
	Platform    domain.AgentSwitchPlatform     `json:"platform"`
	Environment domain.AgentSwitchEnvironment  `json:"environment"`
	Release     string                         `json:"release"`
	Exception   canonicalAgentSwitchExceptions `json:"exception"`
	Fingerprint []string                       `json:"fingerprint"`
	Tags        canonicalAgentSwitchTags       `json:"tags"`
	Contexts    canonicalAgentSwitchContexts   `json:"contexts"`
}
type canonicalAgentSwitchExceptions struct {
	Values []canonicalAgentSwitchException `json:"values"`
}
type canonicalAgentSwitchException struct {
	Type       string                         `json:"type"`
	Value      string                         `json:"value"`
	Stacktrace canonicalAgentSwitchStacktrace `json:"stacktrace"`
}
type canonicalAgentSwitchStacktrace struct {
	Frames []canonicalAgentSwitchFrame `json:"frames"`
}
type canonicalAgentSwitchFrame struct {
	Module   string `json:"module"`
	Function string `json:"function"`
	Filename string `json:"filename"`
	Lineno   int    `json:"lineno"`
	InApp    bool   `json:"in_app"`
}
type canonicalAgentSwitchTags struct {
	Feature            string                               `json:"feature"`
	Platform           domain.AgentSwitchPlatform           `json:"platform"`
	ReportKind         domain.AgentSwitchReportKind         `json:"report_kind"`
	Subsystem          string                               `json:"subsystem"`
	Mode               domain.SessionMode                   `json:"mode"`
	Phase              domain.AgentSwitchState              `json:"phase"`
	FailurePoint       domain.AgentSwitchFailurePoint       `json:"failure_point"`
	ErrorCode          domain.AgentSwitchErrorCode          `json:"error_code"`
	FaultCode          domain.AgentSwitchFaultCode          `json:"fault_code"`
	Execution          domain.AgentSwitchExecution          `json:"execution"`
	FromHarness        domain.AgentHarness                  `json:"from_harness"`
	TargetHarness      domain.AgentHarness                  `json:"target_harness"`
	TargetStartMode    domain.AgentSwitchTargetStartMode    `json:"target_start_mode"`
	RuntimeBackend     domain.AgentSwitchRuntimeBackend     `json:"runtime_backend"`
	CallOutcome        domain.AgentSwitchCallOutcome        `json:"call_outcome"`
	Ownership          domain.AgentSwitchOwnership          `json:"ownership"`
	Compensation       domain.AgentSwitchCompensation       `json:"compensation"`
	UserImpact         domain.AgentSwitchUserImpact         `json:"user_impact"`
	Release            string                               `json:"release"`
	ClassifierCallsite domain.AgentSwitchClassifierCallsite `json:"classifier_callsite"`
	Channel            domain.AgentSwitchChannel            `json:"channel"`
	OS                 domain.AgentSwitchOS                 `json:"os"`
}
type canonicalAgentSwitchContexts struct {
	AgentSwitch canonicalAgentSwitchContext `json:"agent_switch"`
}
type canonicalAgentSwitchContext struct {
	SourceStopConfirmed  domain.AgentSwitchTriState          `json:"source_stop_confirmed"`
	TargetOwnerCommitted domain.AgentSwitchTriState          `json:"target_owner_committed"`
	GateRetained         domain.AgentSwitchTriState          `json:"gate_retained"`
	ElapsedTimeBucket    domain.AgentSwitchElapsedTimeBucket `json:"elapsed_time_bucket"`
}

var _ ports.AgentSwitchFailureEventEncoder = AgentSwitchEventEncoder{}
