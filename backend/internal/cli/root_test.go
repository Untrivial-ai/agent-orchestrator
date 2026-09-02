package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/aoagents/agent-orchestrator/backend/internal/daemonmeta"
	"github.com/aoagents/agent-orchestrator/backend/internal/runfile"
	"github.com/aoagents/agent-orchestrator/backend/internal/telemetrymeta"
)

func TestRootHelpDoesNotShowDaemon(t *testing.T) {
	out, _, err := executeCLI(t, Deps{}, "--help")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "\n  daemon") {
		t.Fatalf("hidden daemon command leaked into help:\n%s", out)
	}
	for _, want := range []string{"start", "stop", "status", "doctor", "completion", "version"} {
		if !strings.Contains(out, want) {
			t.Fatalf("help missing %q:\n%s", want, out)
		}
	}
}

func TestRootCommandsHaveUniqueNames(t *testing.T) {
	seen := make(map[string]struct{})
	for _, cmd := range NewRootCommand(Deps{}).Commands() {
		if _, exists := seen[cmd.Name()]; exists {
			t.Fatalf("root command %q is registered more than once", cmd.Name())
		}
		seen[cmd.Name()] = struct{}{}
	}
}

func TestCommandsRejectUnexpectedArgs(t *testing.T) {
	for _, args := range [][]string{
		{"daemon", "extra"},
		{"start", "extra"},
		{"stop", "extra"},
		{"status", "extra"},
		{"doctor", "extra"},
		{"version", "extra"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			_, _, err := executeCLI(t, Deps{}, args...)
			if err == nil {
				t.Fatal("expected usage error")
			}
			if got := ExitCode(err); got != 2 {
				t.Fatalf("ExitCode(%v) = %d, want 2", err, got)
			}
		})
	}
}

func TestChatHostRejectsMalformedInternalArgumentsAsUsage(t *testing.T) {
	setConfigEnv(t)
	for _, args := range [][]string{
		{"chat-host"},
		{"chat-host", "session", "/tmp/data", "/tmp/work", "provider"},
		{"chat-host", "session", "/tmp/data", "/tmp/work", "not-a-separator", "provider"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			_, _, err := executeCLI(t, Deps{}, args...)
			if err == nil {
				t.Fatal("expected usage error")
			}
			if got := ExitCode(err); got != 2 {
				t.Fatalf("ExitCode(%v) = %d, want 2", err, got)
			}
		})
	}
}

func TestVersionEmitsCLIInvocationBestEffort(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "")
	cfg := setConfigEnv(t)
	called := make(chan map[string]string, 1)
	if err := runfile.Write(cfg.runFile, runfile.Info{PID: os.Getpid(), Port: 3001, StartedAt: time.Unix(100, 0).UTC()}); err != nil {
		t.Fatal(err)
	}

	if _, _, err := executeCLI(t, Deps{
		HTTPClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Path == "/internal/telemetry/cli-invoked" {
				defer req.Body.Close()
				var body map[string]string
				if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
					t.Fatalf("decode telemetry body: %v", err)
				}
				called <- body
				return jsonResponse(http.StatusAccepted, ""), nil
			}
			return jsonResponse(http.StatusNotFound, ""), nil
		})},
		ProcessAlive: func(pid int) bool { return pid == os.Getpid() },
	}, "version"); err != nil {
		t.Fatal(err)
	}
	select {
	case body := <-called:
		if body["actorType"] != "user" {
			t.Fatalf("telemetry actorType = %q, want user", body["actorType"])
		}
	default:
		t.Fatal("version did not emit CLI invocation")
	}
}

func TestShouldEmitCLIInvocationSkipsNonUsageAndRoutineInternalCommands(t *testing.T) {
	byName := map[string]*cobra.Command{}
	for _, cmd := range NewRootCommand(Deps{}).Commands() {
		byName[cmd.Name()] = cmd
	}
	for name, want := range map[string]bool{
		"daemon": false, // supervisor-driven bootstrapping, not human usage
		"start":  false,
		// hooks/status are routine internal polling paths; pty-host is only an
		// internal Windows runtime process. Successful executions should not
		// count as CLI usage.
		"hooks":    false,
		"pty-host": false,
		"status":   false,
		"spawn":    true,
	} {
		cmd, ok := byName[name]
		if !ok {
			t.Fatalf("command %q not registered", name)
		}
		if got := shouldEmitCLIInvocation(cmd); got != want {
			t.Errorf("shouldEmitCLIInvocation(%s) = %v, want %v", cmd.CommandPath(), got, want)
		}
	}
}

func TestCLIInvocationActorType(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "")
	byName := map[string]*cobra.Command{}
	for _, cmd := range NewRootCommand(Deps{}).Commands() {
		byName[cmd.Name()] = cmd
	}

	if got := cliInvocationActorType(byName["hooks"]); got != "agent" {
		t.Fatalf("hooks actor = %q, want agent", got)
	}
	if got := cliInvocationActorType(byName["status"]); got != "user" {
		t.Fatalf("status actor without session env = %q, want user", got)
	}

	t.Setenv("AO_SESSION_ID", "ao-session-1")
	if got := cliInvocationActorType(byName["status"]); got != "agent" {
		t.Fatalf("status actor with session env = %q, want agent", got)
	}
}

