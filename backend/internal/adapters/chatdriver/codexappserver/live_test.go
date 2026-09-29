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
	if len(os.Args) >= 6 && os.Args[1] == "chat-host" && os.Args[5] == "--" {
		err := persistenthost.Run(context.Background(), persistenthost.Config{
			SessionID: os.Args[2], DataDir: os.Args[3], Workdir: os.Args[4],
			Env: os.Environ(), Argv: os.Args[6:], Protocol: persistenthost.ProtocolRaw,
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
	models, err := d.DiscoverModels(ctx, workspace, envMap())
	if err != nil {
		t.Fatalf("DiscoverModels: %v", err)
	}
	if len(models) == 0 {
		t.Fatal("Codex account returned no visible models")
	}
	model := strings.TrimSpace(os.Getenv("AO_LIVE_CODEX_MODEL"))
	if model == "" {
		for _, available := range models {
			if available.Default {
				model = available.ID
				break
			}
		}
	}
	if model == "" {
		model = models[0].ID
	}
	t.Logf("using provider-listed model %s", model)
	dataDir := t.TempDir()

	conv, err := d.Start(ctx, ports.ChatStartConfig{
		SessionID:     "ao-live",
		DataDir:       dataDir,
		WorkspacePath: workspace,
		Model:         model,
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

	if _, err := conv.SendTurn(ctx, ports.ChatUserMessage{
		Text:            "Reply with exactly the word: acknowledged",
		ClientMessageID: "live-1",
		Origin:          domain.MessageOriginHuman,
	}); err != nil {
		t.Fatalf("SendTurn: %v", err)
	}

	var (
		sawDelta bool
		state    domain.TurnState
		turnErr  error
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
				turnErr = ev.Err
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
		t.Errorf("turn state = %q, want completed; error=%v", state, turnErr)
	}

	// Resume on a fresh process must recover the same thread — this is the
	// daemon-restart path.
	if err := conv.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	resumed, err := d.Resume(ctx, ports.ChatResumeConfig{
		SessionID:              "ao-live",
		ProviderConversationID: threadID,
		DataDir:                dataDir,
		WorkspacePath:          workspace,
		Model:                  model,
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
	t.Logf("resumed thread %s on a fresh app-server process", threadID)
	if _, err := resumed.SendTurn(ctx, ports.ChatUserMessage{
		Text:            "What exact word did I ask you to reply with?",
		ClientMessageID: "live-resume-check",
		Origin:          domain.MessageOriginHuman,
	}); err != nil {
		t.Fatalf("SendTurn after resume: %v", err)
	}
	var resumedAnswer strings.Builder
	var resumedErr error
	for {
		select {
		case event, ok := <-resumed.Events():
			if !ok {
				t.Fatalf("event stream closed before resumed turn completed; answer=%q", resumedAnswer.String())
			}
			switch event.Kind {
			case ports.ChatEventMessageDelta:
				resumedAnswer.WriteString(event.Delta)
			case ports.ChatEventTurnCompleted:
				if event.TurnState != domain.TurnStateCompleted {
					resumedErr = event.Err
					t.Fatalf("resumed turn state = %q; error=%v; answer=%q", event.TurnState, resumedErr, resumedAnswer.String())
				}
				if !strings.Contains(strings.ToLower(resumedAnswer.String()), "acknowledged") {
					t.Fatalf("resumed answer = %q, want prior conversation context", resumedAnswer.String())
				}
				return
			}
		case <-ctx.Done():
			t.Fatalf("resumed turn timed out: %v; answer=%q", ctx.Err(), resumedAnswer.String())
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
