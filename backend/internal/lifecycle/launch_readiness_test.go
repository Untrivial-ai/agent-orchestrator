package lifecycle

import (
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func readinessRecord() domain.SessionRecord {
	return domain.SessionRecord{
		ID: "mer-1", ProjectID: "mer", Activity: domain.Activity{State: domain.ActivityIdle},
		Metadata:        domain.SessionMetadata{RuntimeLaunchID: "launch-1"},
		LaunchReadiness: domain.LaunchReadiness{State: domain.LaunchReadinessLaunching, LaunchID: "launch-1"},
	}
}

func TestMarkSpawnedSeedsLaunchReadiness(t *testing.T) {
	for _, tt := range []struct {
		name                 string
		mode                 domain.SessionMode
		metadata             domain.SessionMetadata
		launch, conversation string
	}{
		{"fresh", domain.SessionModeTUI, domain.SessionMetadata{RuntimeLaunchID: "next"}, "next", ""},
		{"resume", domain.SessionModeTUI, domain.SessionMetadata{RuntimeLaunchID: "next", AgentSessionID: "native", AgentSessionIDLaunchID: "next"}, "next", "native"},
		{"old conversation", domain.SessionModeTUI, domain.SessionMetadata{RuntimeLaunchID: "next", AgentSessionID: "old", AgentSessionIDLaunchID: "previous"}, "next", ""},
		{"chat", domain.SessionModeChat, domain.SessionMetadata{ControllerGeneration: "controller", ProviderConversationID: "thread"}, "controller", "thread"},
		{"no owner", domain.SessionModeTUI, domain.SessionMetadata{}, "", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m, st, _ := newManager()
			rec := readinessRecord()
			rec.Mode = tt.mode
			st.sessions[rec.ID] = rec
			if err := m.MarkSpawned(ctx, rec.ID, tt.metadata); err != nil {
				t.Fatal(err)
			}
			got := st.sessions[rec.ID].LaunchReadiness
			if got.LaunchID != tt.launch || got.ConversationID != tt.conversation || got.Resume != (tt.conversation != "" && tt.mode == domain.SessionModeTUI) {
				t.Fatalf("readiness: %+v", got)
			}
			if tt.launch == "" {
				if got != (domain.LaunchReadiness{}) {
					t.Fatalf("unowned readiness: %+v", got)
				}
			} else if got.State != domain.LaunchReadinessLaunching || got.UpdatedAt.IsZero() {
				t.Fatalf("unseeded readiness: %+v", got)
			}
		})
	}
}

func TestLaunchReadinessConversationFence(t *testing.T) {
	for _, mode := range []domain.SessionMode{domain.SessionModeTUI, domain.SessionModeChat} {
		t.Run(string(mode), func(t *testing.T) {
			m, st, _ := newManager()
			rec := readinessRecord()
			rec.Mode = mode
			if mode == domain.SessionModeChat {
				rec.Metadata.ControllerGeneration = "launch-1"
				rec.Metadata.ProviderConversationID = "native-1"
			}
			st.sessions[rec.ID] = rec
			signal := ports.ActivitySignal{AgentSessionID: "native-1", LaunchID: "launch-1", Event: "session-start"}
			if mode == domain.SessionModeChat {
				signal.LaunchID = ""
				signal.ControllerGeneration = "launch-1"
			}
			if err := m.ApplyActivitySignal(ctx, rec.ID, signal); err != nil {
				t.Fatal(err)
			}
			bound := st.sessions[rec.ID]
			if bound.LaunchReadiness.ConversationID != "native-1" || bound.LaunchReadiness.State != domain.LaunchReadinessLaunching || !bound.FirstSignalAt.IsZero() {
				t.Fatalf("metadata proved readiness: %+v", bound)
			}
			signal.Valid = true
			signal.State = domain.ActivityActive
			signal.AgentSessionID = "wrong-conversation"
			if err := m.ApplyActivitySignal(ctx, rec.ID, signal); err != nil {
				t.Fatal(err)
			}
			if st.sessions[rec.ID].Revision != bound.Revision {
				t.Fatal("mismatched conversation wrote session")
			}
			signal.AgentSessionID = "native-1"
			signal.LaunchID = "stale"
			if mode == domain.SessionModeChat {
				signal.ControllerGeneration = "stale"
			}
			if err := m.ApplyActivitySignal(ctx, rec.ID, signal); err != nil {
				t.Fatal(err)
			}
			if st.sessions[rec.ID].Revision != bound.Revision {
				t.Fatal("stale launch wrote session")
			}
			signal.LaunchID = "launch-1"
			if mode == domain.SessionModeChat {
				signal.LaunchID = ""
				signal.ControllerGeneration = "launch-1"
			}
			if err := m.ApplyActivitySignal(ctx, rec.ID, signal); err != nil {
				t.Fatal(err)
			}
			if got := st.sessions[rec.ID].LaunchReadiness; got.State != domain.LaunchReadinessReady || got.ConversationID != "native-1" {
				t.Fatalf("current conversation not ready: %+v", got)
			}
		})
	}
}

