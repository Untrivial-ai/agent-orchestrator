# Cloud Notification Foundation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver low-latency, durable, source-tagged cloud notifications for agent events across Docker, NodeOps, and Coder without changing local notification behavior.

**Architecture:** Agent hooks append stable events to a worker-local SQLite outbox under `AO_DATA_DIR`; the running worker flushes them over the cloud terminal relay, with authenticated HTTP as the fallback. The control plane derives tenancy from worker claims, deduplicates and persists notifications plus an ordered event stream in PostgreSQL, acknowledges only after commit, and exposes REST/SSE recovery APIs. The cloud frontend renders immediate hints as pending, replaces them with durable records, and visibly tags them as `Cloud`.

**Tech Stack:** Go 1.26, modernc SQLite, PostgreSQL/pgx/goose, chi HTTP, coder/websocket, Electron main-process stream proxy, React 19, TanStack Query, Vitest.

**Spec:** `docs/superpowers/specs/2026-09-21-cloud-notification-foundation-design.md`

## Global Constraints

- Modify only cloud-owned Go code, cloud migrations/configuration, and cloud-gated frontend code; do not modify local daemon, SQLite, SCM observer, lifecycle, or API contracts.
- The control-plane PostgreSQL database is the permanent authority; the sandbox outbox is temporary retry durability.
- Delete an outbox event only after the control plane acknowledges a durable insert or a durable duplicate.
- Derive organization, project, session, recipient, worker, and epoch from authenticated server-side state; never trust them from an event payload.
- Use one provider-neutral worker implementation for Docker, NodeOps, and Coder.
- Cloud notification DTOs always carry `source: "cloud"`; local notifications remain visually and behaviorally unchanged.
- The client may deduplicate presentation but never owns transport or semantic deduplication.
- Phase 1 does not implement GitHub PR/check/review webhook handling, PR-panel refreshes, or CI-failure injection.

## Review Focus

- A worker reconnects with a new epoch while an old outbox row remains: the old row must not become a notification owned by the replacement worker.
- The relay commits an event but loses the ACK: retry must return a successful duplicate ACK and create one inbox row.
- The client receives a durable confirmation before its best-effort hint: it must render one durable row and discard the late hint.
- A user belongs to two organizations: list, read, and stream operations must never cross tenant or recipient boundaries.
- The live stream loses a PostgreSQL wake-up or a sequence: REST/event replay must converge without duplicate toasts.

---

### Task 1: Define cloud notification domain and PostgreSQL authority

**Files:**
- Create: `cloud/internal/domain/notification.go`
- Create: `cloud/internal/postgres/migrations/00040_cloud_notifications.sql`
- Create: `cloud/internal/postgres/notification_store.go`
- Create: `cloud/internal/postgres/notification_store_test.go`
- Modify: `cloud/internal/httpapi/server.go`

**Interfaces:**
- Produces: `domain.AgentNotificationEvent`, `domain.Notification`, `domain.NotificationEvent`, `domain.NotificationPage`, and `domain.NotificationFilter`.
- Produces store methods `AcceptNotificationEvent`, `ClaimNotificationEvent`, `CompleteNotificationEvent`, `RetryNotificationEvent`, `CreateNotificationFromIngress`, `ListNotifications`, `ListNotificationEvents`, and `MarkNotificationsRead`.
- Consumes authenticated worker identity as separate method parameters; no identity fields exist in the public event payload.

- [ ] **Step 1: Write failing domain and store contract tests**

Add table tests that pin valid types/statuses, cursor ordering, transport idempotency, semantic deduplication, epoch fencing, and recipient isolation. Define the public event shape exactly:

```go
type AgentNotificationEvent struct {
	EventID    string          `json:"eventId"`
	Type       NotificationType `json:"type"`
	OccurredAt time.Time       `json:"occurredAt"`
	Payload    json.RawMessage `json:"payload"`
}

type Notification struct {
	ID, OrgID, RecipientUserID, ProjectID, SessionID string
	Source, Type, Title, Body, DedupeKey, Status      string
	Metadata                                          json.RawMessage
	ResolvedAt                                        *time.Time
	CreatedAt, UpdatedAt                              time.Time
}
```

The stale-epoch test must call `AcceptNotificationEvent` with an epoch that no longer matches `ao_worker_connections` and expect `postgres.ErrStaleWorker`. The duplicate test must submit the same `(worker_id, worker_epoch, event_id)` twice and assert one ingress row.

