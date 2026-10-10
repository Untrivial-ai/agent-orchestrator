package agentauth

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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
		{name: "unavailable command", agentID: "cursor", code: "AGENT_AUTH_UNAVAILABLE"},
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

func TestStartOpensFXNativeLogin(t *testing.T) {
	t.Parallel()

	opener := &recordingTerminalOpener{}
	svc := New(foundExecutable("fx"), opener)

	_, err := svc.Start(context.Background(), "fx")
	if err != nil {
		t.Fatalf("Start(fx): %v", err)
	}
	want := shellterm.OpenCommandTerminalInput{
		Argv:  []string{"/test/bin/fx", "login"},
		Title: "Log in to fx",
	}
	if !reflect.DeepEqual(opener.input, want) {
		t.Fatalf("OpenCommandTerminal input = %#v, want %#v", opener.input, want)
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

	terminal := shellterm.ShellTerminal{HandleID: "shellterm-123", Title: "Log in to Droid"}
	opener := &recordingTerminalOpener{terminal: terminal}
	svc := New(foundExecutable("droid"), opener)

	got, err := svc.Start(context.Background(), "droid")
	if err != nil {
		t.Fatalf("Start(droid): %v", err)
	}
	if opener.calls != 1 {
		t.Fatalf("OpenCommandTerminal calls = %d, want 1", opener.calls)
	}
	wantInput := shellterm.OpenCommandTerminalInput{
		Argv:  []string{"/test/bin/droid"},
		Title: "Log in to Droid",
	}
	if !reflect.DeepEqual(opener.input, wantInput) {
		t.Fatalf("OpenCommandTerminal input = %#v, want %#v", opener.input, wantInput)
	}
	if got.AgentID != "droid" || got.Action != ActionLogin || got.Guidance != "Select Open login after Droid finishes starting" || got.TerminalInput != "/login\r" || got.Terminal != terminal {
		t.Fatalf("Start(droid) = %#v, want display-safe Droid result with terminal %#v", got, terminal)
	}
	data, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "argv") || strings.Contains(string(data), "initialInput") {
		t.Fatalf("Start(droid) serialized trusted terminal input: %s", data)
	}
}

func TestStartPiInjectsLoginAutomatically(t *testing.T) {
	t.Parallel()

	opener := &recordingTerminalOpener{}
	svc := New(foundExecutable("pi"), opener)

	got, err := svc.Start(context.Background(), "pi")
	if err != nil {
		t.Fatalf("Start(pi): %v", err)
	}
	wantInput := shellterm.OpenCommandTerminalInput{
		Argv:         []string{"/test/bin/pi"},
		Title:        "Log in to Pi",
		InitialInput: "/login",
		InitialInputReadyStates: []shellterm.InitialInputReadyState{
			{Text: "0.0%/"},
			{Text: "Pi can explain its own features"},
		},
		SendInitialInputOnReadyTimeout: true,
	}
	if !reflect.DeepEqual(opener.input, wantInput) {
		t.Fatalf("OpenCommandTerminal input = %#v, want %#v", opener.input, wantInput)
	}
	if got.TerminalInput != "" {
		t.Fatalf("Start(pi) terminal input = %q, want none so the login is not held behind a button", got.TerminalInput)
	}
}

// Screens captured from Pi 0.85.1 in an AO auth terminal (120 columns), so the
// reviewed markers are checked against what Pi actually renders.
const (
	piDefaultStartupScreen = ` pi v0.85.1
 escape interrupt · ctrl+c/ctrl+d clear/exit · / commands · ! bash · ctrl+o more
 Press ctrl+o to show full startup help and loaded resources.

 Pi can explain its own features and look up its docs. Ask it how to use or extend Pi.

────────────────────────────────────────────────────────────────────────────────
~\.ao\data\auth-workspace\shellterm-a15141f25c4872b3
0.0%/262k (auto)                                         (moonshotai) kimi-k2.6 • medium`
	piQuietStartupWithModelScreen = `────────────────────────────────────────────────────────────────────────────────
~\.ao\data\auth-workspace\shellterm-a86fe63f7a8f957d
0.0%/1.0M (auto)                                       (zai-coding-cn) glm-5.3 • high`
	piQuietStartupWithoutModelScreen = `────────────────────────────────────────────────────────────────────────────────
~\.ao\data\auth-workspace\shellterm-0c1d2e3f4a5b6c7d
0.0%/0 (auto)                                                                   no-model`
	piStillLoadingScreen = ` pi v0.85.1
 escape interrupt · ctrl+c/ctrl+d clear/exit · / commands · ! bash · ctrl+o more`
)

func TestPiReadyStatesMatchRenderedPiScreens(t *testing.T) {
	t.Parallel()

	readyStates := planByAgentID["pi"].initialInputReadyStates
	for _, tc := range []struct {
		name   string
		screen string
		ready  bool
	}{
		{name: "default startup", screen: piDefaultStartupScreen, ready: true},
		{name: "quietStartup with a selected model", screen: piQuietStartupWithModelScreen, ready: true},
		{name: "quietStartup without a model", screen: piQuietStartupWithoutModelScreen, ready: true},
		{name: "before the footer renders", screen: piStillLoadingScreen, ready: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := shellterm.MatchInitialInputReadyState(tc.screen, readyStates)
			if (got != nil) != tc.ready {
				t.Fatalf("MatchInitialInputReadyState = %#v, want ready %v", got, tc.ready)
			}
		})
	}
}

