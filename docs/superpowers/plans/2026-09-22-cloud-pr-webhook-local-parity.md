# Cloud PR Webhook Local-Parity Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make AO Cloud update and react to every GitHub PR fact supported by local, using GitHub App webhooks as the fast path and the unchanged 30-second scanner as reconciliation.

**Architecture:** Webhooks remain durable invalidation signals. The Cloud GitHub service resolves the affected tracked PRs, fetches one authoritative normalized snapshot with an installation token, and transactionally updates Cloud PR/review/comment facts plus local-parity side effects. The shared inspector consumes the complete Cloud summary; worker feedback uses the existing durable terminal transport.

**Tech Stack:** Go 1.x, chi, pgx/PostgreSQL with RLS and goose migrations, GitHub REST/GraphQL APIs, React/TypeScript, TanStack Query, Vitest.

**Spec:** `docs/superpowers/specs/2026-09-22-cloud-pr-webhook-local-parity-design.md`

## Global Constraints

- Do not modify any file under `backend/`; local is read-only reference material.
- Docker, NodeOps, and Coder must use the same provider-neutral control-plane path.
- Do not remove, slow, or otherwise change the existing 30-second scanner.
- Do not create commits unless the user explicitly requests one.
- GitHub payloads are invalidation hints; installation routing and authoritative provider reads determine tenant state.
- Bell behavior must match local: `needs_input`, `ready_to_merge`, `pr_merged`, and `pr_closed_unmerged`; do not create new `pr_opened` or `ci_failed` rows.
- Preserve existing uncommitted work and avoid unrelated cleanup.

## Review Focus

- Out-of-order or duplicate webhook deliveries must converge without duplicate notifications or terminal prompts; covered in Tasks 1, 4, and 5.
- A partial review-thread page must not erase older unresolved feedback; covered in Tasks 2 and 3.
- Review/comment bodies containing control bytes must be sanitized before terminal injection; covered in Task 5.
- A disabled toggle must preserve visible facts while suppressing only worker delivery; covered in Tasks 4, 5, and 7.
- Unknown installations, repositories, SHAs, and PRs must complete as tenant-safe no-ops rather than retry forever; covered in Tasks 1 and 6.

---

### Task 1: Accept and route every local-parity webhook trigger

**Files:**
- Modify: `cloud/internal/httpapi/github_handlers.go`
- Modify: `cloud/internal/httpapi/github_webhook_phase2_test.go`
- Modify: `cloud/internal/githubapp/webhook_scm.go`
- Modify: `cloud/internal/githubapp/webhook_scm_test.go`
- Modify: `cloud/internal/githubapp/service.go`

**Interfaces:**
- Consumes: `domain.GitHubWebhookDelivery`, installation/repository routing, tracked PR lookup.
- Produces: `scmWebhookTargets(event string, payload []byte) (scmWebhookTargetSet, error)` and dispatch of PR-specific or repository/SHA-specific authoritative refreshes.

- [ ] **Step 1: Write failing intake tests**

Add table cases proving `pull_request_review_comment`, `pull_request_review_thread`, `status`, and `push` are accepted alongside existing events, and that PR numbers/head SHAs/repository refs are extracted from their real payload envelopes.

```go
for _, event := range []string{
    "pull_request", "pull_request_review", "pull_request_review_comment",
    "pull_request_review_thread", "check_run", "check_suite", "status", "push",
} {
    if !supportedGitHubWebhookEvent(event) { t.Fatalf("%s not supported", event) }
}
```

- [ ] **Step 2: Run the intake tests and verify RED**

Run: `cd cloud && go test ./internal/httpapi -run 'TestSupportedGitHubWebhookEvent|TestGitHubWebhookPullRequestNumber'`

Expected: FAIL because the four new event types are rejected or unresolved.

- [ ] **Step 3: Add failing routing tests**

Cover PR-specific review/comment/thread events, SHA-specific status/check events, repository-wide push events, malformed payloads, and unknown tracked objects. Assert unknown objects return success/no-op and malformed JSON returns `postgres.ErrInvalid`.

- [ ] **Step 4: Run routing tests and verify RED**

Run: `cd cloud && go test ./internal/githubapp -run 'TestSCMWebhook'`

Expected: FAIL on the new target forms.

- [ ] **Step 5: Implement the target parser and dispatcher**

Introduce explicit target data rather than overloading one PR number:

```go
type scmWebhookTargetSet struct {
    PullRequestNumber int
    HeadSHA           string
    BeforeSHA         string
    AfterSHA          string
    RepositoryWide    bool
}
```