- [ ] **Step 2: Run the focused tests and verify failure**

Run:

```bash
cd cloud && go test ./internal/domain ./internal/postgres -run 'Notification' -count=1
```

Expected: FAIL because the notification types, migration-backed store, and methods do not exist.

- [ ] **Step 3: Add the migration and domain types**

Create three RLS-protected tables:

```sql
CREATE TABLE ao_notification_ingress (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id UUID NOT NULL REFERENCES ao_organizations(id) ON DELETE CASCADE,
    project_id UUID NOT NULL,
    session_id UUID NOT NULL,
    recipient_user_id UUID,
    worker_id TEXT NOT NULL,
    worker_epoch BIGINT NOT NULL CHECK (worker_epoch > 0),
    event_id TEXT NOT NULL CHECK (btrim(event_id) <> ''),
    event_type TEXT NOT NULL,
    payload JSONB NOT NULL CHECK (jsonb_typeof(payload) = 'object'),
    occurred_at TIMESTAMPTZ NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'claimed', 'complete', 'failed')),
    attempt_count INTEGER NOT NULL DEFAULT 0,
    lease_owner TEXT,
    lease_until TIMESTAMPTZ,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (worker_id, worker_epoch, event_id)
);
```

Add `ao_notifications` with a partial unique index on `(org_id, recipient_user_id, dedupe_key) WHERE resolved_at IS NULL`, and `ao_notification_events` with a `BIGSERIAL sequence`. Add organization-scoped RLS policies to all three tables. Add an `ao_notification_event` trigger channel only through explicit transaction writes; do not add application-side manual duplicates.

- [ ] **Step 4: Implement PostgreSQL operations**

`AcceptNotificationEvent` must lock/validate the current worker connection, derive `project_id` and `created_by_user_id` from `ao_sessions`, insert with `ON CONFLICT DO NOTHING`, and return an accepted result for both the insert and exact duplicate. A collision with the same transport key but different type/payload returns `ErrIdempotencyMismatch`.

`CreateNotificationFromIngress` runs one transaction that creates/reuses the semantic notification, appends `ao_notification_events` only when visible state changed, marks ingress complete, and executes:

```sql
SELECT pg_notify('ao_notification_event', $1)
```

with the organization ID as payload after the durable writes are staged in the transaction.

- [ ] **Step 5: Run focused tests and migration smoke**

Run:

```bash
cd cloud && go test ./internal/domain ./internal/postgres -run 'Notification' -count=1
cd cloud && go test ./internal/postgres -count=1
```

Expected: PASS. If database-backed tests require `AO_CLOUD_TEST_DATABASE_URL`, run them against the repository's Docker PostgreSQL and state the exact skipped coverage when Docker is unavailable.

- [ ] **Step 6: Commit the authority layer**

```bash
git add cloud/internal/domain/notification.go cloud/internal/postgres/migrations/00040_cloud_notifications.sql cloud/internal/postgres/notification_store.go cloud/internal/postgres/notification_store_test.go cloud/internal/httpapi/server.go
git commit -m "feat(cloud): add durable notification storage"
```

### Task 2: Add the cloud notification processor and semantic policy

**Files:**
- Create: `cloud/internal/notification/service.go`
- Create: `cloud/internal/notification/service_test.go`
- Modify: `cloud/cmd/ao-cloud/main.go`

**Interfaces:**
- Consumes Task 1 store methods through a narrow `notification.Store` interface.
- Produces `notification.Service.Run(context.Context)` and `ProcessOne(context.Context) error`.
- Produces pure `BuildNotification(domain.NotificationIngress) (domain.Notification, error)` for deterministic type/title/body/dedup mapping.

- [ ] **Step 1: Write failing processor tests with a fake store**

Cover `needs_input`, `agent_failed`, and `agent_completed`; missing recipient; malformed payload; duplicate semantic keys; transient retry; terminal invalid payload; lease expiry; and context cancellation. Pin mappings:

```go
needs_input    -> "Agent needs input"
agent_failed   -> "Agent stopped with an error"
agent_completed -> "Agent completed its work"
```

Use keys:

```text
needs-input:<session-id>:<activity-id>
agent-failed:<session-id>:<worker-epoch>
agent-completed:<session-id>:<worker-epoch>:<activity-id>
```

