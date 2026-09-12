//go:build !windows

// Package e2e drives a real `ao daemon` over HTTP.
//
// These are not unit tests with a fake seam. They boot the actual daemon binary
// against a throwaway data dir, register a real git project, and talk to a real
// agent. That is the only level at which some of chat mode's promises can be
// checked at all: "the mode you picked is the mode you get", "your message
// survives a restart", "the agent asked before it acted" are claims about the
// whole stack — routes, store, controller, provider — not about any one package.
//
// Gated on AO_CHAT_E2E=1 because they cost real model calls and need a working
// local agent install:
//
//	AO_CHAT_E2E=1 go test ./e2e/ -v -timeout 20m
//
// Windows is excluded: the harness uses process groups to make sure a killed
// daemon takes its agent child processes with it.
package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

const gateEnv = "AO_CHAT_E2E"

// requireE2E skips unless the gate is set and the agent binary is present.
// Skipping loudly with a reason beats a green run that exercised nothing.
func requireE2E(t *testing.T) {
	t.Helper()
	if os.Getenv(gateEnv) != "1" {
		t.Skipf("set %s=1 to run the chat end-to-end suite (real daemon, real model calls)", gateEnv)
	}
	bin := codexBinary()
	if _, err := exec.LookPath(bin); err != nil {
		t.Skipf("agent binary %q is not on PATH: %v", bin, err)
	}
}

func codexBinary() string {
	if bin := os.Getenv("AO_CODEX_BIN"); bin != "" {
		return bin
	}
	return "codex"
}

/* ---- the daemon under test --------------------------------------------- */

var (
	buildOnce sync.Once
	aoBinary  string
	buildErr  error
)

// buildDaemon compiles the binary under test once per `go test` process. Testing
// the built binary rather than an in-process assembly is the point: wiring bugs
// live in main and in the daemon's own boot path, which an in-process harness
// would skip right past.
func buildDaemon(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "ao-e2e-bin-")
		if err != nil {
			buildErr = err
			return
		}
		out := filepath.Join(dir, "ao")
		cmd := exec.Command("go", "build", "-o", out, "./cmd/ao")
		cmd.Dir = ".."
		if combined, err := cmd.CombinedOutput(); err != nil {
			buildErr = fmt.Errorf("go build ./cmd/ao: %w\n%s", err, combined)
			return
		}
		aoBinary = out
	})
	if buildErr != nil {
		t.Fatalf("build daemon: %v", buildErr)
	}
	return aoBinary
}

type daemon struct {
	t       *testing.T
	dataDir string
	port    int
	baseURL string
	logPath string

	cmd  *exec.Cmd
	pgid int
}

// startDaemon boots a daemon on dataDir. Passing a dataDir that a previous
// daemon used is how the restart scenarios work: the second boot has to find the
// first one's sessions on disk.
func startDaemon(t *testing.T, dataDir string) *daemon {
	t.Helper()
	bin := buildDaemon(t)
	port := freePort(t)

	logPath := filepath.Join(t.TempDir(), fmt.Sprintf("daemon-%d.log", port))
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("create daemon log: %v", err)
	}

	cmd := exec.Command(bin, "daemon")
	cmd.Env = append(os.Environ(),
		"AO_DATA_DIR="+dataDir,
		"AO_RUN_FILE="+filepath.Join(dataDir, "running.json"),
		"AO_PORT="+strconv.Itoa(port),
	)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	// Own process group: a hard kill must take the agent child processes with it,
	// or a killed daemon leaves an app-server holding the conversation and the
	// next boot's resume races a process that is still alive.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		t.Fatalf("start daemon: %v", err)
	}
	d := &daemon{
		t:       t,
		dataDir: dataDir,
		port:    port,
		baseURL: fmt.Sprintf("http://127.0.0.1:%d/api/v1", port),
		logPath: logPath,
		cmd:     cmd,
		pgid:    cmd.Process.Pid,
	}
	t.Cleanup(d.kill)
	d.waitReady()
	// Kill this daemon's sessions before it goes away. A TUI session's tmux lives
	// outside the data dir and outlives the daemon, so without this the machine
	// accumulates panes that later collide by name. Best-effort: the crash
	// scenarios kill the daemon on purpose and these calls simply fail.
	t.Cleanup(d.killAllSessions)
	return d
}

