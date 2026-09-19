package vmbrowser

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	// streamAlreadyDisabledMessage mirrors STREAM_ALREADY_DISABLED_MESSAGE in
	// agent-browser-runtime.ts. A non-zero exit containing it is success.
	streamAlreadyDisabledMessage = "Streaming is not enabled for this session"

	defaultCommandTimeout = 60 * time.Second
	maxOutputBytes        = 1 << 20
	maxScreenshotBytes    = 5 << 20
)

// Runner executes one agent-browser CLI invocation with the given environment.
// Tests inject fakes.
type Runner interface {
	Run(ctx context.Context, env []string, args ...string) (stdout, stderr string, exitCode int, err error)
}

// EngineOptions configures the agent-browser engine for one session.
type EngineOptions struct {
	BinaryPath     string
	Root           string
	SessionID      string
	CommandTimeout time.Duration
	Logger         *slog.Logger
}

// Engine drives the pinned agent-browser binary against the supervised
// Chromium, mirroring the Electron runtime's invocation contract.
type Engine struct {
	chromium *Chromium
	runner   Runner
	opts     EngineOptions
	// namespace is the agent-browser session namespace: stable for this
	// engine's lifetime, unique per engine (mirrors the Electron per-session
	// runtime with its random suffix).
	namespace string
	// socketDir holds the engine's short-lived agent-browser daemon sockets.
	// It must stay SHORT: agent-browser enforces a 103-byte unix socket path
	// limit, and a path derived from the full session id (a UUID in the VM)
	// overflows it. A short /tmp prefix mirrors the Electron runtime's
	// socket-dir aliasing for the same reason.
	socketDir string
}

// NewEngine creates the engine. runner == nil uses the exec-based runner.
func NewEngine(chromium *Chromium, runner Runner, opts EngineOptions) *Engine {
	if runner == nil {
		runner = execRunner{binary: opts.BinaryPath, timeout: opts.CommandTimeout}
	}
	if opts.CommandTimeout <= 0 {
		opts.CommandTimeout = defaultCommandTimeout
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &Engine{
		chromium:  chromium,
		runner:    runner,
		opts:      opts,
		namespace: engineNamespace(opts.SessionID),
	}
}

// engineNamespace derives a SHORT agent-browser namespace from the session id.
// agent-browser builds "<socketDir>/namespaces/<ns>/run/<ns>.sock" and rejects
// paths over 103 bytes, so the session id is hashed instead of embedded (UUID
// session ids in the VM would overflow); the random suffix keeps engines
// unique, mirroring the Electron runtime's per-session random suffix.
func engineNamespace(sessionID string) string {
	digest := sha256.Sum256([]byte(sessionID))
	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		return "s-" + hex.EncodeToString(digest[:6])
	}
	return "s-" + hex.EncodeToString(digest[:6]) + "-" + hex.EncodeToString(suffix)
}

// ensureSocketDir lazily creates the engine's short socket dir under /tmp. The
// dir must stay SHORT for the same 103-byte socket path limit; deriving it
// from the session root would overflow with UUID session ids.
func (e *Engine) ensureSocketDir() (string, error) {
	if e.socketDir != "" {
		return e.socketDir, nil
	}
	dir, err := os.MkdirTemp("", "abr-")
	if err != nil {
		return "", fmt.Errorf("create agent-browser socket dir: %w", err)
	}
	e.socketDir = dir
	return dir, nil
}

// nativeEnvAllowlist mirrors NATIVE_ENV_ALLOWLIST (lowercase keys). The binary
// is third-party; it must not inherit worker credentials.
var nativeEnvAllowlist = map[string]struct{}{
	"path": {}, "pathext": {}, "systemroot": {}, "windir": {}, "comspec": {},
	"temp": {}, "tmp": {}, "tmpdir": {}, "lang": {}, "lc_all": {}, "lc_ctype": {},
	"term": {}, "colorterm": {}, "no_color": {}, "force_color": {},
}

