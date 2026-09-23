package systemcheck

import (
	"context"
	"errors"
	"fmt"
	"io"
	"runtime"
	"slices"
	"testing"

	agentsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/agent"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/shellterm"
)

type fakeHarnessCatalog struct {
	inventory   agentsvc.Inventory
	err         error
	calls       int
	binary      agentsvc.Info
	binaryOK    bool
	binaryCalls int
}

type fakeCommandRunner struct {
	err     error
	errors  []error
	stdout  string
	argv    []string
	argvLog [][]string
	run     func(context.Context) error
}

type fakeGitHubAuthTerminalOpener struct {
	input shellterm.OpenCommandTerminalInput
}

func (f *fakeGitHubAuthTerminalOpener) OpenCommandTerminal(_ context.Context, input shellterm.OpenCommandTerminalInput) (shellterm.ShellTerminal, error) {
	f.input = input
	return shellterm.ShellTerminal{HandleID: "shellterm-github"}, nil
}

func (f *fakeCommandRunner) Run(ctx context.Context, argv []string, stdout, _ io.Writer) error {
	if f.run != nil {
		return f.run(ctx)
	}
	f.argv = append([]string(nil), argv...)
	f.argvLog = append(f.argvLog, append([]string(nil), argv...))
	_, _ = io.WriteString(stdout, f.stdout)
	if index := len(f.argvLog) - 1; index < len(f.errors) {
		return f.errors[index]
	}
	return f.err
}

func (f *fakeHarnessCatalog) RefreshFresh(context.Context) (agentsvc.Inventory, error) {
	f.calls++
	return f.inventory, f.err
}

func (f *fakeHarnessCatalog) FindInstalledBinary(context.Context) (agentsvc.Info, bool) {
	f.binaryCalls++
	return f.binary, f.binaryOK
}

func lookPathFound(paths map[string]string) func(string) (string, error) {
	return func(name string) (string, error) {
		if p, ok := paths[name]; ok {
			return p, nil
		}
		return "", errors.New("exec: " + name + ": executable file not found in $PATH")
	}
}

func TestCheck_AllSatisfied(t *testing.T) {
	catalog := &fakeHarnessCatalog{inventory: agentsvc.Inventory{
		Installed: []agentsvc.Info{{ID: "claude-code", Label: "Claude Code"}},
	}}
	svc := NewWithCommandRunner(catalog, executableFinderFunc(lookPathFound(map[string]string{
		"git":  "/usr/bin/git",
		"tmux": "/usr/bin/tmux",
		"gh":   "/usr/bin/gh",
	})), &fakeCommandRunner{stdout: `{"hosts":{"github.com":[{"active":true,"state":"success"}]}}`})

	report, err := svc.Check(context.Background())
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if !report.Ready {
		t.Fatalf("Ready = false, want true; requirements=%+v", report.Requirements)
	}
	if len(report.Requirements) != 5 {
		t.Fatalf("len(Requirements) = %d, want 5", len(report.Requirements))
	}
	wantOrder := []string{"git", "tmux", "harness", "gh", "github-auth"}
	wantRequired := map[string]bool{"git": true, "tmux": true, "harness": true, "gh": false, "github-auth": false}
	for i, id := range wantOrder {
		if report.Requirements[i].ID != id {
			t.Fatalf("Requirements[%d].ID = %q, want %q", i, report.Requirements[i].ID, id)
		}
		if !report.Requirements[i].Satisfied {
			t.Fatalf("Requirements[%d] (%s) not satisfied", i, id)
		}
		if report.Requirements[i].Required != wantRequired[id] {
			t.Fatalf("Requirements[%d] (%s).Required = %v, want %v", i, id, report.Requirements[i].Required, wantRequired[id])
		}
	}
}