Route PR events by number, check/status events by PR number or SHA, and push by repository plus before/after SHA. Extend `githubapp.Store` only with the narrow tracked-PR lookup methods needed by those routes.

- [ ] **Step 6: Run Task 1 tests and checkpoint without committing**

Run: `cd cloud && go test ./internal/httpapi ./internal/githubapp`

Expected: PASS.

---

### Task 2: Fetch one authoritative GitHub PR snapshot

**Files:**
- Create: `cloud/internal/githubapp/scm_snapshot.go`
- Create: `cloud/internal/githubapp/scm_snapshot_test.go`
- Modify: `cloud/internal/githubapp/client.go`
- Modify: `cloud/internal/domain/pull_request.go`

**Interfaces:**
- Consumes: installation token, `domain.PullRequestRef`.
- Produces: `FetchPullRequestSnapshot(ctx context.Context, ref domain.PullRequestRef) (domain.PullRequestSnapshot, error)` containing metadata, checks/status contexts, submitted reviews, threads/comments, and mergeability.

- [ ] **Step 1: Define failing normalization tests**

Fixture a GraphQL response containing PR metadata, a check run, a legacy status context, `COMMENTED` and `CHANGES_REQUESTED` reviews, resolved and unresolved threads, a bot reply, and pagination flags. Assert normalized values match local's vocabulary.

```go
if got.Reviews[0].State != contract.ReviewNone || got.Reviews[0].Body != "looks good with one note" {
    t.Fatalf("commented review = %+v", got.Reviews[0])
}
```

- [ ] **Step 2: Run snapshot tests and verify RED**

Run: `cd cloud && go test ./internal/githubapp -run 'TestNormalizePullRequestSnapshot|TestFetchPullRequestSnapshot'`

Expected: FAIL because the snapshot types/fetcher do not exist.

- [ ] **Step 3: Add snapshot domain types**

Define focused Cloud types:

```go
type PullRequestSnapshot struct {
    Observation PullRequestObservation
    AuthorAvatarURL string
    BaseSHA, MergeCommitSHA string
    CreatedAtProvider, UpdatedAtProvider, MergedAtProvider, ClosedAtProvider *time.Time
    Checks []PullRequestCheck
    Reviews []PullRequestReview
    Threads []PullRequestReviewThread
    Comments []PullRequestReviewComment
    ReviewsPartial bool
}
```

Each child type carries the provider ID, display fields, bot flag, URLs, timestamps, and resolution/outdated state needed by the shared inspector and dedupe layer.

- [ ] **Step 4: Implement authenticated GraphQL fetch and normalization**

Add an installation-token GraphQL request helper to `client.go`. Query at most 100 check contexts, the bounded latest review summaries, and paged review threads. Follow check-context pages; mark review data partial when the configured review window is exceeded. Preserve the previous snapshot on any provider error.

- [ ] **Step 5: Test partial pages and hostile bodies**

Add cases for no checks, `hasNextPage`, missing authors, unknown review states, deleted comments, and bodies containing escape/control bytes. Normalization preserves raw visible text for UI storage; sanitization belongs to terminal formatting in Task 5.

- [ ] **Step 6: Run Task 2 tests and checkpoint without committing**

Run: `cd cloud && go test ./internal/githubapp -run 'Test.*PullRequestSnapshot'`

Expected: PASS.

---

### Task 3: Persist the normalized review/read model transactionally

**Files:**
- Create: `cloud/internal/postgres/migrations/00047_cloud_pr_observation_parity.sql`
- Modify: `cloud/internal/postgres/migrate_scm_test.go`
- Create: `cloud/internal/postgres/pull_request_snapshot_store.go`
- Create: `cloud/internal/postgres/pull_request_snapshot_store_test.go`
- Modify: `cloud/internal/postgres/pull_request_store.go`
- Modify: `cloud/internal/domain/pull_request.go`

**Interfaces:**
- Consumes: `ApplyPullRequestSnapshot(ctx, orgID, pullRequestID string, snapshot domain.PullRequestSnapshot) (domain.PullRequestTransition, error)`.
- Produces: durable normalized reviews/threads/comments and one previous/current transition for downstream effects.

- [ ] **Step 1: Write the migration drift test first**

Assert migration 47 creates `ao_pr_reviews` and `ao_pr_review_comments`, extends `ao_pr_review_threads` with bot/outdated fields, adds session `auto_inject_review` and `terminate_on_pr_merge`, and creates RLS policies/indexes.

