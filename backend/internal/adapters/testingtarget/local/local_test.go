package local

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	processutil "github.com/aoagents/agent-orchestrator/backend/internal/process"
	"github.com/aoagents/agent-orchestrator/backend/internal/runfile"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type fakeSystem struct {
	a        *Adapter
	s        *launch
	pids     map[int]processInfo
	times    map[int]time.Time
	signals  []int
	ready    map[string]any
	status   map[string]int
	windows  []string
	listener error
}

func fixture(t *testing.T) *fakeSystem {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	s := &launch{root: root, frontend: filepath.Join(root, "checkout", "frontend"), tmux: "/fixture/ao/tmux", port: 32100,
		daemon: filepath.Join(root, "daemon", "ao"), revision: "checked-revision", preflight: json.RawMessage(`{"goVersion":"go1.27.1","nodeVersion":"v22.23.2"}`),
		target: domain.TestTargetIdentity{ID: "test", LaunchID: "target-test", Generation: 1, ElectronPID: 11, ElectronStartedAt: now, DaemonPID: 12, DaemonStartedAt: now.Add(time.Second), DataDir: filepath.Join(root, "data")},
		owned:  map[int]time.Time{11: now, 12: now.Add(time.Second)}}
	s.rootInfo, err = os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(s.frontend), 0o700); err != nil {
		t.Fatal(err)
	}
	leasePath := filepath.Join(filepath.Dir(s.frontend), ".ao-testing-active")
	if err := os.WriteFile(leasePath, []byte(s.target.LaunchID), 0o600); err != nil {
		t.Fatal(err)
	}
	s.leaseInfo, err = os.Stat(leasePath)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSystem{s: s, pids: map[int]processInfo{11: {PID: 11, Parent: 1}, 12: {PID: 12, Parent: 11}, 13: {PID: 13, Parent: 11}, 99: {PID: 99, Parent: 1}},
		times: map[int]time.Time{11: now, 12: now.Add(time.Second), 13: now.Add(time.Second), 99: now}, status: map[string]int{}}
	f.ready = map[string]any{"status": "ready", "pid": 12, "executablePath": s.daemon, "workingDirectory": s.target.DataDir, "startupWorkingDirectory": s.frontend}
	f.a = &Adapter{launches: map[string]*launch{"test": s}, ops: operations{
		home: func() (string, error) { return root, nil },
		startTime: func(pid int) (time.Time, error) {
			if _, ok := f.pids[pid]; !ok {
				return time.Time{}, syscall.ESRCH
			}
			return f.times[pid], nil
		},
		processes: func(ctx context.Context) ([]processInfo, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			var p []processInfo
			for _, v := range f.pids {
				p = append(p, v)
			}
			return p, nil
		},
		signal: func(pid int, _ syscall.Signal) error {
			f.signals = append(f.signals, pid)
			delete(f.pids, pid)
			return nil
		},
		windows:      func(context.Context, int) ([]string, error) { return f.windows, nil },
		listenerGone: func(context.Context, int) error { return f.listener },
		freePort:     func() (int, error) { return s.port, nil },
		tmuxBinary:   func(string) (string, error) { return "/fixture/ao/tmux", nil },
		build: func(_ context.Context, frontend, executable string) error {
			if filepath.Dir(executable) != filepath.Join(filepath.Dir(filepath.Dir(frontend)), "attempt", "daemon") {
				t.Fatal("daemon build escaped its attempt", executable)
			}
			if err := os.Mkdir(filepath.Dir(executable), 0o700); err != nil {
				return err
			}
			return os.WriteFile(executable, []byte("owned daemon"), 0o700)
		},
		run: func(ctx context.Context, exe string, args, _ []string) ([]byte, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if exe != f.s.tmux || len(args) != 3 || args[0] != "-L" || args[1] != "testing-"+f.s.target.ID || (args[2] != "kill-server" && args[2] != "list-sessions") {
				t.Fatalf("unscoped tmux command: %s %v", exe, args)
			}
			return []byte("no server running on /tmp/testing-socket"), errors.New("exit 1")
		},
		client: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
			if r.Method != http.MethodGet || r.URL.Host != "127.0.0.1:32100" {
				t.Errorf("unexpected endpoint %s %s", r.Method, r.URL)
			}
			body := []byte(`{"items":[]}`)
			if r.URL.Path == "/readyz" {
				var err error
				body, err = json.Marshal(f.ready)
				if err != nil {
					return nil, err
				}
			}
			status := f.status[r.URL.Path]
			if status == 0 {
				status = http.StatusOK
			}
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(string(body))), Header: make(http.Header)}, nil
		})},
	}}
	writeInfo(t, s)
	if err := os.WriteFile(filepath.Join(root, "target.log"), []byte("final target output\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}

func writeInfo(t *testing.T, s *launch) {
	t.Helper()
	data, err := json.Marshal(runfile.Info{PID: s.target.DaemonPID, Port: s.port, StartedAt: s.target.DaemonStartedAt, Owner: "app", AppRunID: s.target.LaunchID})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.root, "running.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestTargetEnvironmentStripsInheritedAO(t *testing.T) {
	f := fixture(t)
	inherited := []string{"PATH=/bin", "HOME=/private/home", "AO_PORT=0", "AO_DATA_DIR=/real/data", "AO_RUN_FILE=/real/run", "AO_TMUX_BINARY=/real/tmux", "AO_TMUX_SOCKET_NAME=ao", "AO_BROWSER_TOKEN=secret-sentinel", "AO_TELEMETRY_TOKEN=secret-sentinel", "AO_FUTURE_VARIABLE=secret-sentinel", "NODE_OPTIONS=--require unsafe", "ELECTRON_RUN_AS_NODE=1", "ELECTRON_ENABLE_LOGGING=0"}
	env := targetEnv(inherited, f.s, true, false)
	values := make(map[string]string)
	for _, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		if _, exists := values[key]; exists {
			t.Fatalf("duplicate env key %s", key)
		}
		values[key] = value
		if strings.Contains(value, "secret-sentinel") || strings.Contains(value, "/real/") {
			t.Fatal("inherited AO state leaked")
		}
	}
	for key, want := range map[string]string{"PATH": "/bin", "HOME": "/private/home", "AO_DATA_DIR": f.s.target.DataDir, "AO_DAEMON_COMMAND": daemonCommand(f.s.daemon), "AO_RUN_FILE": filepath.Join(f.s.root, "running.json"), "AO_PORT": "32100", "AO_DEV_ELECTRON_DIR": filepath.Join(f.s.root, "electron"), "AO_FAKE_HARNESS": "1", "AO_APP_RUN_ID": f.s.target.LaunchID, "ELECTRON_ENABLE_LOGGING": "1", "AO_ALLOWED_ORIGINS": "app://renderer,http://127.0.0.1:32100", "AO_TMUX_BINARY": f.s.tmux, "AO_TMUX_SOCKET_NAME": "testing-test"} {
		if values[key] != want {
			t.Errorf("%s differs", key)
		}
	}
	for _, key := range []string{"AO_BROWSER_TOKEN", "AO_TELEMETRY_TOKEN", "AO_FUTURE_VARIABLE", "NODE_OPTIONS", "ELECTRON_RUN_AS_NODE"} {
		if _, exists := values[key]; exists {
			t.Errorf("unexpected %s", key)
		}
	}
}

func TestStopRemovesPrivateStateButKeepsWarmCodeAndEvidence(t *testing.T) {
	for _, blocked := range []bool{false, true} {
		t.Run(strconv.FormatBool(blocked), func(t *testing.T) {
			f := fixture(t)
			for _, name := range []string{"data", "electron", "fixtures", "checkout/node_modules"} {
				dir := filepath.Join(f.s.root, name)
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "kept"), []byte("owned"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			for _, name := range []string{"app-state.json", "update-settings.json", "target-ao"} {
				if err := os.WriteFile(filepath.Join(f.s.root, name), []byte("owned"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if blocked {
				f.listener = errors.New("listener absence unproved")
			}
			result, err := f.a.Stop(context.Background(), f.s.target)
			if blocked {
				if err == nil || result.State != domain.TestCleanupFailed {
					t.Fatal("uncertain cleanup succeeded", result, err)
				}
			} else if err != nil || result.State != domain.TestCleanupComplete {
				t.Fatal(result, err)
			}
			for _, name := range []string{"data", "electron", "fixtures", "app-state.json", "update-settings.json", "target-ao"} {
				_, err := os.Stat(filepath.Join(f.s.root, name))
				if blocked && err != nil || !blocked && !errors.Is(err, os.ErrNotExist) {
					t.Fatal("private state removed without proof or left behind", name, err)
				}
			}
			for _, name := range []string{"checkout/node_modules/kept", "target.log"} {
				if _, err := os.Stat(filepath.Join(f.s.root, name)); err != nil {
					t.Fatal("warm code or evidence deleted", name, err)
				}
			}
		})
	}
}

func TestPrivateStateCleanupRefusesReplacedRoot(t *testing.T) {
	f := fixture(t)
	original := f.s.root + "-original"
	if err := os.Rename(f.s.root, original); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(original) })
	if err := os.MkdirAll(filepath.Join(f.s.root, "data"), 0o700); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(f.s.root, "data", "foreign")
	if err := os.WriteFile(foreign, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := removePrivateState(f.s); err == nil {
		t.Fatal("replaced state directory was accepted")
	}
	if data, err := os.ReadFile(foreign); err != nil || string(data) != "preserve" {
		t.Fatal("foreign state was removed", err)
	}
}

func TestReadinessRejectsChangedIdentity(t *testing.T) {
	tests := []struct {
		name   string
		change func(*fakeSystem)
	}{
		{"pid reuse", func(f *fakeSystem) { f.times[11] = f.times[11].Add(time.Microsecond) }},
		{"readiness pid", func(f *fakeSystem) { f.ready["pid"] = 99 }},
		{"executable", func(f *fakeSystem) { f.ready["executablePath"] = "/installed/ao" }},
		{"cwd", func(f *fakeSystem) { f.ready["startupWorkingDirectory"] = "/installed" }},
		{"sessions unavailable", func(f *fakeSystem) { f.status["/api/v1/sessions"] = 503 }},
		{"launch id", func(f *fakeSystem) {
			data := `{"pid":12,"port":32100,"owner":"app","app_run_id":"other"}`
			if err := os.WriteFile(filepath.Join(f.s.root, "running.json"), []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := fixture(t)
			test.change(f)
			if err := f.a.Probe(context.Background(), f.s.target); err == nil {
				t.Fatal("changed target admitted")
			}
		})
	}
}

func TestReadinessAndFixedQueries(t *testing.T) {
	f := fixture(t)
	if err := f.a.Probe(context.Background(), f.s.target); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.s.owned[13]; !ok {
		t.Fatal("descendant not captured")
	}
	target := f.s.target
	target.WindowID = "77"
	for _, resource := range []domain.TestDaemonResource{domain.TestDaemonProjects, domain.TestDaemonSessions} {
		result, err := f.a.QueryDaemon(context.Background(), target, domain.TestDaemonQueryRequest{Resource: resource})
		if err != nil || !json.Valid(result.Data) {
			t.Fatalf("query failed: %v", err)
		}
	}
	if _, err := f.a.QueryDaemon(context.Background(), target, domain.TestDaemonQueryRequest{Resource: "/shutdown"}); err == nil {
		t.Fatal("arbitrary route admitted")
	}
	target.Generation++
	if err := f.a.Probe(context.Background(), target); err == nil {
		t.Fatal("foreign generation admitted")
	}
}

func TestScopedTeardownAndFinalLogs(t *testing.T) {
	f := fixture(t)
	result, err := f.a.Stop(context.Background(), f.s.target)
	if err != nil || result.State != domain.TestCleanupComplete {
		t.Fatalf("cleanup %v: %v", result, err)
	}
	if len(f.signals) != 3 {
		t.Fatalf("signals %v", f.signals)
	}
	for _, pid := range f.signals {
		if pid == 99 {
			t.Fatal("foreign process signalled")
		}
	}
	if _, ok := f.pids[99]; !ok {
		t.Fatal("foreign process removed")
	}
	if _, err := os.Stat(filepath.Join(f.s.root, "running.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("run file remains")
	}
	logs, err := f.a.ReadLogs(context.Background(), f.s.target, domain.TestReadLogsRequest{MaxBytes: 5})
	if err != nil || logs.Text != "final" || !logs.Truncated || logs.NextCursor != "5" {
		t.Fatalf("final logs %v %v", logs, err)
	}
	if _, err := f.a.Stop(context.Background(), f.s.target); err != nil {
		t.Fatal("second stop", err)
	}
}

func TestCleanupReportsLeftoversAndRefusesReusedPID(t *testing.T) {
	f := fixture(t)
	f.times[11] = f.times[11].Add(time.Microsecond)
	f.windows = []string{"777"}
	f.listener = errors.New("listener still exists")
	result, err := f.a.Stop(context.Background(), f.s.target)
	if err == nil || result.State != domain.TestCleanupFailed {
		t.Fatal("uncertain cleanup reported success")
	}
	leftovers := strings.Join(result.Leftovers, " ")
	for _, want := range []string{"PID 11", "window 777", "listener", "run file"} {
		if !strings.Contains(leftovers, want) {
			t.Errorf("missing %s in %s", want, leftovers)
		}
	}
	for _, pid := range f.signals {
		if pid == 11 || pid == 99 {
			t.Fatalf("unsafe signal %d", pid)
		}
	}
	if _, err := f.a.ReadLogs(context.Background(), f.s.target, domain.TestReadLogsRequest{}); err != nil {
		t.Fatal("failed cleanup lost final logs", err)
	}
}

func TestCancelledCleanupNeverSignals(t *testing.T) {
	f := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := f.a.Stop(ctx, f.s.target)
	if err == nil || result.State != domain.TestCleanupFailed || len(f.signals) != 0 {
		t.Fatalf("cancelled cleanup %v %v signals %v", result, err, f.signals)
	}
}

func TestStartUsesPreparedCheckoutAndCapturedIdentity(t *testing.T) {
	for _, realProviders := range []bool{false, true} {
		t.Run(fmt.Sprintf("real=%t", realProviders), func(t *testing.T) { testStartUsesPreparedCheckout(t, realProviders) })
	}
}

func preparedStartFixture(t *testing.T, realProviders bool) (*fakeSystem, ports.TestingTargetSpec) {
	t.Helper()
	t.Setenv("AO_FAKE_HARNESS", "inherited-sentinel")
	f := fixture(t)
	f.a.launches = make(map[string]*launch)
	base := filepath.Join(f.s.root, ".ao", "dev", "agentic-target")
	checkout := filepath.Join(base, "checkout")
	frontend := filepath.Join(checkout, "frontend")
	if err := os.MkdirAll(frontend, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "--quiet"}, {"-c", "user.name=Test", "-c", "user.email=test@example.test", "commit", "--quiet", "--allow-empty", "--no-verify", "-m", "test"}} {
		command := exec.Command("git", args...)
		command.Dir, command.Env = checkout, strippedEnv(os.Environ())
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("fixture git: %s %v", output, err)
		}
	}
	command := exec.Command("git", "rev-parse", "HEAD")
	command.Dir, command.Env = checkout, strippedEnv(os.Environ())
	head, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for _, name := range []string{".vite/build/main.js", ".vite/build/ao-main.cjs", ".vite/build/preload.js", ".vite/build/annotate-preload.js", ".vite/renderer/main_window/index.html", "daemon/ao"} {
		path := filepath.Join(frontend, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256([]byte("fixture"))
		files[name] = hex.EncodeToString(sum[:])
	}
	if err := os.MkdirAll(filepath.Dir(electronPath(frontend)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(electronPath(frontend), []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest, err := json.Marshal(map[string]any{"commitSHA": strings.TrimSpace(string(head)), "files": files, "preflight": map[string]string{"goVersion": "go1.27.1", "nodeVersion": "v22.23.2"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(frontend, ".vite", "testing-target.json"), manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	preparedByController := false
	f.a.ops.prepare = func(_ context.Context, gotFrontend, commit, root string) error {
		if gotFrontend != frontend || commit != strings.TrimSpace(string(head)) || root != filepath.Join(base, "attempt") {
			t.Fatal("preparation escaped the pinned checkout or owned attempt")
		}
		preparedByController = true
		return nil
	}
	f.a.ops.start = func(exe, cwd string, env []string, _ *os.File) (int, error) {
		if !preparedByController {
			t.Fatal("controller did not prepare the revision before launch")
		}
		if exe != electronPath(frontend) || cwd != frontend {
			t.Fatal("wrong checkout")
		}
		values := make(map[string]string)
		for _, entry := range env {
			key, value, _ := strings.Cut(entry, "=")
			values[key] = value
		}
		wantFake := "1"
		if realProviders {
			wantFake = "0"
		}
		if values["AO_FAKE_HARNESS"] != wantFake || values["AO_DEV_ELECTRON_DIR"] != filepath.Join(base, "attempt", "electron") {
			t.Fatal("missing isolation")
		}
		if values["AO_TMUX_BINARY"] != "/fixture/ao/tmux" {
			t.Fatal("target did not receive the cleanup tmux binary")
		}
		if values["AO_DAEMON_COMMAND"] != daemonCommand(filepath.Join(base, "attempt", "daemon", "ao")) {
			t.Fatal("Electron did not receive the owned daemon command")
		}
		f.s = f.a.launches[strings.TrimPrefix(values["AO_APP_RUN_ID"], "target-")]
		f.s.target.DaemonPID = 12
		f.s.target.DaemonStartedAt = f.times[12]
		writeInfo(t, f.s)
		f.ready["executablePath"] = f.s.daemon
		f.ready["workingDirectory"] = f.s.target.DataDir
		f.ready["startupWorkingDirectory"] = frontend
		return 11, nil
	}
	spec := ports.TestingTargetSpec{AttemptID: "attempt", Generation: 1, CheckoutPath: checkout, CommitSHA: strings.TrimSpace(string(head)), Deadline: time.Now().Add(time.Minute)}
	if realProviders {
		spec.RecipeSnapshot = `{"realProviders":true}`
	}
	return f, spec
}

func testStartUsesPreparedCheckout(t *testing.T, realProviders bool) {
	t.Helper()
	f, spec := preparedStartFixture(t, realProviders)
	target, err := f.a.Start(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if target.ElectronPID != 11 || target.DaemonPID != 12 || !target.ElectronStartedAt.Equal(f.times[11]) || !target.DaemonStartedAt.Equal(f.times[12]) {
		t.Fatalf("wrong identity %v", target)
	}
	if _, err := f.a.Stop(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	spec.AttemptID = "../escape"
	if _, err := f.a.Start(context.Background(), spec); err == nil {
		t.Fatal("traversal admitted")
	}
	spec.AttemptID = domain.TestAttemptID(strconv.Itoa(2))
	spec.CheckoutPath = t.TempDir()
	if _, err := f.a.Start(context.Background(), spec); err == nil {
		t.Fatal("outside checkout admitted")
	}
}

func TestCleanupStopsReparentedTmuxOnOnlyItsSocket(t *testing.T) {
	f := fixture(t)
	f.pids[88] = processInfo{PID: 88, Parent: 1}
	f.times[88] = time.Now()
	var commands []string
	f.a.ops.run = func(_ context.Context, exe string, args, env []string) ([]byte, error) {
		if exe != f.s.tmux || len(args) != 3 || args[0] != "-L" || args[1] != "testing-test" {
			t.Fatalf("unscoped tmux command: %s %v", exe, args)
		}
		if len(f.signals) != 3 {
			t.Fatal("tmux cleanup ran before owned tree teardown")
		}
		commands = append(commands, args[2])
		switch args[2] {
		case "kill-server":
			delete(f.pids, 88)
			return nil, nil
		case "list-sessions":
			return []byte("no server running on /tmp/testing-test"), errors.New("exit 1")
		default:
			t.Fatalf("unexpected tmux command %v", args)
			return nil, nil
		}
	}
	result, err := f.a.Stop(context.Background(), f.s.target)
	if err != nil || result.State != domain.TestCleanupComplete || !reflect.DeepEqual(commands, []string{"kill-server", "list-sessions"}) {
		t.Fatalf("orphan cleanup %+v: %v, commands %v", result, err, commands)
	}
	if _, alive := f.pids[99]; !alive {
		t.Fatal("foreign server was removed")
	}
	for _, pid := range f.signals {
		if pid == 88 || pid == 99 {
			t.Fatal("reparented or foreign process was signalled by PID")
		}
	}
}

func TestCleanupReportsTmuxSocketLeftovers(t *testing.T) {
	for _, mode := range []string{"server survives", "inventory fails", "kill fails"} {
		t.Run(mode, func(t *testing.T) {
			f := fixture(t)
			f.a.ops.run = func(_ context.Context, _ string, args, _ []string) ([]byte, error) {
				if args[2] == "kill-server" {
					if mode == "kill fails" {
						return []byte("permission denied"), errors.New("exit 1")
					}
					return nil, nil
				}
				if mode == "inventory fails" {
					return []byte("connection refused"), errors.New("exit 1")
				}
				return []byte("session: 1 windows"), nil
			}
			result, err := f.a.Stop(context.Background(), f.s.target)
			if err == nil || result.State != domain.TestCleanupFailed || !strings.Contains(strings.Join(result.Leftovers, " "), "tmux socket testing-test") {
				t.Fatalf("tmux leftover not reported: %+v %v", result, err)
			}
		})
	}
}

func TestTmuxAbsenceRequiresNoServerError(t *testing.T) {
	for _, test := range []struct {
		message string
		absent  bool
	}{
		{"no server running on /tmp/testing-test", true},
		{"error connecting to /tmp/testing-test (No such file or directory)", true},
		{"error connecting to /tmp/testing-test (Permission denied)", false},
		{"error connecting to /tmp/testing-test (Connection refused)", false},
		{"unexpected client failure", false},
	} {
		t.Run(test.message, func(t *testing.T) {
			f := fixture(t)
			f.a.ops.run = func(context.Context, string, []string, []string) ([]byte, error) {
				return []byte(test.message), errors.New("exit 1")
			}
			alive, err := f.a.tmuxCommand(context.Background(), f.s, "list-sessions")
			if alive || (err == nil) != test.absent {
				t.Fatalf("absence %v: alive %v, err %v", test.absent, alive, err)
			}
		})
	}
}

func TestDescendantVanishingDuringInventory(t *testing.T) {
	for _, captured := range []bool{false, true} {
		t.Run(strconv.FormatBool(captured), func(t *testing.T) {
			f := fixture(t)
			if captured {
				if err := f.a.captureTree(context.Background(), f.s); err != nil {
					t.Fatal(err)
				}
			}
			original := f.a.ops.startTime
			f.a.ops.startTime = func(pid int) (time.Time, error) {
				if pid == 13 {
					delete(f.pids, pid)
					return time.Time{}, syscall.EIO
				}
				return original(pid)
			}
			if err := f.a.Probe(context.Background(), f.s.target); err != nil {
				t.Fatal("gone child failed observation", err)
			}
			if !f.s.vanished[13] {
				t.Fatal("vanished child was not recorded")
			}
			if _, err := f.a.Stop(context.Background(), f.s.target); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRequiredOrUnreadableProcessIsNotSkipped(t *testing.T) {
	for _, pid := range []int{11, 12, 13} {
		t.Run(strconv.Itoa(pid), func(t *testing.T) {
			f := fixture(t)
			original := f.a.ops.startTime
			f.a.ops.startTime = func(observed int) (time.Time, error) {
				if observed == pid {
					if pid != 13 {
						delete(f.pids, pid)
					}
					return time.Time{}, syscall.EIO
				}
				return original(observed)
			}
			if err := f.a.captureTree(context.Background(), f.s); err == nil {
				t.Fatal("required or unreadable PID admitted")
			}
			if f.s.vanished[pid] {
				t.Fatal("live or required PID recorded as gone")
			}
		})
	}
}

func TestZombieDescendantIsGone(t *testing.T) {
	f := fixture(t)
	original := f.a.ops.startTime
	f.a.ops.startTime = func(pid int) (time.Time, error) {
		if pid == 13 {
			return time.Time{}, fmt.Errorf("PID %d is a zombie: %w", pid, processutil.ErrNotRunning)
		}
		return original(pid)
	}
	if err := f.a.captureTree(context.Background(), f.s); err != nil {
		t.Fatal(err)
	}
	if !f.s.vanished[13] {
		t.Fatal("zombie descendant was not noted")
	}
	if _, err := f.a.Stop(context.Background(), f.s.target); err != nil {
		t.Fatal(err)
	}
	for _, pid := range f.signals {
		if pid == 13 {
			t.Fatal("zombie was signalled")
		}
	}
}

func TestDescendantVanishesBeforeSignal(t *testing.T) {
	for _, signalErr := range []error{syscall.ESRCH, syscall.EIO} {
		t.Run(signalErr.Error(), func(t *testing.T) {
			f := fixture(t)
			original := f.a.ops.signal
			f.a.ops.signal = func(pid int, signal syscall.Signal) error {
				if pid == 13 {
					delete(f.pids, pid)
					return signalErr
				}
				return original(pid, signal)
			}
			result, err := f.a.Stop(context.Background(), f.s.target)
			if err != nil || result.State != domain.TestCleanupComplete {
				t.Fatalf("gone child failed cleanup: %v %v", result, err)
			}
		})
	}
}

func TestReadLogsNeverObservesProcesses(t *testing.T) {
	f := fixture(t)
	f.a.ops.processes = func(context.Context) ([]processInfo, error) {
		t.Fatal("ReadLogs enumerated processes")
		return nil, nil
	}
	f.a.ops.startTime = func(int) (time.Time, error) { t.Fatal("ReadLogs inspected a PID"); return time.Time{}, nil }
	if err := os.MkdirAll(filepath.Join(f.s.target.DataDir, "logs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.s.target.DataDir, "daemon.log"), []byte("daemon file output\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.s.target.DataDir, "logs", "daemon.log"), []byte("second daemon file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := f.a.ReadLogs(context.Background(), f.s.target, domain.TestReadLogsRequest{MaxBytes: 262144})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"final target output", "daemon file output", "second daemon file"} {
		if !strings.Contains(result.Text, want) {
			t.Errorf("missing %s", want)
		}
	}
	if !strings.HasPrefix(result.NextCursor, "v1:") {
		t.Fatal("multi-file cursor missing")
	}
	next, err := f.a.ReadLogs(context.Background(), f.s.target, domain.TestReadLogsRequest{Cursor: result.NextCursor})
	if err != nil || next.Text != "" {
		t.Fatalf("logs repeated: %v %v", next, err)
	}
	if err := os.WriteFile(filepath.Join(f.s.root, "fixture-oracle.json"), []byte("private-marker"), 0o600); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.Text, "private-marker") {
		t.Fatal("oracle leaked")
	}
}

func TestRequiredZombieStillFailsObservation(t *testing.T) {
	for _, pid := range []int{11, 12} {
		t.Run(strconv.Itoa(pid), func(t *testing.T) {
			f := fixture(t)
			original := f.a.ops.startTime
			f.a.ops.startTime = func(observed int) (time.Time, error) {
				if observed == pid {
					return time.Time{}, processutil.ErrNotRunning
				}
				return original(observed)
			}
			if err := f.a.Probe(context.Background(), f.s.target); err == nil {
				t.Fatal("required zombie admitted")
			}
		})
	}
}