- [ ] **Step 2: Verify tests fail**

```bash
cd cloud && go test ./internal/notification -count=1
```

Expected: FAIL because the package does not exist.

- [ ] **Step 3: Implement the leased processor**

Use a one-second wake/fallback ticker, a 30-second lease, a 20-second processing timeout, ten attempts, and capped exponential backoff. A missing recipient marks the ingress complete without a visible notification so migrated sessions with no creator cannot poison the queue.

Expose a non-blocking wake method:

```go
func (s *Service) Wake() {
	select { case s.wake <- struct{}{}: default: }
}
```

- [ ] **Step 4: Wire and verify the processor**

Construct the service in `cloud/cmd/ao-cloud/main.go`, run it under the server context, and route `ao_notification_event` PostgreSQL wake-ups to live API subscribers rather than to the processor's claim loop. Ingest calls `Service.Wake()` directly after acceptance.

Run:

```bash
cd cloud && go test ./internal/notification ./cmd/ao-cloud -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit the processor**

```bash
git add cloud/internal/notification cloud/cmd/ao-cloud/main.go
git commit -m "feat(cloud): process agent notifications"
```

### Task 3: Expose authenticated ingest, inbox, read, and recovery APIs

**Files:**
- Create: `cloud/internal/httpapi/notification_handlers.go`
- Create: `cloud/internal/httpapi/notification_handlers_test.go`
- Create: `cloud/internal/httpapi/notification_stream.go`
- Create: `cloud/internal/httpapi/notification_stream_test.go`
- Modify: `cloud/internal/httpapi/server.go`
- Modify: `cloud/internal/httpapi/transport.go`
- Modify: `cloud/cmd/ao-cloud/main.go`
- Modify: `contracts/cloud/openapi.yaml`

**Interfaces:**
- Produces worker `POST /api/cloud/v1/worker/notification-events`.
- Produces user `GET /api/cloud/v1/orgs/{orgId}/notifications`, `GET /notification-events`, `PATCH /notifications/{id}`, and `POST /notifications/read-all`.
- Produces `Server.HandleNotificationEventNotify(payload string)` for PostgreSQL listener wake-ups.

- [ ] **Step 1: Write failing handler tests**

Test missing scope, stale epoch, unknown JSON fields, non-object/oversized payload, unsupported type, exact duplicate ACK, idempotency mismatch, cross-org access, recipient isolation, invalid cursor, unread/all filtering, explicit ID read, read-all, stream replay, keepalive, sequence ordering, and drain shutdown.

The worker response is:

```json
{"accepted":true,"eventId":"evt_01K...","duplicate":false}
```

and an exact duplicate returns HTTP 200 with `duplicate: true`; a new durable insert returns HTTP 202.

- [ ] **Step 2: Verify handler tests fail**

```bash
cd cloud && go test ./internal/httpapi -run 'Notification' -count=1
```

Expected: FAIL because the routes and store contract methods are absent.

- [ ] **Step 3: Implement validation and REST responses**

Allow only `needs_input`, `agent_failed`, and `agent_completed`; cap event IDs at 128 bytes and payloads at 8 KiB; require RFC3339 timestamps no more than five minutes in the future; and require the new `worker:notification` scope. Add the scope to worker tickets and `workerCapabilities` only after both worker and CP code understand it.

User list responses include:

```json
{
  "items": [],
  "page": {"hasMore": false},
  "unreadCount": 0,
  "latestSequence": 0
}
```

Every notification response includes `source: "cloud"`.

- [ ] **Step 4: Implement durable SSE recovery**

Use `after` as a non-negative JavaScript-safe sequence. Replay `ao_notification_events` in pages of 100, wake immediately from an org-filtered in-memory registry fed by `HandleNotificationEventNotify`, poll every five seconds as a missed-NOTIFY fallback, and write a keepalive every fifteen seconds.

SSE frames use the durable sequence as `id` and event kind as `event`.

- [ ] **Step 5: Update the cloud OpenAPI contract and run tests**

```bash
cd cloud && go test ./internal/httpapi -run 'Notification' -count=1
cd cloud && go test ./...
```

Expected: PASS.

- [ ] **Step 6: Commit the API layer**

```bash
git add cloud/internal/httpapi cloud/cmd/ao-cloud/main.go contracts/cloud/openapi.yaml
git commit -m "feat(cloud): expose notification APIs"
```

### Task 4: Add the sandbox SQLite outbox and hook producers

**Files:**
- Modify: `cloud/go.mod`
- Modify: `cloud/go.sum`
- Create: `cloud/internal/notificationoutbox/outbox.go`
- Create: `cloud/internal/notificationoutbox/outbox_test.go`
- Create: `cloud/internal/notificationoutbox/flusher.go`
- Create: `cloud/internal/notificationoutbox/flusher_test.go`
- Modify: `cloud/cmd/ao-cloud-agent/main.go`
- Modify: `cloud/cmd/ao-worker/main.go`
- Modify: `cloud/internal/worker/protocol.go`

**Interfaces:**
- Produces `notificationoutbox.Open(path string) (*Outbox, error)`.
- Produces `Enqueue`, `Ready`, `MarkRetry`, `Delete`, and `Close` methods.
- Produces `Flusher{Outbox, Deliver, Clock, Logger}.Run(context.Context)`.
- Extends `worker.TerminalStreamFrame` with `EventID`, `EventType`, `OccurredAt`, and `Payload` only for notification frame types.

- [ ] **Step 1: Write failing outbox and flusher tests**

Use `t.TempDir()` and a real SQLite file. Test write-before-deliver, stable IDs, concurrent hook writer/worker reader under WAL mode, startup recovery, ACK deletion, retry retention, exponential backoff with jitter bounds, epoch mismatch discard, 1,000-row/8 MiB capacity bounds, and cancellation.

Include the review-focus case: enqueue under epoch 4, start flusher as epoch 5, and assert the row is discarded without delivery.

- [ ] **Step 2: Verify tests fail**

```bash
cd cloud && go test ./internal/notificationoutbox -count=1
```

Expected: FAIL because the package does not exist.

- [ ] **Step 3: Implement SQLite outbox**

Add `modernc.org/sqlite` to the cloud module. Store at:

```text
<AO_DATA_DIR>/notification-outbox.db
```

Set mode `0600`, enable WAL, `busy_timeout=2000`, and create the bounded schema on open. Do not import the backend SQLite store or write outside `AO_DATA_DIR`.

- [ ] **Step 4: Produce events from the cloud helper**

In `runHook`, continue publishing `agent.activity` exactly as today. Additionally enqueue:

- `needs_input` for `waiting_input` or `blocked`, with stable correlation from `toolUseId` or a hash of harness/event/agent-session ID;
- `agent_failed` for terminal session-end reasons other than clear/resume;
- no `agent_completed` from ordinary `stop`, because Stop means turn-idle and would create noise.

Read `AO_CLOUD_WORKER_EPOCH`, `AO_DATA_DIR`, and `AO_SESSION_ID` from the environment. Hook enqueue is bounded and best-effort so it never breaks the coding agent.

- [ ] **Step 5: Wire the worker flusher**

After bootstrap, export `AO_CLOUD_WORKER_EPOCH`, open the outbox, and run its flusher alongside heartbeat and worker transport. The deliverer first attempts the relay notification frame and falls back to `POST /worker/notification-events` when no stream is live. Both paths return the same durable ACK contract.

- [ ] **Step 6: Run package and worker tests**

```bash
cd cloud && go test ./internal/notificationoutbox ./cmd/ao-cloud-agent ./cmd/ao-worker -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit the sandbox producer**