func TestTelemetryMetaClassifiesRegisteredCommandPaths(t *testing.T) {
	systemCommands := map[string]struct{}{
		"ao agent-process":           {},
		"ao agent-process supervise": {},
		"ao chat-host":               {},
		"ao completion":              {},
		"ao daemon":                  {},
		"ao help":                    {},
		"ao pty-host":                {},
		"ao start":                   {},
	}

	var failures []string
	var walk func(*cobra.Command)
	walk = func(cmd *cobra.Command) {
		for _, child := range cmd.Commands() {
			path := telemetrymeta.NormalizeCommandPath(child.CommandPath())
			got := telemetrymeta.CLIActorType("", path)
			if got == "system" {
				_, explicitlySystem := systemCommands[path]
				if !explicitlySystem && !telemetrymeta.IsRoutineInternalCLICommand(path) {
					failures = append(failures, path)
				}
			}
			walk(child)
		}
	}
	walk(NewRootCommand(Deps{}))

	if len(failures) > 0 {
		t.Fatalf("actor-less command paths classified as system: %s", strings.Join(failures, ", "))
	}
}

func TestUsageErrorCommandDropsUserArgs(t *testing.T) {
	for _, tc := range []struct {
		args        []string
		wantCommand string
		wantPath    string
	}{
		{[]string{"send", "orchestrator-pack-5", "wake", "heartbeat.reconcile"}, "send", "ao send"},
		{[]string{"status", "extra"}, "status", "ao status"},
		{[]string{"not-a-command", "whatever"}, "ao", "ao"},
		{[]string{"--bad-flag"}, "ao", "ao"},
	} {
		command, path := usageErrorCommand(tc.args)
		if command != tc.wantCommand || path != tc.wantPath {
			t.Errorf("usageErrorCommand(%v) = (%q, %q), want (%q, %q)", tc.args, command, path, tc.wantCommand, tc.wantPath)
		}
	}
}

func TestUsageErrorEmitsCLIUsageTelemetryBestEffort(t *testing.T) {
	cfg := setConfigEnv(t)
	called := make(chan string, 1)
	if err := runfile.Write(cfg.runFile, runfile.Info{PID: os.Getpid(), Port: 3001, StartedAt: time.Unix(100, 0).UTC()}); err != nil {
		t.Fatal(err)
	}

	deps := Deps{
		HTTPClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Path == "/internal/telemetry/cli-usage-error" {
				called <- req.URL.Path
				return jsonResponse(http.StatusAccepted, ""), nil
			}
			return jsonResponse(http.StatusNotFound, ""), nil
		})},
		ProcessAlive: func(pid int) bool { return pid == os.Getpid() },
	}
	err := executeWithDeps(deps, []string{"status", "extra"})
	if err == nil {
		t.Fatal("expected usage error")
	}
	select {
	case path := <-called:
		if path != "/internal/telemetry/cli-usage-error" {
			t.Fatalf("telemetry path = %q, want /internal/telemetry/cli-usage-error", path)
		}
	default:
		t.Fatal("usage error did not emit CLI usage telemetry")
	}
}

func TestStatusStoppedJSON(t *testing.T) {
	setConfigEnv(t)

	out, _, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return false }}, "status", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"state": "stopped"`) {
		t.Fatalf("status did not report stopped:\n%s", out)
	}
	if strings.Contains(out, "startedAt") {
		t.Fatalf("stopped JSON should omit startedAt:\n%s", out)
	}
}

func TestStopRemovesStaleRunFile(t *testing.T) {
	cfg := setConfigEnv(t)
	if err := runfile.Write(cfg.runFile, runfile.Info{PID: 999999, Port: 3001, StartedAt: time.Unix(100, 0).UTC()}); err != nil {
		t.Fatal(err)
	}

	out, _, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return false }}, "stop", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"state": "stopped"`) {
		t.Fatalf("stop did not report stopped:\n%s", out)
	}
	info, err := runfile.Read(cfg.runFile)
	if err != nil {
		t.Fatal(err)
	}
	if info != nil {
		t.Fatalf("stale run-file was not removed: %#v", info)
	}
}

func TestStopDoesNotShutdownUnverifiedReusedPID(t *testing.T) {
	cfg := setConfigEnv(t)
	shutdownCalled := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case "/readyz":
			_, _ = w.Write([]byte(`{"status":"ready"}`))
		case "/shutdown":
			shutdownCalled <- struct{}{}
			http.Error(w, "unexpected shutdown", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	if err := runfile.Write(cfg.runFile, runfile.Info{PID: 4242, Port: serverPort(t, srv.URL), StartedAt: time.Unix(100, 0).UTC()}); err != nil {
		t.Fatal(err)
	}

	out, _, err := executeCLI(t, Deps{
		ProcessAlive: func(pid int) bool { return pid == 4242 },
	}, "stop", "--json")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-shutdownCalled:
		t.Fatal("stop requested shutdown from a process whose health probe did not prove AO daemon ownership")
	default:
	}
	if !strings.Contains(out, `"state": "stopped"`) {
		t.Fatalf("stop did not report stopped:\n%s", out)
	}
	info, err := runfile.Read(cfg.runFile)
	if err != nil {
		t.Fatal(err)
	}
	if info != nil {
		t.Fatalf("unverified run-file was not removed: %#v", info)
	}
}

