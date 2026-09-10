package codexappserver

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/codexappserver/codexproto"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Scripted replies must be single-line JSON: readFrame is newline-delimited, so a
// pretty-printed reply is read as several unparseable frames and the request never
// completes.

// steerConversation opens a conversation with a turn already in flight, which is the
// only state a steer is meaningful in.
func steerConversation(t *testing.T) (*conversation, *scriptedServer) {
	t.Helper()
	d, srv := newTestDriver(t)
	srv.respondTo(codexproto.MethodTurnSteer, `{"turnId":"turn-1"}`)

	opened, err := d.Start(context.Background(), ports.ChatStartConfig{WorkspacePath: "/tmp/ws"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = opened.Close() })

	conv, ok := opened.(*conversation)
	if !ok {
		t.Fatalf("Start returned %T, want *conversation", opened)
	}
	if _, err := conv.SendTurn(context.Background(), ports.ChatUserMessage{
		Text:   "do the long thing",
		Origin: domain.MessageOriginHuman,
	}); err != nil {
		t.Fatalf("SendTurn: %v", err)
	}
	return conv, srv
}

// refuseSteer scripts a raw JSON-RPC error object, so a test can reproduce the
// provider's structured refusal payload verbatim. scriptedServer.replyError only
// carries a code and a message, and the payload is the whole point here.
func refuseSteer(srv *scriptedServer, errorJSON string) {
	srv.mu.Lock()
	defer srv.mu.Unlock()
	srv.failures[codexproto.MethodTurnSteer] = errorJSON
}

// The shape on the wire, checked against the generated params rather than a
// hand-written struct: expectedTurnId is a precondition the provider enforces, and
// sending the wrong field name would make every steer land as "no active turn".
func TestSteerSendsTheProvidersPreconditionShape(t *testing.T) {
	conv, srv := steerConversation(t)

	ref, err := conv.Steer(context.Background(), "turn-1", ports.ChatUserMessage{
		Text:            "actually, stop and just summarize",
		ClientMessageID: "steer-1",
		Origin:          domain.MessageOriginHuman,
		Content: []ports.ChatContent{{
			Type: "image", Data: "aGVsbG8=", MIMEType: "image/png",
		}},
	})
	if err != nil {
		t.Fatalf("Steer: %v", err)
	}
	// Observed against codex-cli 0.146.0: the turn id is preserved, so the guidance
	// belongs to the turn the user is already watching.
	if ref.ProviderTurnID != "turn-1" {
		t.Errorf("steered turn = %q, want turn-1", ref.ProviderTurnID)
	}

	sent := srv.awaitFrame(func(f frame) bool { return f.Method == codexproto.MethodTurnSteer })
	var params codexproto.TurnSteerParams
	if err := json.Unmarshal(sent.Params, &params); err != nil {
		t.Fatalf("turn/steer params: %v", err)
	}
	if params.ThreadID != "thread-1" {
		t.Errorf("threadId = %q, want thread-1", params.ThreadID)
	}
	if params.ExpectedTurnID != "turn-1" {
		t.Errorf("expectedTurnId = %q, want turn-1", params.ExpectedTurnID)
	}
	if params.ClientUserMessageID == nil || *params.ClientUserMessageID != "steer-1" {
		t.Errorf("clientUserMessageId = %v, want steer-1", params.ClientUserMessageID)
	}
	if len(params.Input) != 2 || params.Input[0].Type != codexproto.UserInputTypeText {
		t.Fatalf("input = %+v, want text followed by image", params.Input)
	}
	if params.Input[0].Text == nil || *params.Input[0].Text != "actually, stop and just summarize" {
		t.Errorf("input text = %v", params.Input[0].Text)
	}
	if params.Input[1].Type != codexproto.UserInputTypeImage || params.Input[1].URL == nil ||
		*params.Input[1].URL != "data:image/png;base64,aGVsbG8=" {
		t.Errorf("image input = %+v", params.Input[1])
	}
}

