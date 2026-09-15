# Claude Auth and Provider Correctness Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Resolve every correctness issue in the latest PR review while keeping Cloud out of the PR.

**Architecture:** Keep protocol-neutral ACP behavior generic and inject Claude-specific terminal classification and daemon-owned cache invalidation through narrow callbacks. Make provider probing conservative, project-scoped, and catalog-oriented, then carry validated model/effort settings through both fresh launch and restore.

**Tech Stack:** Go 1.26, React/TypeScript, Vitest, `httptest`, AWS SDK v2, `golang.org/x/oauth2/google`.

**Spec:** `docs/superpowers/specs/2026-09-16-claude-auth-provider-correctness-design.md`

## Global Constraints

- Do not modify `cloud/`.
- Never classify permission, transport, or ambiguous gateway responses as unauthorized.
- Keep unrelated working-tree changes unstaged.
- Use focused tests locally and broad checks in PR CI.

---

### Task 1: Claude-owned ACP terminal authentication failures

**Files:**
- Modify: `backend/internal/adapters/chatdriver/acp/driver.go`
- Modify: `backend/internal/adapters/chatdriver/acp/client.go`
- Modify: `backend/internal/adapters/chatdriver/acp/prompt_failure_test.go`
- Modify: `backend/internal/adapters/chatdriver/acp/auth_required_test.go`
- Modify: `backend/internal/adapters/chatdriver/claudeacp/driver.go`
- Modify: `backend/internal/adapters/chatdriver/claudeacp/driver_test.go`

**Interfaces:**
- Produces: an ACP `PromptResponseFailure` callback that maps a structured provider response to an error.
- Consumes: Claude's exact `jetbrains.air.sessionFailure` metadata contract and `login` action.

- [ ] Add a failing Claude ACP regression for assistant auth-expiry output followed by `stopReason=end_turn`.
- [ ] Add a failing generic ACP regression proving assistant prose and generic request-error phrases are not authentication signals.
- [ ] Replace `acpAuthRejectionPhrases` with protocol-code-only generic handling.
- [ ] Move synthetic terminal metadata interpretation behind the Claude binding callback.
- [ ] Run `go test ./internal/adapters/chatdriver/acp ./internal/adapters/chatdriver/claudeacp`.
- [ ] Commit the focused change.

### Task 2: Invalidate both daemon authentication caches

**Files:**
- Modify: `backend/internal/adapters/chatdriver/claudeacp/driver.go`
- Modify: `backend/internal/daemon/daemon.go`
- Modify: `backend/internal/service/agent/readiness.go`
- Modify: corresponding daemon/service/Claude ACP tests.

**Interfaces:**
- Produces: `claudeacp.New(..., onAuthRejected func())` or an equivalent config field supplied by the daemon.
- Consumes: `Service.InvalidateAgentAuthentication("claude-code")` and `Service.RecheckAgent("claude-code")`.

- [ ] Write a failing test that observes private cache invalidation, readiness invalidation, and asynchronous recheck scheduling.
- [ ] Inject the daemon-owned callback into the Claude ACP driver.
- [ ] Keep callback execution non-blocking and idempotent.
- [ ] Run focused daemon, service, and Claude ACP tests.
- [ ] Commit the focused change.

### Task 3: Conservative HTTP provider contracts

**Files:**
- Modify: `backend/pkg/agentcreds/agentcreds.go`
- Modify: `backend/pkg/agentcreds/providers.go`
- Modify: `backend/pkg/agentcreds/bedrock.go`
- Modify: `backend/pkg/agentcreds/vertex.go`
- Modify: `backend/pkg/agentcreds/providers_test.go`
- Modify: `backend/pkg/agentcreds/signing_test.go`

**Interfaces:**
- Produces: provider-aware response classification and complete paginated model results.

- [ ] Add failing tests for generic 403 => unknown, gateway 429 => unknown, first-party 429 => valid, and Claude OAuth headers.
- [ ] Add a failing pagination test asserting `limit` and cursor traversal.
- [ ] Add a failing Vertex URL-shape test for `/v1beta1/publishers/anthropic/models`.
- [ ] Add a failing Bedrock test proving model listing is catalog evidence, not launch-readiness proof.
- [ ] Implement the minimal provider-specific classification and pagination behavior.
- [ ] Run `go test ./pkg/agentcreds`.
- [ ] Commit the focused change.

### Task 4: Project-scoped command credentials and Claude auth report

