package codexappserver

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/persistenthost"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestMain(m *testing.M) {
	if len(os.Args) >= 7 && os.Args[1] == "chat-host" {
		if os.Args[5] != "--" {
			os.Exit(2)
		}
		err := persistenthost.Run(context.Background(), persistenthost.Config{
			SessionID: os.Args[2], DataDir: os.Args[3], Workdir: os.Args[4],
			Env: os.Environ(), Argv: os.Args[6:],
		})
		if err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// TestLiveCodexAppServer drives a real `codex app-server`. It is skipped unless
// AO_CODEX_LIVE=1, because it needs a local Codex install, working auth, and it
// makes real model calls. Everything else in this package runs against pipes.
//
// Run it after changing the protocol layer:
//
//	AO_CODEX_LIVE=1 go test ./internal/adapters/chatdriver/codexappserver/ -run Live -v
func TestLiveCodexAppServer(t *testing.T) {
	if os.Getenv("AO_CODEX_LIVE") != "1" {
		t.Skip("set AO_CODEX_LIVE=1 to run against a real codex app-server")
	}

	bin := os.Getenv("AO_CODEX_BIN")
	if bin == "" {
		bin = "codex"
	}
	if _, err := exec.LookPath(bin); err != nil {
		t.Skipf("codex binary %q not on PATH: %v", bin, err)
	}

	workspace := t.TempDir()
	dataDir := t.TempDir()
	t.Cleanup(func() { _ = persistenthost.Shutdown(context.Background(), dataDir, "ao-live") })
	seedGitWorkspace(t, workspace)

	d := New(livePlugin{bin: bin}, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))

	caps, err := d.Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if missing := ports.MissingProductionCapabilities(caps); len(missing) != 0 {
		t.Fatalf("missing production capabilities: %v", missing)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	conv, err := d.Start(ctx, ports.ChatStartConfig{
		SessionID:     "ao-live",
		DataDir:       dataDir,
		WorkspacePath: workspace,
		Env:           envMap(),
		Permissions:   ports.PermissionModeDefault,
		SystemPrompt:  "You are in an automated test. Answer in one short sentence.",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = conv.Close() }()

	threadID := conv.ProviderConversationID()
	if threadID == "" {
		t.Fatal("no provider conversation id after Start")
	}
	t.Logf("thread %s", threadID)
	const codeword = "teal-orbit-7319"

	if _, err := conv.SendTurn(ctx, ports.ChatUserMessage{
		Text:            "Remember this codeword for my next message: " + codeword + ". Reply with exactly the word: acknowledged",
		ClientMessageID: "live-1",
		Origin:          domain.MessageOriginHuman,
	}); err != nil {
		t.Fatalf("SendTurn: %v", err)
	}

	var (
		sawDelta bool
		state    domain.TurnState
	)
collect:
	for {
		select {
		case ev, ok := <-conv.Events():
			if !ok {
				t.Fatal("event stream closed before the turn completed")
			}
			switch ev.Kind {
			case ports.ChatEventMessageDelta:
				sawDelta = true
			case ports.ChatEventApprovalRequested:
				// Default posture is never-ask, so an approval here means the
				// permission mapping regressed.
				t.Errorf("unexpected approval request under default permissions: %s", ev.Summary)
				_ = conv.ResolveRequest(ctx, ev.RequestID, ports.ChatDecision{ID: "accept"})
			case ports.ChatEventTurnCompleted:
				state = ev.TurnState
				break collect
			case ports.ChatEventControllerState:
				if ev.ControllerState == ports.ChatControllerStopped {
					t.Fatalf("controller stopped before the turn completed: %v", ev.Err)
				}
			}
		case <-ctx.Done():
			t.Fatalf("timed out: %v", ctx.Err())
		}
	}

	if !sawDelta {
		t.Error("no streaming deltas observed")
	}
	if state != domain.TurnStateCompleted {
		t.Errorf("turn state = %q, want completed", state)
	}

	// Resume on a fresh process must recover the same thread — this is the
	// daemon-restart path.
	if err := conv.(ports.ChatProviderHibernator).Hibernate(); err != nil {
		t.Fatalf("Hibernate: %v", err)
	}

	resumed, err := d.Resume(ctx, ports.ChatResumeConfig{
		SessionID:              "ao-live",
		DataDir:                dataDir,
		ProviderConversationID: threadID,
		WorkspacePath:          workspace,
		Env:                    envMap(),
		Permissions:            ports.PermissionModeDefault,
	})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	defer func() { _ = resumed.Close() }()

	if got := resumed.ProviderConversationID(); got != threadID {
		t.Fatalf("resumed thread = %q, want %q", got, threadID)
	}
	if _, err := resumed.SendTurn(ctx, ports.ChatUserMessage{
		Text:            "What codeword did I ask you to remember in my preceding message? Reply with only that codeword.",
		ClientMessageID: "live-2",
		Origin:          domain.MessageOriginHuman,
	}); err != nil {
		t.Fatalf("SendTurn after hibernation: %v", err)
	}
	var answer string
	for {
		select {
		case ev, ok := <-resumed.Events():
			if !ok {
				t.Fatal("resumed event stream closed before the second turn completed")
			}
			switch ev.Kind {
			case ports.ChatEventMessageDelta:
				answer += ev.Delta
			case ports.ChatEventMessageCompleted:
				if ev.Text != "" {
					answer = ev.Text
				}
			case ports.ChatEventTurnCompleted:
				if ev.TurnState != domain.TurnStateCompleted {
					t.Fatalf("resumed turn state = %q, want completed", ev.TurnState)
				}
				if !strings.Contains(strings.ToLower(answer), codeword) {
					t.Fatalf("resumed thread lost earlier context: answer = %q, want %q", answer, codeword)
				}
				return
			case ports.ChatEventApprovalRequested:
				t.Errorf("unexpected approval request after resume: %s", ev.Summary)
				_ = resumed.ResolveRequest(ctx, ev.RequestID, ports.ChatDecision{ID: "accept"})
			case ports.ChatEventControllerState:
				if ev.ControllerState == ports.ChatControllerStopped {
					t.Fatalf("resumed controller stopped before the second turn completed: %v", ev.Err)
				}
			}
		case <-ctx.Done():
			t.Fatalf("resumed turn timed out: %v", ctx.Err())
		}
	}
}

// livePlugin stands in for AO's Codex agent plugin so this test exercises the
// driver rather than binary discovery.
type livePlugin struct{ bin string }

func (p livePlugin) ResolveBinary(context.Context) (string, error) { return p.bin, nil }
func (p livePlugin) AuthStatus(context.Context) (ports.AgentAuthStatus, error) {
	return ports.AgentAuthStatusAuthorized, nil
}

func envMap() map[string]string {
	out := map[string]string{}
	for _, kv := range os.Environ() {
		for i := 0; i < len(kv); i++ {
			if kv[i] == '=' {
				out[kv[:i]] = kv[i+1:]
				break
			}
		}
	}
	return out
}

func seedGitWorkspace(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"add", "."},
		{"-c", "user.email=test@example.com", "-c", "user.name=test", "commit", "-q", "-m", "seed"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
}