func TestCheckStartup_SkipsAgentInventory(t *testing.T) {
	catalog := &fakeHarnessCatalog{
		err:      errors.New("agent auth probe must not run at startup"),
		binary:   agentsvc.Info{ID: "claude-code", Label: "Claude Code"},
		binaryOK: true,
	}
	runner := &fakeCommandRunner{err: errors.New("credential probe must not run at startup")}
	svc := NewWithCommandRunner(catalog, executableFinderFunc(lookPathFound(map[string]string{
		"git":  "/usr/bin/git",
		"tmux": "/usr/bin/tmux",
		"gh":   "/usr/bin/gh",
	})), runner)

	report, err := svc.CheckStartup(context.Background())
	if err != nil {
		t.Fatalf("CheckStartup() error = %v", err)
	}
	if !report.Ready {
		t.Fatalf("Ready = false, want true; requirements=%+v", report.Requirements)
	}
	if catalog.calls != 0 {
		t.Fatalf("RefreshFresh calls = %d, want 0", catalog.calls)
	}
	if catalog.binaryCalls != 1 {
		t.Fatalf("FindInstalledBinary calls = %d, want 1", catalog.binaryCalls)
	}
	if runner.argv != nil {
		t.Fatalf("startup auth probe argv = %#v, want no command", runner.argv)
	}
	if len(report.Requirements) != 4 {
		t.Fatalf("len(Requirements) = %d, want 4", len(report.Requirements))
	}
	for i, want := range []string{"git", "tmux", "harness", "gh"} {
		if report.Requirements[i].ID != want {
			t.Fatalf("Requirements[%d].ID = %q, want %q", i, report.Requirements[i].ID, want)
		}
	}
}

func TestCheckStartup_NoAgentBinaryBlocksReady(t *testing.T) {
	svc := NewWithLookPath(&fakeHarnessCatalog{}, lookPathFound(map[string]string{
		"git":  "/usr/bin/git",
		"tmux": "/usr/bin/tmux",
	}))

	report, err := svc.CheckStartup(context.Background())
	if err != nil {
		t.Fatalf("CheckStartup() error = %v", err)
	}
	if report.Ready {
		t.Fatalf("Ready = true, want false")
	}
	if harness := requirementByID(t, report, "harness"); harness.Satisfied || !harness.Required {
		t.Fatalf("harness = %+v, want required unsatisfied requirement", harness)
	}
}

func TestCheck_GitMissing(t *testing.T) {
	catalog := &fakeHarnessCatalog{inventory: agentsvc.Inventory{
		Installed: []agentsvc.Info{{ID: "claude-code", Label: "Claude Code"}},
	}}
	svc := NewWithLookPath(catalog, lookPathFound(map[string]string{
		"tmux": "/usr/bin/tmux",
	}))

	report, err := svc.Check(context.Background())
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if report.Ready {
		t.Fatalf("Ready = true, want false")
	}
	git := requirementByID(t, report, "git")
	if git.Satisfied {
		t.Fatalf("git.Satisfied = true, want false")
	}
	if git.Detail == "" {
		t.Fatalf("git.Detail is empty, want a not-found message")
	}
	if tmux := requirementByID(t, report, "tmux"); !tmux.Satisfied {
		t.Fatalf("tmux.Satisfied = false, want true")
	}
}

func TestCheck_TmuxMissing(t *testing.T) {
	catalog := &fakeHarnessCatalog{inventory: agentsvc.Inventory{
		Installed: []agentsvc.Info{{ID: "claude-code", Label: "Claude Code"}},
	}}
	svc := NewWithLookPath(catalog, lookPathFound(map[string]string{
		"git": "/usr/bin/git",
	}))

	report, err := svc.Check(context.Background())
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if report.Ready {
		t.Fatalf("Ready = true, want false")
	}
	tmux := requirementByID(t, report, "tmux")
	if tmux.Satisfied {
		t.Fatalf("tmux.Satisfied = true, want false")
	}
	if tmux.Detail == "" {
		t.Fatalf("tmux.Detail is empty, want a not-found message")
	}
}

func TestCheck_UsesBundledTmuxOverride(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("tmux is not required on Windows")
	}
	const bundled = "/opt/ao/resources/tmux/bin/tmux"
	t.Setenv("AO_TMUX_BINARY", bundled)
	catalog := &fakeHarnessCatalog{inventory: agentsvc.Inventory{
		Installed: []agentsvc.Info{{ID: "claude-code", Label: "Claude Code"}},
	}}
	var requested string
	svc := NewWithLookPath(catalog, func(name string) (string, error) {
		if name == "git" {
			return "/usr/bin/git", nil
		}
		if name == bundled {
			requested = name
			return bundled, nil
		}
		return "", errors.New("not found")
	})

	report, err := svc.Check(context.Background())
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if requested != bundled {
		t.Fatalf("tmux lookup = %q, want bundled path %q", requested, bundled)
	}
	if tmux := requirementByID(t, report, "tmux"); !tmux.Satisfied || tmux.Detail != bundled {
		t.Fatalf("tmux requirement = %+v, want satisfied bundled path", tmux)
	}
}

