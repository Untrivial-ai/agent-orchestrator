package sessionmanager

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

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
				target, domain.SessionInterfaceTransitionDrain)
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
		domain.SessionModeChat, domain.SessionInterfaceTransitionDrain)
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
