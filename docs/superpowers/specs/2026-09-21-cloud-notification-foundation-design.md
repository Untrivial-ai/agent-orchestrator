# Cloud Notification Foundation Design

**Date:** 2026-09-21
**Phase:** 1 of 2
**Branch:** `cloud/scm-notif`

## Purpose

Build the cloud-only notification foundation used by Docker, NodeOps, and
Coder workers. Agent-originated events should appear immediately in the cloud
client, survive client and control-plane disconnects, become durable in the
control-plane PostgreSQL database, and recover through HTTP when live delivery
is interrupted.

Phase 1 does not process GitHub pull-request, check, or review webhooks. Phase
2 will use this notification foundation for SCM state, the right-side PR panel,
and automatic CI-failure feedback.

The local daemon, local SQLite schema, local SCM observer, local lifecycle
manager, and local notification transport are out of scope and must not be
modified.

## Product requirements

- Agent notifications appear immediately when the live terminal relay is
  healthy.
- A notification is permanent only after the control plane commits it to
  PostgreSQL.
- An agent event remains retryable in the sandbox until the control plane
  acknowledges durable acceptance.
- Duplicate transport attempts and duplicate logical conditions do not create
  duplicate durable notifications.
- A reconnecting client recovers notification history and unread state from
  the control plane over HTTP.
- Every cloud notification carries `source: "cloud"` and renders a visible
  `Cloud` tag. Local notifications remain unchanged.
- One provider-neutral implementation serves Docker, NodeOps, and Coder.

## Architecture

```text
AO agent/activity hook
        |
        v
Sandbox SQLite outbox
        |
        v
Authenticated terminal relay
        |------------------------------|
        v                              v
Client notification_hint       Control-plane intake
        |                              |
        |                       Permanent PostgreSQL
        |                              |
        |                       Durable acceptance ACK
        |                              |
        |<----- notification_created --|
        v
Pending hint becomes durable inbox item
```

The live path optimizes latency. PostgreSQL and the recovery API provide
correctness. A hint is never treated as permanent state.

## Sandbox outbox

Each sandbox uses an embedded SQLite outbox. Running a PostgreSQL server in
every sandbox would add a process, credentials, startup latency, memory use,
and provider-specific lifecycle work without improving this small retry queue.
SQLite gives process-independent persistence for the lifetime of the sandbox
and works identically on Docker, NodeOps, and Coder.

The outbox is temporary durability, not permanent authority. If the entire
sandbox and its volume are destroyed before control-plane acknowledgement, an
unacknowledged event can be lost.

Each row contains:

```text
event_id
event_type
payload
created_at
attempt_count
next_attempt_at
worker_epoch
```

Properties:

- `event_id` is generated once and reused for every retry.
- Inserts happen before the event is offered to the relay.
- Urgent events flush immediately; normal events may be coalesced briefly.
- Failed sends use bounded exponential backoff with jitter.
- A row is deleted only after a durable control-plane acknowledgement for the
  same event and worker epoch.
- Client receipt never authorizes deletion.
- Startup resumes all unacknowledged rows.
- Payload size and row count are bounded so a broken connection cannot consume
  the sandbox disk indefinitely.

The outbox belongs in cloud worker/sandbox code. It must not reuse or modify
the local daemon's SQLite store.

## Event envelope

The relay uses one provider-neutral envelope:

```json
{
  "version": 1,
  "eventId": "evt_01K...",
  "type": "needs_input",
  "occurredAt": "2026-09-21T10:00:00Z",
  "payload": {}
}
```

The sandbox does not provide trusted organization, project, user, or session
ownership. The control plane derives that context from the authenticated
worker JWT, active worker epoch, and stored session relationships.

Phase 1 permits a small allowlist of agent-originated types such as
`needs_input`, `agent_completed`, and `agent_failed`. SCM types are reserved
for Phase 2 producers in the control plane.

## Relay fan-out and acknowledgement

The existing live terminal transport gains typed notification frames. Terminal
bytes and notification frames share the connection but remain distinct
protocol messages.