func TestCheck_NoHarnessInstalled(t *testing.T) {
	catalog := &fakeHarnessCatalog{inventory: agentsvc.Inventory{Installed: nil}}
	svc := NewWithLookPath(catalog, lookPathFound(map[string]string{
		"git":  "/usr/bin/git",
		"tmux": "/usr/bin/tmux",
	}))

	report, err := svc.Check(context.Background())
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if report.Ready {
		t.Fatalf("Ready = true, want false")
	}
	harness := requirementByID(t, report, "harness")
	if harness.Satisfied {
		t.Fatalf("harness.Satisfied = true, want false")
	}
	if harness.Detail == "" {
		t.Fatalf("harness.Detail is empty, want a not-found message")
	}
}

func TestCheck_HarnessCatalogError(t *testing.T) {
	catalog := &fakeHarnessCatalog{err: errors.New("probe boom")}
	svc := NewWithLookPath(catalog, lookPathFound(map[string]string{
		"git":  "/usr/bin/git",
		"tmux": "/usr/bin/tmux",
	}))

	report, err := svc.Check(context.Background())
	if err != nil {
		t.Fatalf("Check() error = %v, want nil (harness errors fold into the requirement)", err)
	}
	if report.Ready {
		t.Fatalf("Ready = true, want false")
	}
	harness := requirementByID(t, report, "harness")
	if harness.Satisfied {
		t.Fatalf("harness.Satisfied = true, want false")
	}
	if harness.Detail != "probe boom" {
		t.Fatalf("harness.Detail = %q, want %q", harness.Detail, "probe boom")
	}
}

func TestCheck_GHPresent(t *testing.T) {
	catalog := &fakeHarnessCatalog{inventory: agentsvc.Inventory{
		Installed: []agentsvc.Info{{ID: "claude-code", Label: "Claude Code"}},
	}}
	svc := NewWithLookPath(catalog, lookPathFound(map[string]string{
		"git":  "/usr/bin/git",
		"tmux": "/usr/bin/tmux",
		"gh":   "/usr/bin/gh",
	}))

	report, err := svc.Check(context.Background())
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if !report.Ready {
		t.Fatalf("Ready = false, want true")
	}
	gh := requirementByID(t, report, "gh")
	if !gh.Satisfied {
		t.Fatalf("gh.Satisfied = false, want true")
	}
	if gh.Required {
		t.Fatalf("gh.Required = true, want false (gh is advisory)")
	}
	if gh.Detail == "" {
		t.Fatalf("gh.Detail is empty, want the resolved path")
	}
}

// TestCheck_GHMissing confirms gh being absent neither fails the requirement's
// sibling checks nor flips Ready — this is the whole point of gh being
// Required: false. Since git/tmux/harness are all satisfied here, this is
// also the "Ready stays true when ONLY gh is unsatisfied" case.
func TestCheck_GHMissing(t *testing.T) {
	catalog := &fakeHarnessCatalog{inventory: agentsvc.Inventory{
		Installed: []agentsvc.Info{{ID: "claude-code", Label: "Claude Code"}},
	}}
	svc := NewWithLookPath(catalog, lookPathFound(map[string]string{
		"git":  "/usr/bin/git",
		"tmux": "/usr/bin/tmux",
	}))

	report, err := svc.Check(context.Background())
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if !report.Ready {
		t.Fatalf("Ready = false, want true (gh is advisory and must not block readiness); requirements=%+v", report.Requirements)
	}
	gh := requirementByID(t, report, "gh")
	if gh.Satisfied {
		t.Fatalf("gh.Satisfied = true, want false")
	}
	if gh.Required {
		t.Fatalf("gh.Required = true, want false")
	}
	if gh.Detail == "" {
		t.Fatalf("gh.Detail is empty, want a not-found message")
	}
}