```bash
git add cloud/go.mod cloud/go.sum cloud/internal/notificationoutbox cloud/cmd/ao-cloud-agent cloud/cmd/ao-worker cloud/internal/worker/protocol.go
git commit -m "feat(cloud): buffer agent notifications in sandboxes"
```

### Task 5: Carry fast hints and durable ACKs through the terminal relay

**Files:**
- Modify: `cloud/internal/workertransport/stream.go`
- Modify: `cloud/internal/workertransport/stream_test.go`
- Modify: `cloud/internal/httpapi/terminal_stream.go`
- Modify: `cloud/internal/httpapi/terminal_stream_test.go`
- Modify: `cloud/internal/httpapi/terminal_handlers.go`
- Modify: `frontend/src/renderer/lib/cloud-terminal-mux.ts`
- Modify: `frontend/src/renderer/lib/cloud-terminal-mux.test.ts`
- Create: `frontend/src/renderer/lib/cloud-notification-hints.ts`
- Create: `frontend/src/renderer/lib/cloud-notification-hints.test.ts`

**Interfaces:**
- Produces worker frames `notification`, CP frames `notification_ack`, and browser frames `notification_hint`.
- Produces `subscribeCloudNotificationHints(listener)` and `publishCloudNotificationHint(hint)` in the renderer.
- Consumes Task 3's durable ingest operation through a shared server method so WS and HTTP validation cannot drift.

