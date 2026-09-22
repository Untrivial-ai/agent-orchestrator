package shellterm

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
)

func TestBuildCueCommandArgv(t *testing.T) {
	command := `printf "héllo world" | tee output.txt`
	tests := []struct {
		name  string
		goos  string
		shell []string
		want  []string
	}{
		{"macOS zsh", "darwin", []string{"/bin/zsh"}, []string{"/bin/zsh", "-lc", command}},
		{"Linux bash", "linux", []string{"/bin/bash"}, []string{"/bin/bash", "-lc", command}},
		{"PowerShell", "windows", []string{`C:\Program Files\PowerShell\7\pwsh.exe`, "-NoLogo"}, []string{`C:\Program Files\PowerShell\7\pwsh.exe`, "-NoLogo", "-Command", command}},
		{"Command Prompt", "windows", []string{`C:\Windows\System32\cmd.exe`}, []string{`C:\Windows\System32\cmd.exe`, "/D", "/S", "/C", command}},
		{"Git Bash", "windows", []string{`C:\Program Files\Git\bin\bash.exe`, "--login", "-i"}, []string{`C:\Program Files\Git\bin\bash.exe`, "--login", "-i", "-c", command}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := buildCueCommandArgv(tt.shell, command, tt.goos)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, tt.want) {
				t.Fatalf("argv = %#v, want %#v", got, tt.want)
			}
			if got[len(got)-1] != command {
				t.Fatal("command was split or rewritten")
			}
		})
	}
}

func cueTestShell(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		return "powershell"
	}
	t.Setenv("SHELL", "/bin/sh")
	return ""
}

func TestOpenCueCommandTerminalUsesProjectRootWithoutSession(t *testing.T) {
	root := t.TempDir()
	rt := newFakeShellRuntime()
	st := &fakeShellTerminalStore{}
	svc := newTestService(rt, st, &fakeProjectRootLocator{roots: map[domain.ProjectID]string{"portfolio": root}})

	term, err := svc.OpenCueCommandTerminal(context.Background(), OpenCueCommandTerminalInput{
		ProjectID: "portfolio", Shell: cueTestShell(t), Command: `echo "héllo"`, Title: "Build",
	})
	if err != nil {
		t.Fatal(err)
	}
	if term.ProjectID != "portfolio" || term.SessionID != "" || term.WorkingDir != root {
		t.Fatalf("terminal = %+v", term)
	}
	if len(rt.created) != 1 || rt.created[0].WorkspacePath != root || !rt.created[0].ExitOnCommandCompletion {
		t.Fatalf("runtime creates = %+v", rt.created)
	}
	if rt.created[0].Argv[len(rt.created[0].Argv)-1] != `echo "héllo"` {
		t.Fatalf("command argv = %#v", rt.created[0].Argv)
	}
}

func TestCueCommandTerminalStatusAndStopRetainTerminal(t *testing.T) {
	root := t.TempDir()
	rt := newFakeShellRuntime()
	st := &fakeShellTerminalStore{}
	svc := newTestService(rt, st, &fakeProjectRootLocator{roots: map[domain.ProjectID]string{"portfolio": root}})
	term, err := svc.OpenCueCommandTerminal(context.Background(), OpenCueCommandTerminalInput{
		ProjectID: "portfolio", Shell: cueTestShell(t), Command: "sleep 10", Title: "Wait",
	})
	if err != nil {
		t.Fatal(err)
	}
	status, err := svc.CueCommandTerminalStatus(context.Background(), term.HandleID)
	if err != nil || status.State != "running" {
		t.Fatalf("running status=%+v err=%v", status, err)
	}
	status, err = svc.StopCueCommandTerminal(context.Background(), term.HandleID)
	if err != nil || status.State != "stopped" || !slices.Equal(rt.interrupted, []string{term.HandleID}) {
		t.Fatalf("stop status=%+v err=%v interrupts=%v", status, err, rt.interrupted)
	}
	listed, err := svc.ListShellTerminalsForCurrentAppRun(context.Background())
	if err != nil || len(listed) != 1 || listed[0].HandleID != term.HandleID || len(st.records) != 1 {
		t.Fatalf("listed=%+v records=%+v err=%v", listed, st.records, err)
	}
	status, err = svc.StopCueCommandTerminal(context.Background(), term.HandleID)
	if err != nil || status.State != "stopped" || len(rt.interrupted) != 1 {
		t.Fatalf("idempotent stop status=%+v err=%v interrupts=%v", status, err, rt.interrupted)
	}
}