func TestCheckGitHubAuth_IsAdvisory(t *testing.T) {
	catalog := &fakeHarnessCatalog{binary: agentsvc.Info{ID: "claude-code", Label: "Claude Code"}, binaryOK: true}
	runner := &fakeCommandRunner{err: errors.New("not logged in")}
	svc := NewWithCommandRunner(catalog, executableFinderFunc(lookPathFound(map[string]string{
		"git": "/usr/bin/git", "tmux": "/usr/bin/tmux", "gh": "/usr/bin/gh",
	})), runner)

	auth, err := svc.CheckGitHubAuth(context.Background())
	if err != nil {
		t.Fatalf("CheckGitHubAuth() error = %v", err)
	}
	if auth.Satisfied || auth.Required {
		t.Fatalf("github-auth = %+v, want unsatisfied advisory", auth)
	}
	wantCalls := [][]string{
		{"/usr/bin/gh", "auth", "status", "--active", "--json", "hosts"},
		{"/usr/bin/gh", "auth", "token"},
	}
	if len(runner.argvLog) != len(wantCalls) {
		t.Fatalf("auth probe argv = %#v, want %#v", runner.argvLog, wantCalls)
	}
	for i := range wantCalls {
		if !slices.Equal(runner.argvLog[i], wantCalls[i]) {
			t.Fatalf("auth probe argv = %#v, want %#v", runner.argvLog, wantCalls)
		}
	}
}

func TestCheckGitHubAuth_FallsBackForOlderGitHubCLI(t *testing.T) {
	runner := &fakeCommandRunner{errors: []error{errors.New("unknown flag: --json"), nil}}
	svc := NewWithCommandRunner(&fakeHarnessCatalog{}, executableFinderFunc(lookPathFound(map[string]string{
		"gh": "/usr/bin/gh",
	})), runner)

	auth, err := svc.CheckGitHubAuth(context.Background())
	if err != nil {
		t.Fatalf("CheckGitHubAuth() error = %v", err)
	}
	if !auth.Satisfied {
		t.Fatalf("github-auth = %+v, want local token fallback to satisfy auth", auth)
	}
	wantCalls := [][]string{
		{"/usr/bin/gh", "auth", "status", "--active", "--json", "hosts"},
		{"/usr/bin/gh", "auth", "token"},
	}
	if len(runner.argvLog) != len(wantCalls) {
		t.Fatalf("auth probe argv = %#v, want %#v", runner.argvLog, wantCalls)
	}
	for i := range wantCalls {
		if !slices.Equal(runner.argvLog[i], wantCalls[i]) {
			t.Fatalf("auth probe argv = %#v, want %#v", runner.argvLog, wantCalls)
		}
	}
}

func TestCheckGitHubAuth_LocalTokenIsFloor(t *testing.T) {
	for _, output := range []string{
		`{"hosts":{"github.com":[{"active":true,"state":"error","error":"proxyconnect tcp: connection refused"}]}}`,
		`{"hosts":{"github.com":[{"active":true,"state":"failure"}]}}`,
		`invalid json`,
	} {
		for _, hasToken := range []bool{true, false} {
			t.Run(output+fmt.Sprint(hasToken), func(t *testing.T) {
				var tokenErr error
				if !hasToken {
					tokenErr = errors.New("no local token")
				}
				runner := &fakeCommandRunner{stdout: output, errors: []error{nil, tokenErr}}
				svc := NewWithCommandRunner(&fakeHarnessCatalog{}, executableFinderFunc(lookPathFound(map[string]string{"gh": "/usr/bin/gh"})), runner)
				auth, err := svc.CheckGitHubAuth(context.Background())
				if err != nil || auth.Satisfied != hasToken {
					t.Fatalf("auth = %+v, err = %v, want satisfied=%v", auth, err, hasToken)
				}
				if len(runner.argvLog) != 2 || !slices.Equal(runner.argvLog[1], []string{"/usr/bin/gh", "auth", "token"}) {
					t.Fatalf("calls = %v", runner.argvLog)
				}
			})
		}
	}
}

func TestCheckGitHubAuth_AcceptsActiveEnterpriseHost(t *testing.T) {
	runner := &fakeCommandRunner{stdout: `{"hosts":{"github.example.com":[{"active":true,"state":"success"}]}}`}
	svc := NewWithCommandRunner(&fakeHarnessCatalog{}, executableFinderFunc(lookPathFound(map[string]string{
		"gh": "/usr/bin/gh",
	})), runner)

	auth, err := svc.CheckGitHubAuth(context.Background())
	if err != nil {
		t.Fatalf("CheckGitHubAuth() error = %v", err)
	}
	if !auth.Satisfied {
		t.Fatalf("github-auth = %+v, want satisfied for active Enterprise host", auth)
	}
}