**Files:**
- Modify: `backend/pkg/agentcreds/chain.go`
- Modify: `backend/pkg/agentcreds/resolve.go`
- Modify: `backend/pkg/agentcreds/agentcreds.go`
- Modify: `backend/pkg/agentcreds/*_test.go`
- Modify: `backend/internal/adapters/agent/claudecode/auth.go`
- Modify: `backend/internal/adapters/agent/claudecode/auth_test.go`

**Interfaces:**
- Produces: a private command result carrying separate stdout/stderr and command context carrying working directory/environment.
- Consumes: `ports.AgentModelDiscoveryRequest.WorkingDir` and `.Env`.

- [ ] Add a failing test where successful gcloud stdout is accompanied by a stderr warning.
- [ ] Add failing tests that assert AWS/gcloud and `claude auth status` receive project cwd and merged launch environment.
- [ ] Replace `CombinedOutput` parsing with stdout-only success parsing and stderr diagnostics.
- [ ] Thread project context through credential resolution and Claude auth reporting.
- [ ] Run focused `agentcreds` and `claudecode` tests.
- [ ] Commit the focused change.

### Task 5: Discovery errors and cache semantics

**Files:**
- Modify: `backend/internal/adapters/agent/modelcatalog/catalog.go`
- Modify: `backend/internal/adapters/agent/modelcatalog/claude_provider_test.go`
- Modify: `backend/internal/service/agent/catalog.go`
- Modify: `backend/internal/service/agent/catalog_test.go`

**Interfaces:**
- Produces: Claude discovery errors that reach `Service.Models`; stale-cache retention on failures.

- [ ] Write a failing service test that seeds provider IDs/efforts, fails refresh, and expects the stale provider catalog.
- [ ] Change Claude discovery to return provider errors rather than successful static fallback.
- [ ] Return static aliases only when no usable provider cache exists.
- [ ] Remove the incomplete fingerprint as a cache-validity shortcut, or replace it only with a value covering every discovery input.
- [ ] Run focused modelcatalog and agent-service tests.
- [ ] Commit the focused change.

### Task 6: Model/effort validation and restore persistence

**Files:**
- Modify: `backend/internal/ports/agent.go`
- Modify: `backend/pkg/agentruntime/command.go`
- Modify: `backend/pkg/agentruntime/command_test.go`
- Modify: `backend/internal/adapters/agent/claudecode/claudecode.go`
- Modify: `backend/internal/adapters/agent/claudecode/claudecode_test.go`
- Modify: `backend/internal/session_manager/manager.go`
- Modify: `backend/internal/session_manager/agent_switching.go`
- Modify: relevant session-manager tests.

**Interfaces:**
- Produces: `Effort string` on restore config and Claude restore argv.
- Consumes: the resolved session `AgentConfig` and Claude provider catalog before durable spawn.

- [ ] Add a failing command test expecting `--model <id> --effort <level>` on Claude restore.
- [ ] Add a failing session-manager test proving invalid Claude TUI model/effort is rejected before persistence/spawn.
- [ ] Thread effort through every restore constructor and Claude adapter.
- [ ] Reuse catalog validation for explicit TUI and Chat-to-TUI fallback before durable work.
- [ ] Run focused agentruntime, Claude adapter, and session-manager tests.
- [ ] Commit the focused change.

### Task 7: Desktop and mobile readiness behavior

**Files:**
- Modify: `frontend/src/renderer/components/TaskComposer.tsx`
- Modify: `frontend/src/renderer/components/TaskComposer.test.tsx`
- Modify: `packages/mobile/lib/agentPicker.ts`
- Modify: mobile picker tests.

**Interfaces:**
- Produces: cached unauthorized state that warns but does not disable submission; mobile `configured` => `auth-unknown`.

- [ ] Add failing desktop test showing cached unauthorized does not disable submit/recheck.
- [ ] Remove cached unauthorized from `canSubmit`; preserve launch-time readiness rejection handling.
- [ ] Add failing mobile test for `configured` mapping.
- [ ] Remove `configured` from the mobile authorized condition.
- [ ] Run the focused Vitest suites and frontend typecheck.
- [ ] Commit the focused change.

### Task 8: Final verification and PR hygiene

**Files:**
- Verify all files changed by Tasks 1-7.

**Interfaces:**
- Produces: a PR with no Cloud diff and evidence for every reviewer comment.

- [ ] Run `git diff --check` and verify `git diff <base>...HEAD -- cloud` is empty.
- [ ] Run focused Go suites for ACP, Claude, model catalog, credentials, agent service, session manager, and runtime commands.
- [ ] Run focused frontend/mobile tests and `npm run frontend:typecheck`.
- [ ] Run the pinned golangci-lint command before pushing.
- [ ] Push to `untrivial/ao/credential-validation` and monitor PR checks.