// killAllSessions tears down every session this daemon still considers live.
func (d *daemon) killAllSessions() {
	var list struct {
		Sessions []struct {
			ID           string `json:"id"`
			IsTerminated bool   `json:"isTerminated"`
		} `json:"sessions"`
	}
	if status, err := d.call("GET", "/sessions", nil, &list); err != nil || status != http.StatusOK {
		return
	}
	for _, session := range list.Sessions {
		if session.IsTerminated {
			continue
		}
		_, _ = d.call("POST", "/sessions/"+session.ID+"/kill", nil, nil)
	}
}

func (d *daemon) waitReady() {
	d.t.Helper()
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		var settings struct {
			DefaultSessionMode string   `json:"defaultSessionMode"`
			ChatHarnesses      []string `json:"chatHarnesses"`
		}
		if status, err := d.call("GET", "/settings", nil, &settings); err == nil && status == http.StatusOK {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	d.t.Fatalf("daemon never became ready on :%d\n%s", d.port, d.tailLog())
}

// stop shuts the daemon down the way a user quitting the app would.
func (d *daemon) stop() {
	if d.cmd == nil || d.cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-d.pgid, syscall.SIGTERM)
	done := make(chan struct{})
	go func() { _, _ = d.cmd.Process.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		_ = syscall.Kill(-d.pgid, syscall.SIGKILL)
		<-done
	}
	d.cmd = nil
	d.waitPortFree()
}

// kill is the crash case: no shutdown hooks run, nothing gets settled on the way
// out. What the next boot finds on disk is all it has.
func (d *daemon) kill() {
	if d.cmd == nil || d.cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-d.pgid, syscall.SIGKILL)
	_, _ = d.cmd.Process.Wait()
	d.cmd = nil
	d.waitPortFree()
}

func (d *daemon) waitPortFree() {
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", d.port), 200*time.Millisecond)
		if err != nil {
			return
		}
		_ = conn.Close()
		time.Sleep(200 * time.Millisecond)
	}
}

func (d *daemon) tailLog() string {
	data, err := os.ReadFile(d.logPath)
	if err != nil {
		return fmt.Sprintf("(no daemon log: %v)", err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	// Successful request logs are noise here: the interesting lines are warnings,
	// errors, and anything the chat controller wrote. Keeping them out is what
	// makes a failure message readable at all.
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.Contains(line, "level=INFO msg=\"http request\"") && strings.Contains(line, "status=200") {
			continue
		}
		kept = append(kept, line)
	}
	if len(kept) > 60 {
		kept = kept[len(kept)-60:]
	}
	return "--- daemon log (non-200 requests, warnings, errors) ---\n" + strings.Join(kept, "\n")
}

/* ---- HTTP -------------------------------------------------------------- */

var httpClient = &http.Client{Timeout: 30 * time.Second}

func (d *daemon) call(method, path string, body, out any) (int, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequest(method, d.baseURL+path, reader)
	if err != nil {
		return 0, err
	}
	if body != nil {
		req.Header.Set("content-type", "application/json")
	}
	res, err := httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = res.Body.Close() }()
	payload, err := io.ReadAll(res.Body)
	if err != nil {
		return res.StatusCode, err
	}
	if out != nil && len(payload) > 0 {
		if err := json.Unmarshal(payload, out); err != nil {
			return res.StatusCode, fmt.Errorf("decode %s %s (%d): %w: %s",
				method, path, res.StatusCode, err, payload)
		}
	}
	return res.StatusCode, nil
}

// mustCall fails the test unless the response status matches.
func (d *daemon) mustCall(method, path string, want int, body, out any) {
	d.t.Helper()
	var raw json.RawMessage
	status, err := d.call(method, path, body, &raw)
	if err != nil {
		d.t.Fatalf("%s %s: %v", method, path, err)
	}
	if status != want {
		// The envelope deliberately hides internal detail from clients, so the
		// daemon's own log is the only place the cause is written down.
		d.t.Fatalf("%s %s: status %d, want %d: %s\n%s", method, path, status, want, raw, d.tailLog())
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			d.t.Fatalf("%s %s: decode: %v: %s", method, path, err, raw)
		}
	}
}