For each outbox event, the relay independently attempts:

1. A best-effort `notification_hint` to connected clients.
2. An authenticated ingest request to the control plane.

The two attempts may run concurrently; client delivery does not wait for the
database. Failure of the hint does not block durable ingestion, and failure of
durable ingestion does not discard the hint or the outbox row.

The control plane returns an ACK after either:

- inserting the event into its durable ingress table; or
- finding the same event already durably present.

Only that ACK removes the sandbox row.

## Control-plane persistence

Phase 1 adds cloud-only PostgreSQL migrations for three responsibilities.

### Durable ingress

`ao_notification_ingress` stores authenticated agent events before background
processing. Its uniqueness boundary is:

```text
UNIQUE(worker_id, worker_epoch, event_id)
```

It stores processing status, attempt count, lease ownership, next-attempt time,
the derived organization/session context, and the validated event payload.

### Notification inbox

`ao_notifications` stores the user-visible record:

```text
id
org_id
recipient_user_id
project_id
session_id
pull_request_id
source = cloud
type
title
body
metadata
dedupe_key
status = unread | read
resolved_at
created_at
updated_at
```

Row-level security and service-side access checks enforce organization and
recipient isolation. A partial unique constraint on the logical notification
key prevents duplicate unresolved notifications for the same condition.

### Durable notification event stream

`ao_notification_events` records ordered changes such as:

- `notification_created`
- `notification_updated`
- `notification_resolved`

Each row has a monotonic sequence used as the reconnect cursor. The inbox and
event row are written in the same transaction. PostgreSQL `NOTIFY` is only a
wake-up accelerator after commit; the event table remains authoritative if a
wake-up is missed.

## Deduplication

Deduplication is layered, but the control plane is authoritative.

### Transport identity

The durable ingress unique constraint suppresses retries of the exact same
agent event. Duplicate ingestion returns a successful ACK so the sandbox can
clear its row.

### Semantic notification identity

The notification processor derives a stable key for the logical condition,
for example:

```text
needs-input:<session-id>:<activity-id>
agent-failed:<session-id>:<worker-epoch>
```

This prevents different transport events from creating duplicate visible
notifications for the same condition.

### Client presentation identity

The frontend deduplicates pending hints by `(source, eventId)` and durable
records by `(source, notificationId)`. When a durable event references a
pending `eventId`, the pending item is replaced in place.

Client deduplication is presentation-only. It cannot authorize acknowledgements
or permanent deletion because clients may disconnect, lose storage, or run on
multiple devices.

## Notification processing

A background control-plane processor leases ingress rows and, in one
transaction:

1. Revalidates the event type and current session/worker epoch.
2. Derives the recipient, project, and target session.
3. Inserts or reuses the semantic notification.
4. Appends a durable notification event when visible state changed.
5. Marks the ingress row complete.

Invalid payloads become terminal failures. Transient database or processing
errors retry with bounded exponential backoff. Logs and metrics include event
type and internal identifiers but never notification bodies or credentials.

## APIs

Phase 1 adds authenticated cloud endpoints equivalent to:

```text
POST  /api/cloud/v1/worker/notification-events
GET   /api/cloud/v1/orgs/{orgId}/notifications
GET   /api/cloud/v1/orgs/{orgId}/notification-events?after=<sequence>
PATCH /api/cloud/v1/orgs/{orgId}/notifications/{notificationId}
POST  /api/cloud/v1/orgs/{orgId}/notifications/read-all
```

The worker endpoint uses worker authentication and never accepts tenant
ownership from the request body. User endpoints use the existing user
principal and organization membership checks.

Notification listing supports unread/all filters, cursor pagination, and an
unread count. Updating a notification supports marking it read. Resolution is
owned by control-plane producers rather than a client action.

## Live delivery and HTTP recovery

Live messages include:

```text
notification_hint
notification_created
notification_updated
notification_resolved
notification_ack
```

The durable messages carry the control-plane event sequence. A client stores
the latest applied sequence.

