package sessionmanager

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/lifecycle"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestInterfaceTransitionPromptlessHookCannotAuthorizeFreshConversation(t *testing.T) {
	for _, tc := range []struct {
		name      string
		events    []string
		origin    domain.ConversationCheckpointOrigin
		state     domain.ConversationCheckpointState
		unsettled bool
	}{
		{name: "submitted prompt", events: []string{"user-prompt-submit"},
			origin: domain.ConversationCheckpointOriginHuman, state: domain.ConversationCheckpointPrompt},
		{name: "completed turn", events: []string{"user-prompt-submit", "stop"},
			origin: domain.ConversationCheckpointOriginHuman, state: domain.ConversationCheckpointComplete},
		{name: "unpaired stop", events: []string{"stop"}, unsettled: true},
		{name: "coordination turn", events: []string{"user-prompt-submit"},
			origin: domain.ConversationCheckpointOriginCoordination, state: domain.ConversationCheckpointCoordination},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			manager, store, runtime, _, log := newTransitionManager(t, domain.SessionModeTUI)
			useFastInterfaceTransitionTimings(manager)
			manager.agents = singleAgent{agent: untouchedEmptyTransitionAgent{}}
			recorder := lifecycle.New(&transitionLifecycleStore{transitionStore: store}, nil)
			for _, event := range tc.events {
				state := domain.ActivityActive
				if event == "stop" {
					state = domain.ActivityIdle
				}
				if err := recorder.ApplyActivitySignal(ctx, "session-1", ports.ActivitySignal{
					Valid: true, State: state, Event: event,
					LaunchID: "old-tui-generation", AgentSessionID: "native-1",
					ConversationCheckpointOrigin: tc.origin, Timestamp: time.Now().UTC(),
				}); err != nil {
					t.Fatal(err)
				}
			}
			rec := store.sessions["session-1"]
			if rec.Metadata.LatestUserPrompt != "" || rec.Metadata.LatestAssistantUpdate != "" ||
				rec.Metadata.ConversationCheckpointState != tc.state ||
				rec.Metadata.ConversationCheckpointUnsettled != tc.unsettled ||
				(tc.origin == domain.ConversationCheckpointOriginHuman && rec.Metadata.LatestUserPromptAt.IsZero()) {
				t.Fatalf("promptless provider hook did not create the expected turn evidence: %+v", rec.Metadata)
			}

			transition, err := manager.StartInterfaceTransition(ctx, rec.ID, domain.SessionModeChat,
				domain.SessionInterfaceTransitionDrain, domain.SessionInterfaceTransitionHistoryStrict)
			if err == nil {
				settled := awaitTransition(t, store, transition.ID)
				t.Fatalf("promptless turn was admitted as a fresh conversation: %+v", settled)
			}
			if !errors.Is(err, ErrNativeConversationMissing) {
				t.Fatalf("promptless turn freshness rejection = %v, want missing native conversation", err)
			}
			if len(store.transitions) != 0 || runtime.destroyed != 0 || len(*log) != 0 {
				t.Fatalf("freshness check stopped the source despite accepted work: %v", *log)
			}
		})
	}
}