func TestCueCommandTerminalStatusReportsExitAndRejectsOtherShells(t *testing.T) {
	root := t.TempDir()
	rt := newFakeShellRuntime()
	svc := newTestService(rt, &fakeShellTerminalStore{}, &fakeProjectRootLocator{roots: map[domain.ProjectID]string{"portfolio": root}})
	term, err := svc.OpenCueCommandTerminal(context.Background(), OpenCueCommandTerminalInput{
		ProjectID: "portfolio", Shell: cueTestShell(t), Command: "true", Title: "Done",
	})
	if err != nil {
		t.Fatal(err)
	}
	rt.childExited = true
	status, err := svc.CueCommandTerminalStatus(context.Background(), term.HandleID)
	if err != nil || status.State != "exited" {
		t.Fatalf("exit status=%+v err=%v", status, err)
	}
	_, err = svc.CueCommandTerminalStatus(context.Background(), "shellterm-other")
	var apiErr *apierr.Error
	if !errors.As(err, &apiErr) || apiErr.Kind != apierr.KindNotFound {
		t.Fatalf("other status error=%v", err)
	}
}

func TestOpenCueCommandTerminalUsesExactSessionWorktree(t *testing.T) {
	root := t.TempDir()
	worktree := t.TempDir()
	rt := newFakeShellRuntime()
	st := &fakeShellTerminalStore{}
	sessions := &fakeSessionWorkspaceLocator{sessions: map[domain.SessionID]fakeSessionWorkspace{
		"portfolio-3": {workspacePath: worktree, projectID: "portfolio", activity: domain.ActivityIdle},
	}}
	svc := newTestServiceWithSessions(rt, st, &fakeProjectRootLocator{roots: map[domain.ProjectID]string{"portfolio": root}}, sessions)

	term, err := svc.OpenCueCommandTerminal(context.Background(), OpenCueCommandTerminalInput{
		ProjectID: "portfolio", SessionID: "portfolio-3", Shell: cueTestShell(t), Command: "go test ./...", Title: "Tests",
	})
	if err != nil {
		t.Fatal(err)
	}
	if term.WorkingDir != filepath.Clean(worktree) || term.SessionID != "portfolio-3" {
		t.Fatalf("terminal = %+v", term)
	}
	if len(st.records) != 1 || st.records[0].SessionID != "portfolio-3" {
		t.Fatalf("records = %+v", st.records)
	}
}

