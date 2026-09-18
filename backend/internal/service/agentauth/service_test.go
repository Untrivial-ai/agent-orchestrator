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
		{name: "unavailable command", agentID: "opencode", code: "AGENT_AUTH_UNAVAILABLE"},
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

func TestStartOpensOpenCodeNativeLogin(t *testing.T) {
	t.Parallel()

	opener := &recordingTerminalOpener{}
	svc := New(foundExecutable("opencode"), opener)

	_, err := svc.Start(context.Background(), "opencode")
	if err != nil {
		t.Fatalf("Start(opencode): %v", err)
	}
	want := shellterm.OpenCommandTerminalInput{
		Argv:  []string{"/test/bin/opencode", "auth", "login"},
		Title: "Log in to OpenCode",
	}
	if !reflect.DeepEqual(opener.input, want) {
		t.Fatalf("OpenCommandTerminal input = %#v, want %#v", opener.input, want)
	}
}

func TestStartOpensResolvedPlanAndReturnsSafeTerminal(t *testing.T) {
	t.Parallel()

	terminal := shellterm.ShellTerminal{HandleID: "shellterm-123", Title: "Log in to OpenCode"}
	opener := &recordingTerminalOpener{terminal: terminal}
	svc := New(foundExecutable("opencode"), opener)

	got, err := svc.Start(context.Background(), "opencode")
	if err != nil {
		t.Fatalf("Start(opencode): %v", err)
	}
	if opener.calls != 1 {
		t.Fatalf("OpenCommandTerminal calls = %d, want 1", opener.calls)
	}
	wantInput := shellterm.OpenCommandTerminalInput{
		Argv:  []string{"/test/bin/opencode", "auth", "login"},
		Title: "Log in to OpenCode",
	}
	if !reflect.DeepEqual(opener.input, wantInput) {
		t.Fatalf("OpenCommandTerminal input = %#v, want %#v", opener.input, wantInput)
	}
	if got.AgentID != "opencode" || got.Action != ActionLogin || got.Guidance != "Native provider chooser" || got.TerminalInput != "" || got.Terminal != terminal {
		t.Fatalf("Start(opencode) = %#v, want display-safe result with terminal %#v", got, terminal)
	}
	data, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "argv") || strings.Contains(string(data), "initialInput") {
		t.Fatalf("Start(opencode) serialized trusted terminal input: %s", data)
	}
}

func TestStartFallsBackToAgentResolvedBinaryOutsidePATH(t *testing.T) {
	t.Parallel()

	opener := &recordingTerminalOpener{}
	resolver := managedExecutableResolver{agentID: "opencode", path: "/Users/test/.opencode/bin/opencode"}
	svc := NewWithAgentResolver(resolver, resolver, opener)

	_, err := svc.Start(context.Background(), "opencode")
	if err != nil {
		t.Fatalf("Start(opencode): %v", err)
	}
	if got := opener.input.Argv; !reflect.DeepEqual(got, []string{"/Users/test/.opencode/bin/opencode", "auth", "login"}) {
		t.Fatalf("terminal argv = %#v, want adapter-resolved opencode binary", got)
	}
}

func TestStartPrefersAdapterResolvedBinaryOverGenericPATHMatch(t *testing.T) {
	t.Parallel()

	opener := &recordingTerminalOpener{}
	resolver := managedExecutableResolver{agentID: "opencode", path: "/validated/anomalyco/opencode"}
	svc := NewWithAgentResolver(foundExecutable("opencode"), resolver, opener)

	_, err := svc.Start(context.Background(), "opencode")
	if err != nil {
		t.Fatalf("Start(opencode): %v", err)
	}
	if got := opener.input.Argv; !reflect.DeepEqual(got, []string{"/validated/anomalyco/opencode", "auth", "login"}) {
		t.Fatalf("terminal argv = %#v, want adapter-validated opencode binary", got)
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