// apiError is the daemon's error envelope. Asserting on `code` rather than on
// prose keeps these tests from breaking when a message is reworded.
type apiError struct {
	Error   string `json:"error"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (d *daemon) callExpectingError(method, path string, body any) (int, apiError) {
	d.t.Helper()
	var out apiError
	status, err := d.call(method, path, body, &out)
	if err != nil {
		d.t.Fatalf("%s %s: %v", method, path, err)
	}
	return status, out
}

/* ---- fixtures ---------------------------------------------------------- */

// seedProject creates a real git repo and registers it, because a session's
// workspace is a real worktree cut from a real branch.
//
// The project id is made unique per run because session ids derive from it and a
// TUI session's tmux name is global to the machine, not scoped to the data dir. A
// fixed fixture name collides with its own leftovers from an earlier run.
func seedProject(t *testing.T, d *daemon, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), fmt.Sprintf("%s%d", name, uniqueSuffix()))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	run := func(args ...string) {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=ao-e2e", "GIT_AUTHOR_EMAIL=e2e@example.com",
			"GIT_COMMITTER_NAME=ao-e2e", "GIT_COMMITTER_EMAIL=e2e@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}
	run("git", "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# e2e fixture\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	run("git", "add", "-A")
	run("git", "commit", "-m", "init")
	// Strict default-branch resolution deliberately refuses a local-only HEAD.
	// Give the fixture a primary remote with a cached HEAD like a real project.
	run("git", "remote", "add", "origin", dir)
	run("git", "fetch", "origin", "main")
	run("git", "remote", "set-head", "origin", "main")

	var out struct {
		Project struct{ ID string } `json:"project"`
	}
	d.mustCall("POST", "/projects", http.StatusCreated, map[string]any{"path": dir}, &out)
	if out.Project.ID == "" {
		// Some builds answer 200 for an already-registered path; re-read to get the id.
		d.mustCall("POST", "/projects", http.StatusOK, map[string]any{"path": dir}, &out)
	}
	if out.Project.ID == "" {
		t.Fatalf("project registration returned no id for %s", dir)
	}
	return out.Project.ID
}

// setPermissions sets the project's agent permission mode. Approvals are only
// reachable through it: AO maps permission mode onto the provider's approval
// policy at launch, and the default posture deliberately never asks.
func setPermissions(t *testing.T, d *daemon, projectID, mode string) {
	t.Helper()
	d.mustCall("PUT", "/projects/"+projectID+"/config", http.StatusOK, map[string]any{
		"config": map[string]any{
			"agentConfig": map[string]any{"permissions": mode},
		},
	}, nil)
}

type spawned struct {
	Session struct {
		ID      string `json:"id"`
		Mode    string `json:"mode"`
		Status  string `json:"status"`
		Harness string `json:"harness"`
	} `json:"session"`
}

func spawn(t *testing.T, d *daemon, req map[string]any) spawned {
	t.Helper()
	var out spawned
	d.mustCall("POST", "/sessions", http.StatusCreated, req, &out)
	if out.Session.ID == "" {
		t.Fatalf("spawn returned no session id: %+v", out)
	}
	return out
}

/* ---- conversation reads ------------------------------------------------ */

type turn struct {
	// Set when an undo discarded the turn: the row survives so a client can say
	// what was taken back, which is the distinction the rollback scenarios assert.
	RolledBack     bool   `json:"rolledBack"`
	ID             string `json:"id"`
	State          string `json:"state"`
	ProviderTurnID string `json:"providerTurnId"`
	ErrorMessage   string `json:"errorMessage"`
	RequestedAt    string `json:"requestedAt"`
	// Diff is what the turn changed on disk. A nil pointer and an empty file list
	// are different answers: the first means the provider never reported, the second
	// means it reported that nothing changed.
	Diff *turnDiff `json:"diff"`
	// Plan is the agent's plan for the turn. Nil means it made none, which is not the
	// same as an empty plan.
	Plan *turnPlan `json:"plan"`
}

// turnPlan is the agent's plan, as structured steps rather than prose: the per-step
// status is where the agent is up to, and that is not recoverable from a sentence.
type turnPlan struct {
	Explanation string     `json:"explanation"`
	Steps       []planStep `json:"steps"`
}

type planStep struct {
	Text   string `json:"text"`
	Status string `json:"status"`
}

type turnDiff struct {
	Files     []diffFile `json:"files"`
	Truncated bool       `json:"truncated"`
}

type diffFile struct {
	Path      string `json:"path"`
	OldPath   string `json:"oldPath"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Status    string `json:"status"`
}

