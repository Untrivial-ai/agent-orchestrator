package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/runfile"
)

// commandTimeout bounds a mutating daemon call. Spawns do real work (git
// worktree add, tmux launch, hook install), so it is generous compared to the
// status probe timeout.
const commandTimeout = 2 * time.Minute

// maxDrainedBodyBytes bounds how much of an unused response body the CLI
// discards for keep-alive reuse without an unbounded read.
const maxDrainedBodyBytes = 4 << 10

// apiError is the subset of the daemon's JSON error envelope the CLI surfaces.
// RequestID is surfaced so a failed command can be correlated with daemon logs.
type apiError struct {
	Message   string `json:"message"`
	Code      string `json:"code"`
	RequestID string `json:"requestId"`
}

type apiResponseError struct {
	StatusCode int
	ErrorBody  apiError
}

var errDaemonUnavailable = errors.New("AO daemon unavailable")

// daemonUnavailableError keeps the established user-facing diagnostics while
// giving the few idempotent CLI operations that can safely retry a typed signal.
// Most commands continue returning this error immediately through doJSON.
type daemonUnavailableError struct {
	message string
	cause   error
}

func (e daemonUnavailableError) Error() string { return e.message }

func (e daemonUnavailableError) Unwrap() error { return e.cause }

func (e daemonUnavailableError) Is(target error) bool {
	return target == errDaemonUnavailable || errors.Is(e.cause, target)
}

// daemonResponseBody marks read failures for retry by idempotent calls. Keeping
// the marker at the reader boundary leaves JSON syntax and value errors intact.
type daemonResponseBody struct{ io.ReadCloser }

func (b daemonResponseBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		return n, daemonUnavailableError{message: err.Error(), cause: err}
	}
	return n, err
}

func (e apiResponseError) Error() string {
	if e.ErrorBody.Message == "" {
		return fmt.Sprintf("daemon returned HTTP %d", e.StatusCode)
	}
	return e.ErrorBody.String()
}

// String renders the envelope for the user: "<message> (<code>) [request <id>]",
// omitting whichever parts the daemon left empty.
func (e apiError) String() string {
	msg := e.Message
	if e.Code != "" {
		msg = fmt.Sprintf("%s (%s)", msg, e.Code)
	}
	if e.RequestID != "" {
		msg = fmt.Sprintf("%s [request %s]", msg, e.RequestID)
	}
	return msg
}

// getJSON sends GET /api/v1/<path> to the running daemon and decodes a 2xx
// response into out. A missing daemon or non-2xx API envelope is rendered the
// same way as mutating calls.
func (c *commandContext) getJSON(ctx context.Context, path string, out any) error {
	return c.doJSON(ctx, http.MethodGet, path, nil, out)
}

// postJSON sends body as JSON to POST /api/v1/<path> on the running daemon and
// decodes a 2xx response into out (out may be nil). A non-2xx response becomes
// an error built from the API error envelope. A missing run-file or a stale one
// (dead PID) yields a clear "not running" message rather than a
// connection-refused dump.
func (c *commandContext) postJSON(ctx context.Context, path string, body, out any) error {
	return c.doJSON(ctx, http.MethodPost, path, body, out)
}

// patchJSON sends body as JSON to PATCH /api/v1/<path> on the running daemon
// and decodes a 2xx response into out.
func (c *commandContext) patchJSON(ctx context.Context, path string, body, out any) error {
	return c.doJSON(ctx, http.MethodPatch, path, body, out)
}

// putJSON sends body as JSON to PUT /api/v1/<path> on the running daemon and
// decodes a 2xx response into out.
func (c *commandContext) putJSON(ctx context.Context, path string, body, out any) error {
	return c.doJSON(ctx, http.MethodPut, path, body, out)
}

// deleteJSON sends DELETE /api/v1/<path> to the running daemon and decodes a
// 2xx response into out.
func (c *commandContext) deleteJSON(ctx context.Context, path string, out any) error {
	return c.doJSON(ctx, http.MethodDelete, path, nil, out)
}

func (c *commandContext) doJSON(ctx context.Context, method, path string, body, out any) error {
	return c.doJSONPath(ctx, method, "/api/v1/"+path, body, out)
}