func (e *Engine) environment(endpoint Endpoint) ([]string, error) {
	runDir := filepath.Join(e.opts.Root, "run")
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		return nil, fmt.Errorf("prepare agent-browser runtime dir: %w", err)
	}
	socketDir, err := e.ensureSocketDir()
	if err != nil {
		return nil, err
	}
	configPath := filepath.Join(runDir, "config.json")
	if err := os.WriteFile(configPath, []byte("{}\n"), 0o600); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, fmt.Errorf("write agent-browser config: %w", err)
	}

	environment := []string{}
	for _, pair := range os.Environ() {
		name, _, _ := strings.Cut(pair, "=")
		if _, ok := nativeEnvAllowlist[strings.ToLower(name)]; ok {
			environment = append(environment, pair)
		}
	}
	environment = append(environment,
		"HOME="+runDir,
		"USERPROFILE="+runDir,
		"XDG_CONFIG_HOME="+runDir,
		"XDG_CACHE_HOME="+runDir,
		"TEMP="+runDir,
		"TMP="+runDir,
		"TMPDIR="+runDir,
		"AGENT_BROWSER_CONFIG="+configPath,
		"AGENT_BROWSER_CDP="+endpoint.WebSocketURL,
		"AGENT_BROWSER_SOCKET_DIR="+socketDir,
		"AGENT_BROWSER_SESSION="+e.namespace,
		"AGENT_BROWSER_NAMESPACE="+e.namespace,
		"AGENT_BROWSER_CONTENT_BOUNDARIES=1",
		"AGENT_BROWSER_MAX_OUTPUT=50000",
		"AGENT_BROWSER_IDLE_TIMEOUT_MS=300000",
		"AGENT_BROWSER_AUTO_CONNECT=0",
	)
	return environment, nil
}

// Execute translates and runs one service action, returning the parsed result
// map (always marked untrustedExternalContent).
func (e *Engine) Execute(ctx context.Context, action string, args map[string]any) (map[string]any, error) {
	argv, err := NativeArguments(action, args)
	if err != nil {
		return nil, err
	}
	raw, err := e.runJSON(ctx, append(argv, "--json"))
	if err != nil {
		return nil, err
	}
	return parseAgentBrowserJSON(raw)
}

// disableStreaming reasserts the input-surface policy before every command.
// The native daemon can expire and be recreated between commands and its
// replacement starts streaming by default; "already disabled" (non-zero exit
// carrying the marker) is the desired state, any other failure is fatal.
func (e *Engine) disableStreaming(ctx context.Context, environment []string) error {
	stdout, stderr, exitCode, err := e.runner.Run(ctx, environment, "stream", "disable")
	if err == nil && exitCode != 0 &&
		!strings.Contains(stdout, streamAlreadyDisabledMessage) &&
		!strings.Contains(stderr, streamAlreadyDisabledMessage) {
		return &CommandError{
			Code:    "AGENT_BROWSER_START_FAILED",
			Message: strings.TrimSpace(stderr+" "+stdout) + " (unable to disable agent-browser streaming)",
		}
	}
	if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		return &CommandError{Code: "AGENT_BROWSER_START_FAILED", Message: err.Error()}
	}
	return nil
}

// runJSON performs the stream-disable preamble and one JSON command.
func (e *Engine) runJSON(ctx context.Context, argv []string) (string, error) {
	environment, err := e.prepare(ctx)
	if err != nil {
		return "", err
	}
	if err := e.disableStreaming(ctx, environment); err != nil {
		return "", err
	}

	stdout, stderr, exitCode, err := e.runner.Run(ctx, environment, argv...)
	if err != nil {
		return "", &CommandError{Code: "AGENT_BROWSER_COMMAND_FAILED", Message: err.Error()}
	}
	if exitCode != 0 {
		message := strings.TrimSpace(stderr)
		if message == "" {
			message = strings.TrimSpace(stdout)
		}
		if message == "" {
			message = fmt.Sprintf("agent-browser exited with code %d", exitCode)
		}
		return "", &CommandError{Code: "AGENT_BROWSER_COMMAND_FAILED", Message: message}
	}
	return stdout, nil
}