// An empty turn id falls back to the turn the conversation last saw, so a caller
// that has not tracked one is not forced to guess.
func TestSteerFallsBackToTheKnownActiveTurn(t *testing.T) {
	conv, srv := steerConversation(t)

	if _, err := conv.Steer(context.Background(), "", ports.ChatUserMessage{Text: "narrow it down"}); err != nil {
		t.Fatalf("Steer: %v", err)
	}
	sent := srv.awaitFrame(func(f frame) bool { return f.Method == codexproto.MethodTurnSteer })
	var params codexproto.TurnSteerParams
	if err := json.Unmarshal(sent.Params, &params); err != nil {
		t.Fatalf("turn/steer params: %v", err)
	}
	if params.ExpectedTurnID != "turn-1" {
		t.Errorf("expectedTurnId = %q, want the last known turn", params.ExpectedTurnID)
	}
}

// With no turn ever started there is nothing to name, and the provider must not be
// asked to steer the empty string.
func TestSteerWithoutAnyTurnIsTypedAndNeverReachesTheProvider(t *testing.T) {
	d, srv := newTestDriver(t)
	opened, err := d.Start(context.Background(), ports.ChatStartConfig{WorkspacePath: "/tmp/ws"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = opened.Close() }()

	_, err = opened.(*conversation).Steer(context.Background(), "", ports.ChatUserMessage{Text: "hello"})
	if !errors.Is(err, ports.ErrChatNoSteerableTurn) {
		t.Fatalf("err = %v, want ErrChatNoSteerableTurn", err)
	}
	if srv.sentMethod(codexproto.MethodTurnSteer) {
		t.Error("asked the provider to steer a turn AO does not have")
	}
}

func TestSteerRejectsEmptyText(t *testing.T) {
	conv, srv := steerConversation(t)

	_, err := conv.Steer(context.Background(), "turn-1", ports.ChatUserMessage{Text: "   \n"})
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("err = %v, want a rejection naming the empty text", err)
	}
	if srv.sentMethod(codexproto.MethodTurnSteer) {
		t.Error("sent an empty steer to the provider")
	}
}

// The provider's refusal when nothing is in flight. Observed verbatim from a live
// app-server: code -32600, message "no active turn to steer", no data. It reaches a
// user as "there is no turn to steer", never as an internal error.
func TestSteerTranslatesNoActiveTurnRefusal(t *testing.T) {
	conv, srv := steerConversation(t)
	refuseSteer(srv, `{"code":-32600,"message":"no active turn to steer"}`)

	_, err := conv.Steer(context.Background(), "turn-1", ports.ChatUserMessage{Text: "too late"})
	if !errors.Is(err, ports.ErrChatNoSteerableTurn) {
		t.Fatalf("err = %v, want ErrChatNoSteerableTurn", err)
	}
}

// The other refusal, and the one that is distinguishable without reading prose: a
// turn IS running but its kind cannot take guidance. Payload copied from a live
// app-server refusing a steer during a manual compaction.
func TestSteerTranslatesNotSteerableRefusalFromItsStructuredPayload(t *testing.T) {
	conv, srv := steerConversation(t)
	refuseSteer(srv, `{"code":-32600,"message":"cannot steer a compact turn",`+
		`"data":{"message":"cannot steer a compact turn",`+
		`"codexErrorInfo":{"activeTurnNotSteerable":{"turnKind":"compact"}},`+
		`"additionalDetails":null}}`)

	_, err := conv.Steer(context.Background(), "turn-1", ports.ChatUserMessage{Text: "hurry up"})
	if !errors.Is(err, ports.ErrChatTurnNotSteerable) {
		t.Fatalf("err = %v, want ErrChatTurnNotSteerable", err)
	}
	// The kind is what makes the refusal actionable: "wait for the compaction" is
	// advice a user can follow.
	if !strings.Contains(err.Error(), "compact") {
		t.Errorf("err = %v, want it to name the turn kind", err)
	}
	if errors.Is(err, ports.ErrChatNoSteerableTurn) {
		t.Error("a running-but-unsteerable turn was reported as no turn at all")
	}
}