- [ ] **Step 2: Run migration tests and verify RED**

Run: `cd cloud && go test ./internal/postgres -run TestSCMMigration`

Expected: FAIL because migration 47 is absent.

- [ ] **Step 3: Add migration 47**

Use provider IDs as stable unique keys under a PR. Store the session policy defaults as `TRUE`, matching local. Add an `ao_scm_feedback_outbox` with a unique `application_key`, JSON payload, leasing fields, retry status, and timestamps. Do not alter already-applied migration 46.

- [ ] **Step 4: Write failing PostgreSQL snapshot tests**

Test complete replacement, partial merge, comment resolution, edited bodies, review-policy capture on first observation, cross-org RLS denial, and previous/current transition output.

- [ ] **Step 5: Run store tests and verify RED**

Run: `cd cloud && AO_CLOUD_TEST_DATABASE_URL="$AO_CLOUD_TEST_DATABASE_URL" go test ./internal/postgres -run TestApplyPullRequestSnapshot`

Expected: FAIL because the store method is absent.

- [ ] **Step 6: Implement transactional snapshot application**

Within one `withOrg` transaction, lock the PR, load prior child facts, update aggregate metadata, upsert reviews/threads/comments, delete missing children only for complete snapshots, preserve each record's first-observed `auto_inject_review`, touch the owning session, and return a semantic transition object.

- [ ] **Step 7: Run Task 3 tests and checkpoint without committing**

Run: `cd cloud && go test ./internal/postgres -run 'TestSCMMigration|TestApplyPullRequestSnapshot'`

Expected: PASS.

---

### Task 4: Implement exact local notification transitions

**Files:**
- Create: `cloud/internal/postgres/pr_notification_store.go`
- Create: `cloud/internal/postgres/pr_notification_store_test.go`
- Modify: `cloud/internal/postgres/pull_request_store.go`
- Modify: `cloud/internal/githubapp/webhook_scm.go`
- Modify: `cloud/internal/githubapp/service.go`
- Delete behavior only (not historical rows) from: `cloud/internal/postgres/ci_feedback_store.go`

**Interfaces:**
- Consumes: previous/current `domain.PullRequestSnapshot` transition.
- Produces: `ApplyPullRequestNotificationTransition(...)` creating/resolving `ready_to_merge`, `pr_merged`, or `pr_closed_unmerged` events.

- [ ] **Step 1: Write failing transition tests**

Cover blocked-to-ready, ready-to-blocked, open-to-merged, open-to-closed, duplicate/out-of-order delivery, and non-notifying PR-open/CI-failure/comment changes. Assert created/resolved notification events and `pg_notify` publication.

- [ ] **Step 2: Run tests and verify RED**

Run: `cd cloud && go test ./internal/postgres -run TestPullRequestNotificationTransition`

Expected: FAIL because local-parity transitions are not implemented and extra notification paths remain.

- [ ] **Step 3: Implement the shared readiness predicate and notification reducer**

Mirror local's rule exactly: open, non-draft, CI passing, no changes requested, no actionable unresolved human comments, and mergeability mergeable. Use semantic dedupe keys and resolve ready-to-merge when any condition stops holding.

- [ ] **Step 4: Stop producing non-local bell types**

Remove calls that create `pr_opened` and `ci_failed` notification rows while preserving PR facts, worker feedback, existing notification-history reads, and existing historical rows.

- [ ] **Step 5: Run Task 4 tests and checkpoint without committing**

Run: `cd cloud && go test ./internal/postgres ./internal/githubapp -run 'Test.*Notification|TestSCMWebhook'`

Expected: PASS.

---

### Task 5: Deliver CI, review, comment, and conflict feedback like local

**Files:**
- Create: `cloud/internal/postgres/scm_feedback_store.go`
- Create: `cloud/internal/postgres/scm_feedback_store_test.go`
- Create: `cloud/internal/scmfeedback/dispatcher.go`
- Create: `cloud/internal/scmfeedback/dispatcher_test.go`
- Modify: `cloud/internal/app/app.go`
- Modify: `cloud/internal/domain/ci_feedback.go`
- Modify: `cloud/internal/postgres/ci_feedback_store.go`

**Interfaces:**
- Consumes: semantic PR transition plus session policies.
- Produces: leased `domain.SCMFeedback` records accepted by the existing durable terminal-input request transport.

- [ ] **Step 1: Write failing policy/dedupe tests**

