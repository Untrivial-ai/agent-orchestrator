package lifecycle

import (
	"fmt"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func seedLaunchReadiness(rec domain.SessionRecord, metadata domain.SessionMetadata, now time.Time) domain.LaunchReadiness {
	launchID := strings.TrimSpace(metadata.RuntimeLaunchID)
	conversationID := ""
	resume := false
	if domain.NormalizeSessionMode(rec.Mode) == domain.SessionModeChat {
		launchID = strings.TrimSpace(metadata.ControllerGeneration)
		conversationID = strings.TrimSpace(metadata.ProviderConversationID)
		resume = conversationID != "" && conversationID == rec.Metadata.ProviderConversationID
	} else if metadata.AgentSessionIDLaunchID == launchID {
		conversationID = strings.TrimSpace(metadata.AgentSessionID)
		resume = conversationID != ""
	}
	if launchID == "" {
		return domain.LaunchReadiness{}
	}
	return domain.LaunchReadiness{
		State: domain.LaunchReadinessLaunching, LaunchID: launchID,
		ConversationID: conversationID, Resume: resume, UpdatedAt: now,
	}
}

func signalOwnsLaunch(rec domain.SessionRecord, s ports.ActivitySignal) bool {
	if rec.LaunchReadiness.LaunchID == "" {
		return false
	}
	if domain.NormalizeSessionMode(rec.Mode) == domain.SessionModeChat {
		return rec.LaunchReadiness.LaunchID == rec.Metadata.ControllerGeneration &&
			s.ControllerGeneration == rec.Metadata.ControllerGeneration
	}
	return rec.LaunchReadiness.LaunchID == rec.Metadata.RuntimeLaunchID &&
		s.LaunchID == rec.Metadata.RuntimeLaunchID
}

func readinessConversation(rec domain.SessionRecord, s ports.ActivitySignal) string {
	if s.AgentSessionID != "" {
		return s.AgentSessionID
	}
	// Internal observations are already fenced by controller ownership or the
	// pre-probe revision; public hooks must supply their own conversation identity.
	if domain.NormalizeSessionMode(rec.Mode) == domain.SessionModeChat {
		return rec.Metadata.ProviderConversationID
	}
	if s.ExpectedRevision != nil && rec.Metadata.AgentSessionIDLaunchID == s.LaunchID {
		return rec.Metadata.AgentSessionID
	}
	return ""
}

func readinessConversationMismatch(rec domain.SessionRecord, s ports.ActivitySignal) bool {
	r := rec.LaunchReadiness
	if r.State == "" || !signalOwnsLaunch(rec, s) {
		return false
	}
	conversation := readinessConversation(rec, s)
	return r.ConversationID != "" && conversation != "" && r.ConversationID != conversation
}

// applyLaunchReadiness runs inside the activity projection, including its CAS retry.
func applyLaunchReadiness(rec *domain.SessionRecord, s ports.ActivitySignal, now time.Time) bool {
	before := rec.LaunchReadiness
	if before.State == "" || !signalOwnsLaunch(*rec, s) {
		return false
	}
	conversation := readinessConversation(*rec, s)
	if before.ConversationID != "" && conversation != "" && before.ConversationID != conversation {
		return false
	}
	if before.ConversationID == "" && conversation != "" {
		rec.LaunchReadiness.ConversationID = conversation
	}
	if !s.Valid || before.State == domain.LaunchReadinessReady {
		return rec.LaunchReadiness != before
	}
	if s.State == domain.ActivityExited {
		cause := launchExitCause(s)
		if cause == string(domain.LaunchFailureResumeInvalid) &&
			(conversation == "" || rec.LaunchReadiness.ConversationID == "") {
			cause = "process_exited"
		}
		applyLaunchFailure(rec, cause, now)
		return rec.LaunchReadiness != before
	}
	if before.ConversationID != "" && conversation == "" {
		return rec.LaunchReadiness != before
	}
	switch s.State {
	case domain.ActivityActive, domain.ActivityIdle:
		rec.LaunchReadiness.State = domain.LaunchReadinessReady
		rec.LaunchReadiness.Cause = ""
		rec.LaunchReadiness.UpdatedAt = now
	case domain.ActivityBlocked, domain.ActivityWaitingInput:
		if before.Unresolved() {
			rec.LaunchReadiness.State = domain.LaunchReadinessNeedsInput
			rec.LaunchReadiness.UpdatedAt = now
		}
	}
	return rec.LaunchReadiness != before
}

func applyLaunchFailure(rec *domain.SessionRecord, cause string, now time.Time) {
	r := &rec.LaunchReadiness
	if !r.Unresolved() {
		if r.State != domain.LaunchReadinessLaunchFailed && r.State != domain.LaunchReadinessResumeInvalid {
			return
		}
		if failureCauseRank(cause) <= failureCauseRank(r.Cause) {
			return
		}
	}
	r.State = domain.LaunchReadinessLaunchFailed
	if cause == string(domain.LaunchFailureResumeInvalid) {
		r.State = domain.LaunchReadinessResumeInvalid
	}
	r.Cause = cause
	r.UpdatedAt = now
}

func failureCauseRank(cause string) int {
	switch cause {
	case string(domain.LaunchFailureResumeInvalid):
		return 3
	case string(domain.LaunchFailureProcessStartFailed):
		return 2
	case "", "process_exited", "workload_exited":
		return 0
	default:
		return 1
	}
}

func launchExitCause(s ports.ActivitySignal) string {
	if s.LaunchFailureCause != "" && s.LaunchFailureCause.Valid() {
		return string(s.LaunchFailureCause)
	}
	if s.ExitCode != nil && *s.ExitCode >= 0 {
		return fmt.Sprintf("exit_code_%d", *s.ExitCode)
	}
	return "process_exited"
}