- [ ] **Step 1: Write failing relay tests**

Test valid notification fan-out, client saturation without durable loss, DB failure without ACK, duplicate retry ACK, invalid frame rejection, stale worker rejection, notification frames not entering terminal byte replay, and browser hint ordering independent from durable confirmation.

- [ ] **Step 2: Verify relay tests fail**

```bash
cd cloud && go test ./internal/workertransport ./internal/httpapi -run 'Notification|TerminalStream' -count=1
cd frontend && npm test -- src/renderer/lib/cloud-terminal-mux.test.ts src/renderer/lib/cloud-notification-hints.test.ts
```

Expected: FAIL on unknown frames and missing hint bus.

- [ ] **Step 3: Extend worker and CP streams**

Serialize writes under the existing stream mutex. The CP validates the notification envelope, forwards a best-effort hint to connected browser terminal clients, durably accepts the event, and then replies:

```json
{"type":"notification_ack","eventId":"evt_01K...","duplicate":false}
```

An uncertain WS failure leaves the outbox row intact; the HTTP fallback or next stream retries it.

- [ ] **Step 4: Extend the cloud terminal mux**

Decode `notification_hint` separately from terminal output and publish it to the cloud-only hint bus. The bus keeps no permanent history; subscribers own pending UI state. A late hint whose `eventId` is already confirmed is ignored by the cloud notification cache layer added in Task 6.

- [ ] **Step 5: Run relay and mux tests**

Run the Step 2 commands again. Expected: PASS.

- [ ] **Step 6: Commit the fast path**

```bash
git add cloud/internal/workertransport cloud/internal/httpapi/terminal_stream.go cloud/internal/httpapi/terminal_stream_test.go cloud/internal/httpapi/terminal_handlers.go frontend/src/renderer/lib/cloud-terminal-mux.ts frontend/src/renderer/lib/cloud-terminal-mux.test.ts frontend/src/renderer/lib/cloud-notification-hints.ts frontend/src/renderer/lib/cloud-notification-hints.test.ts
git commit -m "feat(cloud): relay notification hints"
```

### Task 6: Add the typed cloud notification client and reconciliation cache

**Files:**
- Modify: `frontend/src/renderer/lib/cloud-cp/types.ts`
- Modify: `frontend/src/renderer/lib/cloud-cp/client.ts`
- Modify: `frontend/src/renderer/lib/cloud-cp/client.test.ts`
- Modify: `frontend/src/renderer/lib/cloud-cp/stream-bridge.ts`
- Modify: `frontend/src/renderer/lib/cloud-cp/index.ts`
- Create: `frontend/src/renderer/lib/cloud-notifications.ts`
- Create: `frontend/src/renderer/lib/cloud-notifications.test.ts`
- Create: `frontend/src/renderer/hooks/useCloudNotifications.ts`
- Create: `frontend/src/renderer/hooks/useCloudNotifications.test.tsx`

**Interfaces:**
- Produces `CloudCpNotification`, `CloudCpNotificationEvent`, and page/read response types with literal `source: "cloud"`.
- Extends `CloudCpClient` with `listNotifications`, `listNotificationEvents`, `markNotificationsRead`, and `subscribeNotificationEvents`.
- Produces source-qualified query keys `['cloud-notifications', baseUrl, orgId, status]`.

- [ ] **Step 1: Write failing client and cache tests**

Test URL encoding, bearer proxy use, pagination, abort, malformed SSE, sequence ordering, reconnect-after cursor, hint-before-confirmation replacement, confirmation-before-hint suppression, duplicate confirmation, resolved update, org/base-URL cache isolation, and HTTP full reconciliation.

The frontend model must require:

```ts
export interface CloudCpNotification {
  id: string;
  source: "cloud";
  eventId?: string;
  orgId: string;
  projectId?: string;
  sessionId?: string;
  type: string;
  title: string;
  body: string;
  status: "unread" | "read";
  resolvedAt?: string;
  createdAt: string;
  updatedAt: string;
}
```

