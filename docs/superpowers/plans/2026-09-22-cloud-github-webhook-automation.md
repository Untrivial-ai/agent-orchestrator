# Cloud GitHub Webhook Automation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make signed GitHub App events update durable Cloud PR/CI facts, notifications, Kanban placement, and conditional automatic CI feedback to a Cloud worker.

**Architecture:** Extend the existing signed GitHub webhook queue and its installation-ordered processor. The processor refreshes an authoritative installation-scoped PR snapshot, atomically persists meaningful transitions with a CI-feedback outbox, then hands feedback to the established terminal-input transport. The renderer retains shared presentation but reads and writes the policy through Cloud APIs.

**Tech Stack:** Go, PostgreSQL/goose/pgx, Chi, GitHub App REST client, worker WebSocket/terminal transport, React, TypeScript, TanStack Query, Vitest.

**Spec:** `docs/superpowers/specs/2026-09-22-cloud-github-webhook-automation-design.md`

## Global Constraints

- Modify `cloud/` and Cloud-gated frontend code only. Do not modify `backend/`, local storage, local daemon routes, OAuth callback, or onboarding.
- Docker, NodeOps, and Coder use one control-plane implementation; no provider-specific webhook or feedback logic.
- Retain `ao_github_webhook_deliveries` as signed, deduplicated, installation-ordered ingress and retain the 30-second scanner as recovery-only.
- Persist facts, derive Kanban status using `backend/pkg/contract`, and never add a persisted Cloud display status.
- Always record CI failure; enqueue feedback only when Cloud session `autoInjectCI` is true. Never add a manual send control.
- Feedback must remain idempotent over duplicate deliveries, retries, process restart, and worker reconnect.

## Review Focus

- Duplicate delivery ID with changed metadata conflicts at intake — Task 2.
- Signed event for untracked or cross-org PR is a safe no-op — Task 3.
- Repeated check failure creates one notification and one feedback instruction — Task 4.
- Disabled policy updates panel/Kanban but queues no terminal input — Task 5.
- Disconnect during dispatch preserves one retriable feedback item — Task 5.

---

## File Structure

- Create migration `cloud/internal/postgres/migrations/00046_cloud_scm_webhooks.sql` for session policy, application dedupe, and feedback outbox.
- Extend `cloud/internal/domain/{types,github}.go` with Cloud policy and webhook/feedback types.
- Extend `cloud/internal/githubapp/service.go` with focused `webhook_scm.go` processing.
- Add `cloud/internal/postgres/ci_feedback_store.go` and use it from `pull_request_store.go`.
- Add `cloud/internal/cifeedback/dispatcher.go`; wire it in `cloud/cmd/ao-cloud/main.go`.
- Add Cloud policy endpoint/response in `cloud/internal/httpapi/{server,resource_handlers,github_handlers}.go`.
- Extend `frontend/src/renderer/lib/cloud-cp/{types,client}.ts`, `hooks/useWorkspaceQuery.ts`, and `components/SessionInspector.tsx`.

### Task 1: Persist Cloud CI policy and feedback outbox

**Files:**
- Create: `cloud/internal/postgres/migrations/00046_cloud_scm_webhooks.sql`
- Modify: `cloud/internal/domain/types.go`, `cloud/internal/postgres/project_session_store.go`
- Test: existing Cloud PostgreSQL migration/store tests

**Interfaces:**
- Produces `domain.Session.AutoInjectCI bool`, default `true`.
- Produces unique `application_key` feedback records with org/session/PR ownership and lease/delivery status.

- [ ] **Step 1: Write failing schema tests**

```go
func TestCloudSCMWebhookMigrationAddsPolicyAndOutbox(t *testing.T) {
    requireColumn(t, "ao_sessions", "auto_inject_ci")
    requireTable(t, "ao_ci_feedback_outbox")
    requireUniqueIndex(t, "ao_ci_feedback_outbox", "application_key")
}
```

- [ ] **Step 2: Run the test and confirm it fails**

Run: `cd cloud && go test ./internal/postgres -run TestCloudSCMWebhookMigrationAddsPolicyAndOutbox -count=1`

Expected: FAIL because migration 00046 does not exist.

- [ ] **Step 3: Add minimal migration and scans**