Assert CI failure queues only with `auto_inject_ci`; unresolved human inline feedback and changes-requested reviews queue only with captured `auto_inject_review`; ordinary `COMMENTED` reviews and bot-only feedback do not queue; conflicts queue once per conflict signature; repeated snapshots do not duplicate rows.

- [ ] **Step 2: Run store tests and verify RED**

Run: `cd cloud && go test ./internal/postgres -run TestSCMFeedback`

Expected: FAIL because provider-neutral feedback storage does not exist.

- [ ] **Step 3: Implement effect keys and sanitized messages**

Use CI head/fingerprint keys, provider review IDs, provider comment IDs plus revisions, and head/base conflict signatures. Strip C0/C1 control bytes before placing attacker-controlled author/body/path/URL data into terminal input.

- [ ] **Step 4: Write failing dispatcher tests**

Test live delivery, disconnected durable acceptance, retry after transport failure, stale worker epoch recovery, and exactly-once application-key behavior.

- [ ] **Step 5: Run dispatcher tests and verify RED**

Run: `cd cloud && go test ./internal/scmfeedback`

Expected: FAIL before the dispatcher is implemented.

- [ ] **Step 6: Implement and wire the dispatcher**

Follow the existing CI dispatcher lifecycle and terminal-request API, but consume provider-neutral `SCMFeedback`. Keep migration-46 CI rows drainable during local development or migrate pending rows transactionally in migration 47.

- [ ] **Step 7: Run Task 5 tests and checkpoint without committing**

Run: `cd cloud && go test ./internal/scmfeedback ./internal/postgres`

Expected: PASS.

---

### Task 6: Make every webhook use the authoritative application path

**Files:**
- Modify: `cloud/internal/githubapp/webhook_scm.go`
- Modify: `cloud/internal/githubapp/webhook_scm_test.go`
- Modify: `cloud/internal/githubapp/pull_request_status.go`
- Modify: `cloud/internal/githubapp/pull_request_status_test.go`
- Modify: `cloud/internal/postgres/pull_request_store.go`

**Interfaces:**
- Consumes: target resolution from Task 1, snapshot fetcher from Task 2, transactional application from Tasks 3–5.
- Produces: one `RefreshAndApplyPullRequestSnapshot(ctx, ref)` entrypoint shared by webhooks and the existing scanner.

- [ ] **Step 1: Write failing end-to-end service tests**

For every supported event, assert the correct tracked PR set is refreshed and one current snapshot is applied. Include out-of-order events, unknown SHA/repo/PR, provider 404/rate limit, and push affecting multiple tracked open PRs.

- [ ] **Step 2: Run service tests and verify RED**

Run: `cd cloud && go test ./internal/githubapp -run 'TestProcessSCMWebhook|TestRefreshAndApply'`

Expected: FAIL until all events share the new entrypoint.

- [ ] **Step 3: Implement the shared refresh/application entrypoint**

Replace the narrow status refresh in `processSCMWebhook` with the authoritative snapshot flow. Update the existing scanner to call the same method without changing its ticker duration, scheduling, or start/stop behavior.

- [ ] **Step 4: Prove the fallback interval is unchanged**

Keep/add a test asserting the scanner is configured for `30*time.Second`, and inspect startup wiring to ensure no new poller exists.

- [ ] **Step 5: Run Task 6 tests and checkpoint without committing**

Run: `cd cloud && go test ./internal/githubapp`

Expected: PASS.

---

### Task 7: Expose complete Cloud summaries and policies to the shared UI

**Files:**
- Modify: `cloud/internal/httpapi/pull_request_handlers.go`
- Modify: `cloud/internal/httpapi/pull_request_handlers_test.go`
- Modify: `cloud/internal/httpapi/resource_handlers.go`
- Modify: `cloud/internal/postgres/project_session_store.go`
- Modify: `frontend/src/renderer/lib/cloud-cp/types.ts`
- Modify: `frontend/src/renderer/lib/cloud-cp/client.ts`
- Modify: `frontend/src/renderer/lib/cloud-cp/client.test.ts`
- Modify: `frontend/src/renderer/hooks/useSessionScmSummary.ts`
- Modify: `frontend/src/renderer/hooks/useSessionScmSummary.test.tsx`
- Modify: `frontend/src/renderer/components/SessionInspector.tsx`
- Modify: `frontend/src/renderer/components/SessionInspector.test.tsx`

