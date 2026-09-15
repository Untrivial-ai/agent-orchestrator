package lifecycle

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/fx/herdr"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestActivitySignalExpectedHarnessFencesStateAndMetadata(t *testing.T) {
	for _, state := range []bool{false, true} {
		store := newFakeStore()
		store.sessions["mer-1"] = domain.SessionRecord{
			ID: "mer-1", Harness: domain.HarnessCodex,
			Activity: domain.Activity{State: domain.ActivityIdle},
			Metadata: domain.SessionMetadata{RuntimeLaunchID: "launch-1", AgentSessionID: "codex-native"},
		}
		m := New(store, nil)
		if err := m.ApplyActivitySignal(ctx, "mer-1", ports.ActivitySignal{
			Valid: state, State: domain.ActivityActive, LaunchID: "launch-1",
			AgentSessionID: "fx-native", ExpectedHarness: domain.HarnessFX,
		}); err != nil {
			t.Fatal(err)
		}
		got := store.sessions["mer-1"]
		if got.Activity.State != domain.ActivityIdle || got.Metadata.AgentSessionID != "codex-native" {
			t.Fatalf("fx report changed another harness: %+v", got)
		}
	}
}

func TestHerdrSocketPersistsNativeMetadataAndFencesRuntimeGenerations(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fx has no native Windows support")
	}
	dir, err := os.MkdirTemp("/tmp", "ao-herdr-lifecycle-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	store := newFakeAgentSwitchLifecycleStore()
	store.setSession(domain.SessionRecord{ID: "mer-1", Harness: domain.HarnessFX,
		Activity: domain.Activity{State: domain.ActivityIdle}, Metadata: domain.SessionMetadata{RuntimeLaunchID: "launch-1"}})
	store.setSession(domain.SessionRecord{ID: "mer-2", Harness: domain.HarnessFX,
		Activity: domain.Activity{State: domain.ActivityIdle}, Metadata: domain.SessionMetadata{RuntimeLaunchID: "other-launch"}})
	m := New(store, nil)
	server, err := herdr.Start(context.Background(), dir, m, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Stop()
	report := func(session domain.SessionID, launch, method, key, value string) {
		t.Helper()
		line, err := json.Marshal(map[string]any{"id": "7", "method": method, "params": map[string]string{
			"pane_id": herdr.PaneID(session, launch), "source": "custom:fx", "agent": "fx", key: value,
		}})
		if err != nil {
			t.Fatal(err)
		}
		conn, err := net.Dial("unix", herdr.SocketPath(dir))
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(time.Second))
		if _, err := fmt.Fprintln(conn, string(line)); err != nil {
			t.Fatal(err)
		}
		var reply struct {
			OK bool `json:"ok"`
		}
		if err := json.NewDecoder(conn).Decode(&reply); err != nil || !reply.OK {
			t.Fatalf("report=%s reply=%+v err=%v", line, reply, err)
		}
	}
	report("mer-1", "launch-1", "pane.report_agent_session", "agent_session_id", "native-fx-42")
	got := store.session("mer-1")
	if got.Metadata.AgentSessionID != "native-fx-42" || got.Metadata.AgentSessionIDLaunchID != "launch-1" || got.Activity.State != domain.ActivityIdle || !got.FirstSignalAt.IsZero() {
		t.Fatalf("metadata-only report changed activity or lost native id: %+v", got)
	}
	for _, tc := range []struct {
		state string
		want  domain.ActivityState
	}{{"working", domain.ActivityActive}, {"blocked", domain.ActivityBlocked}, {"idle", domain.ActivityWaitingInput}} {
		report("mer-1", "launch-1", "pane.report_agent", "state", tc.state)
		if got := store.session("mer-1"); got.Activity.State != tc.want {
			t.Fatalf("state %s mapped to %s", tc.state, got.Activity.State)
		}
	}
	// Restore uses this lifecycle commit boundary to install a replacement
	// runtime. Old fx processes retain their old pane identity.
	if err := m.PrepareLaunch("mer-1", "launch-2"); err != nil {
		t.Fatal(err)
	}
	if err := m.MarkSpawned(context.Background(), "mer-1", domain.SessionMetadata{RuntimeHandleID: "runtime-2", RuntimeLaunchID: "launch-2"}); err != nil {
		t.Fatal(err)
	}
	report("mer-1", "launch-2", "pane.report_agent", "state", "working")
	before := store.session("mer-1")
	report("mer-1", "launch-1", "pane.report_agent", "state", "blocked")
	report("mer-1", "launch-1", "pane.report_agent_session", "agent_session_id", "stale-native")
	if got := store.session("mer-1"); got != before {
		t.Fatalf("old-generation report mutated restored session: %+v", got)
	}
	report("mer-2", "launch-2", "pane.report_agent", "state", "blocked")
	if got := store.session("mer-2"); got.Activity.State != domain.ActivityIdle {
		t.Fatalf("report crossed session identity: %+v", got)
	}
	before.Harness = domain.HarnessCodex
	store.setSession(before)
	report("mer-1", "launch-2", "pane.report_agent", "state", "blocked")
	if got := store.session("mer-1"); got != before {
		t.Fatalf("report crossed harness identity: %+v", got)
	}
}