func TestOpenCueCommandTerminalRejectsUnusableSessionWithoutSpawning(t *testing.T) {
	worktree := t.TempDir()
	tests := []struct {
		name    string
		id      domain.SessionID
		target  fakeSessionWorkspace
		project domain.ProjectID
	}{
		{"unknown", "missing", fakeSessionWorkspace{}, "portfolio"},
		{"wrong project", "target", fakeSessionWorkspace{workspacePath: worktree, projectID: "other"}, "portfolio"},
		{"terminated", "target", fakeSessionWorkspace{workspacePath: worktree, projectID: "portfolio", terminated: true}, "portfolio"},
		{"exited", "target", fakeSessionWorkspace{workspacePath: worktree, projectID: "portfolio", activity: domain.ActivityExited}, "portfolio"},
		{"blocked", "target", fakeSessionWorkspace{workspacePath: worktree, projectID: "portfolio", activity: domain.ActivityBlocked}, "portfolio"},
		{"missing worktree", "target", fakeSessionWorkspace{workspacePath: filepath.Join(worktree, "gone"), projectID: "portfolio"}, "portfolio"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rt := newFakeShellRuntime()
			sessions := map[domain.SessionID]fakeSessionWorkspace{}
			if tt.name != "unknown" {
				sessions[tt.id] = tt.target
			}
			svc := newTestServiceWithSessions(rt, &fakeShellTerminalStore{}, &fakeProjectRootLocator{}, &fakeSessionWorkspaceLocator{sessions: sessions})
			_, err := svc.OpenCueCommandTerminal(context.Background(), OpenCueCommandTerminalInput{
				ProjectID: tt.project, SessionID: tt.id, Shell: cueTestShell(t), Command: "pwd", Title: "Where",
			})
			if err == nil {
				t.Fatal("expected rejection")
			}
			if len(rt.created) != 0 {
				t.Fatalf("runtime created for rejected target: %+v", rt.created)
			}
		})
	}
}

func TestOpenCueCommandTerminalPropagatesLookupAndRuntimeFailures(t *testing.T) {
	lookupErr := errors.New("database unavailable")
	rt := newFakeShellRuntime()
	svc := newTestServiceWithSessions(rt, &fakeShellTerminalStore{}, &fakeProjectRootLocator{}, &fakeSessionWorkspaceLocator{err: lookupErr})
	_, err := svc.OpenCueCommandTerminal(context.Background(), OpenCueCommandTerminalInput{
		ProjectID: "portfolio", SessionID: "portfolio-3", Shell: cueTestShell(t), Command: "pwd", Title: "Where",
	})
	if !errors.Is(err, lookupErr) || len(rt.created) != 0 {
		t.Fatalf("lookup error = %v, creates = %+v", err, rt.created)
	}

	rt = newFakeShellRuntime()
	rt.createErr = errors.New("PTY unavailable")
	root := t.TempDir()
	svc = newTestService(rt, &fakeShellTerminalStore{}, &fakeProjectRootLocator{roots: map[domain.ProjectID]string{"portfolio": root}})
	_, err = svc.OpenCueCommandTerminal(context.Background(), OpenCueCommandTerminalInput{
		ProjectID: "portfolio", Shell: cueTestShell(t), Command: "pwd", Title: "Where",
	})
	if err == nil || len(rt.created) != 0 {
		t.Fatalf("runtime error = %v, creates = %+v", err, rt.created)
	}
}

func TestOpenCueCommandTerminalRejectsInvalidInput(t *testing.T) {
	svc := newTestService(newFakeShellRuntime(), &fakeShellTerminalStore{}, &fakeProjectRootLocator{})
	for _, in := range []OpenCueCommandTerminalInput{
		{Command: "pwd", Title: "Where"},
		{ProjectID: "portfolio", Command: "   ", Title: "Where"},
		{ProjectID: "portfolio", Command: "pwd", Title: "   "},
	} {
		_, err := svc.OpenCueCommandTerminal(context.Background(), in)
		var apiErr *apierr.Error
		if !errors.As(err, &apiErr) || apiErr.Kind != apierr.KindInvalid {
			t.Fatalf("error = %v, want invalid api error", err)
		}
	}
}

func TestOpenCueCommandTerminalHonorsCancellation(t *testing.T) {
	root := t.TempDir()
	rt := newFakeShellRuntime()
	rt.createCtxErr = true
	svc := newTestService(rt, &fakeShellTerminalStore{}, &fakeProjectRootLocator{roots: map[domain.ProjectID]string{"portfolio": root}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := svc.OpenCueCommandTerminal(ctx, OpenCueCommandTerminalInput{
		ProjectID: "portfolio", Shell: cueTestShell(t), Command: "pwd", Title: "Where",
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context canceled", err)
	}
	if len(rt.created) != 0 {
		t.Fatalf("created = %+v, want none", rt.created)
	}
}