// fileByPath finds a changed file, so an assertion does not depend on the order the
// provider happened to list them in.
func (d turnDiff) fileByPath(path string) (diffFile, bool) {
	for _, file := range d.Files {
		if file.Path == path {
			return file, true
		}
	}
	return diffFile{}, false
}

type message struct {
	ID       string `json:"id"`
	TurnID   string `json:"turnId"`
	Sequence int64  `json:"sequence"`
	Revision int64  `json:"revision"`
	Role     string `json:"role"`
	Origin   string `json:"origin"`
	Text     string `json:"text"`
	Stream   bool   `json:"streaming"`
}

type activity struct {
	ID        string `json:"id"`
	TurnID    string `json:"turnId"`
	Sequence  int64  `json:"sequence"`
	Kind      string `json:"activityKind"`
	Status    string `json:"status"`
	Summary   string `json:"summary"`
	RequestID string `json:"requestId"`
	// Detail is the typed payload for the activity's kind. An approval's offered
	// decisions live in here rather than as a top-level field, which is the shape
	// the renderer reads.
	DetailJSON json.RawMessage `json:"detail"`
}

type decisionOpt struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// decisions is what a client can actually render buttons from. Reading it the way
// the renderer does is the point: a field the daemon writes somewhere the client
// never looks is indistinguishable from a field it never wrote.
func (a activity) decisions() []decisionOpt {
	var detail struct {
		Decisions []decisionOpt `json:"decisions"`
	}
	if len(a.DetailJSON) == 0 {
		return nil
	}
	if err := json.Unmarshal(a.DetailJSON, &detail); err != nil {
		return nil
	}
	return detail.Decisions
}

type turnSettings struct {
	Model           string `json:"model"`
	ReasoningEffort string `json:"reasoningEffort"`
	ApprovalMode    string `json:"approvalMode"`
}

type snapshot struct {
	ConversationID string       `json:"conversationId"`
	Settings       turnSettings `json:"settings"`
	Mode           string       `json:"mode"`
	Controller     string       `json:"controller"`
	Title          string       `json:"title"`
	LatestSequence int64        `json:"latestSequence"`
	Turns          []turn       `json:"turns"`
	Messages       []message    `json:"messages"`
	Activities     []activity   `json:"activities"`
	// Pointers because absent and zero are different claims: the provider not
	// having reported yet is not the same as a conversation using no tokens.
	Usage      *usageState      `json:"usage"`
	RateLimits *rateLimitsState `json:"rateLimits"`
	// Nil until a compaction has run, which is the distinction the compaction
	// scenarios assert on.
	CompactedAt *string `json:"compactedAt"`
	// Provider state that belongs to the conversation rather than to a turn. All nil
	// until the provider reports, so absent stays distinguishable from empty.
	ModelReroute *modelRerouteState `json:"modelReroute"`
	Account      *accountState      `json:"account"`
	ThreadState  *threadState       `json:"threadState"`
	MCPServers   []mcpServerState   `json:"mcpServers"`
}

// modelRerouteState is the provider answering with a model other than the one asked
// for. Present means the composer selected model is no longer what is replying.
type modelRerouteState struct {
	FromModel      string `json:"fromModel"`
	ToModel        string `json:"toModel"`
	Reason         string `json:"reason"`
	ProviderTurnID string `json:"providerTurnId"`
	At             string `json:"at"`
}