- [ ] **Step 2: Verify frontend tests fail**

```bash
cd frontend && npm test -- src/renderer/lib/cloud-cp/client.test.ts src/renderer/lib/cloud-notifications.test.ts src/renderer/hooks/useCloudNotifications.test.tsx
```

Expected: FAIL because cloud notification APIs and cache do not exist.

- [ ] **Step 3: Implement client methods and stream bridge**

Use the existing Electron main-process cloud CP proxy so bearer tokens never enter renderer code. Add an org-level bridged SSE path and keep its error/abort semantics identical to `subscribeSessionEventsBridged`.

- [ ] **Step 4: Implement pending/durable reconciliation**

Key pending entries by `cloud:eventId` and durable rows by `cloud:notificationId`. Persist only the last durable sequence, never pending bodies. On initial load, reconnect, stream error, or detected sequence gap, fetch durable events after the cursor; if replay is unavailable, invalidate the unread/all REST pages.

- [ ] **Step 5: Run focused tests and typecheck**

```bash
cd frontend && npm test -- src/renderer/lib/cloud-cp/client.test.ts src/renderer/lib/cloud-notifications.test.ts src/renderer/hooks/useCloudNotifications.test.tsx
cd frontend && npm run typecheck
```

Expected: PASS.

- [ ] **Step 6: Commit the frontend data layer**

```bash
git add frontend/src/renderer/lib/cloud-cp frontend/src/renderer/lib/cloud-notifications.ts frontend/src/renderer/lib/cloud-notifications.test.ts frontend/src/renderer/hooks/useCloudNotifications.ts frontend/src/renderer/hooks/useCloudNotifications.test.tsx
git commit -m "feat(cloud): reconcile cloud notifications"
```

### Task 7: Render cloud notifications with explicit source separation

**Files:**
- Create: `frontend/src/renderer/components/CloudNotificationRuntime.tsx`
- Create: `frontend/src/renderer/components/CloudNotificationRuntime.test.tsx`
- Create: `frontend/src/renderer/components/CloudNotificationList.tsx`
- Create: `frontend/src/renderer/components/CloudNotificationList.test.tsx`
- Modify: `frontend/src/renderer/components/NotificationCenter.tsx`
- Modify: `frontend/src/renderer/components/NotificationCenter.test.tsx`
- Modify: `frontend/src/renderer/routes/_shell.tsx`
- Modify: `frontend/src/renderer/i18n/en.json`
- Modify: `frontend/src/renderer/i18n/de.json`
- Modify: `frontend/src/renderer/i18n/es.json`
- Modify: `frontend/src/renderer/i18n/fr.json`
- Modify: `frontend/src/renderer/i18n/ja.json`
- Modify: `frontend/src/renderer/i18n/ko.json`
- Modify: `frontend/src/renderer/i18n/pt-BR.json`
- Modify: `frontend/src/renderer/i18n/zh-CN.json`

**Interfaces:**
- Consumes Task 6 hooks and Task 5 hint subscription.
- Produces a cloud-gated runtime mounted beside the existing local runtime.
- Leaves local notification queries and mutations unchanged.

- [ ] **Step 1: Write failing UI tests**

Test pending hint rendering, durable replacement, unread badge contribution, toast-once behavior, visible `Cloud` chip, local row without the chip, cloud read action routed only to CP, local read action routed only to daemon, same ID from both sources, cloud sign-out cleanup, organization switch cleanup, and absent cloud configuration.

- [ ] **Step 2: Verify UI tests fail**

```bash
cd frontend && npm test -- src/renderer/components/CloudNotificationRuntime.test.tsx src/renderer/components/CloudNotificationList.test.tsx src/renderer/components/NotificationCenter.test.tsx
```

Expected: FAIL because cloud runtime and source presentation do not exist.

- [ ] **Step 3: Implement a cloud-gated runtime**

Mount `CloudNotificationRuntime` only when `useCloudCp().ready` and `useCloudOrg().org` are available. It subscribes to the hint bus and durable CP stream, updates only cloud-qualified caches, and drives cloud toasts. It must not register local EventSource listeners or call local notification endpoints.

- [ ] **Step 4: Add the visible source tag**