// A refusal AO does not model must stay a plain failure rather than being
// mistranslated into "nothing to steer", which would tell the user to resend
// guidance the agent may already have.
func TestSteerLeavesUnknownProviderErrorsAlone(t *testing.T) {
	conv, srv := steerConversation(t)
	refuseSteer(srv, `{"code":-32603,"message":"internal error"}`)

	_, err := conv.Steer(context.Background(), "turn-1", ports.ChatUserMessage{Text: "guidance"})
	if err == nil {
		t.Fatal("Steer succeeded against a failing provider")
	}
	if errors.Is(err, ports.ErrChatNoSteerableTurn) || errors.Is(err, ports.ErrChatTurnNotSteerable) {
		t.Fatalf("err = %v, want an untranslated failure", err)
	}
	if !strings.Contains(err.Error(), codexproto.MethodTurnSteer) {
		t.Errorf("err = %v, want it to name the failing method", err)
	}
}

// A response with no turn id still has to be attributed somewhere, and the turn the
// caller asked about is the only honest answer.
func TestSteerFallsBackToTheRequestedTurnWhenTheProviderNamesNone(t *testing.T) {
	conv, srv := steerConversation(t)
	srv.respondTo(codexproto.MethodTurnSteer, `{}`)

	ref, err := conv.Steer(context.Background(), "turn-1", ports.ChatUserMessage{Text: "guidance"})
	if err != nil {
		t.Fatalf("Steer: %v", err)
	}
	if ref.ProviderTurnID != "turn-1" {
		t.Errorf("steered turn = %q, want the requested turn", ref.ProviderTurnID)
	}
}

