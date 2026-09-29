package integration

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/cli"
	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/runfile"
)

func TestClaudeResumeHookConfirmsNativeIdentityForCurrentLaunch(t *testing.T) {
	ctx := context.Background()
	st := newStack(t)
	session, _, _, err := st.sm.Spawn(ctx, ports.SpawnConfig{
		ProjectID: "mer",
		Kind:      domain.KindWorker,
		Branch:    "resume-hook",
		Prompt:    "continue",
	})
	if err != nil {
		t.Fatalf("spawn session: %v", err)
	}
	record, ok, err := st.store.GetSession(ctx, session.ID)
	if err != nil || !ok {
		t.Fatalf("read session: ok=%v err=%v", ok, err)
	}
	record.Metadata.RuntimeLaunchID = "launch-resumed"
	record.Metadata.AgentSessionID = "claude-native"
	record.Metadata.AgentSessionIDLaunchID = "launch-before-resume"
	if err := st.store.UpdateSession(ctx, record); err != nil {
		t.Fatalf("seed resumed session: %v", err)
	}

	server := httptest.NewServer(httpd.NewRouterWithControl(
		config.Config{},
		slog.New(slog.DiscardHandler),
		nil,
		httpd.APIDeps{Activity: st.lcm},
		httpd.ControlDeps{},
	))
	defer server.Close()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse daemon URL: %v", err)
	}
	_, portText, err := net.SplitHostPort(parsed.Host)
	if err != nil {
		t.Fatalf("split daemon host: %v", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("parse daemon port: %v", err)
	}
	runPath := filepath.Join(t.TempDir(), "running.json")
	if err := runfile.Write(runPath, runfile.Info{
		PID: os.Getpid(), Port: port, StartedAt: time.Unix(100, 0).UTC(),
	}); err != nil {
		t.Fatalf("write daemon run file: %v", err)
	}
	t.Setenv("AO_RUN_FILE", runPath)
	t.Setenv("AO_DATA_DIR", t.TempDir())
	t.Setenv("AO_SESSION_ID", string(session.ID))
	t.Setenv("AO_RUNTIME_LAUNCH_ID", "launch-resumed")

	var stdout, stderr bytes.Buffer
	command := cli.NewRootCommand(cli.Deps{
		In:           strings.NewReader(`{"source":"resume","session_id":"claude-native"}`),
		Out:          &stdout,
		Err:          &stderr,
		ProcessAlive: func(int) bool { return true },
		Sleep:        func(time.Duration) {},
	})
	command.SetArgs([]string{"hooks", "claude-code", "session-start"})
	if err := command.Execute(); err != nil {
		t.Fatalf("run Claude resume hook: %v\nstderr=%s", err, stderr.String())
	}

	got, ok, err := st.store.GetSession(ctx, session.ID)
	if err != nil || !ok {
		t.Fatalf("read updated session: ok=%v err=%v", ok, err)
	}
	if got.Metadata.AgentSessionID != "claude-native" ||
		got.Metadata.AgentSessionIDLaunchID != "launch-resumed" {
		t.Fatalf("native identity = id:%q launch:%q, want id:%q launch:%q",
			got.Metadata.AgentSessionID,
			got.Metadata.AgentSessionIDLaunchID,
			"claude-native",
			"launch-resumed",
		)
	}
}