func TestInterfaceTransitionInitialSessionStartStillAllowsFreshConversation(t *testing.T) {
	ctx := context.Background()
	manager, store, _, chat, _ := newTransitionManager(t, domain.SessionModeTUI)
	useFastInterfaceTransitionTimings(manager)
	manager.agents = singleAgent{agent: untouchedEmptyTransitionAgent{}}
	rec := store.sessions["session-1"]
	rec.Metadata.AgentSessionID = ""
	rec.Metadata.AgentSessionIDLaunchID = ""
	store.sessions[rec.ID] = rec
	recorder := lifecycle.New(&transitionLifecycleStore{transitionStore: store}, nil)
	if err := recorder.ApplyActivitySignal(ctx, rec.ID, ports.ActivitySignal{
		Valid: true, State: domain.ActivityIdle, Event: "session-start",
		LaunchID: "old-tui-generation", AgentSessionID: "native-1", Timestamp: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if store.sessions[rec.ID].Metadata.ConversationCheckpointState != domain.ConversationCheckpointEmpty {
		t.Fatal("initial SessionStart did not establish an empty checkpoint")
	}
	transition, err := manager.StartInterfaceTransition(ctx, rec.ID, domain.SessionModeChat,
		domain.SessionInterfaceTransitionDrain, domain.SessionInterfaceTransitionHistoryStrict)
	if err != nil {
		t.Fatalf("untouched initial launch was rejected: %v", err)
	}
	settled := awaitTransition(t, store, transition.ID)
	if settled.Phase != domain.SessionInterfaceTransitionCompleted || settled.NativeConversationID != "" ||
		chat.start.ProviderConversationID != "" {
		t.Fatalf("untouched initial launch did not start fresh: %+v", settled)
	}
}

func TestInterfaceTransitionReservedTranscriptRequiresUntouchedTerminal(t *testing.T) {
	for _, name := range []string{
		"absent", "existing", "empty", "directory", "relative", "lookup error",
		"user prompt", "assistant response", "unknown surface", "chat",
	} {
		t.Run(name, func(t *testing.T) {
			manager, store, runtime, _, log := newTransitionManager(t, domain.SessionModeTUI)
			useFastInterfaceTransitionTimings(manager)
			manager.agents = singleAgent{agent: untouchedEmptyTransitionAgent{}}
			rec := store.sessions["session-1"]
			path := filepath.Join(t.TempDir(), "reserved.jsonl")
			switch name {
			case "existing", "empty", "lookup error":
				content := []byte("transcript")
				if name == "empty" {
					content = nil
				}
				if err := os.WriteFile(path, content, 0o600); err != nil {
					t.Fatal(err)
				}
				if name == "lookup error" {
					path = filepath.Join(path, "child.jsonl")
				}
			case "directory":
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			case "relative":
				path = "reserved.jsonl"
			case "user prompt":
				rec.Metadata.LatestUserPrompt = "preserve this work"
			case "assistant response":
				rec.Metadata.LatestAssistantUpdate = "completed work"
			case "unknown surface":
				manager.agents = singleAgent{agent: emptyTransitionAgent{}}
			case "chat":
				rec.Mode = domain.SessionModeChat
			}
			rec.Metadata.NativeTranscriptPath = path
			store.sessions[rec.ID] = rec
			withFreshChatHistory(manager, store)
			target := domain.SessionModeChat
			if rec.Mode == domain.SessionModeChat {
				target = domain.SessionModeTUI
			}
			transition, err := manager.StartInterfaceTransition(context.Background(), rec.ID,
				target, domain.SessionInterfaceTransitionDrain, domain.SessionInterfaceTransitionHistoryStrict)
			if name == "absent" || name == "chat" {
				if err != nil {
					t.Fatal(err)
				}
				if settled := awaitTransition(t, store, transition.ID); settled.Phase != domain.SessionInterfaceTransitionCompleted {
					t.Fatalf("reserved path handoff = %+v", settled)
				}
				return
			}
			if !errors.Is(err, ErrNativeConversationMissing) {
				t.Fatalf("unsafe fresh handoff = %v", err)
			}
			if len(store.transitions) != 0 || runtime.destroyed != 0 || len(*log) != 0 {
				t.Fatalf("checking freshness mutated the source: %v", *log)
			}
		})
	}
}

func TestInterfaceTransitionReservedTranscriptRechecksAfterFencing(t *testing.T) {
	manager, store, runtime, _, log := newTransitionManager(t, domain.SessionModeTUI)
	useFastInterfaceTransitionTimings(manager)
	manager.agents = singleAgent{agent: untouchedEmptyTransitionAgent{}}
	rec := store.sessions["session-1"]
	rec.Metadata.NativeTranscriptPath = filepath.Join(t.TempDir(), "reserved.jsonl")
	store.sessions[rec.ID] = rec
	runtime.outputForCall = func(call int) string {
		if call == 1 {
			// Persistence starts after admission's missing-file check. The
			// provider probe still cannot resume it, so stopping would lose work.
			if err := os.WriteFile(rec.Metadata.NativeTranscriptPath, []byte("new turn"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return idleTerminalOutput
	}
	transition, err := manager.StartInterfaceTransition(context.Background(), rec.ID,
		domain.SessionModeChat, domain.SessionInterfaceTransitionDrain, domain.SessionInterfaceTransitionHistoryStrict)
	if err != nil {
		t.Fatal(err)
	}
	settled := awaitTransition(t, store, transition.ID)
	if settled.Phase != domain.SessionInterfaceTransitionFailed || settled.ErrorCode != "NATIVE_SESSION_MISSING" {
		t.Fatalf("switch after persistence started = %+v", settled)
	}
	if runtime.destroyed != 0 || strings.Contains(fmt.Sprint(*log), "start:chat") {
		t.Fatalf("source stopped despite losing untouched proof: %v", *log)
	}
}