func (c *commandContext) postLoopbackJSON(ctx context.Context, path string, body any) error {
	return c.doJSONPath(ctx, http.MethodPost, path, body, nil)
}

func (c *commandContext) doJSONPath(ctx context.Context, method, path string, body, out any) error {
	return c.doJSONPathWithHeaders(ctx, method, path, body, out, nil)
}

func (c *commandContext) doJSONPathWithHeaders(
	ctx context.Context,
	method, path string,
	body, out any,
	headers map[string]string,
) error {
	return c.doJSONPathWithHeadersAndTimeout(ctx, method, path, body, out, headers, commandTimeout)
}

func (c *commandContext) doJSONPathWithHeadersAndTimeout(
	ctx context.Context,
	method, path string,
	body, out any,
	headers map[string]string,
	timeout time.Duration,
) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	info, err := runfile.Read(cfg.RunFilePath)
	if err != nil {
		return err
	}
	if info == nil {
		return daemonUnavailableError{message: "AO daemon is not running — start it with `ao start`"}
	}
	if !c.deps.ProcessAlive(info.PID) {
		return daemonUnavailableError{message: fmt.Sprintf("AO daemon is not running (stale run-file at %s) — start it with `ao start`", cfg.RunFilePath)}
	}

	var reader io.Reader = http.NoBody
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(payload)
	}
	url := fmt.Sprintf("http://%s:%d%s", config.LoopbackHost, info.Port, path)
	req, err := http.NewRequestWithContext(ctx, method, url, reader) // #nosec G704 -- daemon host is fixed loopback; path is an internal API route.
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}

	// Reuse the injected client's transport (keeps it stubbable in tests) but
	// give daemon API calls far more headroom than the 2s status-probe timeout.
	client := *c.deps.HTTPClient
	client.Timeout = timeout
	resp, err := client.Do(req) // #nosec G704 -- request target is the fixed loopback daemon URL above.
	if err != nil {
		return daemonUnavailableError{message: fmt.Sprintf("call daemon: %v", err), cause: err}
	}
	resp.Body = daemonResponseBody{ReadCloser: resp.Body}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var e apiError
		_ = json.NewDecoder(resp.Body).Decode(&e)
		return apiResponseError{StatusCode: resp.StatusCode, ErrorBody: e}
	}
	if out == nil {
		// Explicitly bodyless call (telemetry/activity hooks, fire-and-forget
		// posts). Drain only a bounded remainder so the connection can be
		// reused without an unbounded read.
		_, _ = io.CopyN(io.Discard, resp.Body, maxDrainedBodyBytes)
		return nil
	}
	// A 204 carries no body by contract; a zero-value out is the legitimate
	// result. Any other 2xx with a required decoded result must carry a
	// JSON document — an empty body is a broken contract, not success.
	if resp.StatusCode == http.StatusNoContent {
		_, _ = io.CopyN(io.Discard, resp.Body, maxDrainedBodyBytes)
		return nil
	}
	// Peek at the first non-whitespace byte before decoding: an empty body
	// surfaces as io.EOF, but a literal JSON null decodes successfully into
	// a zero value. Both are a broken contract for a required result, so
	// both are rejected here rather than silently succeeding.
	br := bufio.NewReader(resp.Body)
	for {
		b, err := br.ReadByte()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return fmt.Errorf("decode response: missing required response body (HTTP %d %s %s): %w", resp.StatusCode, method, path, err)
			}
			return fmt.Errorf("decode response: %w", err)
		}
		if b == ' ' || b == '\t' || b == '\n' || b == '\r' {
			continue
		}
		if b == 'n' {
			if rest, err := br.Peek(3); err == nil && string(rest) == "ull" {
				return fmt.Errorf("decode response: missing required response body (HTTP %d %s %s): null response body", resp.StatusCode, method, path)
			}
		}
		if err := br.UnreadByte(); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		break
	}
	if err := json.NewDecoder(br).Decode(out); err != nil {
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("decode response: missing required response body (HTTP %d %s %s): %w", resp.StatusCode, method, path, err)
		}
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}
