# Cloud Browser Stage 1: In-VM Automation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the full `ao browser` verb set work unchanged inside cloud sandbox sessions by running a loopback browser service (browserd) inside the VM worker that drives a supervised headless Chromium through the pinned agent-browser binary.

**Architecture:** The VM worker process (`/ao-worker`) gains an in-process browserd: a loopback HTTP server speaking the exact desktop `/api/v1/browser/{commands,status}` contract, backed by a Chromium process supervisor and an agent-binary engine that mirrors the Electron host's dispatch. The CLI verb tree is extracted from `backend/internal/cli/browser.go` into `backend/pkg/clibrowser` (parameterized by a transport interface) and embedded in the in-VM `ao` CLI (`ao-cloud-agent`) so both desktop and cloud share one verb implementation. Agent automation never touches the control plane.

**Tech Stack:** Go 1.26 (backend + cloud modules, one go.work), cobra, chi, headless Chromium (Debian bookworm package), `vercel-labs/agent-browser` v0.33.1 prebuilt linux binaries (SHA-256 pinned), Docker/compose for the worker image.

**Spec:** `docs/superpowers/specs/2026-09-17-cloud-browser.md` (sections D1, D2, D7, D8, and Stage 1 exit criteria in section 8). Read the spec before executing; this plan argues from it.

## Global Constraints

- Backend module: `github.com/aoagents/agent-orchestrator/backend` (go 1.25.7 toolchain in CI). Cloud module: `github.com/aoagents/agent-orchestrator/cloud` (go 1.26.5). One `go.work` joins them.
- Cloud may import only `backend/pkg/*` (Go internal-package rule). Never import `backend/internal/*` from cloud.
- Cloud pins backend via `replace github.com/aoagents/agent-orchestrator/backend => github.com/Untrivial-ai/agent-orchestrator/backend v0.0.0-<sha>` in `cloud/go.mod`; new `backend/pkg` packages require a pin bump for standalone cloud builds (docker image). Local dev uses the workspace copy.
- Wire contract is frozen: `POST /api/v1/browser/commands`, `GET /api/v1/browser/status?sessionId=...`, capability header `X-AO-Browser-Capability`, error envelope top-level fields `message`, `code`, `requestId`.
- Capability scheme: verifier = base64url(SHA-256("ao-browser-capability-verifier-v1\x00" + sessionID + "\x00" + token)), constant-time compare.
- agent-browser pins (from `frontend/scripts/prepare-agent-browser.mjs`, VERSION = "0.33.1", base `https://github.com/vercel-labs/agent-browser/releases/download/v0.33.1`):
  - linux-x64 asset `agent-browser-linux-x64` sha256 `6e04d06605c4ca62da36e3263086e0f7ceae808b55508de2c3958d4b7fe430aa`
  - linux-arm64 asset `agent-browser-linux-arm64` sha256 `281cce8e3e9eb11fd823b13c085996d7361c35923ad454ce5cb06a5515630e9b`