func (e *Engine) prepare(ctx context.Context) ([]string, error) {
	endpoint, err := e.chromium.EnsureRunning(ctx)
	if err != nil {
		return nil, err
	}
	return e.environment(endpoint)
}

// Screenshot captures the active tab as base64 PNG with its dimensions.
func (e *Engine) Screenshot(ctx context.Context) (data string, width, height int, err error) {
	environment, err := e.prepare(ctx)
	if err != nil {
		return "", 0, 0, err
	}
	directory, err := os.MkdirTemp(e.opts.Root, "screenshot-")
	if err != nil {
		return "", 0, 0, fmt.Errorf("create screenshot directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(directory) }()
	target := filepath.Join(directory, "screenshot.png")

	// The preamble keeps parity with runJSON's policy reassertion, including
	// the already-disabled tolerance.
	if err := e.disableStreaming(ctx, environment); err != nil {
		return "", 0, 0, err
	}
	stdout, stderr, exitCode, err := e.runner.Run(ctx, environment, "screenshot", target, "--json")
	if err != nil {
		return "", 0, 0, &CommandError{Code: "AGENT_BROWSER_COMMAND_FAILED", Message: err.Error()}
	}
	if exitCode != 0 {
		message := strings.TrimSpace(stderr)
		if message == "" {
			message = strings.TrimSpace(stdout)
		}
		return "", 0, 0, &CommandError{Code: "AGENT_BROWSER_COMMAND_FAILED", Message: message}
	}
	if _, err := parseAgentBrowserJSON(stdout); err != nil {
		return "", 0, 0, err
	}
	image, err := os.ReadFile(target)
	if err != nil {
		return "", 0, 0, &CommandError{Code: "AGENT_BROWSER_INVALID_OUTPUT", Message: "Browser screenshot file was not written"}
	}
	if len(image) > maxScreenshotBytes {
		return "", 0, 0, &CommandError{Code: "AGENT_BROWSER_OUTPUT_TOO_LARGE", Message: "Browser screenshot exceeded AO's size limit"}
	}
	width, height, err = pngDimensions(image)
	if err != nil {
		return "", 0, 0, err
	}
	return base64.StdEncoding.EncodeToString(image), width, height, nil
}

// Close best-effort tears down the engine: one agent-browser close command
// when Chromium is running (Close must never START a browser: the idle
// shutdown path calls it repeatedly, and restarting just to say goodbye would
// loop forever), then Chromium itself. Errors are logged by callers.
func (e *Engine) Close(ctx context.Context) error {
	if e.chromium.CurrentStatus().Running {
		if environment, err := e.prepare(ctx); err == nil {
			_, _, _, _ = e.runner.Run(ctx, environment, "close")
		}
	}
	return e.chromium.Stop(ctx)
}

func pngDimensions(image []byte) (int, int, error) {
	invalid := &CommandError{Code: "AGENT_BROWSER_INVALID_OUTPUT", Message: "Browser automation returned an invalid PNG screenshot"}
	if len(image) < 24 || string(image[1:4]) != "PNG" {
		return 0, 0, invalid
	}
	width := int(binary.BigEndian.Uint32(image[16:20]))
	height := int(binary.BigEndian.Uint32(image[20:24]))
	return width, height, nil
}

var staleReferenceMessage = regexp.MustCompile(`^(Unknown ref:|Could not locate element with)`)

// parseAgentBrowserJSON ports parseAgentBrowserJSON: envelope {success, data,
// error, _boundary} into the result map, preserving the content boundary and
// marking everything untrusted.
func parseAgentBrowserJSON(stdout string) (map[string]any, error) {
	invalidOutput := &CommandError{Code: "AGENT_BROWSER_INVALID_OUTPUT", Message: "Browser automation returned invalid structured output"}
	var envelope struct {
		Success  *bool           `json:"success"`
		Data     json.RawMessage `json:"data"`
		Error    json.RawMessage `json:"error"`
		Boundary map[string]any  `json:"_boundary"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		return nil, invalidOutput
	}
	if envelope.Success != nil && !*envelope.Success {
		code, message := commandFailure(envelope.Error)
		return nil, &CommandError{Code: code, Message: message}
	}

	result := map[string]any{}
	if len(envelope.Data) > 0 && string(envelope.Data) != "null" {
		var decoded any
		if err := json.Unmarshal(envelope.Data, &decoded); err != nil {
			return nil, invalidOutput
		}
		if object, ok := decoded.(map[string]any); ok {
			result = object
		} else {
			result = map[string]any{"value": decoded}
		}
	}
	// _boundary is native-output metadata; never forward a page-shaped field
	// with the same name, but preserve a valid boundary.
	delete(result, "_boundary")
	if nonce, ok := envelope.Boundary["nonce"].(string); ok && nonce != "" {
		if origin, ok := envelope.Boundary["origin"].(string); ok {
			result["_boundary"] = map[string]any{"nonce": nonce, "origin": origin}
		}
	}
	result["untrustedExternalContent"] = true
	return result, nil
}

func commandFailure(raw json.RawMessage) (string, string) {
	if len(raw) == 0 {
		return "AGENT_BROWSER_COMMAND_FAILED", "Browser automation failed"
	}
	var structured struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &structured); err == nil && (structured.Code != "" || structured.Message != "") {
		if structured.Code == "STALE_REFERENCE" {
			return "STALE_REFERENCE", structured.Message
		}
		if structured.Message == "" {
			return "AGENT_BROWSER_COMMAND_FAILED", "Browser automation failed"
		}
		return "AGENT_BROWSER_COMMAND_FAILED", structured.Message
	}
	var message string
	if err := json.Unmarshal(raw, &message); err == nil && message != "" {
		if staleReferenceMessage.MatchString(message) {
			return "STALE_REFERENCE", message
		}
		return "AGENT_BROWSER_COMMAND_FAILED", message
	}
	return "AGENT_BROWSER_COMMAND_FAILED", "Browser automation failed"
}

type execRunner struct {
	binary  string
	timeout time.Duration
}

func (r execRunner) Run(ctx context.Context, env []string, args ...string) (string, string, int, error) {
	if r.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.timeout)
		defer cancel()
	}
	// args is the agent-browser argv (subcommand first); the binary path is
	// supplied here, exactly like the Electron runtime's process runner.
	cmd := exec.CommandContext(ctx, r.binary, args...)
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &limitedWriter{buffer: &stdout, limit: maxOutputBytes}
	cmd.Stderr = &limitedWriter{buffer: &stderr, limit: maxOutputBytes}
	if err := cmd.Start(); err != nil {
		return "", "", -1, err
	}
	waitErr := cmd.Wait()
	exitCode := 0
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}
	if stdout.Len() >= maxOutputBytes || stderr.Len() >= maxOutputBytes {
		return stdout.String(), stderr.String(), exitCode,
			&CommandError{Code: "AGENT_BROWSER_OUTPUT_TOO_LARGE", Message: "agent-browser output exceeded AO's limit"}
	}
	if waitErr != nil && exitCode == 0 {
		return stdout.String(), stderr.String(), exitCode, waitErr
	}
	return stdout.String(), stderr.String(), exitCode, nil
}

// limitedWriter stops accepting bytes past limit so oversized native output
// cannot buffer without bound.
type limitedWriter struct {
	buffer *bytes.Buffer
	limit  int
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	if w.buffer.Len()+len(p) > w.limit {
		keep := w.limit - w.buffer.Len()
		if keep > 0 {
			_, _ = w.buffer.Write(p[:keep])
		}
		return len(p), nil // swallow the rest
	}
	return w.buffer.Write(p)
}

var _ Runner = execRunner{}
