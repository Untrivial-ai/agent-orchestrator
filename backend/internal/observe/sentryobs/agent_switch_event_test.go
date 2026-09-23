package sentryobs

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestAgentSwitchEventEncoderMatchesFrozenFixture(t *testing.T) {
	input := fixtureAgentSwitchEventInput()
	encoded, err := (AgentSwitchEventEncoder{}).EncodeAgentSwitchFailureEvent(input)
	if err != nil {
		t.Fatalf("encode event: %v", err)
	}
	if encoded.EnvelopeEncodingVersion != AgentSwitchEnvelopeEncodingV1 {
		t.Fatalf("encoding version = %d", encoded.EnvelopeEncodingVersion)
	}
	want, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "test", "fixtures", "agent-switch-observability", "envelope-v1.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if !bytes.Equal(encoded.Payload, bytes.TrimSpace(want)) {
		t.Fatalf("encoded event does not match frozen fixture:\n%s", encoded.Payload)
	}
	for _, forbidden := range []string{"local-attempt-never-exported", "session-", "switch-", "/Users/", "https://"} {
		if bytes.Contains(encoded.Payload, []byte(forbidden)) {
			t.Fatalf("encoded event contains forbidden value %q", forbidden)
		}
	}
}

func TestAgentSwitchEventEncoderRejectsInvalidProviderMetadata(t *testing.T) {
	input := fixtureAgentSwitchEventInput()
	input.EventID = "ABC"
	if _, err := (AgentSwitchEventEncoder{}).EncodeAgentSwitchFailureEvent(input); err == nil {
		t.Fatal("invalid Sentry event ID was accepted")
	}
	input = fixtureAgentSwitchEventInput()
	input.Release = "https://release.invalid"
	if _, err := (AgentSwitchEventEncoder{}).EncodeAgentSwitchFailureEvent(input); err == nil {
		t.Fatal("invalid release metadata was accepted")
	}
}

func fixtureAgentSwitchEventInput() domain.AgentSwitchEventBuildInput {
	return domain.AgentSwitchEventBuildInput{
		EventID: "0123456789abcdef0123456789abcdef",
		Release: "1.2.3", Environment: domain.AgentSwitchEnvironmentStable,
		Channel: domain.AgentSwitchChannelStable, Platform: domain.AgentSwitchPlatformDaemon,
		OS: domain.AgentSwitchOSDarwin, ElapsedTimeBucket: domain.AgentSwitchElapsedUnder30Seconds,
		Fault: domain.AgentSwitchFault{
			ReportKind:         domain.AgentSwitchReportTerminalFailure,
			FailurePoint:       domain.AgentSwitchFailureChatTargetActivationCommit,
			ClassifierCallsite: domain.AgentSwitchClassifierExecuteChat,
			Phase:              domain.AgentSwitchStartingTarget, ErrorCode: domain.AgentSwitchErrorTargetReadyFailed,
			FaultCode: domain.AgentSwitchFaultNotApplicable, Execution: domain.AgentSwitchExecutionLive,
			ExecutionAttemptID: "local-attempt-never-exported", Mode: domain.SessionModeChat,
			FromHarness: domain.HarnessClaudeCode, TargetHarness: domain.HarnessCodex,
			TargetStartMode:      domain.AgentSwitchTargetStartResumed,
			RuntimeBackend:       domain.AgentSwitchRuntimeChatController,
			CallOutcome:          domain.AgentSwitchCallCommittedResponseLost,
			Ownership:            domain.AgentSwitchOwnershipTarget,
			Compensation:         domain.AgentSwitchCompensationNotNeeded,
			UserImpact:           domain.AgentSwitchUserImpactTargetUnavailable,
			SourceStopConfirmed:  domain.AgentSwitchTriTrue,
			TargetOwnerCommitted: domain.AgentSwitchTriTrue,
			GateRetained:         domain.AgentSwitchTriFalse,
			OccurredAt:           time.Date(2026, 8, 28, 1, 2, 3, 456789000, time.FixedZone("fixture", 5*60*60)),
			Frames: []domain.AgentSwitchStackFrame{{
				Package: "internal/session_manager", Function: "executeChatAgentSwitch",
				Filename: "backend/internal/session_manager/agent_switching_chat.go", Line: 742,
			}},
		},
	}
}