- Screenshot cap 5 MiB (`MAX_SCREENSHOT_BYTES = 5 << 20` in Electron host), command output cap 1 MiB, command timeout 60 s (mirror Electron `COMMAND_TIMEOUT_MS`), close timeout 15 s.
- Engine env contract (mirror `frontend/src/main/agent-browser-runtime.ts` `environment()`): `AGENT_BROWSER_CONFIG`, `AGENT_BROWSER_CDP`, `AGENT_BROWSER_SOCKET_DIR` (per-session socket dir under the browser root), `AGENT_BROWSER_SESSION`, `AGENT_BROWSER_NAMESPACE`, `AGENT_BROWSER_CONTENT_BOUNDARIES=1`, `AGENT_BROWSER_MAX_OUTPUT=50000`, `AGENT_BROWSER_IDLE_TIMEOUT_MS=300000`, `AGENT_BROWSER_AUTO_CONNECT=0`, isolated `HOME`/`XDG_*`/`TMPDIR` dirs under the per-session browser root. The inherited-env allowlist is exactly (lowercase compare): `path, pathext, systemroot, windir, comspec, temp, tmp, tmpdir, lang, lc_all, lc_ctype, term, colorterm, no_color, force_color`.
- Every agent-browser invocation is preceded by `stream disable`; the already-disabled marker is the exact string `"Streaming is not enabled for this session"` (`STREAM_ALREADY_DISABLED_MESSAGE`): a non-zero exit whose stdout or stderr contains it is success.
- URL handling parity (`browser-view-host.ts`): `open` (and `tab-new` with url) goes through `normalizeAgentBrowserURL` (line 3157): trim; reject local paths and `file:` with `BROWSER_URL_FORBIDDEN`; accept scheme-bearing, localhost-like (`isLocalhostLike`, line 2452), or host-like (`looksLikeHost`, line 2420: IPv6 literal, explicit :port, or dotted) input; default scheme via `withDefaultScheme` (line 2402); only `http:`/`https:` pass; error codes `URL_REQUIRED`, `INVALID_URL`, `BROWSER_URL_FORBIDDEN` (all map to HTTP 400). Bare `localhost:3000` MUST work; that is the VM-dev-server case.
- Output sanitization parity (`browser-view-host.ts` lines 3069-3101): every URL or title placed in agent-visible results (`open` nav state, `get url`/`get title`, `tabs`/`tab-*` tab objects, URL-bearing console text) passes `sanitizeBrowserURL` (drop userinfo and hash, replace every query param value with `[redacted]`, non-http(s)/file protocols become `scheme[redacted]`), `sanitizeBrowserTitle`, and `sanitizeURLsInText`. The cloud service ports these to Go `net/url` and applies them at the same points; missing this would leak credentials into cloud transcripts that desktop redacts.
- Untrusted text in `console`/`errors` results is capped at 1 MiB (`MAX_EXTERNAL_TEXT_BYTES`, line 481) with a `[Content truncated at N bytes]` suffix, and each message is wrapped in the untrusted markers by the service (mirror `externalText` + `markUntrusted`, lines 3173-3185; the CLI re-escapes marker collisions).
- No network-dependent Go tests. Use fakes, `httptest`, temp dirs.
- Commit style: conventional commits (`feat:`, `refactor:`, `test:`, `chore:`, `docs:`). One logical change per commit. No attribution trailers.
- Verification commands: `cd backend && go build ./... && go test ./...`, `cd cloud && go build ./... && go test ./...`, `npm run lint` before handoff.
- Chromium flags (browserd): `--headless=new --remote-debugging-address=127.0.0.1 --remote-debugging-port=0 --user-data-dir=<profile> --no-first-run --no-default-browser-check --no-sandbox --disable-dev-shm-usage`. `--no-sandbox` is deliberate: the container is the isolation boundary and Docker userns restrictions break the Chromium sandbox. The chosen port is read from `<user-data-dir>/DevToolsActivePort` (Chromium writes `<port>\n<path>` there); fallback if the target build does not write it with `port=0`: pre-pick an ephemeral port (decided at the Task 1 spike, see Task 4).
- Verb matrix for Stage 1: passthrough and composite verbs per Task 8; `network-*`, `devtools-open`, `devtools-close`, `unhighlight` return `BROWSER_ACTION_UNSUPPORTED_CLOUD` (network capture is spec Stage 3; devtools is a spec non-goal; unhighlight is an Electron overlay feature).
- Profile dir: `<worker dataDir>/browser/<sessionID>/profile`; tabs.json sibling (written in Stage 1's lifecycle task only if trivially small; pause/restore polish is Stage 2+).

## File Structure

Created:

- `backend/pkg/browsercontract/contract.go` — action allowlist, route paths, capability header constant. Public, dependency-free.
- `backend/pkg/browsercontract/authority.go` — stateless HMAC capability issue/verify (string-typed).
- `backend/pkg/browsercontract/contract_test.go`, `authority_test.go`
- `backend/pkg/clibrowser/command.go` — the cobra verb tree (moved from `backend/internal/cli/browser.go`), parameterized by `Transport`.
- `backend/pkg/clibrowser/transport.go` — `Transport` interface + DTOs (request/response/error envelope).
- `backend/pkg/clibrowser/command_test.go` — moved + adapted CLI tests.
- `cloud/internal/vmbrowser/chromium.go` — Chromium process supervisor (lazy start, DevToolsActivePort discovery, restart backoff, park).
- `cloud/internal/vmbrowser/argv.go` — service action → agent-browser argv translation (port of `nativeArgumentsForAction` + validation).
- `cloud/internal/vmbrowser/engine.go` — engine: runs agent-browser binary with the env contract, JSON envelope parse, screenshot flow.
- `cloud/internal/vmbrowser/actmatcher.go` — deterministic instruction matcher (port of `browser-act-matcher.ts`).
- `cloud/internal/vmbrowser/service.go` — loopback HTTP service implementing the desktop browser contract; composite verbs (act, tabs, screenshot, open, get, console, errors).
- `cloud/internal/vmbrowser/*_test.go` — one test file per source file.
- `cloud/cmd/ao-worker/browserd.go` — wiring: capability mint, server start, env injection, idle shutdown.
- `docs/superpowers/notes/2026-09-17-agent-browser-attach-spike.md` — spike record (Task 1).

Modified:

- `backend/internal/service/browser/service.go` — delegates allowlist + authority to `browsercontract`.
- `backend/internal/service/browser/authority.go` — deleted (moved to browsercontract).
- `backend/internal/cli/browser.go` — shrinks to a shim hosting `clibrowser.NewCommand` with a daemon-transport adapter.
- `cloud/cmd/ao-cloud-agent/main.go` — mounts the `browser` verb tree via clibrowser.
- `cloud/cmd/ao-worker/main.go` — starts browserd, injects env.
- `cloud/Dockerfile` — worker stage: chromium package + pinned agent-browser binaries.
- `cloud/go.mod` — backend replace pin bump.
- `docs/cloud-development.md` — browser engine section.

---

### Task 1: Spike — verify agent-browser attach mode against headless Chromium

**Files:**
- Create: `docs/superpowers/notes/2026-09-17-agent-browser-attach-spike.md`

**Interfaces:**
- Consumes: agent-browser v0.33.1 linux-x64 binary, any local chromium/chrome.
- Produces: confirmation (or refutation) recorded in the note, of: (a) the env var and URL form `AGENT_BROWSER_CDP` accepts for a standalone Chromium, (b) that `stream disable` + `open` + `snapshot --interactive --compact --json` + `screenshot <path> --json` work against it, (c) the exact JSON envelope shape (`{"success":bool,"data":...,"error":...,"_boundary":...}`).

This task de-risks Tasks 4-8. If attach mode does not work as the Electron bridge implies, STOP and report the blocker to the orchestrator instead of proceeding.

- [x] **Step 1: Fetch the pinned binary and a headless Chromium locally**

```bash
mkdir -p /tmp/opencode/ab-spike && cd /tmp/opencode/ab-spike
curl -fsSL -o agent-browser \
  https://github.com/vercel-labs/agent-browser/releases/download/v0.33.1/agent-browser-linux-x64
echo "6e04d06605c4ca62da36e3263086e0f7ceae808b55508de2c3958d4b7fe430aa  agent-browser" | sha256sum -c -
chmod +x agent-browser
command -v chromium || command -v chromium-browser || command -v google-chrome
```

Expected: checksum OK; a chromium binary found (if none, `npx playwright install chromium` and use its executable path).

- [x] **Step 2: Run headless Chromium with a loopback debugging port**

```bash
mkdir -p profile
chromium --headless=new --remote-debugging-address=127.0.0.1 --remote-debugging-port=9222 \
  --user-data-dir="$PWD/profile" --no-first-run --no-default-browser-check \
  --no-sandbox --disable-dev-shm-usage about:blank &
sleep 2
curl -s http://127.0.0.1:9222/json/version
```

Expected: JSON including `"webSocketDebuggerUrl": "ws://127.0.0.1:9222/devtools/browser/<uuid>"`. Record the exact value.

- [x] **Step 3: Drive it through env-only attach**

```bash
export AGENT_BROWSER_CDP="ws://127.0.0.1:9222/devtools/browser/<uuid-from-step-2>"
export AGENT_BROWSER_SESSION=spike AGENT_BROWSER_NAMESPACE=spike
export AGENT_BROWSER_CONFIG="$PWD/config.json" AGENT_BROWSER_CONTENT_BOUNDARIES=1
export AGENT_BROWSER_MAX_OUTPUT=50000 AGENT_BROWSER_IDLE_TIMEOUT_MS=300000 AGENT_BROWSER_AUTO_CONNECT=0
echo '{}' > config.json
./agent-browser stream disable; echo "exit=$?"
./agent-browser open https://example.com --json
./agent-browser snapshot --interactive --compact --json
./agent-browser screenshot "$PWD/shot.png" --json; file "$PWD/shot.png"
```

Expected: `stream disable` exits 0 (or prints already-disabled); `open`/`snapshot` print the JSON envelope with `"success":true`; screenshot writes a real PNG. If `AGENT_BROWSER_CDP` with a browser-level ws URL is rejected, retry with `http://127.0.0.1:9222` and record which form works.

- [x] **Step 4: Record results and commit**

Write `docs/superpowers/notes/2026-09-17-agent-browser-attach-spike.md` with: the working endpoint form, each command's envelope output (trimmed), any deviations from the Electron-side assumptions, and the RSS of the chromium process (`ps -o rss= -p <pid>`), which fills the spec's unverified per-session RAM estimate for headless mode.

```bash
git add docs/superpowers/notes/2026-09-17-agent-browser-attach-spike.md
git commit -m "docs: record agent-browser attach-mode spike for cloud browser stage 1"
```

---

### Task 2: `backend/pkg/browsercontract` — shared allowlist + authority

**Files:**
- Create: `backend/pkg/browsercontract/contract.go`, `backend/pkg/browsercontract/authority.go`, `backend/pkg/browsercontract/contract_test.go`, `backend/pkg/browsercontract/authority_test.go`
- Modify: `backend/internal/service/browser/service.go`
- Delete: `backend/internal/service/browser/authority.go` (its tests move to the new package)

**Interfaces:**
- Produces (used by Tasks 7, 9, and the daemon):
  - `func Supported(action string) bool`
  - `func Actions() []string` (sorted, for docs/tests)
  - `const CapabilityHeader = "X-AO-Browser-Capability"`, `const RouteCommands = "/api/v1/browser/commands"`, `const RouteStatus = "/api/v1/browser/status"`
  - `type Authority struct{}`; `func NewAuthority() *Authority`; `func (a *Authority) Issue(sessionID string) (token, verifier string, err error)`; `func (a *Authority) Valid(sessionID, token, verifier string) bool`

- [x] **Step 1: Write the failing tests**

`backend/pkg/browsercontract/contract_test.go`:

```go
package browsercontract_test

import (
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/pkg/browsercontract"
)

func TestSupportedCoversTheDaemonAllowlist(t *testing.T) {
	must := []string{
		"open", "snapshot", "act", "click", "dblclick", "focus", "fill", "type", "press",
		"hover", "highlight", "unhighlight", "scrollintoview", "drag", "tabs", "tab-new",
		"tab-select", "tab-close", "scroll", "select", "check", "uncheck", "get", "wait",
		"screenshot", "network-start", "network-status", "network-list", "network-stop",
		"network-clear", "console", "errors", "frame", "dialog",
		"devtools-open", "devtools-close",
	}
	for _, action := range must {
		if !browsercontract.Supported(action) {
			t.Errorf("Supported(%q) = false, want true", action)
		}
	}
	for _, action := range []string{"", "navigate", "eval", "shell", "__destroy-session"} {
		if browsercontract.Supported(action) {
			t.Errorf("Supported(%q) = true, want false", action)
		}
	}
}

func TestSupportedNormalizesAction(t *testing.T) {
	if !browsercontract.Supported("  SNAPSHOT ") {
		t.Error("Supported must be case- and whitespace-insensitive")
	}
}
```

`backend/pkg/browsercontract/authority_test.go` (adapted from the internal package's tests; keep any extra cases the original file had):

```go
package browsercontract_test

import (
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/pkg/browsercontract"
)

func TestAuthorityIssueAndValid(t *testing.T) {
	authority := browsercontract.NewAuthority()
	token, verifier, err := authority.Issue("sess-1")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if token == "" || verifier == "" {
		t.Fatal("Issue returned empty token or verifier")
	}
	if !authority.Valid("sess-1", token, verifier) {
		t.Error("Valid rejected the capability it issued")
	}
	if authority.Valid("sess-2", token, verifier) {
		t.Error("Valid accepted a capability for a different session")
	}
	if authority.Valid("sess-1", "tampered", verifier) {
		t.Error("Valid accepted a tampered token")
	}
	if authority.Valid("sess-1", "", verifier) || authority.Valid("sess-1", token, "") {
		t.Error("Valid accepted empty credentials")
	}
}
```

- [x] **Step 2: Run tests to verify they fail**

Run: `cd backend && go test ./pkg/browsercontract/`
Expected: FAIL, "no Go files" or undefined import.

- [x] **Step 3: Implement the package**

`backend/pkg/browsercontract/contract.go`:

```go
// Package browsercontract is the single source of truth for the session
// browser command surface shared by the desktop daemon, the desktop CLI, and
// the cloud in-VM browser service. It is deliberately dependency-free.
package browsercontract

import "strings"

const (
	// CapabilityHeader carries the per-session browser capability token.
	CapabilityHeader = "X-AO-Browser-Capability"
	// RouteCommands is the loopback browser command route.
	RouteCommands = "/api/v1/browser/commands"
	// RouteStatus is the loopback browser status route.
	RouteStatus = "/api/v1/browser/status"
)

var actions = map[string]struct{}{
	"open": {}, "snapshot": {}, "act": {}, "click": {}, "dblclick": {}, "focus": {}, "fill": {}, "type": {}, "press": {},
	"hover": {}, "highlight": {}, "unhighlight": {}, "scrollintoview": {}, "drag": {}, "tabs": {}, "tab-new": {},
	"tab-select": {}, "tab-close": {}, "scroll": {}, "select": {}, "check": {},
	"uncheck": {}, "get": {}, "wait": {}, "screenshot": {}, "network-start": {},
	"network-status": {}, "network-list": {}, "network-stop": {}, "network-clear": {},
	"console": {}, "errors": {}, "frame": {}, "dialog": {},
	"devtools-open": {}, "devtools-close": {},
}

// Supported reports whether action is an enabled browser action. Actions are
// matched case-insensitively and ignore surrounding whitespace, matching the
// daemon's normalization at the dispatch boundary.
func Supported(action string) bool {
	_, ok := actions[strings.ToLower(strings.TrimSpace(action))]
	return ok
}

// Actions returns the sorted action list (for docs and tests).
func Actions() []string {
	out := make([]string, 0, len(actions))
	for action := range actions {
		out = append(out, action)
	}
	sort.Strings(out)
	return out
}
```

Add `"sort"` to imports.

`backend/pkg/browsercontract/authority.go` (verbatim logic from `backend/internal/service/browser/authority.go`, retyped to plain strings):

```go
package browsercontract

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

// Authority issues and verifies unguessable per-session browser capabilities.
// It is intentionally stateless so process replacement does not invalidate
// capabilities held by surviving clients.
type Authority struct{}

// NewAuthority returns a stateless browser capability authority.
func NewAuthority() *Authority { return &Authority{} }

// Issue mints a fresh capability for one session launch.
func (a *Authority) Issue(sessionID string) (token, verifier string, err error) {
	if a == nil || sessionID == "" {
		return "", "", fmt.Errorf("issue browser capability: session id is required")
	}
	raw := make([]byte, sha256.Size)
	if _, err := rand.Read(raw); err != nil {
		return "", "", fmt.Errorf("generate browser capability: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(raw)
	return token, capabilityVerifier(sessionID, token), nil
}

// Valid compares the presented token with the durable verifier in constant time.
func (a *Authority) Valid(sessionID, token, verifier string) bool {
	if a == nil || sessionID == "" || token == "" || verifier == "" {
		return false
	}
	expected := capabilityVerifier(sessionID, token)
	return hmac.Equal([]byte(expected), []byte(verifier))
}

func capabilityVerifier(sessionID, token string) string {
	h := sha256.New()
	_, _ = h.Write([]byte("ao-browser-capability-verifier-v1\x00"))
	_, _ = h.Write([]byte(sessionID))
	_, _ = h.Write([]byte{'\x00'})
	_, _ = h.Write([]byte(token))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}
```

- [x] **Step 4: Run package tests**

Run: `cd backend && go test ./pkg/browsercontract/`
Expected: PASS.

- [x] **Step 5: Refactor the internal service to delegate**

In `backend/internal/service/browser/service.go`: replace the local `actions` map and its check with `browsercontract.Supported(action)`; store `authority *browsercontract.Authority`; in `authorize`, call `s.authority.Valid(string(sessionID), strings.TrimSpace(capability), session.Metadata.BrowserCapabilityVerifier)`. Keep every exported signature unchanged (`domain.SessionID` stays at the boundary; convert with `string(...)`). Replace `backend/internal/service/browser/authority.go` with a two-line alias so the daemon wiring (`backend/internal/daemon/daemon.go` lines 191, 492, 562, 780 all reference `browsersvc.NewAuthority()`/pass it through) compiles untouched:

```go
package browser

import "github.com/aoagents/agent-orchestrator/backend/pkg/browsercontract"

// NewAuthority returns the shared stateless browser capability authority.
func NewAuthority() *browsercontract.Authority { return browsercontract.NewAuthority() }
```

Move the deleted file's test cases into `browsercontract/authority_test.go` if they are not already covered.

- [x] **Step 6: Run backend tests**

Run: `cd backend && go build ./... && go test ./internal/service/browser/... ./pkg/browsercontract/...`
Expected: PASS (existing browser service tests unchanged).

- [x] **Step 7: Commit**

```bash
git add backend/pkg/browsercontract backend/internal/service/browser
git commit -m "refactor: extract browser action contract and capability authority into pkg/browsercontract"
```

---

### Task 3: Extract the CLI verb tree into `backend/pkg/clibrowser`

**Files:**
- Create: `backend/pkg/clibrowser/transport.go`, `backend/pkg/clibrowser/command.go`, `backend/pkg/clibrowser/command_test.go`
- Modify: `backend/internal/cli/browser.go` (shrinks to a shim), `backend/internal/cli/browser_test.go` (moves what tests the verb tree)

**Interfaces:**
- Produces (used by Task 9 and the desktop CLI shim):
  - `type Transport interface { BrowserAction(ctx context.Context, action string, args map[string]any) (Response, error); BrowserStatus(ctx context.Context) (Status, error) }`
  - `type Status struct { SessionID string; Connected bool; ConnectedAt time.Time; Transport string }`
  - `type Response struct { RequestID, SessionID, Action string; Result map[string]any }`
  - `type CommandRequest struct { SessionID string; Action string; Args map[string]any }` (wire request body, json tags `sessionId`/`action`/`args`, used by both hosts' transports and the moved wire-shape tests)
  - `type Clock func() time.Time`
  - `func NewCommand(transport Transport, clock Clock, long string) *cobra.Command` — the full `browser` verb tree with its persistent `--json` flag (registered internally); `clock == nil` uses `time.Now`; `long == ""` uses the desktop Long text, non-empty overrides it (cloud passes its own wording).
  - `type UsageError error` marker? No — keep the existing `usageError` behavior inside the package by exporting `Errorf`-style helpers only if the moved code needs them; the moved code must use the exported `Usagef(format string, args ...any) error` returning an error the host CLI maps to exit 2. Concretely `clibrowser` defines `type UsageError struct{ error }` and hosts translate `errors.As`.

- [x] **Step 1: Create the transport interface and move the DTOs**

`backend/pkg/clibrowser/transport.go`:

```go
// Package clibrowser is the shared `ao browser` verb tree used by the desktop
// CLI and the cloud in-VM CLI. Hosts supply the transport; everything else
// (verbs, argument validation, output shaping, untrusted-content markers) is
// identical everywhere.
package clibrowser

import (
	"context"
	"time"
)

// Status is the transport-level browser runtime state.
type Status struct {
	SessionID   string    `json:"sessionId"`
	Connected   bool      `json:"connected"`
	ConnectedAt time.Time `json:"connectedAt,omitempty"`
	Transport   string    `json:"transport"`
}

// Response is one executed browser command's correlated result.
type Response struct {
	RequestID string         `json:"requestId"`
	SessionID string         `json:"sessionId"`
	Action    string         `json:"action"`
	Result    map[string]any `json:"result"`
}

// CommandRequest is the wire request body both hosts send. It exists as a
// typed struct (not an inline map) so the moved CLI wire-shape tests keep
// asserting the exact json tags against the daemon contract.
type CommandRequest struct {
	SessionID string         `json:"sessionId"`
	Action    string         `json:"action"`
	Args      map[string]any `json:"args,omitempty"`
}

// Transport carries browser commands to the session's browser service.
type Transport interface {
	BrowserAction(ctx context.Context, action string, args map[string]any) (Response, error)
	BrowserStatus(ctx context.Context) (Status, error)
}

// Clock supplies the timestamp used for default screenshot filenames.
type Clock func() time.Time
```

- [x] **Step 2: Move the verb tree**

Move from `backend/internal/cli/browser.go` into `backend/pkg/clibrowser/command.go`, with exactly these mechanical changes and no behavior changes:
1. Package clause `package clibrowser`; imports lose `commandContext`.
2. `newBrowserCommand(ctx *commandContext)` becomes `NewCommand(t Transport, clock Clock, long string) *cobra.Command`; the `--json` flag becomes a local `var jsonOutput bool` registered as a persistent flag (both hosts want the same flag); `long` non-empty replaces the desktop Long text.
3. Every `ctx.runBrowserAction(cmd, ...)` becomes `runBrowserAction(t, cmd, ...)`; `ctx.browserAction(...)` becomes `t.BrowserAction(...)`; `ctx.browserStatus(...)` becomes `t.BrowserStatus(...)`.
4. The DTO structs (`browserStatusDTO`, `browserCommandRequestDTO`, `browserCommandResponseDTO`, `browserScreenshotFileResult`) are deleted; the code uses `Status`/`Response` directly. `writeJSON` becomes a local helper writing `json.MarshalIndent` + newline to the writer (copy the internal helper's behavior; do not import internal packages).
5. `usageError{...}` constructions become `UsageError{err}` where `type UsageError struct{ Err error }` with `Error() string`; `exactArgs`/`rangeArgs` move along (they are browser.go-local). `noArgs` and `atMostOneArg` do NOT move: they live in `backend/internal/cli/root.go` (lines 321, 328) and are shared by the whole CLI; clibrowser defines its own private equivalents with the same cobra semantics.
6. `currentBrowserIdentity()` moves verbatim as exported `CurrentIdentity` (it reads `AO_SESSION_ID` and `AO_BROWSER_CAPABILITY` from the environment, which holds in both hosts).
7. All writer functions (`writeBrowserResult`, `browserUntrustedText`, `writeBrowserActResult`, `writeBrowserNetworkResult`, `writeBrowserScreenshot`, `numberInt`, `numberString`) move verbatim; `writeJSON` is NOT moved (it lives in `backend/internal/cli/output.go`, shared) — clibrowser gets a local copy with identical behavior; `ctx.deps.Now()` becomes `clock()` with a package default `var defaultClock Clock = time.Now` used when `clock == nil`.
8. Constants `browserUntrustedBegin/End`, `maxBrowserWaitMillis` move; the header constant now aliases `browsercontract.CapabilityHeader` (import `backend/pkg/browsercontract`).
9. The desktop shim builds requests from `clibrowser.CommandRequest` (identical json tags to the old `browserCommandRequestDTO`; `backend/internal/cli/browser_test.go` asserts wire shapes through these types around lines 20 and 497, and those tests move with the tree). Controllers and `backend/internal/httpd/controllers/dto.go` are untouched: the browser routes appear in the generated OpenAPI spec via specgen's `browserOperations()` (build.go line 648), and since no controller DTO changes, no `npm run api` regeneration is needed for this plan (if an executor touches dto.go anyway, the repo rule applies: regenerate and commit).

- [x] **Step 3: Replace the internal CLI file with a shim**

`backend/internal/cli/browser.go` reduces to:

```go
package cli

import (
	"github.com/spf13/cobra"

	"github.com/aoagents/agent-orchestrator/backend/internal/cli/deps"
	"github.com/aoagents/agent-orchestrator/backend/pkg/clibrowser"
)

func newBrowserCommand(ctx *commandContext) *cobra.Command {
	return clibrowser.NewCommand(
		daemonBrowserTransport{ctx: ctx},
		func() time.Time { return ctx.deps.Now() },
		"",
	)
}

// daemonBrowserTransport executes browser verbs against the loopback daemon
// exactly as the in-tree implementation did.
type daemonBrowserTransport struct {
	ctx *commandContext
}

func (t daemonBrowserTransport) BrowserAction(ctx context.Context, action string, args map[string]any) (clibrowser.Response, error) {
	sessionID, capability, err := clibrowser.CurrentIdentity()
	if err != nil {
		return clibrowser.Response{}, err
	}
	var out clibrowser.Response
	err = t.ctx.doJSONPathWithHeaders(
		ctx, http.MethodPost, "/api/v1/browser/commands",
		map[string]any{"sessionId": sessionID, "action": action, "args": args},
		&out,
		map[string]string{browserCapabilityHeader: capability},
	)
	return out, err
}

func (t daemonBrowserTransport) BrowserStatus(ctx context.Context) (clibrowser.Status, error) {
	sessionID, capability, err := clibrowser.CurrentIdentity()
	if err != nil {
		return clibrowser.Status{}, err
	}
	var out clibrowser.Status
	err = t.ctx.doJSONPathWithHeaders(
		ctx, http.MethodGet,
		"/api/v1/browser/status?sessionId="+url.QueryEscape(sessionID),
		nil, &out,
		map[string]string{browserCapabilityHeader: capability},
	)
	return out, err
}
```

Adjust imports (`context`, `net/http`, `net/url`, `time`; drop the unused deps import if `commandContext` already carries `deps`). Export `CurrentIdentity` from clibrowser (it is `currentBrowserIdentity` renamed). Keep the desktop `usageError` translation: `internal/cli` already maps package-local `usageError`; add `errors.As(err, &clibrowser.UsageError{})` to its exit-code mapping site (grep `usageError` in `backend/internal/cli` for the single mapping point) so clibrowser usage failures still exit 2.

- [x] **Step 4: Move and run the tests**

Move `backend/internal/cli/browser_test.go` cases that exercise the verb tree (argument validation, output writers, untrusted markers, screenshot file handling, act result rendering) to `backend/pkg/clibrowser/command_test.go`, hosting the tree against a fake `Transport` (recorded actions, canned responses). Cases that exercise daemon transport (error envelopes, request IDs) stay in `internal/cli` against `httptest` servers, now going through the shim. Keep both files' table-driven style.

Run: `cd backend && go build ./... && go test ./internal/cli/... ./pkg/clibrowser/...`
Expected: PASS with identical coverage breadth.

- [x] **Step 5: Commit**

```bash
git add backend/pkg/clibrowser backend/internal/cli
git commit -m "refactor: extract shared ao browser verb tree into pkg/clibrowser"
```

---

### Task 4: `cloud/internal/vmbrowser` — Chromium supervisor

**Files:**
- Create: `cloud/internal/vmbrowser/chromium.go`, `cloud/internal/vmbrowser/chromium_test.go`

**Interfaces:**
- Produces (used by Task 6 engine and Task 8 service):
  - `type Spawner interface { Start(ctx context.Context, spec ProcessSpec) (Process, error) }` with `type ProcessSpec struct { BinaryPath string; Args []string; Dir string }` and `type Process interface { Wait() error; Stop() error }`
  - `type Endpoint struct { WebSocketURL string }`
  - `type Status struct { Running bool; StartedAt time.Time; Restarts int; Parked bool }`
  - `type Chromium struct{...}`; `func NewChromium(opts ChromiumOptions) *Chromium`
  - `type ChromiumOptions struct { BinaryPath, UserDataDir string; Spawner Spawner; StartTimeout time.Duration; Logger *slog.Logger }` (StartTimeout default 15 s; Spawner nil means exec.Command-based production spawner)
  - `func (c *Chromium) EnsureRunning(ctx context.Context) (Endpoint, error)` — lazy start; reads `<UserDataDir>/DevToolsActivePort`; restarts with backoff on unexpected exit while wanted; parks after 5 failures in 10 minutes and returns a parked error until `Unpark()`; concurrent calls coalesce to one start.
  - `func (c *Chromium) CurrentStatus() Status`
  - `func (c *Chromium) Stop(ctx context.Context) error`

- [x] **Step 1: Write the failing tests**

`cloud/internal/vmbrowser/chromium_test.go`:

```go
package vmbrowser

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type fakeProcess struct {
	mu      sync.Mutex
	stopped bool
	exit    chan struct{}
}

func (f *fakeProcess) Wait() error { <-f.exit; return nil }
func (f *fakeProcess) Stop() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.stopped {
		return nil
	}
	f.stopped = true
	close(f.exit)
	return nil
}

type fakeSpawner struct {
	mu       sync.Mutex
	starts   int
	specs    []ProcessSpec
	profiles map[int]*fakeProcess
}

func (f *fakeSpawner) Start(_ context.Context, spec ProcessSpec) (Process, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts++
	f.specs = append(f.specs, spec)
	proc := &fakeProcess{exit: make(chan struct{})}
	f.profiles[f.starts] = proc
	// Simulate Chromium writing DevToolsActivePort into the user-data dir.
	portLine := "9333\n/devtools/browser/test-uuid\n"
	if err := os.WriteFile(filepath.Join(spec.Dir, "DevToolsActivePort"), []byte(portLine), 0o600); err != nil {
		return nil, err
	}
	go func() { <-proc.exit }()
	return proc, nil
}

func newTestChromium(t *testing.T, spawner *fakeSpawner) *Chromium {
	t.Helper()
	dir := t.TempDir()
	return NewChromium(ChromiumOptions{
		BinaryPath:  "/usr/bin/chromium",
		UserDataDir: filepath.Join(dir, "profile"),
		Spawner:     spawner,
		StartTimeout: 2 * time.Second,
	}),
	}
}
```

(Adjust the helper's trailing syntax; the intent is a constructor returning both.) Tests to include:

```go
func TestChromiumEnsureRunningLazyStartsAndDiscoversEndpoint(t *testing.T) {
	spawner := &fakeSpawner{profiles: map[int]*fakeProcess{}}
	c := newTestChromium(t, spawner)
	if c.CurrentStatus().Running {
		t.Fatal("chromium must not start until first use")
	}
	endpoint, err := c.EnsureRunning(context.Background())
	if err != nil {
		t.Fatalf("EnsureRunning: %v", err)
	}
	want := "ws://127.0.0.1:9333/devtools/browser/test-uuid"
	if endpoint.WebSocketURL != want {
		t.Fatalf("endpoint = %q, want %q", endpoint.WebSocketURL, want)
	}
	if got := spawner.starts; got != 1 {
		t.Fatalf("starts = %d, want 1", got)
	}
}

func TestChromiumEnsureRunningIsIdempotent(t *testing.T) {
	spawner := &fakeSpawner{profiles: map[int]*fakeProcess{}}
	c := newTestChromium(t, spawner)
	for i := 0; i < 3; i++ {
		if _, err := c.EnsureRunning(context.Background()); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if spawner.starts != 1 {
		t.Fatalf("starts = %d, want 1 (coalesced)", spawner.starts)
	}
}

func TestChromiumParksAfterRepeatedCrashes(t *testing.T) {
	// Spawner whose fake process dies immediately after start, repeatedly.
	crashing := &crashSpawner{}
	c := NewChromium(ChromiumOptions{
		BinaryPath: "/usr/bin/chromium", UserDataDir: filepath.Join(t.TempDir(), "profile"),
		Spawner: crashing, StartTimeout: 500 * time.Millisecond,
	})
	deadline := time.Now().Add(5 * time.Second)
	var parked bool
	for !parked && time.Now().Before(deadline) {
		_, err := c.EnsureRunning(context.Background())
		if err != nil && errors.Is(err, ErrParked) {
			parked = true
		}
	}
	if !parked {
		t.Fatal("chromium never parked after repeated crashes")
	}
	if !c.CurrentStatus().Parked {
		t.Error("status must report Parked")
	}
}

func TestChromiumStopIsSafeWhenNeverStarted(t *testing.T) {
	c := newTestChromium(t, &fakeSpawner{profiles: map[int]*fakeProcess{}})
	if err := c.Stop(context.Background()); err != nil {
		t.Fatalf("Stop on never-started chromium: %v", err)
	}
}
```

Implement `crashSpawner` in the test file: a spawner that starts a process whose `Wait()` returns `errors.New("crashed")` immediately and writes a stale DevToolsActivePort so readiness also fails.

- [x] **Step 2: Run tests to verify they fail**

Run: `cd cloud && go test ./internal/vmbrowser/`
Expected: FAIL, undefined symbols.

- [x] **Step 3: Implement the supervisor**

`cloud/internal/vmbrowser/chromium.go` skeleton with the exact behaviors (write the full file):

```go
// Package vmbrowser is the in-VM browser service ("browserd"): a loopback
// HTTP server speaking the desktop browser contract, backed by a supervised
// headless Chromium and the pinned agent-browser engine.
package vmbrowser

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// ErrParked reports that chromium exhausted its restart budget and requires
// an explicit unpark (the next browser open) to try again.
var ErrParked = errors.New("chromium is parked after repeated failures")

const (
	startBackoffFloor   = 500 * time.Millisecond
	startBackoffCeiling = 8 * time.Second
	parkFailureWindow   = 10 * time.Minute
	parkFailureLimit    = 5
)

type ProcessSpec struct {
	BinaryPath string
	Args       []string
	Dir        string
}

type Process interface {
	Wait() error
	Stop() error
}

type Spawner interface {
	Start(ctx context.Context, spec ProcessSpec) (Process, error)
}

type Endpoint struct {
	WebSocketURL string
}

type ChromiumOptions struct {
	BinaryPath   string
	UserDataDir  string
	Spawner      Spawner
	StartTimeout time.Duration
	Logger       *slog.Logger
}

type Chromium struct {
	opts   ChromiumOptions
	spawner Spawner

	mu        sync.Mutex
	running   bool
	startedAt time.Time
	restarts  int
	failures  []time.Time
	parked    bool
	starting  chan struct{} // non-nil while a start attempt is in flight
	proc      Process
}
```

Behaviors to implement (each guarded by `c.mu`):

1. `EnsureRunning`: if parked → `ErrParked`. If running and the endpoint file still exists → read and return it. Otherwise join/launch the single in-flight start (`c.starting` channel pattern), then: mkdir UserDataDir 0o700; spec args = the Global Constraints flag list; after spawn, poll for `<UserDataDir>/DevToolsActivePort` until StartTimeout (file format: first line port, second line browser path; endpoint = `ws://127.0.0.1:<port><path>`); on success set running/startedAt and spawn a watcher goroutine: `err := proc.Wait()`; on non-nil err while still wanted (not stopped by us), record a failure timestamp, trim failures to the window, and if `len(failures) >= parkFailureLimit` set parked; else sleep backoff (`min(ceiling, floor << len(failures))`) and leave `running=false` so the next call restarts.

Port-selection fallback (decide at the Task 1 spike and record it in the spike note): if the target Chromium build does not write `DevToolsActivePort` for `--remote-debugging-port=0`, pre-pick an ephemeral port instead (bind `net.Listen("tcp", "127.0.0.1:0")`, read the port, close, pass it via `--remote-debugging-port=<port>`) and keep the readiness probe as a TCP dial plus `/json/version` fetch. Same `Endpoint` shape either way. Flag note: bookworm's Chromium accepts both `--headless=new` and plain `--headless` (new mode became the default upstream); use `--headless=new` for explicitness, and if the image's Chromium version rejects it, dropping the `=new` suffix is the sanctioned fix — Task 10's in-image smoke decides.
2. `CurrentStatus`, `Stop` (idempotent, calls `proc.Stop()`, clears running), `Unpark()` (clears parked and failures).

Production spawner (default when `opts.Spawner == nil`):

```go
type execSpawner struct{}

func (execSpawner) Start(ctx context.Context, spec ProcessSpec) (Process, error) {
	cmd := exec.Command(spec.BinaryPath, spec.Args...)
	cmd.Dir = spec.Dir
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &execProcess{cmd: cmd}, nil
}

type execProcess struct {
	cmd *exec.Cmd
}

func (p *execProcess) Wait() error { return p.cmd.Wait() }

func (p *execProcess) Stop() error {
	if p.cmd.Process == nil {
		return nil
	}
	_ = p.cmd.Process.Signal(os.Interrupt)
	done := make(chan error, 1)
	go func() { done <- p.cmd.Wait() }()
	select {
	case <-done:
		return nil
	case <-time.After(5 * time.Second):
		_ = p.cmd.Process.Kill()
		<-done
		return nil
	}
}
```

- [x] **Step 4: Run tests**

Run: `cd cloud && go test ./internal/vmbrowser/ -run Chromium -v`
Expected: PASS.

- [x] **Step 5: Commit**

```bash
git add cloud/internal/vmbrowser/chromium.go cloud/internal/vmbrowser/chromium_test.go
git commit -m "feat(cloud): add in-VM chromium supervisor for the session browser"
```

---

### Task 5: `cloud/internal/vmbrowser` — argv translation (port of `nativeArgumentsForAction`)

**Files:**
- Create: `cloud/internal/vmbrowser/argv.go`, `cloud/internal/vmbrowser/argv_test.go`

**Interfaces:**
- Produces: `func NativeArguments(action string, args map[string]any) ([]string, error)` — exact port of `frontend/src/main/agent-browser-runtime.ts` `nativeArgumentsForAction` + `nativeWaitArguments`, returning `CommandError`-compatible errors via `invalidArgument(msg)`, `referenceRequired(msg)`, `tabIDRequired(msg)` helpers that construct `*CommandError` with codes `INVALID_ARGUMENT`, `REFERENCE_REQUIRED`, `TAB_ID_REQUIRED`, `URL_REQUIRED` (matching the Electron host's codes; see Task 7 for the `CommandError` type).

- [x] **Step 1: Write the failing table tests (mirror the TS suite plus the wait matrix)**

`cloud/internal/vmbrowser/argv_test.go`:

```go
package vmbrowser

import (
	"errors"
	"reflect"
	"testing"
)

func TestNativeArguments(t *testing.T) {
	tests := []struct {
		name    string
		action  string
		args    map[string]any
		want    []string
		wantErr string
		wantCode string
	}{
		{name: "open requires url", action: "open", args: nil, wantErr: "url is required", wantCode: "URL_REQUIRED"},
		{name: "open keeps explicit url", action: "open", args: map[string]any{"url": "https://example.com"}, want: []string{"open", "https://example.com"}},
		{name: "open defaults scheme for localhost dev servers", action: "open", args: map[string]any{"url": "localhost:3000"}, want: []string{"open", "http://localhost:3000"}},
		{name: "open defaults scheme for bare hosts", action: "open", args: map[string]any{"url": "example.com"}, want: []string{"open", "http://example.com"}},
		{name: "open rejects bare words", action: "open", args: map[string]any{"url": "hi"}, wantErr: "requires an explicit http(s) URL or hostname", wantCode: "INVALID_URL"},
		{name: "open rejects posix paths", action: "open", args: map[string]any{"url": "/etc/passwd"}, wantErr: "cannot open local files", wantCode: "BROWSER_URL_FORBIDDEN"},
		{name: "open rejects file scheme", action: "open", args: map[string]any{"url": "file:///tmp/x"}, wantErr: "cannot open local files", wantCode: "BROWSER_URL_FORBIDDEN"},
		{name: "snapshot default", action: "snapshot", want: []string{"snapshot", "--compact"}},
		{name: "snapshot interactive", action: "snapshot", args: map[string]any{"interactive": true}, want: []string{"snapshot", "--interactive", "--compact"}},
		{name: "click strips and adds at", action: "click", args: map[string]any{"ref": "e2"}, want: []string{"click", "@e2"}},
		{name: "click accepts prefxixed ref", action: "click", args: map[string]any{"ref": "@e2"}, want: []string{"click", "@e2"}},
		{name: "click keeps non-element refs verbatim", action: "click", args: map[string]any{"ref": "custom-ref"}, want: []string{"click", "custom-ref"}},
		{name: "click requires ref", action: "click", wantErr: "ref is required", wantCode: "REFERENCE_REQUIRED"},
		{name: "fill", action: "fill", args: map[string]any{"ref": "e3", "text": "hi"}, want: []string{"fill", "@e3", "hi"}},
		{name: "fill allows empty text", action: "fill", args: map[string]any{"ref": "e3", "text": ""}, want: []string{"fill", "@e3", ""}},
		{name: "press", action: "press", args: map[string]any{"key": "Enter"}, want: []string{"press", "Enter"}},
		{name: "drag", action: "drag", args: map[string]any{"ref": "e1", "targetRef": "e2"}, want: []string{"drag", "@e1", "@e2"}},
		{name: "tabs", action: "tabs", want: []string{"tab", "list"}},
		{name: "tab new with url", action: "tab-new", args: map[string]any{"url": "https://example.com"}, want: []string{"tab", "new", "https://example.com"}},
		{name: "tab new bare", action: "tab-new", want: []string{"tab", "new"}},
		{name: "tab select", action: "tab-select", args: map[string]any{"tabId": "t-1"}, want: []string{"tab", "t-1"}},
		{name: "tab select requires id", action: "tab-select", wantErr: "tabId is required", wantCode: "TAB_ID_REQUIRED"},
		{name: "tab close explicit", action: "tab-close", args: map[string]any{"tabId": "t-1"}, want: []string{"tab", "close", "t-1"}},
		{name: "tab close default", action: "tab-close", want: []string{"tab", "close"}},
		{name: "scroll validates direction", action: "scroll", args: map[string]any{"direction": "sideways"}, wantErr: "direction must be up, down, left, or right"},
		{name: "scroll", action: "scroll", args: map[string]any{"direction": "Down", "amount": 900}, want: []string{"scroll", "down", "900"}},
		{name: "scroll clamps default", action: "scroll", args: map[string]any{"direction": "up"}, want: []string{"scroll", "up", "600"}},
		{name: "get url no ref", action: "get", args: map[string]any{"property": "url"}, want: []string{"get", "url"}},
		{name: "get text with ref", action: "get", args: map[string]any{"property": "Text", "ref": "e4"}, want: []string{"get", "text", "@e4"}},
		{name: "get rejects unknown property", action: "get", args: map[string]any{"property": "html"}, wantErr: "Unsupported browser property"},
		{name: "get url rejects ref", action: "get", args: map[string]any{"property": "url", "ref": "e4"}, wantErr: "does not accept an element ref"},
		{name: "get value requires ref", action: "get", args: map[string]any{"property": "value"}, wantErr: "requires an element ref", wantCode: "REFERENCE_REQUIRED"},
		{name: "wait text", action: "wait", args: map[string]any{"text": "Ready", "timeoutMs": 5000}, want: []string{"wait", "--text", "Ready", "--timeout", "5000"}},
		{name: "wait text gone", action: "wait", args: map[string]any{"textGone": "Loading", "timeoutMs": 1000}, want: []string{"wait", "text=Loading", "--state", "hidden", "--timeout", "1000"}},
		{name: "wait selector", action: "wait", args: map[string]any{"selector": ".done", "timeoutMs": 1000}, want: []string{"wait", ".done", "--timeout", "1000"}},
		{name: "wait selector gone", action: "wait", args: map[string]any{"selectorGone": ".done", "timeoutMs": 1000}, want: []string{"wait", ".done", "--state", "detached", "--timeout", "1000"}},
		{name: "wait url", action: "wait", args: map[string]any{"url": "/dashboard", "timeoutMs": 1000}, want: []string{"wait", "--url", "**/dashboard**", "--timeout", "1000"}},
		{name: "wait load", action: "wait", args: map[string]any{"load": true, "timeoutMs": 1000}, want: []string{"wait", "--load", "load", "--timeout", "1000"}},
		{name: "wait fixed ms", action: "wait", args: map[string]any{"ms": 250}, want: []string{"wait", "250"}},
		{name: "wait requires condition", action: "wait", args: map[string]any{"timeoutMs": 1000}, wantErr: "A wait condition is required"},
		{name: "frame main", action: "frame", args: map[string]any{"target": "main"}, want: []string{"frame", "main"}},
		{name: "frame ref", action: "frame", args: map[string]any{"target": "e5"}, want: []string{"frame", "@e5"}},
		{name: "dialog accept with text", action: "dialog", args: map[string]any{"operation": "accept", "text": "ok"}, want: []string{"dialog", "accept", "ok"}},
		{name: "dialog status", action: "dialog", args: map[string]any{"operation": "status"}, want: []string{"dialog", "status"}},
		{name: "dialog validates operation", action: "dialog", args: map[string]any{"operation": "explode"}, wantErr: "dialog operation must be accept, dismiss, or status"},
		{name: "console bare", action: "console", want: []string{"console"}},
		{name: "errors bare", action: "errors", want: []string{"errors"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NativeArguments(tt.action, tt.args)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("want error %q, got %v", tt.wantErr, got)
				}
				var ce *CommandError
				if !errors.As(err, &ce) {
					t.Fatalf("want CommandError, got %#v", err)
				}
				wantCode := tt.wantCode
				if wantCode == "" {
					wantCode = "INVALID_ARGUMENT"
				}
				if ce.Code != wantCode {
					t.Fatalf("code = %q, want %q", ce.Code, wantCode)
				}
				return
			}
			if err != nil {
				t.Fatalf("NativeArguments: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}
```

Add a dedicated test asserting the stableMs branch produces `["wait", "--fn", <expression>, "--timeout", <timeout>]` with the expression containing `__aoDomStability` and the stableMs value interpolated.

- [x] **Step 2: Run tests to verify they fail**

Run: `cd cloud && go test ./internal/vmbrowser/ -run NativeArguments`
Expected: FAIL, undefined.

- [x] **Step 3: Implement the port**

`cloud/internal/vmbrowser/argv.go`: port each branch of `nativeArgumentsForAction` and `nativeWaitArguments` line by line from the TypeScript (source of truth: `frontend/src/main/agent-browser-runtime.ts` lines 623-725), including: `nativeRef` regex `^@?e\d+$` (case-insensitive), `stringValue` trim semantics and `allowEmpty`, `optionalStringValue`, `numberValue` (finite, min/max, round; error text `Numeric argument must be between %d and %d`). The `open` (and `tab-new`) URL branches port `normalizeAgentBrowserURL` (`browser-view-host.ts` lines 3157-3171) with its helpers `withDefaultScheme` (line 2402), `looksLikeHost` (line 2420), `isLocalhostLike` (line 2452), and `normalizeBrowserURL` (line 495), producing the desktop error codes `URL_REQUIRED`, `INVALID_URL`, `BROWSER_URL_FORBIDDEN`. The stableMs branch interpolates the exact MutationObserver expression from the TS (copy it verbatim; it is a JS string passed through).

- [x] **Step 4: Run tests**

Run: `cd cloud && go test ./internal/vmbrowser/ -run NativeArguments -v`
Expected: PASS.

- [x] **Step 5: Commit**

```bash
git add cloud/internal/vmbrowser/argv.go cloud/internal/vmbrowser/argv_test.go
git commit -m "feat(cloud): port agent-browser argv translation for the in-VM browser"
```

---

### Task 6: `cloud/internal/vmbrowser` — engine (binary runner, envelope parse, screenshot)

**Files:**
- Create: `cloud/internal/vmbrowser/engine.go`, `cloud/internal/vmbrowser/engine_test.go`

**Interfaces:**
- Consumes: `Chromium` from Task 4, `NativeArguments` from Task 5.
- Produces (used by Task 7 service):
  - `type CommandError struct { Code, Message string }` with `func (e *CommandError) Error() string` rendering `"message (code)"`; constructors `invalidArgument(msg)`, `referenceRequired(msg)`, `tabIDRequired(msg)`.
  - `type Runner interface { Run(ctx context.Context, env []string, args ...string) (stdout, stderr string, exitCode int, err error) }`
  - `type EngineOptions struct { BinaryPath string; Root string; SessionID string; CommandTimeout time.Duration; Logger *slog.Logger }` (Root = `<dataDir>/browser/<sessionID>`; CommandTimeout default 60 s)
  - `type Engine struct{...}`; `func NewEngine(chromium *Chromium, runner Runner, opts EngineOptions) *Engine`
  - `func (e *Engine) Execute(ctx context.Context, action string, args map[string]any) (map[string]any, error)` — composite-free verbs; builds argv via `NativeArguments`, appends `--json`, runs `stream disable` first, parses the envelope.
  - `func (e *Engine) Screenshot(ctx context.Context) (data string, width, height int, err error)` — base64 PNG.
  - `func (e *Engine) Close(ctx context.Context) error`

- [x] **Step 1: Write the failing tests**

`cloud/internal/vmbrowser/engine_test.go` (fake runner records invocations):

```go
package vmbrowser

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type recordedCall struct {
	env  []string
	args []string
}

type fakeRunner struct {
	calls    []recordedCall
	response func(args []string) (string, string, int)
}

func (f *fakeRunner) Run(_ context.Context, env []string, args ...string) (string, string, int, error) {
	f.calls = append(f.calls, recordedCall{env: env, args: args})
	if f.response == nil {
		return `{"success":true,"data":{"url":"https://example.com"}}`, "", 0, nil
	}
	return f.response(args)
}

func envValue(env []string, key string) string {
	for _, pair := range env {
		if strings.HasPrefix(pair, key+"=") {
			return strings.TrimPrefix(pair, key+"=")
		}
	}
	return ""
}

func TestEngineExecuteRunsStreamDisableThenCommandWithJSON(t *testing.T) {
	runner := &fakeRunner{}
	engine := newTestEngine(t, runner, nil)
	if _, err := engine.Execute(context.Background(), "open", map[string]any{"url": "https://example.com"}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(runner.calls) != 2 {
		t.Fatalf("calls = %d, want 2 (stream disable + command)", len(runner.calls))
	}
	if want := []string{"stream", "disable"}; !equalStrings(runner.calls[0].args, want) {
		t.Fatalf("first call = %v, want %v", runner.calls[0].args, want)
	}
	if last := runner.calls[1].args[len(runner.calls[1].args)-1]; last != "--json" {
		t.Fatalf("command must append --json, got %v", runner.calls[1].args)
	}
}

func TestEngineExecuteTreatsAlreadyDisabledAsSuccess(t *testing.T) {
	runner := &fakeRunner{response: func(args []string) (string, string, int, error) {
		if args[0] == "stream" {
			// Exact marker from STREAM_ALREADY_DISABLED_MESSAGE.
			return "", "Streaming is not enabled for this session", 1, nil
		}
		return `{"success":true,"data":{}}`, "", 0, nil
	}}
	engine := newTestEngine(t, runner, nil)
	if _, err := engine.Execute(context.Background(), "console", nil); err != nil {
		t.Fatalf("Execute with already-disabled stream: %v", err)
	}
}

func TestEngineExecuteMapsFailureEnvelopeToCommandError(t *testing.T) {
	runner := &fakeRunner{response: func([]string) (string, string, int, error) {
		return `{"success":false,"error":{"code":"STALE_REFERENCE","message":"Unknown ref: @e9"}}`, "", 0, nil
	}}
	engine := newTestEngine(t, runner, nil)
	_, err := engine.Execute(context.Background(), "click", map[string]any{"ref": "e9"})
	if err == nil || !strings.Contains(err.Error(), "STALE_REFERENCE") {
		t.Fatalf("want STALE_REFERENCE CommandError, got %v", err)
	}
}

func TestEngineExecuteEnvContract(t *testing.T) {
	runner := &fakeRunner{}
	engine := newTestEngine(t, runner, nil)
	if _, err := engine.Execute(context.Background(), "console", nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	env := runner.calls[len(runner.calls)-1].env
	for key, want := range map[string]string{
		"AGENT_BROWSER_CDP":                "ws://127.0.0.1:9333/devtools/browser/test-uuid",
		"AGENT_BROWSER_SESSION":            "sess-1",
		"AGENT_BROWSER_NAMESPACE":          "sess-1",
		"AGENT_BROWSER_SOCKET_DIR":         filepath.Join(engineRoot, "s"),
		"AGENT_BROWSER_CONTENT_BOUNDARIES": "1",
		"AGENT_BROWSER_MAX_OUTPUT":         "50000",
		"AGENT_BROWSER_IDLE_TIMEOUT_MS":    "300000",
		"AGENT_BROWSER_AUTO_CONNECT":       "0",
	} {
		if got := envValue(env, key); got != want {
			t.Errorf("env %s = %q, want %q", key, got, want)
		}
	}
	if got := envValue(env, "AGENT_BROWSER_CONFIG"); !strings.HasSuffix(got, "config.json") {
		t.Errorf("AGENT_BROWSER_CONFIG = %q, want config.json path", got)
	}
	for _, secret := range []string{"AO_CLOUD_WORKER_TOKEN", "AO_WORKER_BOOTSTRAP_TOKEN"} {
		if envValue(env, secret) != "" || hasEnvPrefix(env, secret) {
			t.Errorf("engine env must not carry %s", secret)
		}
	}
}

func TestEngineScreenshotReturnsBase64WithDimensions(t *testing.T) {
	runner := &fakeRunner{response: func(args []string) (string, string, int, error) {
		if args[0] != "screenshot" {
			return `{"success":true,"data":{}}`, "", 0, nil
		}
		target := args[1]
		png := buildTestPNG(t, 32, 18)
		if err := os.WriteFile(target, png, 0o600); err != nil {
			return "", "", 0, err
		}
		return `{"success":true,"data":{}}`, "", 0, nil
	}}
	engine := newTestEngine(t, runner, nil)
	data, width, height, err := engine.Screenshot(context.Background())
	if err != nil {
		t.Fatalf("Screenshot: %v", err)
	}
	if width != 32 || height != 18 {
		t.Fatalf("dimensions = %dx%d, want 32x18", width, height)
	}
	if len(data) == 0 || !strings.HasSuffix(engine.screenshotDirUsed, "") {
		t.Fatal("screenshot data must be non-empty base64")
	}
}
```

Helper `newTestEngine` builds a `Chromium` on the fake spawner from Task 4 (endpoint `ws://127.0.0.1:9333/devtools/browser/test-uuid`), a temp `Root`, session `sess-1`. `buildTestPNG` writes a minimal 8-byte-signature + IHDR PNG (construct bytes directly; do not link image libs). Implement `equalStrings`, `hasEnvPrefix` helpers in the test file. If the fake-chromium path makes engine construction awkward, add a small `withEndpoint` test seam on Engine instead of contorting the spawner.

- [x] **Step 2: Run tests to verify they fail**

Run: `cd cloud && go test ./internal/vmbrowser/ -run Engine`
Expected: FAIL, undefined.

- [x] **Step 3: Implement the engine**

`cloud/internal/vmbrowser/engine.go` key behaviors (write the full file):

1. Env construction (port of `environment()`): start from the exact `NATIVE_ENV_ALLOWLIST` (see Global Constraints; lowercase name comparison) — never `os.Environ()` wholesale, mirroring the Electron comment that the binary is third-party. Set `HOME`, `USERPROFILE`, `XDG_CONFIG_HOME`, `XDG_CACHE_HOME`, `TEMP`, `TMP`, `TMPDIR` to `<Root>/run`, `AGENT_BROWSER_SOCKET_DIR` to `<Root>/s` (mkdir 0o700), plus the eight `AGENT_BROWSER_*` vars from the test table, with `AGENT_BROWSER_CDP` from `chromium.EnsureRunning(ctx)` (called lazily at Execute/Screenshot time so the browser starts on first verb).
2. Per-command sequence: ensure chromium; mkdir run dir; write `config.json` (`{}\n`) once; run `stream disable` (tolerate the already-disabled marker per the Global Constraints); run the translated argv + `--json`; on non-zero exit return `*CommandError{Code: "AGENT_BROWSER_COMMAND_FAILED", Message: stderr-or-stdout}`.
3. Envelope parse (port `parseAgentBrowserJSON`): invalid JSON → `AGENT_BROWSER_INVALID_OUTPUT`; `success == false` → stale-reference detection (code `STALE_REFERENCE` when `error.code == "STALE_REFERENCE"` or message matches `^(Unknown ref:|Could not locate element with)`), else `AGENT_BROWSER_COMMAND_FAILED` with the string error; success → copy `data` fields (or `{"value": data}` for non-object data), drop `_boundary`, re-add it only when it is an object with string `nonce` and `origin`; always set `untrustedExternalContent: true` on the returned map.
4. `Screenshot`: mkdtemp under Root; argv `["screenshot", target, "--json"]`; read file; reject > `5<<20` bytes (`AGENT_BROWSER_OUTPUT_TOO_LARGE`); parse PNG header (`PNG` signature at bytes 1-4, width = big-endian uint32 at offset 16, height at 20; invalid → `AGENT_BROWSER_INVALID_OUTPUT`); return base64; always remove the temp dir.
5. Output cap: accumulate runner output up to 1 MiB; kill and fail with `AGENT_BROWSER_OUTPUT_TOO_LARGE` beyond (the production runner enforces this; the interface keeps the contract in one place).
6. Production runner (`execRunner`): `exec.CommandContext` with the timeout, `Stdout`/`Stderr` into capped buffers, stdin `nil`.
7. `Close(ctx)`: best-effort cleanup — run `close` once through the runner (ignore errors, mirroring the Electron close-timeout tolerance), then `chromium.Stop(ctx)`; safe to call when nothing ever started. A command that races an idle-shutdown or Close may fail once with `AGENT_BROWSER_COMMAND_FAILED`; the next command lazily restarts Chromium, which is the documented recovery path.

- [x] **Step 4: Run tests**

Run: `cd cloud && go test ./internal/vmbrowser/ -run Engine -v`
Expected: PASS.

- [x] **Step 5: Commit**

```bash
git add cloud/internal/vmbrowser/engine.go cloud/internal/vmbrowser/engine_test.go
git commit -m "feat(cloud): add agent-browser engine for the in-VM browser service"
```

---

### Task 7: `cloud/internal/vmbrowser` — act matcher (port of `browser-act-matcher.ts`)

**Files:**
- Create: `cloud/internal/vmbrowser/actmatcher.go`, `cloud/internal/vmbrowser/actmatcher_test.go`

**Interfaces:**
- Produces: `type MatchOutcome struct { Outcome string; Candidate MatchedElement; Candidates []MatchedElement }` with outcomes `matched`, `ambiguous`, `no-match`; `type MatchedElement struct { Ref, Role, Name string }`; `func MatchInstruction(instruction string, refs any, opts MatchOptions) MatchOutcome` with `type MatchOptions struct { Nth int }`.

- [x] **Step 1: Port the tests**

Read `frontend/src/main/browser-act-matcher.test.ts` (201 lines) and port each table case into `cloud/internal/vmbrowser/actmatcher_test.go`, converting the TS fixture `refs` payloads (the `refs` value from snapshot JSON) into the same `map[string]any`/`[]any` structures the Go engine will see after JSON-decoding agent-browser output. Keep case names and expectations identical. Example shape:

```go
func TestMatchInstruction(t *testing.T) {
	tests := []struct {
		name         string
		instruction  string
		refs         any
		nth          int
		wantOutcome  string
		wantRef      string
		wantCandsLen int
	}{
		{name: "exact role and name", instruction: "the Login button", refs: snapshotRefs(
			matchedRef{Ref: "e1", Role: "button", Name: "Login"},
		), wantOutcome: "matched", wantRef: "e1"},
		// ... port the remaining cases verbatim from browser-act-matcher.test.ts
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MatchInstruction(tt.instruction, tt.refs, MatchOptions{Nth: tt.nth})
			if got.Outcome != tt.wantOutcome {
				t.Fatalf("outcome = %q, want %q", got.Outcome, tt.wantOutcome)
			}
			if tt.wantRef != "" && got.Candidate.Ref != tt.wantRef {
				t.Fatalf("ref = %q, want %q", got.Candidate.Ref, tt.wantRef)
			}
			if tt.wantCandsLen != 0 && len(got.Candidates) != tt.wantCandsLen {
				t.Fatalf("candidates = %d, want %d", len(got.Candidates), tt.wantCandsLen)
			}
		})
	}
}
```

- [x] **Step 2: Run tests to verify they fail**

Run: `cd cloud && go test ./internal/vmbrowser/ -run MatchInstruction`
Expected: FAIL, undefined.

- [x] **Step 3: Port the implementation**

Port `frontend/src/main/browser-act-matcher.ts` (127 lines) function by function into `actmatcher.go`: tokenization, stopword handling, role synonyms, name/text scoring, tie detection and `nth` disambiguation. The Go version operates on `any` (decoded JSON): normalize `refs` accepting both the shape agent-browser emits and defensively nothing else; return `no-match` on unrecognized shapes rather than panicking.

- [x] **Step 4: Run tests**

Run: `cd cloud && go test ./internal/vmbrowser/ -run MatchInstruction -v`
Expected: PASS, case-for-case parity with the TS suite.

- [x] **Step 5: Commit**

```bash
git add cloud/internal/vmbrowser/actmatcher.go cloud/internal/vmbrowser/actmatcher_test.go
git commit -m "feat(cloud): port deterministic act instruction matcher for the in-VM browser"
```

---

### Task 8: `cloud/internal/vmbrowser` — loopback service (dispatch + HTTP contract)

**Files:**
- Create: `cloud/internal/vmbrowser/service.go`, `cloud/internal/vmbrowser/sanitize.go`, `cloud/internal/vmbrowser/service_test.go`, `cloud/internal/vmbrowser/sanitize_test.go`

**Interfaces:**
- Consumes: `Engine` (Task 6), `MatchInstruction` (Task 7), `browsercontract` (Task 2).
- Produces (used by Task 9 wiring):
  - `type ServiceOptions struct { SessionID string; CapabilityVerifier string; Engine *Engine; Logger *slog.Logger }`
  - `func NewService(opts ServiceOptions) *Service`
  - `func (s *Service) Handler() http.Handler` — routes `POST /api/v1/browser/commands`, `GET /api/v1/browser/status`
  - `func (s *Service) Status(ctx context.Context) (Connected bool, StartedAt time.Time)`

- [x] **Step 1: Write the failing tests**

`cloud/internal/vmbrowser/service_test.go` (httptest against `Handler()`; engine backed by the fake runner from Task 6):

```go
func TestServiceCommandHappyPathMirrorsDesktopContract(t *testing.T) {
	service, runner := newTestService(t)
	status, body := post(t, service, "/api/v1/browser/commands", validCapability(t, service),
		`{"sessionId":"sess-1","action":"open","args":{"url":"https://example.com"}}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d, body = %s", status, body)
	}
	var decoded struct {
		RequestID string         `json:"requestId"`
		SessionID string         `json:"sessionId"`
		Action    string         `json:"action"`
		Result    map[string]any `json:"result"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	if decoded.RequestID == "" || decoded.SessionID != "sess-1" || decoded.Action != "open" {
		t.Fatalf("contract mismatch: %+v", decoded)
	}
	if _, ok := decoded.Result["untrustedExternalContent"]; !ok {
		t.Error("result must be marked untrustedExternalContent")
	}
	_ = runner
}

func TestServiceRejectsWrongCapabilityWithForbiddenEnvelope(t *testing.T) {
	service, _ := newTestService(t)
	status, body := post(t, service, "/api/v1/browser/commands", "wrong-token",
		`{"sessionId":"sess-1","action":"open","args":{"url":"https://x.example"}}`)
	if status != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", status)
	}
	assertEnvelope(t, body, "BROWSER_CAPABILITY_INVALID")
}

func TestServiceRejectsOtherSessions(t *testing.T) {
	service, _ := newTestService(t)
	status, body := post(t, service, "/api/v1/browser/commands", validCapability(t, service),
		`{"sessionId":"sess-2","action":"open","args":{"url":"https://x.example"}}`)
	if status != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (capability is session-bound)", status)
	}
	assertEnvelope(t, body, "BROWSER_CAPABILITY_INVALID")
}

func TestServiceRejectsUnsupportedAction(t *testing.T) {
	service, _ := newTestService(t)
	status, body := post(t, service, "/api/v1/browser/commands", validCapability(t, service),
		`{"sessionId":"sess-1","action":"navigate"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", status)
	}
	assertEnvelope(t, body, "BROWSER_ACTION_UNSUPPORTED")
}

func TestServiceCloudUnsupportedVerbs(t *testing.T) {
	for _, action := range []string{"network-start", "network-status", "network-list", "network-stop",
		"network-clear", "devtools-open", "devtools-close", "unhighlight"} {
		t.Run(action, func(t *testing.T) {
			service, _ := newTestService(t)
			status, body := post(t, service, "/api/v1/browser/commands", validCapability(t, service),
				fmt.Sprintf(`{"sessionId":"sess-1","action":%q}`, action))
			if status != http.StatusBadRequest {
				t.Fatalf("%s: status = %d, want 400", action, status)
			}
			assertEnvelope(t, body, "BROWSER_ACTION_UNSUPPORTED_CLOUD")
		})
	}
}

func TestServiceActComposite(t *testing.T) {
	// snapshot returns refs with one matching element; act clicks it.
	runner := &fakeRunner{response: func(args []string) (string, string, int, error) {
		switch args[0] {
		case "snapshot":
			return `{"success":true,"data":{"snapshot":"- button \"Login\" [ref=e1]","refs":[` +
				`{"ref":"e1","role":"button","name":"Login"}]}}`, "", 0, nil
		case "click":
			return `{"success":true,"data":{"clicked":"e1"}}`, "", 0, nil
		}
		return `{"success":true,"data":{}}`, "", 0, nil
	}}
	service, _ := newTestServiceWithRunner(t, runner)
	status, body := post(t, service, "/api/v1/browser/commands", validCapability(t, service),
		`{"sessionId":"sess-1","action":"act","args":{"instruction":"the Login button"}}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d, body = %s", status, body)
	}
	assert result.outcome == "matched", result.resolvedRef == "e1", result.candidate.role == "button".
}

func TestServiceActRetriesOnceOnStaleReference(t *testing.T) {
	// First click fails with STALE_REFERENCE; second snapshot returns a new ref;
	// the retry must succeed and set retried=true.
}

func TestServiceStatusRoute(t *testing.T) {
	service, _ := newTestService(t)
	status, body := get(t, service, "/api/v1/browser/status?sessionId=sess-1", validCapability(t, service))
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	assert body has sessionId, connected (bool), transport == "vm-chromium".
}
```

Write `post`/`get`/`assertEnvelope`/`validCapability`/`newTestService` helpers in the test file (capability minted via `browsercontract.NewAuthority().Issue("sess-1")`). `assertEnvelope` checks top-level `code` and non-empty `message` and `requestId`.

- [x] **Step 2: Run tests to verify they fail**

Run: `cd cloud && go test ./internal/vmbrowser/ -run Service`
Expected: FAIL, undefined.

- [x] **Step 3: Implement the service**

`cloud/internal/vmbrowser/service.go`:

1. Error writer mirrors the desktop controller mapping (`writeBrowserError` in `backend/internal/httpd/controllers/browser.go`): `*CommandError` codes `INVALID_ARGUMENT`, `URL_REQUIRED`, `REFERENCE_REQUIRED`, `TAB_ID_REQUIRED`, `INVALID_URL`, `BROWSER_URL_FORBIDDEN`, `AGENT_BROWSER_COMMAND_BLOCKED`, `BROWSER_ACTION_UNSUPPORTED_CLOUD` → 400 `bad_request`; `STALE_REFERENCE`, `TAB_NOT_FOUND` → 409 `conflict`; `BROWSER_TARGET_UNAVAILABLE`, `BROWSER_AUTOMATION_UNAVAILABLE`, `AGENT_BROWSER_NOT_INSTALLED`, `AGENT_BROWSER_START_FAILED`, `AGENT_BROWSER_OUTPUT_TOO_LARGE` → 503 `unavailable`; everything else 422. Envelope: `{"message": ..., "code": ..., "requestId": ...}` (generate a uuid request id per request).
2. Authorization: capability header → `browsercontract` authority `Valid(sessionID, token, verifier)`; wrong session fails because the verifier is session-bound; also reject mismatched `in.SessionID != opts.SessionID` outright with the same 403 code. Decode requests with `io.LimitReader(r.Body, 1<<20)` (the daemon's browser controller uses a plain decode on the loopback-only route; the cap here mirrors the 1 MiB command cap the Electron side enforces and bounds `fill`/`type` text args).
3. Create `cloud/internal/vmbrowser/sanitize.go` first: Go ports of `sanitizeBrowserURL`, `sanitizeBrowserTitle`, `sanitizeURLsInText`, `externalText` (1 MiB cap + `[Content truncated at N bytes]` suffix), and `markUntrusted` (wrap in the same marker strings the CLI uses; escape exact marker collisions inside the value). Unit tests port the behaviors: `https://u:p@x.example/a?token=1#frag` → `https://x.example/a?token=[redacted]`; `about:blank` unchanged; non-http protocol → `scheme[redacted]`; titles that look like URLs get URL-sanitized. Apply sanitizers at exactly the points the Electron host does (below).
4. Dispatch switch mirroring the Electron host (`browser-view-host.ts` lines 2119-2317):
   - `open`: engine execute; result is a nav-state object `{url, title, canGoBack, canGoForward, isLoading}` with `url`/`title` sanitized (mirror `agentNavState`, line 2540; omit the Electron-only `viewId`). Where engine data lacks nav fields, synthesize from `get url`/`get title` engine calls. CLI only reads `url`.
   - `snapshot`: engine execute; validate `result["snapshot"]` is a string else `BROWSER_AUTOMATION_INVALID_OUTPUT`; return `{text, refs, [_boundary], untrustedExternalContent}`.
   - `act`: port the composite flow (snapshot interactive → `MatchInstruction` → verb via engine (`ACT_VERBS` = click, dblclick, focus, hover, fill, type, check, uncheck; exact set at `browser-view-host.ts:2502`) with `value` required for fill/type → on `STALE_REFERENCE` re-snapshot, re-match original instruction, retry once; outcomes matched/ambiguous/no-match shaped exactly like the Electron host: `outcome`, `instruction`, `candidates?`, `resolvedRef`, `candidate`, `result`, `retried`, `snapshot`, `untrustedExternalContent`).
   - passthrough verbs (`click`, `dblclick`, `focus`, `hover`, `highlight`, `scrollintoview`, `check`, `uncheck`, `fill`, `type`, `press`, `drag`, `scroll`, `select`, `wait`, `frame`, `dialog`): engine execute, return data.
   - `tabs`: engine `tab list` → `{activeTabId, tabs: [{id, url, title, active, favicon?, untrustedExternalContent}], untrustedExternalContent}` with per-tab `url`/`title` sanitized (mirror `agentTabsResult`, line 2531; omit Electron-only `viewId` and `change` — document both omissions in the PR body).
   - `tab-new`/`tab-select`: engine execute, then one follow-up `tab list` to find the active tab; return the single sanitized tab object (mirror `agentTabResult`, line 2522 — a bare tab object, NOT wrapped in `{tab: ...}`).
   - `tab-close`: engine execute; return `{closedTabId, activeTabId, tabs: [...], untrustedExternalContent}` (mirror line 2247).
   - `get`: engine execute; pick `value ?? data[property]`; sanitize `url` and `title` results (mirror lines 2259-2277).
   - `console`/`errors`: engine execute, then port `normalizeNativeMessages` (line 3196) exactly: accept `messages` or `value` arrays; string items become `{level: errors?"error":"log", message: markUntrusted(externalText(sanitizeURLsInText(item))), timestamp: now}`; object items map `level` → `type` → action-based fallback; every message capped, marked, and timestamped. `errors` skips the Electron-only `browserSignalMessages` merge (session signals come from Electron's own view host; cloud has none in Stage 1).
   - `screenshot`: `engine.Screenshot` → `{data, width, height, untrustedExternalContent}`.
   - cloud-unsupported set → `BROWSER_ACTION_UNSUPPORTED_CLOUD` ("This browser action is not available in cloud sessions yet").
5. `GET /api/v1/browser/status`: `{sessionId, connected: <engine/chromium running>, connectedAt, transport: "vm-chromium"}`; missing `sessionId` query → 400 `SESSION_ID_REQUIRED`; capability enforced identically.

- [x] **Step 4: Run tests**

Run: `cd cloud && go test ./internal/vmbrowser/ -v`
Expected: PASS (all files).

- [x] **Step 5: Commit**

```bash
git add cloud/internal/vmbrowser/service.go cloud/internal/vmbrowser/service_test.go
git commit -m "feat(cloud): add loopback browser service speaking the desktop browser contract"
```

---

### Task 9: Wire browserd into the worker and mount the CLI verbs in `ao`

**Files:**
- Create: `cloud/cmd/ao-worker/browserd.go`, `cloud/cmd/ao-worker/browserd_test.go`
- Modify: `cloud/cmd/ao-worker/main.go`, `cloud/cmd/ao-cloud-agent/main.go` (+ its test file if one exists; otherwise add `browser_mount_test.go`)

**Interfaces:**
- Consumes: `vmbrowser.NewService`, `vmbrowser.NewChromium`, `vmbrowser.NewEngine`, `browsercontract.NewAuthority`, `clibrowser.NewCommand`.
- Produces:
  - `func runBrowserd(ctx context.Context, opts BrowserdOptions) error` with `type BrowserdOptions struct { DataDir, SessionID string; AgentEnv map[string]string; Logger *slog.Logger }` — mints the capability, starts the loopback listener on `127.0.0.1:0`, writes nothing to disk except the browser root, injects `AO_BROWSER_CAPABILITY` and `AO_BROWSER_API_URL` into `AgentEnv` (caller owns applying it), serves until ctx done.
  - ao-cloud-agent: `ao browser ...` dispatches to `clibrowser.NewCommand(transport, nil, <cloud long text>)` with a transport reading `AO_BROWSER_API_URL` + `AO_BROWSER_CAPABILITY`.

- [x] **Step 1: Write the failing tests**

`cloud/cmd/ao-worker/browserd_test.go`:

```go
func TestRunBrowserdInjectsEnvAndServesCommands(t *testing.T) {
	env := map[string]string{}
	addrCh := make(chan string, 1)
	opts := BrowserdOptions{
		DataDir: t.TempDir(), SessionID: "sess-1",
		AgentEnv: env, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		OnReady: func(addr string) { addrCh <- addr },
		// Engine/Chromium seams injected for the test:
		NewEngine: func(root, sessionID string) EngineLike { return &noopEngine{} },
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runBrowserd(ctx, opts) }()
	select {
	case addr := <-addrCh:
		if !strings.HasPrefix(addr, "http://127.0.0.1:") {
			t.Fatalf("addr = %q, want loopback http", addr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("browserd never became ready")
	}
	if env["AO_BROWSER_CAPABILITY"] == "" {
		t.Fatal("capability must be injected into the agent env")
	}
	if !strings.HasPrefix(env["AO_BROWSER_API_URL"], "http://127.0.0.1:") {
		t.Fatalf("AO_BROWSER_API_URL = %q", env["AO_BROWSER_API_URL"])
	}
	// A live command round-trip against the noop engine:
	body := postJSON(t, env["AO_BROWSER_API_URL"]+"/api/v1/browser/commands",
		map[string]string{"X-AO-Browser-Capability": env["AO_BROWSER_CAPABILITY"]},
		`{"sessionId":"sess-1","action":"console"}`)
	if !strings.Contains(body, `"action":"console"`) {
		t.Fatalf("unexpected body: %s", body)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("runBrowserd: %v", err)
	}
}
```

`noopEngine` (in the test) implements the engine surface with canned results; add an `EngineLike`/`NewEngine` seam on `BrowserdOptions` so the wiring is testable without Chromium (production default constructs the real `Chromium`+`Engine`).

`cloud/cmd/ao-cloud-agent/browser_mount_test.go`:

```go
func TestBrowserSubcommandMounted(t *testing.T) {
	// Host the cobra tree the main dispatch will use; assert `browser --help`
	// lists open/snapshot/act and that the transport reads AO_BROWSER_API_URL.
	t.Setenv("AO_BROWSER_API_URL", "http://127.0.0.1:1")
	t.Setenv("AO_SESSION_ID", "sess-1")
	t.Setenv("AO_BROWSER_CAPABILITY", "tok")
	cmd := browserCommand()
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected a connection error from the stub URL, proving wiring")
	}
}
```

- [x] **Step 2: Run tests to verify they fail**

Run: `cd cloud && go test ./cmd/ao-worker/ ./cmd/ao-cloud-agent/`
Expected: FAIL, undefined.

- [x] **Step 3: Implement `runBrowserd`**

`cloud/cmd/ao-worker/browserd.go`:

```go
func runBrowserd(ctx context.Context, opts BrowserdOptions) error {
	if opts.DataDir == "" || opts.SessionID == "" {
		return errors.New("browserd requires data dir and session id")
	}
	authority := browsercontract.NewAuthority()
	token, verifier, err := authority.Issue(opts.SessionID)
	if err != nil {
		return fmt.Errorf("mint browser capability: %w", err)
	}
	root := filepath.Join(opts.DataDir, "browser", opts.SessionID)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return fmt.Errorf("create browser root: %w", err)
	}
	newEngine := opts.NewEngine
	if newEngine == nil {
		newEngine = func(root, sessionID string) vmbrowser.EngineLike {
			chromium := vmbrowser.NewChromium(vmbrowser.ChromiumOptions{
				BinaryPath:  chromiumBinaryPath(), // /usr/bin/chromium, overridable via AO_CLOUD_CHROMIUM_PATH
				UserDataDir: filepath.Join(root, "profile"),
				Logger:      opts.Logger,
			})
			return vmbrowser.NewEngine(chromium, nil, vmbrowser.EngineOptions{
				Root: root, SessionID: sessionID, Logger: opts.Logger,
			})
		}
	}
	service := vmbrowser.NewService(vmbrowser.ServiceOptions{
		SessionID: opts.SessionID, CapabilityVerifier: verifier,
		Engine: newEngine(root, opts.SessionID), Logger: opts.Logger,
	})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("listen browserd loopback: %w", err)
	}
	opts.AgentEnv["AO_BROWSER_CAPABILITY"] = token
	opts.AgentEnv["AO_BROWSER_API_URL"] = "http://127.0.0.1:" + strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
	server := &http.Server{Handler: service.Handler()}
	if opts.OnReady != nil {
		opts.OnReady(opts.AgentEnv["AO_BROWSER_API_URL"])
	}
	errCh := make(chan error, 1)
	go func() { errCh <- server.Serve(listener) }()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		<-errCh
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
```

(`Engine`/`EngineLike`: promote the service's engine parameter to the small interface the service actually calls — `Execute` and `Screenshot` — so the seam exists once, in `vmbrowser`.)

Restructure `runBrowserd` into `startBrowserd(...) (env map[string]string, stop func(ctx context.Context) error, err error)` so `main` binds the listener and mints the capability synchronously BEFORE the agent terminal opens (avoiding an env race), then serves in the background. In `cloud/cmd/ao-worker/main.go`, after `agentCommand` is built and before `transportSupervisor.Run` starts (the agent terminal opens inside `Run`, main.go line 79-91 of `workertransport/supervisor.go`):

```go
browserdEnv, stopBrowserd, err := startBrowserd(runCtx, BrowserdOptions{
	DataDir: dataDir, SessionID: bootstrap.SessionID, Logger: logger,
})
if err != nil {
	// Mirror the harness-unavailable precedent (main.go:201-206): a browserd
	// failure must never kill the worker; agents just lose browser verbs.
	logger.Warn("browserd unavailable; continuing without browser verbs", "error", err)
} else {
	defer func() { _ = stopBrowserd(context.Background()) }()
	if agentCommand.Env == nil {
		agentCommand.Env = map[string]string{}
	}
	for key, value := range browserdEnv {
		agentCommand.Env[key] = value
		_ = os.Setenv(key, value) // workspace shell terminals inherit os.Environ()
	}
}
```

Operational details that must not be lost:
- `agentCommand.Env` can be nil when the harness is unavailable (zero-value `workerexec.Command`); guard the map (workspace terminals still get the vars via `os.Setenv` inheritance).
- The `results` channel at main.go line 251 has capacity 5 for 5 goroutines; the browserd serve loop logs its own errors and does NOT push to `results` (a browserd death must not end the worker).
- Idle shutdown (spec D7): a `time.Ticker` inside browserd calls `chromium.Stop` after 30 minutes with no served command and no viewer (Stage 1: no viewers exist, so command activity is the only input). Make the idle duration a field on `BrowserdOptions` (default 30 min) and unit-test it with a millisecond-scale value.
- `startBrowserd` must not write `AO_RUN_FILE`; the cloud transport is URL-based (`AO_BROWSER_API_URL`), not run-file based.

- [x] **Step 4: Mount the verbs in ao-cloud-agent**

In `cloud/cmd/ao-cloud-agent/main.go` `run()`: after the existing arg parsing, before the switch on `args[0]`, add:

```go
if args[0] == "browser" {
	apiURL := strings.TrimSpace(os.Getenv("AO_BROWSER_API_URL"))
	if apiURL == "" {
		return errors.New("ao browser requires a cloud session browser service (AO_BROWSER_API_URL is not set)")
	}
	cmd := clibrowser.NewCommand(
		cloudBrowserTransport{baseURL: strings.TrimRight(apiURL, "/")},
		nil,
		"Inspect and control the target-isolated browser owned by the current AO session.\n\n"+
			"Commands operate the session's browser service inside this cloud sandbox.",
	)
	cmd.SetArgs(args[1:])
	cmd.SetOut(os.Stdout)
	cmd.SetErr(os.Stderr)
	if err := cmd.Execute(); err != nil {
		var usage clibrowser.UsageError
		if errors.As(err, &usage) {
			os.Exit(2)
		}
		fmt.Fprintln(os.Stderr, "ao:", err)
		os.Exit(1)
	}
	return nil
}
```

With `cloudBrowserTransport` implementing `clibrowser.Transport` using the file's existing `client` HTTP style: base URL from env, `X-AO-Browser-Capability` from `AO_BROWSER_CAPABILITY`, decoding `clibrowser.Response`/`Status` and the top-level `{message, code, requestId}` error envelope into an error string formatted `message (code) [request id]` (mirror `backend/internal/cli/client.go` `apiError.String()`).

Two presentation fixes required in the same change:
- Add `browser` to `printUsage` in `cloud/cmd/ao-cloud-agent/main.go` (it currently lists hooks, spawn, list, send, kill, claim-pr).
- The verb tree's Long text says "The desktop app must be open.", which is false in cloud. Give `clibrowser.NewCommand` an optional `Long string` parameter (empty = desktop default) and pass a cloud wording: "Commands operate the session's browser service inside this cloud sandbox." Desktop behavior and help output stay byte-identical.

- [x] **Step 5: Run tests and both module builds**

Run: `cd cloud && go test ./cmd/... ./internal/vmbrowser/... && go build ./...`
Expected: PASS.

- [x] **Step 6: Commit**

```bash
git add cloud/cmd/ao-worker cloud/cmd/ao-cloud-agent
git commit -m "feat(cloud): run the in-VM browser service and mount ao browser verbs in cloud workers"
```

---

### Task 10: Worker image — Chromium + pinned agent-browser

**Files:**
- Modify: `cloud/Dockerfile`
- Modify: `cloud/scripts/test-cloud-local.sh` (only if the smoke additions need a hook; otherwise untouched)

**Interfaces:**
- Consumes: agent-browser pin table from Global Constraints; bookworm chromium package.
- Produces: worker image containing `/usr/bin/chromium`, `/usr/local/lib/ao/agent-browser` (0o755), reachable when the worker runs as `ao-worker`.

- [x] **Step 1: Extend the worker stage**

In `cloud/Dockerfile` worker stage, add to the first `apt-get install` list: `chromium` (bookworm ships a usable headless build). After the cursor-agent RUN block, add:

```dockerfile
RUN set -eu; \
    case "${TARGETARCH}" in \
        amd64) ab_asset=agent-browser-linux-x64;  ab_sha=6e04d06605c4ca62da36e3263086e0f7ceae808b55508de2c3958d4b7fe430aa ;; \
        arm64) ab_asset=agent-browser-linux-arm64; ab_sha=281cce8e3e9eb11fd823b13c085996d7361c35923ad454ce5cb06a5515630e9b ;; \
        *) echo "unsupported agent-browser architecture: ${TARGETARCH}" >&2; exit 1 ;; \
    esac; \
    curl --fail --location --silent --show-error \
        "https://github.com/vercel-labs/agent-browser/releases/download/v0.33.1/${ab_asset}" \
        -o /tmp/agent-browser; \
    echo "${ab_sha}  /tmp/agent-browser" | sha256sum -c -; \
    install -m 0755 /tmp/agent-browser /usr/local/lib/ao/agent-browser; \
    rm -f /tmp/agent-browser; \
    /usr/local/lib/ao/agent-browser --version; \
    chromium --version
```

Add `AO_CLOUD_CHROMIUM_PATH=/usr/bin/chromium` and `AO_CLOUD_AGENT_BROWSER_PATH=/usr/local/lib/ao/agent-browser` as ENV in the worker stage, and make Task 9's `chromiumBinaryPath()`/engine `BinaryPath` read them (update Task 9's helpers if they hardcoded paths).

- [x] **Step 2: Build the image locally**

Run: `docker build --target worker -t ao-cloud-worker:browser-stage1 cloud/`
Expected: build succeeds; the `--version` checks in the layer pass (proves the binaries execute).

- [x] **Step 3: Smoke the image manually**

```bash
docker run --rm ao-cloud-worker:browser-stage1 sh -c 'chromium --headless=new --remote-debugging-address=127.0.0.1 --remote-debugging-port=0 --user-data-dir=/tmp/p --no-first-run --no-sandbox --disable-dev-shm-usage about:blank & sleep 2; ls /tmp/p/DevToolsActivePort && cat /tmp/p/DevToolsActivePort'
```

Expected: the DevToolsActivePort file exists and prints a port plus browser path. Record output in the Task 1 notes file as image-level confirmation.

- [x] **Step 4: Commit**

```bash
git add cloud/Dockerfile
git commit -m "feat(cloud): bake chromium and pinned agent-browser into the worker image"
```

---

### Task 11: Pin bump, docs, full verification

**Files:**
- Modify: `cloud/go.mod`, `cloud/go.sum` (backend replace pin)
- Modify: `docs/cloud-development.md`

- [ ] **Step 1: Bump the backend pin**

The cloud module now imports `backend/pkg/browsercontract` and `backend/pkg/clibrowser`, which do not exist at the pinned commit `5da0ce157982`. After this branch's backend changes are pushed, update `cloud/go.mod`:

```
replace github.com/aoagents/agent-orchestrator/backend => github.com/Untrivial-ai/agent-orchestrator/backend v0.0.0-<new-timestamp>-<new-sha>
```

using the commit that contains Task 2-3 (find it with `git log --format='%H %cI' -- backend/pkg/browsercontract | head -1` after push; convert to the pseudo-version with `go mod download github.com/aoagents/agent-orchestrator/backend@<sha>` inside a scratch module, or `GOFLAGS=-mod=mod go get github.com/Untrivial-ai/agent-orchestrator/backend@<sha>` from `cloud/` with the workspace disabled via `GOWORK=off`). Then `cd cloud && GOWORK=off go build ./...` to prove the standalone build resolves. If the branch is not yet pushed, do this step last, immediately before handoff, against the pushed branch head.

- [x] **Step 2: Document the engine**

Add a "Session browser (Stage 1)" section to `docs/cloud-development.md`: what browserd is, the env vars it injects (`AO_BROWSER_CAPABILITY`, `AO_BROWSER_API_URL`), the verb matrix (supported vs `BROWSER_ACTION_UNSUPPORTED_CLOUD`), the lazy/idle lifecycle, profile location `<dataDir>/browser/<sessionID>/profile`, and how to try it locally with `test-cloud-local.sh` plus `docker exec <worker> ao browser open https://example.com`.

- [x] **Step 3: Full local verification**

CI coverage reality (verified 2026-09-17): `.github/workflows/go.yml` tests `backend/` only (its path filter and working-directory never touch `cloud/`), `npm run lint` is backend go test + golangci-lint, and the public repo has no workflow that runs `cloud/` tests (cloud CI, if any, lives with the private submodule). Therefore the CLOUD module's gate is local: these commands are the authoritative check, and CI greenness alone does not cover this plan's cloud changes.

```bash
cd backend && go build ./... && go test ./... && go vet ./...
cd ../cloud && go build ./... && go test ./... && go vet ./...
cd .. && npm run lint
npm run frontend:typecheck
```

Expected: all green. `frontend/` is untouched by this plan (typecheck is a guard against accidental drift only).

- [ ] **Step 4: Compose end-to-end smoke (Stage 1 exit criteria)**

```bash
cloud/scripts/test-cloud-local.sh   # full existing suite must stay green
# then, with the local stack still up:
docker exec "$(docker ps -q --filter label=ao.provider=docker | head -1)" \
  ao browser open https://example.com
docker exec "$(docker ps -q --filter label=ao.provider=docker | head -1)" \
  ao browser snapshot --interactive
docker exec "$(docker ps -q --filter label=ao.provider=docker | head -1)" \
  ao browser screenshot /tmp/shot.png --json
docker exec "$(docker ps -q --filter label=ao.provider=docker | head -1)" \
  ao browser act "the More information... link"
```

Expected: each command returns local-shaped JSON/text; the screenshot writes a real PNG; act resolves a link. Also record `ps -o rss= -C chromium` inside the worker for the spec's RAM estimate. If compose's worker image tag differs from the locally built one, retag: `docker tag ao-cloud-worker:browser-stage1 ao-cloud-worker:local`.

- [ ] **Step 5: Commit**

```bash
git add cloud/go.mod cloud/go.sum docs/cloud-development.md
git commit -m "chore(cloud): bump backend pin for browser contract packages and document the session browser"
```

---

## Self-Review

**Spec coverage (Stage 1 exit criteria, spec section 8):**
- "browserd, Chromium supervision, agent-browser attach, loopback service with reused backend service code" → Tasks 4, 6, 8, 9. The spec's D2 said "compile `backend/internal/service/browser` into the VM service"; Go's internal-package rule forbids that literal move, so the plan implements D2's intent with the public `pkg/browsercontract` (contract) + `pkg/clibrowser` (CLI) split. The daemon-side allowlist stays identical because the daemon now consumes the same `browsercontract`. This deviation is deliberate and should be noted in the PR body.
- "in a docker sandbox, open/snapshot/act/screenshot return local-shaped JSON via the in-VM CLI" → Task 11 step 4.
- "profile isolated per session" → per-session root `<dataDir>/browser/<sessionID>` (Tasks 6, 9).
- "restart policy behaves" → Task 4 (backoff + park + unpark on next open).
- "measured RSS recorded" → Tasks 1 and 11 step 4.
- Spec D7 idle shutdown (30 min): the service exposes activity via `Status`; idle shutdown is a small timer in `runBrowserd`/`startBrowserd` — fold its implementation into Task 9 step 3 (a `time.Ticker` that calls `chromium.Stop` when no command served in 30 minutes; unit-test by injecting the idle duration). Added here as an explicit requirement so it is not dropped.

**Placeholder scan:** Two test stubs contain prose markers rather than full code (`TestServiceActRetriesOnceOnStaleReference`, the `matchedRef` fixture table). These name the exact scenario and assertions; executors must write them fully before running. No other TBD/TODO patterns; every production step names files and shows the code or the exact mechanical transformation.

**Type consistency:** `Engine` vs `EngineLike` appears in Tasks 8/9 — resolved by making `vmbrowser.ServiceOptions.Engine` the interface type (`EngineLike`) with the concrete `*Engine` satisfying it; Task 9's seam then injects fakes. `Chromium.EnsureRunning` returns `Endpoint{WebSocketURL string}` consumed by the engine env builder. `clibrowser.Transport`/`Response`/`Status` names are used identically in Tasks 3 and 9. `browsercontract.CapabilityHeader` is the single header constant (aliased, not redefined).

**Post-review amendments (2026-09-17, after verifying against the Electron host source):** this plan was reviewed against `browser-view-host.ts` and `agent-browser-runtime.ts` line by line; seven corrections were folded in above rather than left as findings: (1) `open`/`tab-new` URL normalization now ports `normalizeAgentBrowserURL` with desktop error codes, so `localhost:3000` works (the plan previously required an explicit scheme, which would have broken the spec's VM-dev-server requirement); (2) added `sanitize.go` port (`sanitizeBrowserURL`/`Title`/`sanitizeURLsInText`) applied at `open`, `get url/title`, and all tab results, closing a credential-leak parity gap; (3) env contract corrected to include `AGENT_BROWSER_SOCKET_DIR`, the exact `NATIVE_ENV_ALLOWLIST`, and the exact already-disabled marker string; (4) tab/nav result shapes corrected to the real ones (`{activeTabId, tabs}`, bare tab object for new/select, `{closedTabId, ...}` for close, nav-state for `open`), with `viewId`/`change` documented as intentional omissions; (5) `console`/`errors` normalization specified exactly (level fallback chain, 1 MiB `externalText` cap, per-message untrusted markers, timestamps); (6) Task 9 wiring de-duplicated into the synchronous `startBrowserd` form only, plus nil-Env guard, results-channel policy, explicit idle-shutdown step with test seam, `printUsage` line, and a clibrowser `Long` override so cloud help text stops claiming a desktop app; (7) verification section now states that public CI does not run the cloud module and local commands are the authoritative gate.

**Second review pass (2026-09-17, verifying helper and wiring seams against `backend/internal/cli` and `backend/internal/daemon`):** six more corrections folded in: (1) `noArgs`/`atMostOneArg` are shared `root.go` helpers (lines 321, 328), so clibrowser duplicates them privately instead of moving them; (2) `writeJSON` is shared in `output.go` and gets a local copy, and the moved wire-shape tests keep a typed `clibrowser.CommandRequest` (the old `browserCommandRequestDTO` tags, asserted by `browser_test.go` around lines 20 and 497); (3) Task 2 now keeps `browsersvc.NewAuthority()` as a two-line alias so `daemon.go` (lines 191, 492, 562, 780) compiles untouched, minimizing daemon churn; (4) the browser routes live in the generated OpenAPI spec (`specgen` `browserOperations()`, build.go line 648) — Tasks 2-3 never touch controller DTOs, so no `npm run api` regeneration is in scope, stated explicitly to preempt executor doubt; (5) Task 4 gained the DevToolsActivePort/`port=0` fallback decision (pre-picked ephemeral port, decided at the spike) and the `--headless=new` vs `--headless` note; (6) `Engine.Close` behavior and idle-shutdown race semantics documented (one command may fail with `AGENT_BROWSER_COMMAND_FAILED`; the next lazily restarts). Verified correct with no change needed: `commandContext.deps` clock access for the shim, `workerexec.Command{Env map[string]string}` shape for env injection, daemon decode semantics, and the drift-test suite not covering browser routes.

## Execution Handoff

Plan complete and saved to `docs/superpowers/plans/2026-09-17-cloud-browser-stage1-vm-automation.md`. In this AO worker session, subagent dispatch is not available, so the execution options are: inline execution task-by-task in this session (superpowers:executing-plans), or handing individual tasks to fresh AO worker sessions via the orchestrator. Task 1 (the spike) should execute first and gates everything after it.
