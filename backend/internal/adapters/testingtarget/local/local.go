// Package local launches an unpackaged AO from a prepared, isolated checkout.
package local

import (
	"context"
	"crypto/rand"
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
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	processutil "github.com/aoagents/agent-orchestrator/backend/internal/process"
	"github.com/aoagents/agent-orchestrator/backend/internal/runfile"
	"github.com/aoagents/agent-orchestrator/backend/internal/tmuxbin"
)

const maxResponseBytes = 1 << 20

type processInfo struct {
	PID    int
	Parent int
}

type operations struct {
	home         func() (string, error)
	start        func(string, string, []string, *os.File) (int, error)
	startTime    func(int) (time.Time, error)
	processes    func(context.Context) ([]processInfo, error)
	signal       func(int, syscall.Signal) error
	windows      func(context.Context, int) ([]string, error)
	freePort     func() (int, error)
	listenerGone func(context.Context, int) error
	tmuxBinary   func(string) (string, error)
	run          func(context.Context, string, []string, []string) ([]byte, error)
	client       *http.Client
	prepare      func(context.Context, string, string, string) error
	build        func(context.Context, string, string) error
}

type launch struct {
	mu        sync.Mutex
	target    domain.TestTargetIdentity
	root      string
	frontend  string
	daemon    string
	revision  string
	preflight json.RawMessage
	liveGuard *liveDaemonGuard
	tmux      string
	env       []string
	port      int
	log       *os.File
	owned     map[int]time.Time
	vanished  map[int]bool
	stopped   bool
	closing   bool
	rootInfo  os.FileInfo
	leaseInfo os.FileInfo

	setupGroup int
}

// Adapter retains launch ownership in memory. It never attaches to an existing
// app, uses a packaged executable, or reconstructs ownership after a restart.
type Adapter struct {
	mu       sync.Mutex
	launches map[string]*launch
	ops      operations
}

var _ ports.TestingTargetEnvironment = (*Adapter)(nil)
var _ ports.TestingTargetWorkerContext = (*Adapter)(nil)