```sql
ALTER TABLE ao_sessions ADD COLUMN auto_inject_ci BOOLEAN NOT NULL DEFAULT TRUE;
CREATE TABLE ao_ci_feedback_outbox (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  application_key TEXT NOT NULL UNIQUE,
  org_id UUID NOT NULL, session_id UUID NOT NULL, pull_request_id UUID NOT NULL,
  payload JSONB NOT NULL, status TEXT NOT NULL DEFAULT 'pending',
  lease_owner TEXT NOT NULL DEFAULT '', lease_until TIMESTAMPTZ,
  delivered_at TIMESTAMPTZ, created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

Add foreign keys/indexes/check constraints matching Cloud migration conventions, the exact down migration, and field plumbing in every session create/scan response path.

- [ ] **Step 4: Verify and commit**

Run: `gofmt -w cloud/internal/domain/types.go && cd cloud && go test ./internal/postgres -count=1`

Expected: PASS.

```bash
git add cloud/internal/postgres cloud/internal/domain/types.go
git commit -m "feat(cloud): add ci feedback persistence"
```

### Task 2: Accept SCM webhook events securely

**Files:**
- Modify: `cloud/internal/httpapi/github_handlers.go`, `cloud/internal/domain/github.go`
- Test: `cloud/internal/httpapi/github_handlers_test.go`

**Interfaces:**
- Extends `domain.GitHubWebhookDelivery` parsing to retain routing fields for PR number/check identity.
- Enqueues `pull_request`, `check_suite`, `check_run`, and `pull_request_review` after existing signature/body checks.

- [ ] **Step 1: Write failing HTTP tests**

```go
func TestGitHubWebhookEnqueuesSCMEvents(t *testing.T) {
  for _, event := range []string{"pull_request", "check_suite", "check_run", "pull_request_review"} {
    require.Equal(t, http.StatusAccepted, postSignedWebhook(t, event, "id-"+event, trackedPREnvelope()).Code)
  }
}
func TestGitHubWebhookRejectsChangedDuplicate(t *testing.T) {
  require.Equal(t, http.StatusAccepted, postSignedWebhook(t, "check_run", "id", checkRun("failure")).Code)
  require.Equal(t, http.StatusConflict, postSignedWebhook(t, "check_run", "id", checkRun("success")).Code)
}
```

- [ ] **Step 2: Confirm failure**

Run: `cd cloud && go test ./internal/httpapi -run 'TestGitHubWebhook(EnqueuesSCMEvents|RejectsChangedDuplicate)' -count=1`

Expected: FAIL because non-installation events are discarded before enqueue.

- [ ] **Step 3: Implement minimal secure intake**

Keep validation order exactly: headers, `http.MaxBytesReader`, raw body, HMAC verification, bounded typed envelope parsing. Enqueue only the four event types plus existing installation events. Payload-selected org/session IDs are never used. Existing store conflict maps to HTTP 409.

- [ ] **Step 4: Verify and commit**

Run: `cd cloud && go test ./internal/httpapi -run TestGitHubWebhook -count=1`

Expected: PASS.

```bash
git add cloud/internal/httpapi/github_handlers.go cloud/internal/httpapi/github_handlers_test.go cloud/internal/domain/github.go
git commit -m "feat(cloud): queue github scm webhooks"
```

### Task 3: Route SCM deliveries to authoritative tracked PR refreshes

**Files:**
- Create: `cloud/internal/githubapp/webhook_scm.go`, `cloud/internal/githubapp/webhook_scm_test.go`
- Modify: `cloud/internal/githubapp/service.go`, `cloud/internal/postgres/{github_store,pull_request_store}.go`

**Interfaces:**
- Produces `processSCMWebhook(ctx, domain.GitHubWebhookDelivery) error`.
- Produces `Store.PullRequestByGitHubReference(ctx, orgID string, repositoryID int64, number int)`.
- Reuses `RefreshPullRequestStatus(ctx, domain.PullRequestRef)`.

- [ ] **Step 1: Write failing routing tests**

```go
func TestProcessCheckRunRefreshesOnlyTrackedPR(t *testing.T) {
  require.NoError(t, service.processWebhook(ctx, deliveryForCheckRun(repoID, 17)))
  require.Equal(t, contract.CIFailing, store.pullRequest(orgID, 17).CIState)
}
func TestProcessSCMWebhookCrossOrgIsNoOp(t *testing.T) {
  require.NoError(t, service.processWebhook(ctx, deliveryForOtherOrg(repoID, 17)))
  require.Equal(t, contract.CIPassing, store.pullRequest(orgID, 17).CIState)
}
```

- [ ] **Step 2: Confirm failure**

Run: `cd cloud && go test ./internal/githubapp -run 'TestProcess(CheckRunRefreshesOnlyTrackedPR|SCMWebhookCrossOrgIsNoOp)' -count=1`

Expected: FAIL because `processWebhook` only handles installation events.

- [ ] **Step 3: Implement route/refresh**

Resolve the organization solely through `GitHubInstallationRoute`; locate tracked PR using that org plus GitHub repository ID/number. Unknown installation, repository, or PR returns nil. For a match, call the existing installation-token `RefreshPullRequestStatus`; temporary GitHub errors retain existing webhook retry/backoff behavior.

- [ ] **Step 4: Verify and commit**

Run: `cd cloud && go test ./internal/githubapp ./internal/postgres -count=1`

Expected: PASS.

```bash
git add cloud/internal/githubapp cloud/internal/postgres/github_store.go cloud/internal/postgres/pull_request_store.go
git commit -m "feat(cloud): refresh prs from github webhooks"
```

### Task 4: Make PR transitions, notifications, and effects idempotent

**Files:**
- Create: `cloud/internal/postgres/ci_feedback_store.go`, `cloud/internal/postgres/ci_feedback_store_test.go`
- Modify: `cloud/internal/postgres/pull_request_store.go`, `cloud/internal/githubapp/webhook_scm.go`
- Modify: Phase 1 notification store boundary/tests in `cloud/internal/notification/`

**Interfaces:**
- Produces `ApplyGitHubPRObservation(ctx, domain.GitHubPRObservationApplication) (domain.PullRequest, domain.SCMEffects, error)`.
- Produces `ClaimCIFeedback`, `CompleteCIFeedback`, and `RetryCIFeedback`.
- `domain.SCMEffects` has `CIFailureStarted` and `CIFailureResolved`.

- [ ] **Step 1: Write failing transactional tests**

```go
func TestFailureApplicationIsIdempotent(t *testing.T) {
  first := apply(t, failingObservation("sha-1", "unit", "failure"))
  again := apply(t, failingObservation("sha-1", "unit", "failure"))
  require.True(t, first.CIFailureStarted)
  require.False(t, again.CIFailureStarted)
  require.Equal(t, 1, feedbackOutboxCount(t))
  require.Equal(t, 1, unresolvedNotificationCount(t))
}
func TestPassingObservationResolvesFailure(t *testing.T) {
  apply(t, failingObservation("sha-1", "unit", "failure"))
  require.True(t, apply(t, passingObservation("sha-1", "unit")).CIFailureResolved)
}
```

- [ ] **Step 2: Confirm failure**

Run: `cd cloud && go test ./internal/postgres -run 'Test(FailureApplicationIsIdempotent|PassingObservationResolvesFailure)' -count=1`

Expected: FAIL because observations only overwrite PR fields.

- [ ] **Step 3: Implement one transaction**

Lock the tracked PR, update facts, calculate transition, and insert an application record keyed by PR/head/normalized check-set/action. In the same transaction create/resolve Phase 1 Cloud notification and, only for a newly failing enabled policy, insert feedback outbox payload with PR URL, head SHA, failed checks, URLs, and repair instruction. Repeated/pending/unchanged checks add no side effect.

- [ ] **Step 4: Verify and commit**

Run: `cd cloud && go test ./internal/postgres ./internal/notification -count=1`

Expected: PASS.

```bash
git add cloud/internal/postgres cloud/internal/githubapp cloud/internal/notification
git commit -m "feat(cloud): persist github ci failure effects"
```

### Task 5: Dispatch feedback via existing terminal transport

**Files:**
- Create: `cloud/internal/cifeedback/dispatcher.go`, `cloud/internal/cifeedback/dispatcher_test.go`
- Modify: `cloud/cmd/ao-cloud/main.go`, `cloud/internal/postgres/ci_feedback_store.go`

**Interfaces:**
- Produces `cifeedback.Dispatcher.Run(context.Context) error` and `RunOnce(context.Context) error`.
- Consumes leased feedback items and `EnsureWorkerAgentTerminal`/ `QueueTerminalInput`.

- [ ] **Step 1: Write failing dispatcher tests**

```go
func TestDispatcherQueuesOneCIFailurePrompt(t *testing.T) {
  require.NoError(t, dispatcher.RunOnce(ctx))
  require.Contains(t, store.terminalInput(sessionID), "CI failure detected")
  require.Equal(t, 1, store.completedFeedbackCount())
}
func TestUnavailableTerminalLeavesFeedbackRetryable(t *testing.T) {
  store.setTerminalUnavailable(sessionID)
  require.Error(t, dispatcher.RunOnce(ctx))
  require.Equal(t, "retry", store.feedbackStatus(feedbackID))
}
```

- [ ] **Step 2: Confirm failure**

Run: `cd cloud && go test ./internal/cifeedback -run 'Test(DispatcherQueuesOneCIFailurePrompt|UnavailableTerminalLeavesFeedbackRetryable)' -count=1`

Expected: FAIL because package does not exist.

- [ ] **Step 3: Implement lease-safe handoff**

```go
func (d *Dispatcher) RunOnce(ctx context.Context) error {
  item, err := d.store.ClaimCIFeedback(ctx, d.owner, time.Now().Add(d.lease))
  if errors.Is(err, postgres.ErrNotFound) { return nil }
  terminal, err := d.store.EnsureWorkerAgentTerminal(ctx, item.OrgID, item.SessionID, "", 0, d.terminalTTL)
  if err != nil { return d.store.RetryCIFeedback(ctx, item.ID, d.owner, err, d.retryAt()) }
  if err := d.store.QueueTerminalInput(ctx, terminal, item.ApplicationKey, item.Prompt); err != nil {
    return d.store.RetryCIFeedback(ctx, item.ID, d.owner, err, d.retryAt())
  }
  return d.store.CompleteCIFeedback(ctx, item.ID, d.owner)
}
```

Start exactly one dispatcher in `ao-cloud`. A durable terminal request is acceptance; live WebSocket delivery and reconnect replay remain owned by existing transport. Never use Docker/NodeOps/Coder SDKs.

- [ ] **Step 4: Verify and commit**

Run: `cd cloud && go test ./internal/cifeedback ./internal/postgres ./internal/httpapi ./cmd/ao-cloud -count=1`

Expected: PASS.

```bash
git add cloud/internal/cifeedback cloud/internal/postgres/ci_feedback_store.go cloud/cmd/ao-cloud/main.go
git commit -m "feat(cloud): deliver automatic ci feedback"
```

### Task 6: Add Cloud policy API and use it from the existing control

**Files:**
- Modify: `cloud/internal/httpapi/{server,resource_handlers}.go` and tests
- Modify: `frontend/src/renderer/lib/cloud-cp/{types,client}.ts`
- Modify: `frontend/src/renderer/hooks/useWorkspaceQuery.ts`, `frontend/src/renderer/components/SessionInspector.tsx`, and tests

**Interfaces:**
- Produces `PATCH /api/cloud/v1/orgs/{orgId}/sessions/{sessionId}/auto-inject-ci`.
- Adds `autoInjectCI` to `sessionResponse` and `CloudCpSession`.
- Produces `cloudCpClient.setSessionAutoInjectCI(orgID, sessionID, enabled)`.

- [ ] **Step 1: Write failing API and component tests**

```go
func TestSetCloudSessionAutoInjectCI(t *testing.T) {
  response := patchJSON(t, cloudPolicyURL(orgID, sessionID), `{"autoInjectCI":false}`)
  require.Equal(t, http.StatusOK, response.Code)
  require.False(t, decodeSession(t, response).AutoInjectCI)
}
```

```tsx
it("uses cloud policy endpoint, never local API", async () => {
  renderCloudInspector({ autoInjectCI: true });
  await user.click(screen.getByRole("switch", { name: /automatically fix ci failures/i }));
  expect(cloudCpClient.setSessionAutoInjectCI).toHaveBeenCalledWith(orgId, sessionId, false);
  expect(apiClient.PATCH).not.toHaveBeenCalledWith("/api/v1/sessions/{sessionId}/auto-inject-ci", expect.anything());
});
```

- [ ] **Step 2: Confirm failure**

Run: `cd cloud && go test ./internal/httpapi -run TestSetCloudSessionAutoInjectCI -count=1 && cd ../frontend && npm test -- --run src/renderer/components/SessionInspector.test.tsx`

Expected: FAIL because Cloud route/client branch does not exist.

- [ ] **Step 3: Implement authorization and UI branch**

Authorize the session through existing org membership, validate UUID/body, persist `autoInjectCI`, and return normal Cloud session DTO. Preserve optimistic update/rollback; only Cloud offering uses Cloud client API. Local offering leaves the current local PATCH unchanged. Map policy and PR facts through existing shared reducer so failing CI is validating/fixing only when enabled, otherwise needs-review/CI-failing.

- [ ] **Step 4: Verify and commit**

Run: `cd cloud && go test ./internal/httpapi -count=1 && cd ../frontend && npm run typecheck && npm test -- --run src/renderer/components/SessionInspector.test.tsx src/renderer/hooks/useWorkspaceQuery.test.tsx`

Expected: PASS.

```bash
git add cloud/internal/httpapi frontend/src/renderer/lib/cloud-cp frontend/src/renderer/hooks/useWorkspaceQuery.ts frontend/src/renderer/components/SessionInspector.tsx
git commit -m "feat(cloud): control automatic ci feedback"
```

### Task 7: Verify full behavior and document GitHub App webhook setup

**Files:**
- Modify: relevant GitHub/HTTP integration tests
- Modify: `cloud/scripts/test-cloud-local.sh`, `cloud/docs/control-plane.md`
- Modify: `README.md` only if it already describes Cloud GitHub configuration

**Interfaces:**
- Consumes signed fixtures, Cloud notification API, Cloud session/PR API, and fake worker terminal.
- Produces a reproducible local check for enabled/disabled policy and duplicate delivery recovery.

- [ ] **Step 1: Write failing integrated fixture**

```go
func TestSignedFailureUpdatesCloudAndConditionallyInjects(t *testing.T) {
  enabled := createCloudSession(t, true)
  disabled := createCloudSession(t, false)
  deliverSignedCheckFailure(t, enabled.PR)
  deliverSignedCheckFailure(t, disabled.PR)
  require.Equal(t, "failing", cloudPR(t, enabled.ID).CI.State)
  require.Equal(t, "failing", cloudPR(t, disabled.ID).CI.State)
  require.Eventually(t, workerReceivedCIInstruction(enabled.ID))
  require.Never(t, workerReceivedCIInstruction(disabled.ID))
}
```

- [ ] **Step 2: Confirm failure before final integration**

Run: `cd cloud && go test ./internal/githubapp ./internal/httpapi -run TestSignedFailureUpdatesCloudAndConditionallyInjects -count=1`

Expected: FAIL until prior tasks are integrated.

- [ ] **Step 3: Add test-only tunnel documentation and script assertions**

Document `<public-HTTPS-tunnel>/api/cloud/v1/github/webhooks`, the webhook secret, and subscriptions: `pull_request`, `check_suite`, `check_run`, `pull_request_review`. State the public tunnel is local-test-only; production uses configured Cloud HTTPS URL. Script assertions cover durable notification, PR fact, one enabled input, zero disabled inputs, duplicate no-op, and reconnect replay.

- [ ] **Step 4: Run final validation**

Run: `cd cloud && go test ./... && go vet ./... && cd .. && npm run frontend:typecheck && npm run lint`

Expected: PASS. If Docker is available: `npm run cloud:local` then the signed-fixture section of `cloud/scripts/test-cloud-local.sh`.

- [ ] **Step 5: Commit verification/docs**

```bash
git add cloud/internal/githubapp cloud/internal/httpapi cloud/scripts/test-cloud-local.sh cloud/docs/control-plane.md README.md
git commit -m "test(cloud): verify github ci webhook automation"
```

## Plan Self-Review

- Spec coverage: Tasks 1–5 cover persistence, signed ingress, authoritative facts, durable notification effects, and feedback; Task 6 covers Cloud UI and derived Kanban behavior; Task 7 covers all-platform verification and tunnel configuration.
- Placeholder scan: all interfaces and test targets are defined by the task that introduces them; no local/OAuth work is delegated implicitly.
- Type consistency: `AutoInjectCI`, `application_key`, `ApplyGitHubPRObservation`, and `Dispatcher.RunOnce` have one spelling and producing task.
- Review Focus: Tasks 2–5 each add the listed test coverage.