type accountState struct {
	AuthMode         string  `json:"authMode"`
	PlanLabel        string  `json:"planLabel"`
	ReauthRequiredAt *string `json:"reauthRequiredAt"`
	ReauthReason     string  `json:"reauthReason"`
}

// threadState is the PROVIDER lifecycle view of the thread, not the session status
// AO derives. Both are readable from one snapshot, which is why they are separate.
type threadState struct {
	Status     string   `json:"status"`
	WaitingOn  []string `json:"waitingOn"`
	ArchivedAt *string  `json:"archivedAt"`
	ClosedAt   *string  `json:"closedAt"`
}

type mcpServerState struct {
	Name          string `json:"name"`
	Status        string `json:"status"`
	Error         string `json:"error"`
	FailureReason string `json:"failureReason"`
}

// usageState is the conversation's token position, carried as current state on the
// snapshot rather than as timeline rows.
type usageState struct {
	ContextUsed   int64 `json:"contextUsed"`
	ContextWindow int64 `json:"contextWindow"`
	InputTokens   int64 `json:"inputTokens"`
	OutputTokens  int64 `json:"outputTokens"`
	CachedTokens  int64 `json:"cachedTokens"`
	TotalTokens   int64 `json:"totalTokens"`
}

// rateLimitsState is the account's quota position. Percentages in 0..100, with
// negative meaning the provider did not report that window.
type rateLimitsState struct {
	PrimaryUsedPercent       float64 `json:"primaryUsedPercent"`
	SecondaryUsedPercent     float64 `json:"secondaryUsedPercent"`
	PrimaryResetsInSeconds   int64   `json:"primaryResetsInSeconds"`
	SecondaryResetsInSeconds int64   `json:"secondaryResetsInSeconds"`
	PlanLabel                string  `json:"planLabel"`
	// CompactedAt is when history was last summarized to reclaim context, empty if
	// never. On the snapshot because a client consults it on every render and must
	// not have to scan an unbounded timeline for it.
	CompactedAt    string     `json:"compactedAt"`
	Mode           string     `json:"mode"`
	Controller     string     `json:"controller"`
	LatestSequence int64      `json:"latestSequence"`
	Turns          []turn     `json:"turns"`
	Messages       []message  `json:"messages"`
	Activities     []activity `json:"activities"`
}

func (s snapshot) turnByID(id string) (turn, bool) {
	for _, t := range s.Turns {
		if t.ID == id {
			return t, true
		}
	}
	return turn{}, false
}

// userTurnStates maps each inbound message's text to the state of its turn,
// which is how the queue and interrupt scenarios read the timeline: a turn only
// matters here as the fate of one message.
func (s snapshot) userTurnStates() map[string]string {
	states := map[string]string{}
	for _, m := range s.Messages {
		if m.Role != "user" {
			continue
		}
		if t, ok := s.turnByID(m.TurnID); ok {
			states[m.Text] = t.State
		}
	}
	return states
}

func (s snapshot) assistantText() string {
	var b strings.Builder
	for _, m := range s.Messages {
		if m.Role == "assistant" {
			b.WriteString(m.Text)
			b.WriteString("\n")
		}
	}
	return b.String()
}

func (s snapshot) pendingApproval() (activity, bool) {
	for _, a := range s.Activities {
		if a.Kind == "approval" && a.Status == "pending" {
			return a, true
		}
	}
	return activity{}, false
}

func (d *daemon) conversation(sessionID string) snapshot {
	d.t.Helper()
	var out snapshot
	d.mustCall("GET", "/sessions/"+sessionID+"/conversation", http.StatusOK, nil, &out)
	return out
}

// awaitConversation polls until pred holds. Every scenario here is asynchronous —
// the provider streams on its own schedule — so polling with a deadline and a
// diagnostic dump is the only honest way to assert on an outcome.
func (d *daemon) awaitConversation(sessionID string, timeout time.Duration, what string, pred func(snapshot) bool) snapshot {
	d.t.Helper()
	deadline := time.Now().Add(timeout)
	var last snapshot
	for time.Now().Before(deadline) {
		last = d.conversation(sessionID)
		if pred(last) {
			return last
		}
		time.Sleep(300 * time.Millisecond)
	}
	d.t.Fatalf("timed out after %s waiting for %s\n%s\n%s",
		timeout, what, describe(last), d.tailLog())
	return last
}