func TestStopUsesShutdownEndpoint(t *testing.T) {
	cfg := setConfigEnv(t)
	shutdownCalled := make(chan struct{}, 1)
	var shutdownSeen atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			_, _ = fmt.Fprintf(w, `{"status":"ok","service":%q,"pid":%d}`, daemonmeta.ServiceName, os.Getpid())
		case "/readyz":
			_, _ = fmt.Fprintf(w, `{"status":"ready","service":%q,"pid":%d}`, daemonmeta.ServiceName, os.Getpid())
		case "/shutdown":
			if err := runfile.Remove(cfg.runFile); err != nil {
				t.Fatal(err)
			}
			shutdownSeen.Store(true)
			shutdownCalled <- struct{}{}
			_, _ = fmt.Fprintf(w, `{"status":"shutting_down","service":%q,"pid":%d}`, daemonmeta.ServiceName, os.Getpid())
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	if err := runfile.Write(cfg.runFile, runfile.Info{PID: os.Getpid(), Port: serverPort(t, srv.URL), StartedAt: time.Unix(100, 0).UTC()}); err != nil {
		t.Fatal(err)
	}

	out, _, err := executeCLI(t, Deps{
		ProcessAlive: func(pid int) bool {
			if pid != os.Getpid() {
				return false
			}
			return !shutdownSeen.Load()
		},
	}, "stop", "--json")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-shutdownCalled:
	default:
		t.Fatal("stop did not call daemon shutdown endpoint")
	}
	if !strings.Contains(out, `"state": "stopped"`) {
		t.Fatalf("stop did not report stopped:\n%s", out)
	}
}

func TestStatusKeepsLiveProbeFailureUnhealthy(t *testing.T) {
	cfg := setConfigEnv(t)
	if err := runfile.Write(cfg.runFile, runfile.Info{PID: 4242, Port: closedPort(t), StartedAt: time.Unix(100, 0).UTC()}); err != nil {
		t.Fatal(err)
	}

	out, _, err := executeCLI(t, Deps{
		ProcessAlive: func(pid int) bool { return pid == 4242 },
	}, "status", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"state": "unhealthy"`) {
		t.Fatalf("status should keep live probe failures unhealthy:\n%s", out)
	}
	info, err := runfile.Read(cfg.runFile)
	if err != nil {
		t.Fatal(err)
	}
	if info == nil {
		t.Fatal("live probe failure should not remove run-file")
	}
}

func TestStopRefusesUnverifiedLivePID(t *testing.T) {
	cfg := setConfigEnv(t)
	if err := runfile.Write(cfg.runFile, runfile.Info{PID: 4242, Port: closedPort(t), StartedAt: time.Unix(100, 0).UTC()}); err != nil {
		t.Fatal(err)
	}

	_, _, err := executeCLI(t, Deps{
		ProcessAlive: func(pid int) bool { return pid == 4242 },
	}, "stop", "--json")
	if err == nil {
		t.Fatal("stop should fail when daemon ownership cannot be verified")
	}
	info, err := runfile.Read(cfg.runFile)
	if err != nil {
		t.Fatal(err)
	}
	if info == nil {
		t.Fatal("unverified live PID should remain tracked")
	}
}

type testConfig struct {
	runFile string
	dataDir string
}

func setConfigEnv(t *testing.T) testConfig {
	t.Helper()
	dir := t.TempDir()
	cfg := testConfig{
		runFile: filepath.Join(dir, "running.json"),
		dataDir: filepath.Join(dir, "data"),
	}
	t.Setenv("AO_RUN_FILE", cfg.runFile)
	t.Setenv("AO_DATA_DIR", cfg.dataDir)
	t.Setenv("AO_PORT", "3001")
	t.Setenv("AO_PROJECT_ID", "")
	t.Setenv("AO_REQUEST_TIMEOUT", "")
	t.Setenv("AO_SHUTDOWN_TIMEOUT", "")
	return cfg
}

func executeCLI(t *testing.T, deps Deps, args ...string) (string, string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	deps.Out = &out
	deps.Err = &errOut
	if deps.Sleep == nil {
		deps.Sleep = func(time.Duration) {}
	}
	cmd := NewRootCommand(deps)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), errOut.String(), err
}

func serverPort(t *testing.T, raw string) int {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	_, portRaw, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portRaw)
	if err != nil {
		t.Fatal(err)
	}
	return port
}

func closedPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	_, portRaw, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portRaw)
	if err != nil {
		t.Fatal(err)
	}
	return port
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func jsonResponse(status int, body string) *http.Response {
	if body == "" {
		body = "{}"
	}
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
