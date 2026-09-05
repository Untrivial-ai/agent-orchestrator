package conpty

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestNormalizeLinuxProcExecutable(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		want       string
		wantAOExec bool
	}{
		{name: "deleted host", path: "/opt/ao (deleted)", want: "/opt/ao", wantAOExec: true},
		{name: "deleted child supervisor", path: "/opt/agent-orchestrator (deleted)", want: "/opt/agent-orchestrator", wantAOExec: true},
		{name: "live host", path: "/opt/ao", want: "/opt/ao", wantAOExec: true},
		{name: "foreign deleted executable", path: "/usr/bin/python (deleted)", want: "/usr/bin/python", wantAOExec: false},
		{name: "embedded marker", path: "/opt/ao (deleted)/child", want: "/opt/ao (deleted)/child", wantAOExec: false},
		{name: "trailing whitespace", path: "/opt/ao (deleted) ", want: "/opt/ao (deleted) ", wantAOExec: false},
		{name: "different case", path: "/opt/ao (Deleted)", want: "/opt/ao (Deleted)", wantAOExec: false},
		{name: "repeated marker", path: "/opt/ao (deleted) (deleted)", want: "/opt/ao (deleted)", wantAOExec: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := normalizeLinuxProcExecutable(test.path)
			if got != test.want {
				t.Fatalf("normalizeLinuxProcExecutable(%q) = %q, want %q", test.path, got, test.want)
			}
			if gotAOExec := isAOExecutable(got); gotAOExec != test.wantAOExec {
				t.Fatalf("isAOExecutable(%q) = %v, want %v", got, gotAOExec, test.wantAOExec)
			}
		})
	}
}

func TestVerifyLegacyHostIdentityAllowsCollectionBeyondStatusProbeTimeout(t *testing.T) {
	startedAt := time.Now().Add(-time.Second).UTC()
	sess := &hostSession{
		sessionID:    "project-slow-proof",
		pid:          15287,
		registeredAt: startedAt.Add(500 * time.Millisecond).Format(time.RFC3339Nano),
	}
	status := StatusPayload{Alive: true, PID: 15289, ProtocolVersion: 2}
	delay := isAliveTimeout + 100*time.Millisecond
	runtime := &Runtime{
		legacyCollector: func(ctx context.Context, _ *hostSession, _ StatusPayload) (legacyHostIdentityEvidence, error) {
			timer := time.NewTimer(delay)
			defer timer.Stop()
			select {
			case <-timer.C:
				return legacyHostIdentityEvidence{
					listenerPID: sess.pid,
					host: legacyProcessIdentity{
						pid: sess.pid, startedAt: startedAt, executable: "/app/ao",
						argv: []string{"/app/ao", "pty-host", sess.sessionID, "/workspace", "/bin/zsh"},
					},
					child: &legacyProcessIdentity{
						pid: status.PID, ppid: sess.pid, startedAt: startedAt.Add(time.Millisecond),
						executable: "/bin/zsh", argv: []string{"/bin/zsh"},
					},
				}, nil
			case <-ctx.Done():
				return legacyHostIdentityEvidence{}, ctx.Err()
			}
		},
	}

	if err := runtime.verifyLegacyHostIdentity(context.Background(), sess, status); err != nil {
		t.Fatalf("verifyLegacyHostIdentity after %s collection: %v", delay, err)
	}
}

func TestValidateLegacyHostIdentityAcceptsExactShippedProcess(t *testing.T) {
	startedAt := time.Date(2026, 8, 29, 6, 3, 37, 200_000_000, time.UTC)
	sess := &hostSession{
		sessionID:    "project-134",
		addr:         "127.0.0.1:54576",
		pid:          15287,
		launchID:     "launch-40",
		registeredAt: startedAt.Add(300 * time.Millisecond).Format(time.RFC3339Nano),
	}
	status := StatusPayload{Alive: true, PID: 15289, ProtocolVersion: 2}
	evidence := legacyHostIdentityEvidence{
		listenerPID: 15287,
		host: legacyProcessIdentity{
			pid:        15287,
			startedAt:  startedAt,
			executable: "/Applications/Agent Orchestrator.app/Contents/Resources/daemon/ao",
			argv: []string{
				"/Applications/Agent Orchestrator.app/Contents/Resources/daemon/ao",
				"pty-host", "project-134", "/workspace",
				"/Applications/Agent Orchestrator.app/Contents/Resources/daemon/ao",
				"agent-process", "supervise", "--session", "project-134",
				"--launch", "launch-40", "--", "claude",
			},
		},
		child: &legacyProcessIdentity{
			pid:        15289,
			ppid:       15287,
			startedAt:  startedAt.Add(100 * time.Millisecond),
			executable: "/Applications/Agent Orchestrator.app/Contents/Resources/daemon/ao",
			argv: []string{
				"/Applications/Agent Orchestrator.app/Contents/Resources/daemon/ao",
				"agent-process", "supervise", "--session", "project-134",
				"--launch", "launch-40", "--", "claude",
			},
		},
	}
	if err := validateLegacyHostIdentity(sess, status, evidence); err != nil {
		t.Fatal(err)
	}
}

