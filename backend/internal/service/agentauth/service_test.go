package agentauth

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/shellterm"
)

func TestStartRejectsUnstartablePlans(t *testing.T) {
	t.Parallel()

	opener := &recordingTerminalOpener{}
	svc := New(foundExecutables(nil), opener)

	cases := []struct {
		name    string
		agentID string
		code    string
	}{
		{name: "unknown target", agentID: "not-a-harness", code: "AGENT_AUTH_TARGET_UNKNOWN"},
		{name: "unavailable command", agentID: "codex", code: "AGENT_AUTH_UNAVAILABLE"},
		{name: "documentation setup", agentID: "aider", code: "AGENT_AUTH_DOCUMENTATION_ONLY"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.Start(context.Background(), tc.agentID)
			var apiErr *apierr.Error
			if !errors.As(err, &apiErr) || apiErr.Kind != apierr.KindInvalid || apiErr.Code != tc.code {
				t.Fatalf("Start(%q) error = %#v, want invalid %s", tc.agentID, err, tc.code)
			}
		})
	}
	if opener.calls != 0 {
		t.Fatalf("OpenCommandTerminal calls = %d, want 0", opener.calls)
	}
}

func TestStartOpensDevinNativeLogin(t *testing.T) {
	t.Parallel()

	opener := &recordingTerminalOpener{}
	svc := New(foundExecutable("devin"), opener)

	_, err := svc.Start(context.Background(), "devin")
	if err != nil {
		t.Fatalf("Start(devin): %v", err)
	}
	want := shellterm.OpenCommandTerminalInput{
		Argv:  []string{"/test/bin/devin", "auth", "login"},
		Title: "Log in to Devin",
	}
	if !reflect.DeepEqual(opener.input, want) {
		t.Fatalf("OpenCommandTerminal input = %#v, want %#v", opener.input, want)
	}
}

func TestStartOpensResolvedPlanAndReturnsSafeTerminal(t *testing.T) {
	t.Parallel()

	terminal := shellterm.ShellTerminal{HandleID: "shellterm-123", Title: "Log in to Pi"}
	opener := &recordingTerminalOpener{terminal: terminal}
	svc := New(foundExecutable("pi"), opener)

	got, err := svc.Start(context.Background(), "pi")
	if err != nil {
		t.Fatalf("Start(pi): %v", err)
	}
	if opener.calls != 1 {
		t.Fatalf("OpenCommandTerminal calls = %d, want 1", opener.calls)
	}
	wantInput := shellterm.OpenCommandTerminalInput{
		Argv:  []string{"/test/bin/pi"},
		Title: "Log in to Pi",
	}
	if !reflect.DeepEqual(opener.input, wantInput) {
		t.Fatalf("OpenCommandTerminal input = %#v, want %#v", opener.input, wantInput)
	}
	if got.AgentID != "pi" || got.Action != ActionLogin || got.Guidance != "Select Open login after Pi finishes starting" || got.TerminalInput != "/login\r" || got.Terminal != terminal {
		t.Fatalf("Start(pi) = %#v, want display-safe Pi result with terminal %#v", got, terminal)
	}
	data, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "argv") || strings.Contains(string(data), "initialInput") {
		t.Fatalf("Start(pi) serialized trusted terminal input: %s", data)
	}
}

func TestStartFallsBackToAgentResolvedBinaryOutsidePATH(t *testing.T) {
	t.Parallel()

	opener := &recordingTerminalOpener{}
	resolver := managedExecutableResolver{agentID: "claude-code", path: "/Users/test/.claude/local/claude"}
	svc := NewWithAgentResolver(resolver, resolver, opener)

	_, err := svc.Start(context.Background(), "claude-code")
	if err != nil {
		t.Fatalf("Start(claude-code): %v", err)
	}
	if got := opener.input.Argv; !reflect.DeepEqual(got, []string{"/Users/test/.claude/local/claude", "auth", "login"}) {
		t.Fatalf("terminal argv = %#v, want adapter-resolved Claude binary", got)
	}
}

func TestStartPrefersAdapterResolvedBinaryOverGenericPATHMatch(t *testing.T) {
	t.Parallel()

	opener := &recordingTerminalOpener{}
	resolver := managedExecutableResolver{agentID: "muse", path: "/validated/meta/muse"}
	svc := NewWithAgentResolver(foundExecutable("muse"), resolver, opener)

	_, err := svc.Start(context.Background(), "muse")
	if err != nil {
		t.Fatalf("Start(muse): %v", err)
	}
	if got := opener.input.Argv; !reflect.DeepEqual(got, []string{"/validated/meta/muse", "login"}) {
		t.Fatalf("terminal argv = %#v, want adapter-validated Muse binary", got)
	}
}

type recordingTerminalOpener struct {
	calls    int
	input    shellterm.OpenCommandTerminalInput
	terminal shellterm.ShellTerminal
}

type managedExecutableResolver struct {
	agentID string
	path    string
}

func (m managedExecutableResolver) LookPath(string) (string, error) {
	return "", errors.New("not found on PATH")
}

func (m managedExecutableResolver) ResolveAgentBinary(_ context.Context, agentID string) (string, error) {
	if agentID != m.agentID {
		return "", errors.New("unknown agent")
	}
	return m.path, nil
}

func foundExecutable(executable string) ExecutableFinder {
	return executableFinderFunc(func(name string) (string, error) {
		if name != executable {
			return "", errors.New("not found")
		}
		return "/test/bin/" + executable, nil
	})
}

func (o *recordingTerminalOpener) OpenCommandTerminal(_ context.Context, in shellterm.OpenCommandTerminalInput) (shellterm.ShellTerminal, error) {
	o.calls++
	o.input = in
	return o.terminal, nil
}