func TestActivitySignalExpectedHarnessFencesPendingNativeMetadata(t *testing.T) {
	store := newFakeAgentSwitchLifecycleStore()
	ref := domain.AgentNativeSessionID("target-native")
	store.native[ref] = domain.AgentNativeSession{
		ID: ref, AOSessionID: "mer-1", Harness: domain.HarnessCodex, LastGenerationID: "target-generation",
	}
	store.setSession(domain.SessionRecord{ID: "mer-1", Harness: domain.HarnessClaudeCode,
		Metadata: domain.SessionMetadata{RuntimeLaunchID: "source-generation"}})
	store.setActiveSwitch(domain.AgentSwitch{ID: "switch-1", SessionID: "mer-1",
		TargetHarness: domain.HarnessCodex, State: domain.AgentSwitchStartingTarget,
		TargetGenerationID: "target-generation", TargetNativeSessionRef: &ref})
	m := New(store, nil)
	if err := m.ApplyActivitySignal(ctx, "mer-1", ports.ActivitySignal{
		LaunchID: "target-generation", AgentSessionID: "fx-native", ExpectedHarness: domain.HarnessFX,
	}); err != nil {
		t.Fatal(err)
	}
	if got := store.native[ref].NativeSessionID; got != "" {
		t.Fatalf("fx report staged metadata for another target harness: %q", got)
	}
}

// Current reports must survive the startup barrier. Validating the current
// source harness before ReleaseLaunch would drop the new fx process's report.
func TestActivitySignalExpectedHarnessWaitsForOwnershipTransfer(t *testing.T) {
	store := newFakeAgentSwitchLifecycleStore()
	store.setSession(domain.SessionRecord{ID: "mer-1", Harness: domain.HarnessCodex,
		Metadata: domain.SessionMetadata{RuntimeLaunchID: "old-generation"}})
	m := New(store, nil)
	if err := m.PrepareLaunch("mer-1", "fx-generation"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- m.ApplyActivitySignal(ctx, "mer-1", ports.ActivitySignal{
			Valid: true, State: domain.ActivityWaitingInput, LaunchID: "fx-generation",
			AgentSessionID: "fx-native", ExpectedHarness: domain.HarnessFX,
		})
	}()
	select {
	case err := <-done:
		t.Fatalf("report returned before ownership transfer: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	store.setSession(domain.SessionRecord{ID: "mer-1", Harness: domain.HarnessFX,
		Metadata: domain.SessionMetadata{RuntimeLaunchID: "fx-generation"}})
	m.ReleaseLaunch("mer-1", "fx-generation")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	got := store.session("mer-1")
	if got.Metadata.AgentSessionID != "fx-native" || got.Activity.State != domain.ActivityWaitingInput {
		t.Fatalf("current fx report lost after ownership transfer: %+v", got)
	}
}