func TestValidateLegacyHostIdentityAcceptsLaunchlessAuxiliaryHost(t *testing.T) {
	startedAt := time.Date(2026, 8, 29, 6, 3, 37, 0, time.UTC)
	sess := &hostSession{
		sessionID:    "review-terminal-1",
		pid:          15287,
		registeredAt: startedAt.Add(time.Second).Format(time.RFC3339Nano),
	}
	status := StatusPayload{Alive: true, PID: 15289, ProtocolVersion: 2}
	evidence := legacyHostIdentityEvidence{
		listenerPID: 15287,
		host: legacyProcessIdentity{
			pid:        15287,
			startedAt:  startedAt,
			executable: "/app/ao",
			argv:       []string{"/app/ao", "pty-host", "review-terminal-1", "/workspace", "/bin/zsh"},
		},
		child: &legacyProcessIdentity{
			pid:        15289,
			ppid:       15287,
			startedAt:  startedAt.Add(time.Millisecond),
			executable: "/bin/zsh",
			argv:       []string{"/bin/zsh"},
		},
	}
	if err := validateLegacyHostIdentity(sess, status, evidence); err != nil {
		t.Fatal(err)
	}
}

func TestValidateLegacyHostIdentityFailsClosed(t *testing.T) {
	startedAt := time.Date(2026, 8, 29, 6, 3, 37, 0, time.UTC)
	baseSession := func() *hostSession {
		return &hostSession{
			sessionID: "project-134", pid: 15287, launchID: "launch-40",
			registeredAt: startedAt.Format(time.RFC3339Nano),
		}
	}
	baseStatus := func() StatusPayload {
		return StatusPayload{Alive: true, PID: 15289, ProtocolVersion: 2}
	}
	baseEvidence := func() legacyHostIdentityEvidence {
		return legacyHostIdentityEvidence{
			listenerPID: 15287,
			host: legacyProcessIdentity{
				pid: 15287, startedAt: startedAt, executable: "/app/ao",
				argv: []string{"/app/ao", "pty-host", "project-134", "/workspace", "/app/ao", "agent-process", "supervise", "--session", "project-134", "--launch", "launch-40", "--", "agent"},
			},
			child: &legacyProcessIdentity{
				pid: 15289, ppid: 15287, startedAt: startedAt.Add(time.Millisecond),
				executable: "/app/ao",
				argv:       []string{"/app/ao", "agent-process", "supervise", "--session", "project-134", "--launch", "launch-40", "--", "agent"},
			},
		}
	}

	tests := []struct {
		name string
		edit func(*hostSession, *StatusPayload, *legacyHostIdentityEvidence)
		want string
	}{
		{name: "foreign listener", edit: func(_ *hostSession, _ *StatusPayload, e *legacyHostIdentityEvidence) { e.listenerPID++ }, want: "listener owner"},
		{name: "reused host pid", edit: func(_ *hostSession, _ *StatusPayload, e *legacyHostIdentityEvidence) { e.host.pid++ }, want: "host process pid"},
		{name: "foreign executable", edit: func(_ *hostSession, _ *StatusPayload, e *legacyHostIdentityEvidence) {
			e.host.executable = "/usr/bin/python"
		}, want: "not AO"},
		{name: "foreign session", edit: func(_ *hostSession, _ *StatusPayload, e *legacyHostIdentityEvidence) { e.host.argv[2] = "other" }, want: "host argv"},
		{name: "missing managed supervisor", edit: func(_ *hostSession, _ *StatusPayload, e *legacyHostIdentityEvidence) {
			e.host.argv = append(e.host.argv[:4], "claude")
		}, want: "supervisor argv"},
		{name: "foreign launch", edit: func(_ *hostSession, _ *StatusPayload, e *legacyHostIdentityEvidence) { e.host.argv[10] = "other" }, want: "supervisor argv"},
		{name: "reused pid timestamp", edit: func(_ *hostSession, _ *StatusPayload, e *legacyHostIdentityEvidence) {
			e.host.startedAt = startedAt.Add(time.Hour)
		}, want: "does not match"},
		{name: "missing child", edit: func(_ *hostSession, _ *StatusPayload, e *legacyHostIdentityEvidence) { e.child = nil }, want: "child process"},
		{name: "foreign child parent", edit: func(_ *hostSession, _ *StatusPayload, e *legacyHostIdentityEvidence) { e.child.ppid++ }, want: "not parented"},
		{name: "foreign child executable", edit: func(_ *hostSession, _ *StatusPayload, e *legacyHostIdentityEvidence) {
			e.child.executable = "/usr/bin/python"
		}, want: "not AO supervisor"},
		{name: "missing child supervisor", edit: func(_ *hostSession, _ *StatusPayload, e *legacyHostIdentityEvidence) {
			e.child.argv = []string{"claude"}
		}, want: "child supervisor"},
		{name: "foreign child launch", edit: func(_ *hostSession, _ *StatusPayload, e *legacyHostIdentityEvidence) { e.child.argv[6] = "other" }, want: "child supervisor"},
		{name: "partial v3 identity", edit: func(_ *hostSession, s *StatusPayload, _ *legacyHostIdentityEvidence) { s.SessionID = "project-134" }, want: "partial authenticated"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sess, status, evidence := baseSession(), baseStatus(), baseEvidence()
			test.edit(sess, &status, &evidence)
			err := validateLegacyHostIdentity(sess, status, evidence)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}
