package shellterm

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
)

func cueTestShell(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		return "powershell"
	}
	t.Setenv("SHELL", "/bin/sh")
	return ""
}

func TestCueCommandOpensNewNormalProjectShellForEveryInvocation(t *testing.T) {
	root := t.TempDir()
	rt := newFakeShellRuntime()
	st := &fakeShellTerminalStore{}
	svc := newTestService(rt, st, &fakeProjectRootLocator{roots: map[domain.ProjectID]string{"portfolio": root}})
	input := RunCueCommandInput{ProjectID: "portfolio", Shell: cueTestShell(t), Command: `echo "héllo"`}
	first, err := svc.RunCueCommand(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if first.WorkingDir != root || first.SessionID != "" || len(rt.created) != 1 || st.records[0].Transient {
		t.Fatalf("first terminal = %+v, creates = %+v, records = %+v", first, rt.created, st.records)
	}
	if sent := <-rt.sentCh; sent.handleID != first.HandleID || sent.input != input.Command {
		t.Fatalf("sent = %+v", sent)
	}
	input.Command = "npm run build"
	second, err := svc.RunCueCommand(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if second.HandleID == first.HandleID || second.Title == first.Title || len(rt.created) != 2 || len(st.records) != 2 || st.records[1].Transient {
		t.Fatalf("second = %+v, creates = %+v", second, rt.created)
	}
	if sent := <-rt.sentCh; sent.handleID != second.HandleID || sent.input != input.Command {
		t.Fatalf("sent = %+v", sent)
	}
}

func TestCueCommandDoesNotSendToExistingTerminals(t *testing.T) {
	root, workspace := t.TempDir(), t.TempDir()
	rt := newFakeShellRuntime()
	st := &fakeShellTerminalStore{}
	sessions := &fakeSessionWorkspaceLocator{sessions: map[domain.SessionID]fakeSessionWorkspace{"session": {workspacePath: workspace, projectID: "portfolio", activity: domain.ActivityIdle}}}
	svc := newTestServiceWithSessions(rt, st, &fakeProjectRootLocator{roots: map[domain.ProjectID]string{"portfolio": root}}, sessions)
	add := func(id string, project domain.ProjectID, session domain.SessionID, dir string, when time.Time) {
		st.records = append(st.records, ShellTerminalRecord{HandleID: id, ProjectID: project, SessionID: session, WorkingDir: dir, Title: id, CreatedAt: when})
		rt.aliveByHandle[id] = true
	}
	now := time.Now()
	add("old", "portfolio", "session", workspace, now.Add(-time.Hour))
	add("new", "portfolio", "session", workspace, now)
	add("board", "portfolio", "", root, now.Add(time.Hour))
	add("other", "other", "session", workspace, now.Add(2*time.Hour))
	rt.childProbeErr = errors.New("existing terminal probe failed")
	input := RunCueCommandInput{ProjectID: "portfolio", SessionID: "session", Shell: cueTestShell(t), Command: "pwd"}
	term, err := svc.RunCueCommand(context.Background(), input)
	if err != nil || term.HandleID == "old" || term.HandleID == "new" || term.WorkingDir != workspace || term.SessionID != "session" {
		t.Fatalf("new session terminal = %+v, err = %v", term, err)
	}
	if sent := <-rt.sentCh; sent.handleID != term.HandleID || sent.input != "pwd" {
		t.Fatalf("session send = %+v", sent)
	}
	firstHandle := term.HandleID
	term, err = svc.RunCueCommand(context.Background(), input)
	if err != nil || term.HandleID == firstHandle {
		t.Fatalf("next session terminal = %+v, err = %v", term, err)
	}
	if sent := <-rt.sentCh; sent.handleID != term.HandleID || sent.input != "pwd" {
		t.Fatalf("next session send = %+v", sent)
	}
	input.SessionID = ""
	term, err = svc.RunCueCommand(context.Background(), input)
	if err != nil || term.HandleID == "board" || term.SessionID != "" || term.WorkingDir != root {
		t.Fatalf("new board terminal = %+v, err = %v", term, err)
	}
	if sent := <-rt.sentCh; sent.handleID != term.HandleID || sent.input != "pwd" {
		t.Fatalf("board send = %+v", sent)
	}
	if len(rt.created) != 3 {
		t.Fatalf("creates = %+v, want one per invocation", rt.created)
	}
}

func TestCueCommandUsesExactSessionWorktreeAndRejectsUnusableTargets(t *testing.T) {
	root, workspace := t.TempDir(), t.TempDir()
	for _, tc := range []struct {
		name   string
		target fakeSessionWorkspace
		valid  bool
	}{
		{"active", fakeSessionWorkspace{workspacePath: workspace, projectID: "portfolio", activity: domain.ActivityIdle}, true},
		{"wrong project", fakeSessionWorkspace{workspacePath: workspace, projectID: "other"}, false},
		{"terminated", fakeSessionWorkspace{workspacePath: workspace, projectID: "portfolio", terminated: true}, false},
		{"exited", fakeSessionWorkspace{workspacePath: workspace, projectID: "portfolio", activity: domain.ActivityExited}, false},
		{"blocked", fakeSessionWorkspace{workspacePath: workspace, projectID: "portfolio", activity: domain.ActivityBlocked}, false},
		{"missing worktree", fakeSessionWorkspace{workspacePath: filepath.Join(workspace, "gone"), projectID: "portfolio"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt := newFakeShellRuntime()
			svc := newTestServiceWithSessions(rt, &fakeShellTerminalStore{}, &fakeProjectRootLocator{roots: map[domain.ProjectID]string{"portfolio": root}}, &fakeSessionWorkspaceLocator{sessions: map[domain.SessionID]fakeSessionWorkspace{"session": tc.target}})
			term, err := svc.RunCueCommand(context.Background(), RunCueCommandInput{ProjectID: "portfolio", SessionID: "session", Shell: cueTestShell(t), Command: "pwd"})
			if tc.valid {
				if err != nil || term.WorkingDir != workspace || len(rt.created) != 1 {
					t.Fatalf("terminal = %+v, err = %v, creates = %+v", term, err, rt.created)
				}
			} else if err == nil || len(rt.created) != 0 {
				t.Fatalf("err = %v, creates = %+v", err, rt.created)
			}
		})
	}
}

func TestCueCommandDoesNotRetryAmbiguousSendFailure(t *testing.T) {
	root := t.TempDir()
	rt := newFakeShellRuntime()
	rt.sendErr = errors.New("send failed")
	st := &fakeShellTerminalStore{}
	svc := newTestService(rt, st, &fakeProjectRootLocator{roots: map[domain.ProjectID]string{"portfolio": root}})
	_, err := svc.RunCueCommand(context.Background(), RunCueCommandInput{ProjectID: "portfolio", Shell: cueTestShell(t), Command: "npm test"})
	if !errors.Is(err, rt.sendErr) || len(rt.created) != 1 || len(rt.sentCh) != 1 || len(st.records) != 1 || len(rt.destroyed) != 0 {
		t.Fatalf("err = %v, creates = %+v, sends = %d, records = %+v, destroyed = %+v", err, rt.created, len(rt.sentCh), st.records, rt.destroyed)
	}
}

func TestCueCommandRejectsInvalidInputAndCancellation(t *testing.T) {
	svc := newTestService(newFakeShellRuntime(), &fakeShellTerminalStore{}, &fakeProjectRootLocator{})
	for _, input := range []RunCueCommandInput{{Command: "pwd"}, {ProjectID: "portfolio", Command: "  "}} {
		_, err := svc.RunCueCommand(context.Background(), input)
		var apiErr *apierr.Error
		if !errors.As(err, &apiErr) || apiErr.Kind != apierr.KindInvalid {
			t.Fatalf("err = %v", err)
		}
	}
	root := t.TempDir()
	rt := newFakeShellRuntime()
	svc = newTestService(rt, &fakeShellTerminalStore{}, &fakeProjectRootLocator{roots: map[domain.ProjectID]string{"portfolio": root}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := svc.RunCueCommand(ctx, RunCueCommandInput{ProjectID: "portfolio", Shell: cueTestShell(t), Command: "pwd"})
	if !errors.Is(err, context.Canceled) || len(rt.created) != 0 {
		t.Fatalf("err = %v, creates = %+v", err, rt.created)
	}
}