func TestLaunchReadinessTransitions(t *testing.T) {
	for _, tt := range []struct {
		name         string
		initial      domain.LaunchReadinessState
		state        domain.ActivityState
		cause        domain.LaunchFailureCause
		conversation string
		want         domain.LaunchReadinessState
	}{
		{"first activity", domain.LaunchReadinessLaunching, domain.ActivityActive, "", "native", domain.LaunchReadinessReady},
		{"idle proof", domain.LaunchReadinessLaunching, domain.ActivityIdle, "", "native", domain.LaunchReadinessReady},
		{"needs input", domain.LaunchReadinessLaunching, domain.ActivityBlocked, "", "native", domain.LaunchReadinessNeedsInput},
		{"input resolved", domain.LaunchReadinessNeedsInput, domain.ActivityActive, "", "native", domain.LaunchReadinessReady},
		{"normal exit", domain.LaunchReadinessReady, domain.ActivityExited, "", "native", domain.LaunchReadinessReady},
		{"resume crash", domain.LaunchReadinessLaunching, domain.ActivityExited, "", "native", domain.LaunchReadinessLaunchFailed},
		{"diagnosed invalid resume", domain.LaunchReadinessLaunching, domain.ActivityExited, domain.LaunchFailureResumeInvalid, "native", domain.LaunchReadinessResumeInvalid},
		{"diagnosis without conversation", domain.LaunchReadinessLaunching, domain.ActivityExited, domain.LaunchFailureResumeInvalid, "", domain.LaunchReadinessLaunchFailed},
		{"legacy", "", domain.ActivityActive, "", "native", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m, st, _ := newManager()
			rec := readinessRecord()
			rec.LaunchReadiness.State = tt.initial
			rec.LaunchReadiness.Resume = true
			st.sessions[rec.ID] = rec
			signal := ports.ActivitySignal{Valid: true, State: tt.state, LaunchID: "launch-1", AgentSessionID: tt.conversation, LaunchFailureCause: tt.cause}
			if err := m.ApplyActivitySignal(ctx, rec.ID, signal); err != nil {
				t.Fatal(err)
			}
			got := st.sessions[rec.ID]
			if got.LaunchReadiness.State != tt.want || got.IsTerminated {
				t.Fatalf("readiness=%+v terminated=%v", got.LaunchReadiness, got.IsTerminated)
			}
		})
	}
}

func TestLaunchReadinessExitEvidenceArrivalOrders(t *testing.T) {
	for _, runtimeFirst := range []bool{false, true} {
		for _, specificFirst := range []bool{false, true} {
			m, st, _ := newManager()
			rec := readinessRecord()
			st.sessions[rec.ID] = rec
			generic := ports.ActivitySignal{Valid: true, State: domain.ActivityExited, LaunchID: "launch-1"}
			code := 23
			specific := generic
			specific.Event = "process-exited"
			specific.ExitCode = &code
			first, second := generic, specific
			if specificFirst {
				first, second = second, first
			}
			if runtimeFirst {
				if err := m.ApplyRuntimeObservation(ctx, rec.ID, ports.RuntimeFacts{Runtime: ports.ProbeAlive, Workload: ports.ProbeDead, LaunchID: "launch-1"}); err != nil {
					t.Fatal(err)
				}
			}
			for _, s := range []ports.ActivitySignal{first, second} {
				if err := m.ApplyActivitySignal(ctx, rec.ID, s); err != nil {
					t.Fatal(err)
				}
			}
			got := st.sessions[rec.ID].LaunchReadiness
			if got.State != domain.LaunchReadinessLaunchFailed || got.Cause != "exit_code_23" {
				t.Fatalf("runtimeFirst=%v specificFirst=%v: %+v", runtimeFirst, specificFirst, got)
			}
			old := specific
			old.LaunchID = "old-launch"
			zero := 0
			old.ExitCode = &zero
			if err := m.ApplyActivitySignal(ctx, rec.ID, old); err != nil {
				t.Fatal(err)
			}
			if st.sessions[rec.ID].LaunchReadiness != got {
				t.Fatal("stale exit changed readiness")
			}
		}
	}
}