// New uses macOS process/window observations and the existing loopback API.
func New() *Adapter {
	transport := &http.Transport{Proxy: nil}
	return &Adapter{
		launches: make(map[string]*launch),
		ops: operations{
			prepare: prepareTarget, build: buildOwnedDaemon,
			home: os.UserHomeDir, start: startElectron, startTime: nativeStartTime,
			processes: processSnapshot, signal: signalProcess, windows: nativeWindows,
			freePort: unusedPort, listenerGone: listenerGone,
			tmuxBinary: func(frontend string) (string, error) {
				resolution, err := tmuxbin.ResolveWith("", func() (string, error) {
					return filepath.Join(frontend, "daemon", "ao"), nil
				}, exec.LookPath)
				if err != nil {
					return "", err
				}
				return filepath.Abs(resolution.Path)
			},
			run: func(ctx context.Context, executable string, args, env []string) ([]byte, error) {
				command := processutil.CommandContext(ctx, executable, args...)
				command.Env = strippedEnv(env)
				return command.CombinedOutput()
			},
			client: &http.Client{Transport: transport, Timeout: 2 * time.Second,
				CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		},
	}
}

// Start admits only prepared checkouts and creates a fresh private attempt root.
// Its context bounds startup, not the lifetime of the launched application.
func (a *Adapter) Start(ctx context.Context, spec ports.TestingTargetSpec) (result domain.TestTargetIdentity, err error) {
	var empty domain.TestTargetIdentity
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	if spec.Generation < 1 || spec.Deadline.IsZero() || !time.Now().Before(spec.Deadline) {
		return empty, errors.New("target requires a positive generation and future deadline")
	}
	frontend, root, err := a.paths(spec)
	if err != nil {
		return empty, err
	}
	if a.ops.prepare == nil {
		if err := prepared(ctx, frontend, spec.CommitSHA); err != nil {
			return empty, err
		}
	}
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	tmux, err := a.ops.tmuxBinary(frontend)
	if err != nil {
		return empty, fmt.Errorf("resolve target tmux: %w", err)
	}
	var recipe struct {
		VisualMarker  bool `json:"visualMarker"`
		RealProviders bool `json:"realProviders"`
	}
	if spec.RecipeSnapshot != "" {
		if err := json.Unmarshal([]byte(spec.RecipeSnapshot), &recipe); err != nil {
			return empty, fmt.Errorf("target recipe: %w", err)
		}
	}
	id, err := randomID()
	if err != nil {
		return empty, err
	}
	if err := os.Mkdir(root, 0o700); err != nil {
		return empty, fmt.Errorf("create fresh target root: %w", err)
	}
	rootInfo, err := os.Stat(root)
	if err != nil {
		return empty, err
	}
	// Preparation and build commands verify their own process-group shutdown.
	pending := &launch{root: root, rootInfo: rootInfo, frontend: frontend,
		target: domain.TestTargetIdentity{ID: id, LaunchID: "target-" + id, Generation: spec.Generation, DataDir: filepath.Join(root, "data")}}
	launched := false
	defer func() {
		if err == nil || launched {
			return
		}
		var groupErr *setupGroupError
		if errors.As(err, &groupErr) {
			a.mu.Lock()
			if a.launches[id] == nil {
				a.launches[id] = pending
			}
			a.mu.Unlock()
		}
		pending.mu.Lock()
		defer pending.mu.Unlock()
		if pending.log != nil {
			err = errors.Join(err, pending.log.Close())
			pending.log = nil
		}
		if groupErr != nil {
			pending.setupGroup = groupErr.group
			if pending.leaseInfo == nil {
				pending.leaseInfo = groupErr.leaseInfo
			}
			pending.stopped = false
			result = pending.target
			return // Retain state and cleanup identity until absence is proved.
		}
		cleanupErr := removePrivateState(pending)
		pending.stopped = cleanupErr == nil
		err = errors.Join(err, cleanupErr)
	}()
	if a.ops.prepare != nil {
		if err := a.ops.prepare(ctx, frontend, spec.CommitSHA, root); err != nil {
			return empty, err
		}
		if err := prepared(ctx, frontend, spec.CommitSHA); err != nil {
			return empty, err
		}
	}
	if err := os.Mkdir(filepath.Join(root, "fixtures"), 0o700); err != nil {
		return empty, err
	}
	port, err := a.ops.freePort()
	if err != nil {
		return empty, err
	}
	if port < 1 || port > 65535 || port == 3001 {
		return empty, errors.New("target requires an explicit private port other than 3001")
	}
	log, err := os.OpenFile(filepath.Join(root, "target.log"), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return empty, err
	}
	s := &launch{root: root, rootInfo: rootInfo, frontend: frontend, daemon: filepath.Join(root, "daemon", "ao"), revision: spec.CommitSHA, tmux: tmux, port: port, log: log, owned: make(map[int]time.Time),
		target: domain.TestTargetIdentity{ID: id, LaunchID: "target-" + id, Generation: spec.Generation,
			DataDir: filepath.Join(root, "data")}}
	pending = s
	a.mu.Lock()
	for _, existing := range a.launches {
		existing.mu.Lock()
		busy := !existing.stopped && existing.frontend == frontend
		existing.mu.Unlock()
		if busy {
			a.mu.Unlock()
			return empty, errors.New("target checkout already has an owned active launch")
		}
	}
	a.launches[id] = s
	a.mu.Unlock()
	s.mu.Lock()
	lease, err := os.OpenFile(filepath.Join(filepath.Dir(frontend), ".ao-testing-active"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err == nil {
		s.leaseInfo, err = lease.Stat()
		if err == nil {
			_, err = lease.WriteString(s.target.LaunchID)
		}
		err = errors.Join(err, lease.Close())
	}
	if err != nil {
		s.stopped = true
		_ = log.Close()
		s.log = nil
		s.mu.Unlock()
		return s.target, fmt.Errorf("reserve target checkout: %w", err)
	}
	// A preparation could have completed between admission and reservation.
	if err := prepared(ctx, frontend, spec.CommitSHA); err != nil {
		s.stopped = true
		_ = log.Close()
		s.log = nil
		s.mu.Unlock()
		return s.target, err
	}
	if err := a.prepareOwnedDaemon(ctx, s); err != nil {
		s.stopped = true
		_ = log.Close()
		s.log = nil
		s.mu.Unlock()
		return s.target, err
	}
	s.liveGuard, err = a.observeLiveDaemon()
	if err == nil && s.liveGuard.info != nil && s.port == s.liveGuard.info.Port {
		err = errors.New("target port conflicts with live AO daemon")
	}
	if err != nil {
		s.stopped = true
		_ = log.Close()
		s.log = nil
		s.mu.Unlock()
		return s.target, err
	}
	s.env = targetEnv(os.Environ(), s, recipe.VisualMarker, recipe.RealProviders)
	if err := writeTargetCLI(s); err != nil {
		s.stopped = true
		_ = log.Close()
		s.log = nil
		s.mu.Unlock()
		return s.target, fmt.Errorf("prepare target CLI: %w", err)
	}
	pid, err := a.ops.start(electronPath(frontend), frontend, s.env, log)
	if err != nil {
		s.stopped = true
		_ = log.Close()
		s.log = nil
		s.mu.Unlock()
		return s.target, fmt.Errorf("launch Electron: %w", err)
	}
	launched = true
	s.target.ElectronPID = pid
	started, err := a.ops.startTime(pid)
	if err == nil {
		s.target.ElectronStartedAt = started
		s.owned[pid] = started
		err = a.waitReady(ctx, s, spec.Deadline)
		if err == nil {
			err = a.checkLiveDaemon(s.liveGuard)
		}
	}
	if err != nil {
		s.mu.Unlock()
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		_, cleanupErr := a.Stop(cleanupCtx, s.target)
		return s.target, errors.Join(fmt.Errorf("target startup: %w", err), cleanupErr)
	}
	s.mu.Unlock()
	return s.target, nil
}

func (a *Adapter) paths(spec ports.TestingTargetSpec) (string, string, error) {
	home, err := a.ops.home()
	if err != nil {
		return "", "", err
	}
	id := string(spec.AttemptID)
	if id == "" || id == "." || id == ".." || strings.ContainsAny(id, `/\`) || filepath.Base(id) != id {
		return "", "", errors.New("invalid target attempt ID")
	}
	base := filepath.Join(home, ".ao", "dev", "agentic-target")
	if err := os.MkdirAll(base, 0o700); err != nil {
		return "", "", err
	}
	resolved, err := filepath.EvalSymlinks(base)
	if err != nil || resolved != base {
		return "", "", errors.New("target state directory must not contain symlinks")
	}
	checkout, err := filepath.EvalSymlinks(spec.CheckoutPath)
	if err != nil || !beneath(base, checkout) {
		return "", "", errors.New("target checkout must be a prepared directory beneath ~/.ao/dev/agentic-target")
	}
	root := filepath.Join(base, id)
	if spec.StateRoot != "" && filepath.Clean(spec.StateRoot) != root {
		return "", "", errors.New("target state root must match its attempt ID")
	}
	if beneath(checkout, root) || beneath(root, checkout) || root == checkout {
		return "", "", errors.New("target state and checkout must be separate")
	}
	return filepath.Join(checkout, "frontend"), root, nil
}

func beneath(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func prepared(ctx context.Context, frontend, commit string) error {
	var manifest struct {
		CommitSHA string            `json:"commitSHA"`
		Files     map[string]string `json:"files"`
	}
	data, err := os.ReadFile(filepath.Join(frontend, ".vite", "testing-target.json"))
	if err != nil {
		return fmt.Errorf("target checkout has not been prepared: %w", err)
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return err
	}
	if commit == "" || manifest.CommitSHA != commit {
		return errors.New("prepared target revision differs from launch specification")
	}
	command := processutil.CommandContext(ctx, "git", "rev-parse", "HEAD")
	command.Dir, command.Env = filepath.Dir(frontend), strippedEnv(os.Environ())
	head, err := command.Output()
	if err != nil || strings.TrimSpace(string(head)) != commit {
		return errors.New("target checkout HEAD differs from its prepared revision")
	}
	for _, name := range []string{".vite/build/main.js", ".vite/build/ao-main.cjs", ".vite/build/preload.js", ".vite/build/annotate-preload.js", ".vite/renderer/main_window/index.html"} {
		data, err := os.ReadFile(filepath.Join(frontend, name))
		if err != nil {
			return err
		}
		hash := sha256.Sum256(data)
		if manifest.Files[name] == "" || hex.EncodeToString(hash[:]) != manifest.Files[name] {
			return fmt.Errorf("prepared target artifact changed: %s", name)
		}
	}
	exe, err := filepath.EvalSymlinks(electronPath(frontend))
	if err != nil || !beneath(frontend, exe) {
		return errors.New("target Electron executable is missing or escapes its checkout")
	}
	return nil
}

func electronPath(frontend string) string {
	return filepath.Join(frontend, "node_modules", "electron", "dist", "Electron.app", "Contents", "MacOS", "Electron")
}

func randomID() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes[:]), nil
}

func strippedEnv(inherited []string) []string {
	env := make([]string, 0, len(inherited))
	for _, value := range inherited {
		key, _, _ := strings.Cut(value, "=")
		if !strings.HasPrefix(key, "AO_") && key != "ELECTRON_RUN_AS_NODE" && key != "NODE_OPTIONS" && key != "ELECTRON_ENABLE_LOGGING" {
			env = append(env, value)
		}
	}
	return env
}

func targetEnv(inherited []string, s *launch, marker, realProviders bool) []string {
	fake := "1"
	if realProviders {
		fake = "0"
	}
	env := strippedEnv(inherited)
	env = append(env, "AO_DATA_DIR="+s.target.DataDir,
		"AO_DAEMON_COMMAND="+daemonCommand(s.daemon),
		"AO_RUN_FILE="+filepath.Join(s.root, "running.json"), "AO_PORT="+strconv.Itoa(s.port),
		"AO_DEV_ELECTRON_DIR="+filepath.Join(s.root, "electron"), "AO_APP_RUN_ID="+s.target.LaunchID,
		"AO_FAKE_HARNESS="+fake, "AO_TMUX_SOCKET_NAME=testing-"+s.target.ID,
		"AO_TMUX_BINARY="+s.tmux,
		// Node URL reports an opaque origin for the privileged app:// scheme.
		// An explicit valid origin prevents the dev helper adding "null".
		"AO_ALLOWED_ORIGINS=app://renderer,http://127.0.0.1:"+strconv.Itoa(s.port),
		"AO_TELEMETRY_REMOTE=off", "AO_TELEMETRY_EVENTS=off", "AO_SENTRY_DSN=",
		"ELECTRON_ENABLE_LOGGING=1")
	if marker {
		env = append(env, "AO_TARGET_MARKER_ORACLE="+filepath.Join(s.root, "fixture-oracle.json"))
	}
	return env
}

func (a *Adapter) waitReady(ctx context.Context, s *launch, deadline time.Time) error {
	if limit := time.Now().Add(3 * time.Minute); deadline.After(limit) {
		deadline = limit
	}
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var last error
	for {
		last = a.ready(ctx, s)
		if last == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return errors.Join(ctx.Err(), last)
		case <-ticker.C:
		}
	}
}

func (a *Adapter) ready(ctx context.Context, s *launch) error {
	info, err := runfile.Read(filepath.Join(s.root, "running.json"))
	if err != nil || info == nil {
		return errors.Join(errors.New("target run file is not ready"), err)
	}
	if info.PID < 1 || info.Port != s.port || info.Owner != "app" || info.AppRunID != s.target.LaunchID || info.StartedAt.Before(s.target.ElectronStartedAt.Add(-2*time.Second)) {
		return errors.New("target run file identity mismatch")
	}
	if err := a.captureTree(ctx, s); err != nil {
		return err
	}
	started, owned := s.owned[info.PID]
	if !owned {
		return errors.New("target daemon is not an Electron descendant")
	}
	s.target.DaemonPID, s.target.DaemonStartedAt = info.PID, started
	return a.probe(ctx, s)
}

func (a *Adapter) find(target domain.TestTargetIdentity) (*launch, error) {
	a.mu.Lock()
	s := a.launches[target.ID]
	a.mu.Unlock()
	if s == nil {
		return nil, errors.New("unknown target launch")
	}
	s.mu.Lock()
	want := s.target
	s.mu.Unlock()
	want.WindowID, target.WindowID = "", ""
	if want != target {
		return nil, errors.New("target launch identity mismatch")
	}
	return s, nil
}

// Probe validates run-file ownership, exact OS start times and daemon identity.
func (a *Adapter) Probe(ctx context.Context, target domain.TestTargetIdentity) error {
	s, err := a.find(target)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped || s.closing {
		return errors.New("target has stopped")
	}
	return a.probe(ctx, s)
}

// WorkerContext resolves only this live target's paths. The wrapper uses the
// target binary and clears supervisor/session AO settings before each command.
func (a *Adapter) WorkerContext(ctx context.Context, target domain.TestTargetIdentity) (ports.TestingWorkerContext, error) {
	s, err := a.find(target)
	if err != nil {
		return ports.TestingWorkerContext{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped || s.closing {
		return ports.TestingWorkerContext{}, errors.New("target has stopped")
	}
	if err := a.probe(ctx, s); err != nil {
		return ports.TestingWorkerContext{}, err
	}
	facts, err := json.Marshal(struct {
		Revision  string          `json:"revision"`
		Command   string          `json:"launchCommand"`
		Daemon    string          `json:"daemonExecutable"`
		Port      int             `json:"port"`
		DataDir   string          `json:"dataDir"`
		Profile   string          `json:"electronProfile"`
		Preflight json.RawMessage `json:"preflight"`
	}{s.revision, daemonCommand(s.daemon), s.daemon, s.port, s.target.DataDir, filepath.Join(s.root, "electron"), s.preflight})
	if err != nil {
		return ports.TestingWorkerContext{}, err
	}
	return ports.TestingWorkerContext{CheckoutPath: filepath.Dir(s.frontend), CLIPath: filepath.Join(s.root, "target-ao"), RunFilePath: filepath.Join(s.root, "running.json"), DataDir: s.target.DataDir, FixtureDir: filepath.Join(s.root, "fixtures"), LaunchContext: string(facts)}, nil
}

func writeTargetCLI(s *launch) error {
	script := fmt.Sprintf(`#!/usr/bin/env python3
import os
import sys

env = {key: value for key, value in os.environ.items()
       if not key.startswith("AO_") and key not in
       ("CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT", "NODE_OPTIONS", "ELECTRON_RUN_AS_NODE", "ELECTRON_ENABLE_LOGGING")}
env["AO_RUN_FILE"] = %q
env["AO_DATA_DIR"] = %q
os.chdir(%q)
executable = %q
os.execve(executable, [executable, *sys.argv[1:]], env)
`, filepath.Join(s.root, "running.json"), s.target.DataDir, filepath.Dir(s.frontend), s.daemon)
	return os.WriteFile(filepath.Join(s.root, "target-ao"), []byte(script), 0o600)
}

func (a *Adapter) probe(ctx context.Context, s *launch) error {
	for pid, want := range map[int]time.Time{s.target.ElectronPID: s.target.ElectronStartedAt, s.target.DaemonPID: s.target.DaemonStartedAt} {
		got, err := a.ops.startTime(pid)
		if err != nil || !got.Equal(want) {
			return errors.Join(fmt.Errorf("target process identity changed: %d", pid), err)
		}
	}
	info, err := runfile.Read(filepath.Join(s.root, "running.json"))
	if err != nil || info == nil || info.PID != s.target.DaemonPID || info.Port != s.port || info.AppRunID != s.target.LaunchID || info.Owner != "app" {
		return errors.Join(errors.New("target run file identity changed"), err)
	}
	data, err := a.get(ctx, s, "/readyz")
	if err != nil {
		return err
	}
	var probe struct {
		Status                  string `json:"status"`
		PID                     int    `json:"pid"`
		ExecutablePath          string `json:"executablePath"`
		WorkingDirectory        string `json:"workingDirectory"`
		StartupWorkingDirectory string `json:"startupWorkingDirectory"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return err
	}
	if probe.Status != "ready" || probe.PID != s.target.DaemonPID || probe.ExecutablePath != s.daemon || probe.StartupWorkingDirectory != s.frontend || probe.WorkingDirectory != s.target.DataDir {
		return errors.New("target readiness identity mismatch")
	}
	for _, route := range []string{"/api/v1/projects", "/api/v1/sessions"} {
		if _, err := a.get(ctx, s, route); err != nil {
			return err
		}
	}
	if err := a.captureTree(ctx, s); err != nil {
		return err
	}
	// A descendant scan can overlap shutdown. Recheck the two required
	// processes before admitting an observation from this target.
	for pid, want := range map[int]time.Time{s.target.ElectronPID: s.target.ElectronStartedAt, s.target.DaemonPID: s.target.DaemonStartedAt} {
		got, err := a.ops.startTime(pid)
		if err != nil || !got.Equal(want) {
			return errors.Join(fmt.Errorf("target process identity changed: %d", pid), err)
		}
	}
	return nil
}

func (a *Adapter) get(ctx context.Context, s *launch, route string) (json.RawMessage, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+strconv.Itoa(s.port)+route, http.NoBody)
	if err != nil {
		return nil, err
	}
	response, err := a.ops.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK || len(data) > maxResponseBytes || !json.Valid(data) {
		return nil, fmt.Errorf("target GET %s returned invalid or oversized JSON, status %d", route, response.StatusCode)
	}
	return data, nil
}

// QueryDaemon maps the resource enum to fixed read-only routes, with no URL input.
func (a *Adapter) QueryDaemon(ctx context.Context, target domain.TestTargetIdentity, request domain.TestDaemonQueryRequest) (domain.TestDaemonQueryResult, error) {
	var result domain.TestDaemonQueryResult
	var route string
	if (request.Resource == domain.TestDaemonProjects || request.Resource == domain.TestDaemonSessions) && request.SessionID != "" {
		return result, errors.New("session ID is only allowed for a session resource")
	}
	switch request.Resource {
	case domain.TestDaemonProjects:
		route = "/api/v1/projects"
	case domain.TestDaemonSessions:
		route = "/api/v1/sessions"
	case domain.TestDaemonReviews, domain.TestDaemonConversation:
		if !domain.ValidTestSessionID(request.SessionID) {
			return result, errors.New("invalid target session ID")
		}
		route = "/api/v1/sessions/" + request.SessionID + "/" + string(request.Resource)
	default:
		return result, errors.New("unsupported target daemon resource")
	}
	s, err := a.find(target)
	if err != nil {
		return result, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped || s.closing {
		return result, errors.New("target has stopped")
	}
	if err := a.probe(ctx, s); err != nil {
		return result, err
	}
	data, err := a.get(ctx, s, route)
	if err != nil {
		return result, err
	}
	if err := a.probe(ctx, s); err != nil {
		return result, err
	}
	return domain.TestDaemonQueryResult{Resource: request.Resource, Data: data}, nil
}

// ReadLogs reads only this launch's combined output; fixture oracle files and
// arbitrary target files are never exposed. Saved logs remain readable after Stop.
func (a *Adapter) ReadLogs(ctx context.Context, target domain.TestTargetIdentity, request domain.TestReadLogsRequest) (domain.TestLogResult, error) {
	var result domain.TestLogResult
	s, err := a.find(target)
	if err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var offsets [3]int64
	cursor := []string{request.Cursor}
	if strings.HasPrefix(request.Cursor, "v1:") {
		cursor = strings.Split(strings.TrimPrefix(request.Cursor, "v1:"), ",")
		if len(cursor) != len(offsets) {
			return result, errors.New("invalid target log cursor")
		}
	}
	for i, value := range cursor {
		if value == "" {
			continue
		}
		offsets[i], err = strconv.ParseInt(value, 10, 64)
		if err != nil || offsets[i] < 0 {
			return result, errors.New("invalid target log cursor")
		}
	}
	limit := request.MaxBytes
	if limit == 0 {
		limit = 65536
	}
	if limit < 1 || limit > 262144 {
		return result, errors.New("invalid target log limit")
	}
	// Electron pipes its managed daemon into the first captured stream. These
	// two optional files support daemon recipes that also retain a file sink.
	paths := []string{filepath.Join(s.root, "target.log"), filepath.Join(s.target.DataDir, "daemon.log"), filepath.Join(s.target.DataDir, "logs", "daemon.log")}
	var output strings.Builder
	for i, path := range paths {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		info, err := os.Lstat(path)
		// Preparation can fail before the captured Electron stream is created.
		if (i > 0 || s.target.ElectronPID == 0) && errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return result, err
		}
		if !info.Mode().IsRegular() {
			return result, errors.New("target log is not a regular file")
		}
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil || !beneath(s.root, resolved) {
			return result, errors.New("target log escapes its launch directory")
		}
		remaining := limit - output.Len()
		data, truncated, err := logPage(ctx, resolved, offsets[i], remaining)
		if err != nil {
			return result, err
		}
		_, _ = output.Write(data)
		offsets[i] += int64(len(data))
		result.Truncated = result.Truncated || truncated
	}
	result.Text = output.String()
	result.NextCursor = strconv.FormatInt(offsets[0], 10)
	if offsets[1] != 0 || offsets[2] != 0 {
		result.NextCursor = fmt.Sprintf("v1:%d,%d,%d", offsets[0], offsets[1], offsets[2])
	}
	return result, nil
}

func logPage(ctx context.Context, path string, offset int64, limit int) ([]byte, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = file.Close() }()
	buffer := make([]byte, limit+1)
	n, err := file.ReadAt(buffer, offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, false, err
	}
	truncated := n > limit
	if truncated {
		n = limit
	}
	return buffer[:n], truncated, nil
}