// TestLiveSteerKeepsTheTurnAndItsWork drives a real `codex app-server`. Skipped
// unless AO_CODEX_LIVE=1: it needs a local Codex install, working auth, and it makes
// real model calls.
//
//	AO_CODEX_LIVE=1 go test ./internal/adapters/chatdriver/codexappserver/ -run LiveSteer -v
//
// This is the test that earns the capability. Steering is advertised only because
// this passed against codex-cli 0.146.0, and the claim it checks is the one the
// feature exists for: the turn the user was waiting on survives, keeps its id, and
// finishes having followed the correction — not interrupted, not restarted.
func TestLiveSteerKeepsTheTurnAndItsWork(t *testing.T) {
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

	d := New(livePlugin{bin: bin}, slog.New(slog.DiscardHandler))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	opened, err := d.Start(ctx, ports.ChatStartConfig{
		SessionID:     "ao-live-steer",
		WorkspacePath: workspace,
		Env:           envMap(),
		Permissions:   ports.PermissionModeDefault,
		SystemPrompt:  "You are in an automated test. Follow instructions literally.",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = opened.Close() }()

	steerer, ok := opened.(ports.ChatSteerer)
	if !ok {
		t.Fatalf("conversation %T does not implement ChatSteerer", opened)
	}

	// Long enough to still be running when the steer lands, and visibly abandoned if
	// the agent takes the correction.
	ref, err := opened.SendTurn(ctx, ports.ChatUserMessage{
		Text: "Run this exact shell command and then report its output verbatim: " +
			"for i in 1 2 3 4 5 6 7 8 9 10; do echo tick-$i; sleep 3; done",
		ClientMessageID: "live-steer-turn",
		Origin:          domain.MessageOriginHuman,
	})
	if err != nil {
		t.Fatalf("SendTurn: %v", err)
	}
	t.Logf("turn %s", ref.ProviderTurnID)

	// Wait for the provider's own acknowledgement AND for work to be under way. A
	// steer sent before turn/started is refused, which is exactly why the service
	// layer waits for this signal too.
	var starts, completions int
	waitForWork := func() {
		for {
			select {
			case ev, live := <-opened.Events():
				if !live {
					t.Fatal("event stream closed before the turn started working")
				}
				switch ev.Kind {
				case ports.ChatEventTurnStarted:
					starts++
				case ports.ChatEventCommandOutputDelta, ports.ChatEventActivityStarted:
					if starts > 0 {
						return
					}
				case ports.ChatEventTurnCompleted:
					t.Fatal("the turn finished before there was anything to steer")
				}
			case <-ctx.Done():
				t.Fatalf("timed out waiting for the turn to start working: %v", ctx.Err())
			}
		}
	}
	waitForWork()

	steered, err := steerer.Steer(ctx, ref.ProviderTurnID, ports.ChatUserMessage{
		Text: "Change of plan: abandon that loop immediately and reply with the " +
			"single word STEERED and nothing else.",
		ClientMessageID: "live-steer-1",
		Origin:          domain.MessageOriginHuman,
	})
	if err != nil {
		t.Fatalf("Steer: %v", err)
	}
	// The load-bearing observation. A steer that opened a second turn would leave the
	// user's guidance attributed to work they were not watching.
	if steered.ProviderTurnID != ref.ProviderTurnID {
		t.Errorf("steer moved the turn: %q -> %q", ref.ProviderTurnID, steered.ProviderTurnID)
	}

	var (
		text  strings.Builder
		state domain.TurnState
	)
collect:
	for {
		select {
		case ev, live := <-opened.Events():
			if !live {
				t.Fatal("event stream closed before the turn completed")
			}
			switch ev.Kind {
			case ports.ChatEventTurnStarted:
				starts++
			case ports.ChatEventMessageCompleted:
				text.WriteString(ev.Text)
				text.WriteString("\n")
			case ports.ChatEventTurnCompleted:
				completions++
				state = ev.TurnState
				break collect
			case ports.ChatEventControllerState:
				if ev.ControllerState == ports.ChatControllerStopped {
					t.Fatalf("controller stopped before the turn completed: %v", ev.Err)
				}
			}
		case <-ctx.Done():
			t.Fatalf("timed out waiting for the steered turn: %v", ctx.Err())
		}
	}

	// Interrupt-and-resend would have produced an interrupted turn and a second one.
	// Steering produces neither, which is the whole reason to prefer it.
	if state != domain.TurnStateCompleted {
		t.Errorf("steered turn state = %q, want completed", state)
	}
	if starts != 1 || completions != 1 {
		t.Errorf("turn lifecycle = %d started / %d completed, want 1/1", starts, completions)
	}
	if !strings.Contains(text.String(), "STEERED") {
		t.Errorf("the agent never acted on the guidance; final text was:\n%s", text.String())
	}
	t.Logf("steered turn %s completed as %s: %s", steered.ProviderTurnID, state,
		strings.TrimSpace(text.String()))

	// And the refusal, live: with the turn finished there is nothing to join.
	if _, err := steerer.Steer(ctx, ref.ProviderTurnID,
		ports.ChatUserMessage{Text: "too late"}); !errors.Is(err, ports.ErrChatNoSteerableTurn) {
		t.Errorf("steering a finished turn returned %v, want ErrChatNoSteerableTurn", err)
	}
}

// The capability and the method have to agree. Advertising steer while the provider
// does not declare turn/steer would put a control in the UI that fails on use; the
// reverse leaves a working feature switched off.
func TestSteerCapabilityMatchesTheDeclaredProtocol(t *testing.T) {
	if !codexproto.Declares(codexproto.MethodTurnSteer) {
		t.Fatalf("generated protocol (%s) does not declare %s",
			codexproto.ProviderVersion, codexproto.MethodTurnSteer)
	}
	if !capabilities().Has(ports.ChatCapabilitySteer) {
		t.Error("the driver implements turn/steer but does not advertise the capability")
	}
}