Render a compact, low-emphasis chip using the translated label `notifications.sourceCloud = "Cloud"`. The chip is present only for cloud DTOs. Keep the existing local notification row markup and navigation behavior unchanged.

- [ ] **Step 5: Run UI, i18n, and type checks**

```bash
cd frontend && npm test -- src/renderer/components/CloudNotificationRuntime.test.tsx src/renderer/components/CloudNotificationList.test.tsx src/renderer/components/NotificationCenter.test.tsx src/renderer/i18n/renderer-coverage.test.ts
cd frontend && npm run typecheck
```

Expected: PASS.

- [ ] **Step 6: Commit the UI**

```bash
git add frontend/src/renderer/components/CloudNotificationRuntime.tsx frontend/src/renderer/components/CloudNotificationRuntime.test.tsx frontend/src/renderer/components/CloudNotificationList.tsx frontend/src/renderer/components/CloudNotificationList.test.tsx frontend/src/renderer/components/NotificationCenter.tsx frontend/src/renderer/components/NotificationCenter.test.tsx frontend/src/renderer/routes/_shell.tsx frontend/src/renderer/i18n
git commit -m "feat(cloud): display cloud notifications"
```

### Task 8: Verify every cloud provider and the full Phase 1 contract

**Files:**
- Modify: `cloud/scripts/test-cloud-local.sh`
- Create: `cloud/internal/sandbox/notification_conformance_test.go`
- Modify only if a provider omits durable worker data: the relevant files under `cloud/internal/sandbox/docker`, `cloud/internal/sandbox/createos`, or `cloud/internal/sandbox/coder`
- Modify: `cloud/README.md`
- Modify: `cloud/docs/control-plane.md`

**Interfaces:**
- Consumes all previous tasks.
- Produces one shared provider-conformance assertion for Docker, NodeOps/CreateOS, and Coder launch specs.

- [ ] **Step 1: Add failing provider conformance and Docker smoke assertions**

Assert every provider launch supplies a writable, persistent `AO_DATA_DIR`, the same notification capability, and no provider-specific event implementation. Extend the Docker smoke flow to emit one worker event, observe one pending/live signal where supported, recover the durable inbox over REST, resend the same event, and assert one row.

- [ ] **Step 2: Verify the new tests fail before final wiring fixes**

```bash
cd cloud && go test ./internal/sandbox/... -run 'NotificationConformance' -count=1
```

Expected: FAIL for any provider launch that does not preserve `AO_DATA_DIR` or advertise the notification capability.

- [ ] **Step 3: Fix only missing cloud-provider wiring**

Pass the same environment variables, durable data path, worker/helper binaries, and capability through Docker, NodeOps/CreateOS, and Coder. Do not fork notification logic by provider.

- [ ] **Step 4: Run complete validation**

Run narrow checks first, then repository checks:

```bash
cd cloud && go test ./...
cd cloud && go test -race ./...
cd cloud && go vet ./...
cd frontend && npm test
cd frontend && npm run typecheck
cd frontend && npm run build
npm run lint
```

Run the cloud lifecycle smoke test when Docker is available:

```bash
cloud/scripts/test-cloud-local.sh
```

Run the repository workflow validator when its Docker socket requirements are available:

```bash
npx @redwoodjs/agent-ci run --all
```

Expected: all available checks PASS. Report Docker/native-runner/credential gaps precisely rather than labeling them passed.

- [ ] **Step 5: Perform a real cloud UI review**

Start the cloud Docker stack and frontend using the documented development commands. Verify pending-to-durable replacement, `Cloud` tag, unread count, read action, reconnect recovery, and local notification non-regression. Capture logs for any stream retry or dedup failure.

- [ ] **Step 6: Commit conformance and documentation**

```bash
git add cloud/scripts/test-cloud-local.sh cloud/internal/sandbox cloud/README.md cloud/docs/control-plane.md
git commit -m "test(cloud): verify notification delivery across providers"
```

- [ ] **Step 7: Review the complete branch**

Inspect:

```bash
git status --short
git diff --check origin/main...HEAD
git diff --stat origin/main...HEAD
git log --oneline origin/main..HEAD
```

Confirm no files under `backend/` changed and no local notification/API behavior changed. Confirm the branch contains only Phase 1; GitHub webhook and CI automation work remains absent for Phase 2.