func TestCheckGitHubAuth_MissingCommandRunnerDoesNotReportAuthenticated(t *testing.T) {
	svc := NewWithLookPath(&fakeHarnessCatalog{}, lookPathFound(map[string]string{"gh": "/usr/bin/gh"}))

	auth, err := svc.CheckGitHubAuth(context.Background())
	if err != nil {
		t.Fatalf("CheckGitHubAuth() error = %v", err)
	}
	if auth.Satisfied || auth.Required {
		t.Fatalf("github-auth = %+v, want unsatisfied advisory", auth)
	}
}

func TestOpenGitHubAuthTerminalUsesTrustedCommand(t *testing.T) {
	svc := NewWithLookPath(&fakeHarnessCatalog{}, lookPathFound(map[string]string{"gh": "/usr/local/bin/gh"}))
	opener := &fakeGitHubAuthTerminalOpener{}
	svc.SetGitHubAuthTerminalOpener(opener)

	terminal, err := svc.OpenGitHubAuthTerminal(context.Background())
	if err != nil {
		t.Fatalf("OpenGitHubAuthTerminal() error = %v", err)
	}
	if terminal.HandleID != "shellterm-github" {
		t.Fatalf("terminal handle = %q, want shellterm-github", terminal.HandleID)
	}
	if got, want := opener.input.Argv, []string{"/usr/local/bin/gh", "auth", "login"}; !slices.Equal(got, want) {
		t.Fatalf("terminal argv = %#v, want %#v", got, want)
	}
	if opener.input.Title != "Connect GitHub" {
		t.Fatalf("terminal title = %q, want Connect GitHub", opener.input.Title)
	}
}

func TestCheck_ContextAlreadyDone(t *testing.T) {
	catalog := &fakeHarnessCatalog{}
	svc := NewWithLookPath(catalog, lookPathFound(nil))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := svc.Check(ctx)
	if err == nil {
		t.Fatalf("Check() error = nil, want context.Canceled")
	}
}

func requirementByID(t *testing.T, report Report, id string) Requirement {
	t.Helper()
	for _, req := range report.Requirements {
		if req.ID == id {
			return req
		}
	}
	t.Fatalf("no requirement with id %q in %+v", id, report.Requirements)
	return Requirement{}
}

// The first subprocess consumes its entire budget; fallback must still be able
// to read credentials, without extending a caller's own cancellation deadline.
func TestCheckGitHubAuth_FallbackHasFreshDeadline(t *testing.T) {
	calls := 0
	runner := &fakeCommandRunner{run: func(ctx context.Context) error {
		calls++
		if calls == 1 {
			<-ctx.Done()
			return ctx.Err()
		}
		if err := ctx.Err(); err != nil {
			t.Fatalf("fallback started with expired context: %v", err)
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("fallback has no deadline")
		}
		return nil
	}}
	svc := NewWithCommandRunner(&fakeHarnessCatalog{}, executableFinderFunc(lookPathFound(map[string]string{"gh": "/usr/bin/gh"})), runner)
	auth, err := svc.CheckGitHubAuth(context.Background())
	if err != nil || !auth.Satisfied || calls != 2 {
		t.Fatalf("auth = %+v, err = %v, calls = %d", auth, err, calls)
	}
}

func TestCheckGitHubAuth_EmptyHostsIsSignedOut(t *testing.T) {
	runner := &fakeCommandRunner{stdout: `{"hosts":{}}`}
	svc := NewWithCommandRunner(&fakeHarnessCatalog{}, executableFinderFunc(lookPathFound(map[string]string{"gh": "/usr/bin/gh"})), runner)
	auth, err := svc.CheckGitHubAuth(context.Background())
	if err != nil || auth.Satisfied {
		t.Fatalf("auth = %+v, err = %v", auth, err)
	}
	if len(runner.argvLog) != 1 {
		t.Fatalf("unexpected token fallback: %v", runner.argvLog)
	}
}