func TestLaunchReadinessNeedsMatchingConversationProof(t *testing.T) {
	m, st, _ := newManager()
	rec := readinessRecord()
	rec.LaunchReadiness.ConversationID = "native"
	st.sessions[rec.ID] = rec
	if err := m.ApplyActivitySignal(ctx, rec.ID, ports.ActivitySignal{Valid: true, State: domain.ActivityActive, LaunchID: "launch-1"}); err != nil {
		t.Fatal(err)
	}
	if st.sessions[rec.ID].LaunchReadiness.State != domain.LaunchReadinessLaunching {
		t.Fatal("unidentified activity proved readiness")
	}
	if err := m.ApplyActivitySignal(ctx, rec.ID, ports.ActivitySignal{Valid: true, State: domain.ActivityActive, AgentSessionID: "native", LaunchID: "launch-1", Timestamp: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if st.sessions[rec.ID].LaunchReadiness.State != domain.LaunchReadinessReady {
		t.Fatal("same-state identified activity did not prove readiness")
	}
}

func TestLaunchReadinessNewConversationBoundary(t *testing.T) {
	m, st, _ := newManager()
	rec := readinessRecord()
	observed := time.Now().UTC().Add(-time.Minute)
	rec.LaunchReadiness.State = domain.LaunchReadinessReady
	rec.LaunchReadiness.ConversationID = "old"
	rec.Metadata.AgentSessionID = "old"
	rec.Metadata.AgentSessionIDLaunchID = "launch-1"
	rec.Metadata.NativeIdentityObservedAt = observed
	st.sessions[rec.ID] = rec
	start := ports.ActivitySignal{Event: "session-start", LaunchID: "launch-1", AgentSessionID: "new", Timestamp: observed.Add(time.Second)}
	if err := m.ApplyActivitySignal(ctx, rec.ID, start); err != nil {
		t.Fatal(err)
	}
	got := st.sessions[rec.ID]
	if got.LaunchReadiness.State != domain.LaunchReadinessLaunching || got.LaunchReadiness.ConversationID != "new" {
		t.Fatalf("new conversation inherited readiness: %+v", got.LaunchReadiness)
	}
	late := ports.ActivitySignal{Valid: true, State: domain.ActivityActive, Event: "user-prompt-submit", LaunchID: "launch-1", AgentSessionID: "old", Timestamp: observed}
	if err := m.ApplyActivitySignal(ctx, rec.ID, late); err != nil {
		t.Fatal(err)
	}
	if st.sessions[rec.ID].Revision != got.Revision {
		t.Fatal("old conversation wrote the replacement")
	}
	late.AgentSessionID = "new"
	late.Timestamp = observed.Add(2 * time.Second)
	if err := m.ApplyActivitySignal(ctx, rec.ID, late); err != nil {
		t.Fatal(err)
	}
	if st.sessions[rec.ID].LaunchReadiness.State != domain.LaunchReadinessReady {
		t.Fatal("replacement did not become ready")
	}
}

func TestLaunchReadinessExplicitDiagnosisArrivalOrders(t *testing.T) {
	for _, explicitFirst := range []bool{false, true} {
		m, st, _ := newManager()
		rec := readinessRecord()
		rec.LaunchReadiness.ConversationID = "native"
		st.sessions[rec.ID] = rec
		code := 1
		generic := ports.ActivitySignal{Valid: true, State: domain.ActivityExited, LaunchID: "launch-1", ExitCode: &code}
		explicit := generic
		explicit.AgentSessionID = "native"
		explicit.LaunchFailureCause = domain.LaunchFailureResumeInvalid
		signals := []ports.ActivitySignal{generic, explicit}
		if explicitFirst {
			signals[0], signals[1] = signals[1], signals[0]
		}
		for _, signal := range signals {
			if err := m.ApplyActivitySignal(ctx, rec.ID, signal); err != nil {
				t.Fatal(err)
			}
		}
		if got := st.sessions[rec.ID].LaunchReadiness; got.State != domain.LaunchReadinessResumeInvalid || got.Cause != "resume_invalid" {
			t.Fatalf("explicitFirst=%v: %+v", explicitFirst, got)
		}
	}
}

func TestLaunchReadinessUncertainRuntimeProbe(t *testing.T) {
	m, st, _ := newManager()
	rec := readinessRecord()
	st.sessions[rec.ID] = rec
	for _, facts := range []ports.RuntimeFacts{
		{Runtime: ports.ProbeFailed, Workload: ports.ProbeDead, LaunchID: "launch-1"},
		{Runtime: ports.ProbeAlive, Workload: ports.ProbeFailed, LaunchID: "launch-1"},
		{Runtime: ports.ProbeAlive, Workload: ports.ProbeDead, LaunchID: "previous"},
	} {
		if err := m.ApplyRuntimeObservation(ctx, rec.ID, facts); err != nil {
			t.Fatal(err)
		}
		if got := st.sessions[rec.ID]; got.LaunchReadiness != rec.LaunchReadiness || got.IsTerminated {
			t.Fatalf("uncertain probe changed readiness: %+v", got)
		}
	}
}