**Interfaces:**
- Consumes: normalized Cloud PR summary and session policies.
- Produces: the existing shared `SessionPRSummary` shape and Cloud policy mutations without local API calls.

- [ ] **Step 1: Write failing HTTP response tests**

Assert reviews include `COMMENTED` summaries and bodies, unresolved/resolved reviewer groups include comment locations and captured injection flags, mergeability reasons include unresolved comments, and session responses expose `autoInjectReview` plus `terminateOnPrMerge`.

- [ ] **Step 2: Run Cloud HTTP tests and verify RED**

Run: `cd cloud && go test ./internal/httpapi -run 'Test.*PullRequest|Test.*SessionPolicy'`

Expected: FAIL on placeholder review arrays/policies.

- [ ] **Step 3: Implement Cloud response assembly and policy endpoints**

Read normalized child rows, construct the same semantic summary as local, and add Cloud session policy updates with optimistic-concurrency-safe store writes. Do not import local controller packages or call local daemon routes.

- [ ] **Step 4: Write failing frontend mapping/rendering tests**

Fixture the user's `COMMENTED` review and inline feedback. Assert it appears in the existing Reviews panel, disabled policies show “not sent,” Cloud toggles call Cloud endpoints, and no local `/api/v1/...` policy route is invoked.

- [ ] **Step 5: Run frontend tests and verify RED**

Run: `cd frontend && npm test -- --run src/renderer/hooks/useSessionScmSummary.test.tsx src/renderer/components/SessionInspector.test.tsx src/renderer/lib/cloud-cp/client.test.ts`

Expected: FAIL before the complete Cloud mapping is implemented.

- [ ] **Step 6: Implement the minimal Cloud adapters**

Map Cloud `repository` to shared `repo`, preserve all review/comment fields, invalidate the affected Cloud session/PR queries on durable events, and branch only the policy mutation transport based on Cloud context. Keep shared presentation logic unchanged.

- [ ] **Step 7: Run Task 7 tests and checkpoint without committing**

Run the Cloud HTTP and targeted frontend commands above.

Expected: PASS.

---

### Task 8: Integration verification and runtime proof

**Files:**
- Modify: `cloud/internal/githubapp/webhook_scm_test.go`
- Modify: `cloud/internal/postgres/pull_request_snapshot_store_test.go`
- Modify: `frontend/src/renderer/components/SessionsBoard.test.tsx` only if Cloud fixtures require the complete summary.
- Modify: `docs/superpowers/plans/2026-09-22-cloud-pr-webhook-local-parity.md` checkboxes only.

**Interfaces:**
- Consumes: all previous tasks.
- Produces: evidence that one provider-neutral path works across Cloud providers and that local files remain untouched.

- [ ] **Step 1: Add signed fixture integration coverage**

Exercise PR open/synchronize, status/check failure/recovery, submitted comment/change-request/approval, inline comment create/edit/resolve, conflict/ready, merge, and closed-unmerged. Assert database state, notification events, and feedback-outbox effects after each transition.

- [ ] **Step 2: Run focused suites**

Run:

```bash
cd cloud && go test ./internal/githubapp ./internal/postgres ./internal/httpapi ./internal/scmfeedback
cd cloud && go vet ./...
cd frontend && npm run typecheck
cd frontend && npm test -- --run src/renderer/hooks/useSessionScmSummary.test.tsx src/renderer/components/SessionInspector.test.tsx src/renderer/lib/cloud-cp/client.test.ts src/renderer/components/SessionsBoard.test.tsx
```

Expected: PASS.

- [ ] **Step 3: Run complete suites and report unrelated environmental failures exactly**

Run:

```bash
cd cloud && go test ./...
cd frontend && npm test -- --run
```

Expected: all feature-relevant tests pass; any pre-existing native ABI/test-harness failures are named rather than hidden.

- [ ] **Step 4: Run repository hygiene checks**

Run:

```bash
git diff --check
git diff --name-only -- backend
git status --short
```

Expected: no whitespace errors and no changed `backend/` files.

- [ ] **Step 5: Restart and perform a live webhook-path test**

Start the local Cloud stack with the configured public URL and corrected webhook secret, start the frontend, perform one real GitHub review/comment transition, and verify timestamps for webhook receipt, durable snapshot/notification event, SSE invalidation, UI update, and any eligible worker input. Confirm logs still report a 30-second scanner interval.

- [ ] **Step 6: Final verification checkpoint without committing**

Record commands, pass/fail counts, runtime URLs, and any test limitations in the handoff. Do not commit or push until explicitly requested.