// awaitLiveController waits for a session to be live with a controller attached.
//
// Reattaching after a restart is the boot pass's job — reconcile captures the
// session, records a restore marker, and the restore pass relaunches it — so a
// client has nothing to call and simply waits, which is what the renderer does.
func (d *daemon) awaitLiveController(sessionID string, timeout time.Duration) snapshot {
	d.t.Helper()
	deadline := time.Now().Add(timeout)
	var last snapshot
	for time.Now().Before(deadline) {
		var read struct {
			Session struct {
				Status       string `json:"status"`
				IsTerminated bool   `json:"isTerminated"`
			} `json:"session"`
		}
		if status, err := d.call("GET", "/sessions/"+sessionID, nil, &read); err == nil && status == http.StatusOK {
			if !read.Session.IsTerminated {
				var snap snapshot
				if code, err := d.call("GET", "/sessions/"+sessionID+"/conversation", nil, &snap); err == nil && code == http.StatusOK {
					last = snap
					if snap.Controller == "ready" || snap.Controller == "busy" {
						return snap
					}
				}
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	d.t.Fatalf("session %s never came back with a live controller within %s\n%s\n%s",
		sessionID, timeout, describe(last), d.tailLog())
	return last
}

func describe(s snapshot) string {
	var b strings.Builder
	fmt.Fprintf(&b, "--- conversation (controller=%s seq=%d) ---\n", s.Controller, s.LatestSequence)
	for _, t := range s.Turns {
		fmt.Fprintf(&b, "  turn %s state=%s provider=%s err=%q\n",
			short(t.ID), t.State, short(t.ProviderTurnID), t.ErrorMessage)
	}
	for _, m := range s.Messages {
		fmt.Fprintf(&b, "  msg  seq=%d %s/%s %q\n", m.Sequence, m.Role, m.Origin, trim(m.Text, 70))
	}
	for _, a := range s.Activities {
		fmt.Fprintf(&b, "  act  seq=%d %s/%s %q req=%s\n",
			a.Sequence, a.Kind, a.Status, trim(a.Summary, 60), a.RequestID)
	}
	return b.String()
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func trim(text string, n int) string {
	text = strings.Join(strings.Fields(text), " ")
	if len(text) > n {
		return text[:n] + "…"
	}
	return text
}

func terminal(state string) bool {
	switch state {
	case "completed", "interrupted", "failed":
		return true
	default:
		return false
	}
}

/* ---- misc -------------------------------------------------------------- */

// uniqueSuffix is a short monotonic tag for fixture names. Session ids are
// user-visible and derived from the project id, so it stays short.
func uniqueSuffix() int64 {
	return suffixCounter.Add(1) + time.Now().Unix()%100000
}

var suffixCounter atomic.Int64

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	if err := l.Close(); err != nil {
		t.Fatalf("release reserved port: %v", err)
	}
	return port
}

// harnessWithoutChatDriver names an agent the daemon says cannot run chat mode.
//
// Read from the daemon rather than hardcoded: drivers get added, and a test naming
// an agent by hand quietly stops testing anything the day that agent gains one.
func harnessWithoutChatDriver(t *testing.T, d *daemon) string {
	t.Helper()
	var settings struct {
		ChatHarnesses []string `json:"chatHarnesses"`
	}
	d.mustCall("GET", "/settings", http.StatusOK, nil, &settings)

	supported := map[string]bool{}
	for _, harness := range settings.ChatHarnesses {
		supported[harness] = true
	}
	// Any real harness will do; these are simply ones with no machine protocol AO
	// drives. The loop is what keeps the test honest if one of them ever gains a
	// driver.
	for _, candidate := range []string{"aider", "goose", "continue", "cline", "amp"} {
		if !supported[candidate] {
			return candidate
		}
	}
	t.Fatalf("every candidate harness now has a chat driver: %v", settings.ChatHarnesses)
	return ""
}