On initial load, reconnect, sequence gap, or stream replacement, the client:

1. Requests notification events after its last sequence.
2. Applies events in sequence order.
3. Fetches the current unread inbox when its cursor is absent or outside the
   retained replay window.
4. Replaces matching pending hints with durable records.
5. Reconciles read and resolved state.

HTTP recovery reads only permanent control-plane data. It cannot recover a
hint that has not yet reached PostgreSQL; that event remains the sandbox
outbox's responsibility.

## Frontend separation from local

Cloud notification DTOs and live frames always include:

```json
{ "source": "cloud" }
```

The frontend uses source-qualified cache identities and routes mutations to
the matching backend. Cloud read/history operations call the control plane;
local operations continue calling the loopback daemon.

The visible notification row and toast include a compact `Cloud` tag. Local
notifications retain their existing appearance. Identical IDs from the two
sources cannot collide, and switching between local and cloud projects cannot
leak cached notifications across modes.

Shared presentational components may be reused, but Phase 1 must not alter
local API contracts, persistence, event transport, or behavior.

## All-cloud-platform support

No notification logic is implemented in Docker-, NodeOps-, or Coder-specific
provisioning code. Each provider receives the same worker binary, embedded
outbox, event envelope, retry policy, and authenticated relay protocol.

Provider adapters are responsible only for starting the existing worker and
mounting its normal durable sandbox data path. Conformance tests exercise the
same notification scenario against every supported provider configuration.

## Failure behavior

- **Client disconnected:** durable ingest continues; REST recovers the inbox.
- **Control plane unavailable:** the sandbox retains and retries the event.
- **Relay retries after an uncertain response:** ingress uniqueness returns an
  idempotent ACK.
- **PostgreSQL wake-up missed:** live readers replay the durable event table or
  recover over HTTP.
- **Processor restart:** leased ingress rows become claimable after lease
  expiry.
- **Multiple connected clients:** each reconciles the same durable sequence;
  read state remains server-owned.
- **Sandbox destroyed before ACK:** the unacknowledged event may be lost; this
  is the explicit boundary between temporary and permanent durability.

## Testing

### Sandbox and relay

- write-before-send ordering;
- stable event IDs across retries;
- deletion only after durable ACK;
- startup recovery;
- client-hint failure independent from CP ingestion;
- bounded outbox behavior;
- shared conformance across Docker, NodeOps, and Coder.

### Control plane

- worker authentication and epoch fencing;
- tenant context derived server-side;
- transport and semantic deduplication;
- concurrent duplicate ingestion;
- processor lease expiry and retry;
- transactionally consistent inbox and event rows;
- row-level organization and recipient isolation;
- pagination, read-all, and reconnect cursors.

### Frontend

- immediate pending hint;
- durable confirmation replaces rather than duplicates the hint;
- `Cloud` tag on cloud notifications only;
- source-qualified cache identity and mutation routing;
- sequence-gap and reconnect recovery;
- multiple notification events coalesced without lost invalidation;
- no local notification regressions.

## Phase 1 acceptance criteria

1. An agent-originated event appears immediately as a pending cloud
   notification on a connected client.
2. The event survives a temporary control-plane outage in the sandbox outbox.
3. The sandbox deletes the event only after durable CP acknowledgement.
4. The durable notification survives client and control-plane restarts.
5. Retries create exactly one inbox item.
6. A disconnected client recovers through HTTP with correct unread state.
7. Cloud notifications are visibly and structurally separated from local
   notifications.
8. The same implementation and tests cover Docker, NodeOps, and Coder.
9. No local daemon or local SCM/notification code is changed.

## Phase 1 non-goals

- GitHub PR/check/review webhook processing.
- Right-side PR inspector updates.
- Automatic CI-failure injection.
- Email, Slack, or mobile push delivery.
- Importing every GitHub repository notification.
- Refactoring or sharing the local daemon's notification implementation.

Those SCM responsibilities belong to Phase 2 and consume the durable
notification service defined here.