func TestStartFallsBackToAgentResolvedBinaryOutsidePATH(t *testing.T) {
	t.Parallel()

	opener := &recordingTerminalOpener{}
	resolver := managedExecutableResolver{agentID: "claude-code", path: "/Users/test/.claude/local/claude"}
	svc := NewWithAgentResolver(resolver, resolver, opener, "")
	svc.selfExecutable = func() (string, error) { return "/Applications/AO.app/Contents/MacOS/ao", nil }

	_, err := svc.Start(context.Background(), "claude-code")
	if err != nil {
		t.Fatalf("Start(claude-code): %v", err)
	}
	if got := opener.input.Argv; !reflect.DeepEqual(got, []string{"/Applications/AO.app/Contents/MacOS/ao", "claude-login", "--executable", "/Users/test/.claude/local/claude"}) {
		t.Fatalf("terminal argv = %#v, want trusted Claude login menu with adapter-resolved binary", got)
	}
}

func TestStartPrefersAdapterResolvedBinaryOverGenericPATHMatch(t *testing.T) {
	t.Parallel()

	opener := &recordingTerminalOpener{}
	resolver := managedExecutableResolver{agentID: "muse", path: "/validated/meta/muse"}
	svc := NewWithAgentResolver(foundExecutable("muse"), resolver, opener, "")

	_, err := svc.Start(context.Background(), "muse")
	if err != nil {
		t.Fatalf("Start(muse): %v", err)
	}
	if got := opener.input.Argv; !reflect.DeepEqual(got, []string{"/validated/meta/muse", "login"}) {
		t.Fatalf("terminal argv = %#v, want adapter-validated Muse binary", got)
	}
}

func TestStartPreparesKimiAuthWorkspaceWithSeededTrust(t *testing.T) {
	// Not parallel: isolates HOME/KIMI_CODE_HOME so the kimi adapter's trust
	// seed lands in a throwaway home instead of the developer's real one.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("KIMI_CODE_HOME", "")

	dataDir := t.TempDir()
	opener := &recordingTerminalOpener{}
	svc := NewWithAgentResolver(foundExecutable("kimi"), nil, opener, dataDir)

	if _, err := svc.Start(context.Background(), "kimi"); err != nil {
		t.Fatalf("Start(kimi): %v", err)
	}
	wantDir := filepath.Join(dataDir, "auth-workspace", "kimi")
	if opener.input.WorkingDir != wantDir {
		t.Fatalf("terminal working dir = %q, want %q", opener.input.WorkingDir, wantDir)
	}
	if got := opener.input.Argv; !reflect.DeepEqual(got, []string{"/test/bin/kimi"}) {
		t.Fatalf("terminal argv = %#v, want kimi TUI launch", got)
	}
	if opener.input.InitialInput != "/login" {
		t.Fatalf("initial input = %q, want automatic /login injection", opener.input.InitialInput)
	}
	if got := opener.input.InitialInputReadyStates; !reflect.DeepEqual(got, []shellterm.InitialInputReadyState{{Text: "Run /login or /provider to get started."}}) {
		t.Fatalf("initial input ready states = %#v, want Kimi unauthenticated ready message", got)
	}
	matches, err := filepath.Glob(filepath.Join(home, ".kimi-code", "workspace-trust", "wd_*"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("seeded trust records = %v (err %v), want exactly one", matches, err)
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("read trust record: %v", err)
	}
	if !strings.Contains(string(data), `"root":"`+wantDir+`"`) {
		t.Fatalf("trust record = %s, want root %q", data, wantDir)
	}
}

func TestStartPreparesCopilotAuthWorkspaceWithSeededTrust(t *testing.T) {
	// Not parallel: isolates HOME/COPILOT_HOME so the copilot adapter's trust
	// seed lands in a throwaway home instead of the developer's real one.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("COPILOT_HOME", "")

	dataDir := t.TempDir()
	opener := &recordingTerminalOpener{}
	svc := NewWithAgentResolver(foundExecutable("copilot"), nil, opener, dataDir)

	if _, err := svc.Start(context.Background(), "copilot"); err != nil {
		t.Fatalf("Start(copilot): %v", err)
	}
	wantDir := filepath.Join(dataDir, "auth-workspace", "copilot")
	if opener.input.WorkingDir != wantDir {
		t.Fatalf("terminal working dir = %q, want %q", opener.input.WorkingDir, wantDir)
	}
	if got := opener.input.Argv; !reflect.DeepEqual(got, []string{"/test/bin/copilot"}) {
		t.Fatalf("terminal argv = %#v, want copilot TUI launch", got)
	}
	if opener.input.InitialInput != "/login" {
		t.Fatalf("initial input = %q, want automatic /login injection", opener.input.InitialInput)
	}
	if got := opener.input.InitialInputReadyStates; !reflect.DeepEqual(got, []shellterm.InitialInputReadyState{{Text: "/ commands"}}) {
		t.Fatalf("initial input ready states = %#v, want Copilot composer footer", got)
	}
	if !opener.input.SendInitialInputOnReadyTimeout {
		t.Fatal("Copilot login does not fall back to /login after a slow startup")
	}
	data, err := os.ReadFile(filepath.Join(home, ".copilot", "config.json"))
	if err != nil {
		t.Fatalf("read copilot config: %v", err)
	}
	realDir, err := filepath.EvalSymlinks(wantDir)
	if err != nil {
		t.Fatalf("resolve auth workspace: %v", err)
	}
	if !strings.Contains(string(data), `"`+realDir+`"`) {
		t.Fatalf("copilot config = %s, want trusted folder %q", data, realDir)
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
