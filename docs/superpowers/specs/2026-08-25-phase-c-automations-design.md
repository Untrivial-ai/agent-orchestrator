# Phase C Automations Design

## Summary

Phase C adds recurring, unattended AO sessions as a daemon-owned feature. An
automation stores a validated recurrence, project and agent configuration, and
prompt. The daemon creates durable run occurrences and starts them through the
same session service used by the HTTP API and tracker intake.

The feature ships as one PR with backend storage and scheduling, REST and CLI
surfaces, and a minimal desktop list/create/toggle/delete experience. It does
not move scheduling into Electron, bypass the daemon from the CLI, or use an OS
scheduler.

## Goals

- Schedule recurring worker or orchestrator sessions for a registered project.
- Preserve schedule intent across daemon restarts and timezone/DST changes.
- Provide at-least-once dispatch with durable run history and bounded recovery.
- Prevent overlapping sessions from one automation.
- Avoid duplicate session creation during normal concurrent ticks and the
  scheduler's post-spawn crash window.
- Expose one consistent contract to the desktop and thin CLI.
- Keep all user-visible automation state under the daemon's existing `~/.ao`
  data directory.

## Non-goals

- Editing or injecting prompts into an existing live session.
- Machine-wide or cloud scheduling while the AO daemon is not running.
- Arbitrary cron extensions such as seconds, years, `L`, `W`, or `#`.
- Multiple concurrent runs of one automation.
- Notifications, approval policies, conditional workflows, or chained jobs.
- Moving lifecycle or scheduler logic into Electron.

## Confirmed product decisions

- Startup catch-up creates at most three missed occurrences per automation.
- Every automation stores an explicit IANA timezone. Desktop creation defaults
  to the Electron machine timezone; timestamps persisted for comparison are
  UTC.
- One run may be active per automation. Later occurrences wait durably rather
  than starting overlapping sessions or being silently skipped.
- Run history is removed with its automation after destructive confirmation.
- Raw RRule is the canonical schedule. Five-field cron is accepted as input
  sugar only when it can be converted without changing cron semantics.

## Architecture

### Domain and service

`backend/internal/domain/automation.go` defines automation and run records,
schedule input, run states, and validation vocabulary.

`backend/internal/service/automation/` is the only owner of automation
business rules. It validates and canonicalizes schedules, performs CRUD,
calculates occurrences, claims due work, advances schedules, reconciles stale
work, starts sessions, and observes linked session completion. Controllers,
CLI, and the polling loop do not duplicate those rules.

The service depends on narrow store and spawner interfaces. Production wiring
passes the SQLite store and the existing session service. The session service
continues to call `session_manager.Manager.Spawn`; no new launch path is added.

### Observer and daemon wiring

`backend/internal/observe/automations/` owns only cadence and cancellation. It
uses the shared observation poll-loop pattern, calls one synchronous service
poll method immediately, and repeats on a short ticker. A failing occurrence is
recorded and does not stop other automations or daemon readiness.

Daemon startup constructs the automation service before HTTP serving, runs its
best-effort reconciliation, then starts the observer. Shutdown waits for the
observer's done channel with the existing lifecycle wiring.

### API clients

The HTTP controller translates JSON and service errors. The CLI calls those
routes through the shared command client. The frontend uses the generated
`openapi-fetch` client and TanStack Query. No client reads SQLite or invokes a
runtime directly.

## Persistence

The next migration on current `origin/main` is
`backend/internal/storage/sqlite/migrations/0149_automations.sql`. It adds new
tables and the session idempotency link without modifying merged migrations.

### `automations`

| Column | Contract |
| --- | --- |
| `id` | UUID text primary key |
| `project_id` | required FK to `projects`; delete cascades from a removed project |
| `display_name` | trimmed, 1-120 characters |
| `prompt` | trimmed, 1-4096 bytes, matching session spawn limits |
| `kind` | `worker` or `orchestrator` |
| `harness` | empty means the project's current default; otherwise a supported harness |
| `rrule_text` | canonical RFC 5545 recurrence rule without an embedded timezone |
| `timezone` | validated IANA timezone name |
| `enabled` | boolean scheduling gate |
| `next_run_at` | next not-yet-materialized occurrence in UTC |
| `last_run_at` | most recently materialized scheduled time in UTC, nullable |
| `created_at`, `updated_at` | UTC timestamps |

An index on `(enabled, next_run_at)` supports due scans. Project and display
name are also indexed for list ordering/filtering.

### `automation_runs`

| Column | Contract |
| --- | --- |
| `id` | UUID text primary key |
| `automation_id` | required FK with `ON DELETE CASCADE` |
| `scheduled_for` | occurrence time in UTC |
| `session_id` | nullable FK to `sessions`; retained as null if a session row is later deleted |
| `status` | `pending`, `spawning`, `running`, `completed`, or `failed` |
| `attempt_count` | non-negative dispatch attempts |
| `claimed_at` | time the spawning lease was acquired, nullable |
| `lease_expires_at` | time another poll may reclaim `spawning`, nullable |
| `started_at`, `finished_at` | nullable UTC lifecycle timestamps |
| `error_message` | bounded diagnostic text, empty unless failed/retried |
| `created_at`, `updated_at` | UTC timestamps |

`UNIQUE(automation_id, scheduled_for)` is the occurrence idempotency boundary.
Indexes cover automation history and active-state lookup.

### Session idempotency

The migration adds nullable `automation_run_id` to `sessions`, with a unique
index and FK to `automation_runs(id)` using `ON DELETE SET NULL`.
`ports.SpawnConfig` gains `AutomationRunID`. Session creation checks for an
existing session with that run ID before allocating a new one. If concurrent
callers race, the unique constraint winner is returned after the losing insert
reloads it.

This closes the dangerous crash window where a session exists but the scheduler
has not yet persisted its `session_id`. It does not claim exactly-once agent
execution: a runtime may still fail after durable session creation, and normal
session reconciliation remains authoritative for that lifecycle.

### Queries and generated code

New SQL lives in `backend/internal/storage/sqlite/queries/automations.sql` and
the existing session query source is extended for `automation_run_id`. `npm run
sqlc` regenerates `backend/internal/storage/sqlite/gen/*`; generated files are
never edited by hand.

## Scheduling semantics

### Schedule input

Create and update accept exactly one of:

- `rrule`: an RFC 5545 recurrence rule supported by
  `github.com/teambition/rrule-go`; or
- `cron`: a standard five-field cron expression that the server converts to a
  semantically equivalent RRule.

`timezone` is required by the service contract. The desktop supplies the
machine timezone by default. CLI defaults to the local IANA timezone when it
can resolve one and otherwise requires `--timezone`; the server never guesses
UTC for an omitted or invalid zone.

The server rejects schedules that have no future occurrence, unsupported cron
features, ambiguous cron day-of-month/day-of-week combinations, malformed
RRules, embedded conflicting timezone declarations, or occurrences more
frequent than once per minute.

Daily and weekly desktop presets produce ordinary RRule input, so presets and
raw input follow identical validation and calculation paths.

### Materialization and catch-up

On create or schedule update, the service computes the first occurrence after
the request's effective start time and stores it in `next_run_at`.

For an enabled automation whose `next_run_at <= now`, one database transaction:

1. Materializes up to three due occurrences using literal scheduled times.
2. Inserts each run with `pending`, tolerating the occurrence unique conflict.
3. Sets `last_run_at` to the newest materialized time.
4. Advances `next_run_at` to the first occurrence strictly after `now`, even if
   dispatch later fails.

The three-occurrence cap applies both at daemon startup and during ordinary
polling after a long pause. Older missed windows beyond the cap are intentionally
coalesced; they are not inserted as failed or skipped runs.

### Non-overlap and dispatch

For each automation, the service completes linked runs whose sessions are now
terminated, then considers pending runs oldest-first.

- A `spawning` or `running` run blocks all later occurrences.
- Otherwise, the oldest pending run is atomically leased as `spawning`, with
  `attempt_count` incremented.
- The service calls the existing session spawn path with `AutomationRunID`.
- Success links the session and changes the run to `running`.
- A validation or permanent spawn failure changes the run to `failed` and
  allows the next pending occurrence on a later poll.
- A canceled context or transient/internal failure releases the expired lease
  for reconciliation rather than falsely recording successful execution.

Only one pending occurrence is dispatched for an automation per poll. This
keeps restart work bounded even when several automations have backlogs.

### Completion

A successful spawn does not mean the automation run is complete. While a run
is `running`, polling reads the linked durable session fact. `is_terminated`
changes the run to `completed` and stamps `finished_at`. Failed or unknown
runtime probes never complete a run; session lifecycle remains the source of
truth.

### Reconciliation

Boot reconciliation is best-effort and idempotent:

1. Recover `spawning` rows whose lease expired.
2. If an idempotent session already exists, link it and mark the run `running`.
3. Otherwise return the run to `pending` for a retry.
4. Complete `running` rows whose linked sessions are durably terminated.
5. Fail structurally invalid orphan rows with a bounded diagnostic.
6. Materialize at most three missed occurrences per enabled automation and
   advance its schedule.

One bad automation is logged and skipped without blocking daemon startup.

### Enable, update, and delete behavior

- Disabling prevents materialization and new dispatch. It does not terminate a
  running session or delete pending/history rows.
- Re-enabling recomputes the next future occurrence and does not replay the
  disabled interval.
- A schedule update recomputes the next future occurrence; already materialized
  runs retain their original scheduled times.
- Other field updates affect only future session spawns.
- Delete removes the automation and run history after explicit confirmation;
  linked sessions remain normal sessions and their automation link becomes
  null.

## REST API

Routes are mounted beneath `/api/v1`:

- `GET /automations?projectId=&enabled=&limit=&cursor=`
- `POST /automations`
- `GET /automations/{automationId}`
- `PATCH /automations/{automationId}`
- `DELETE /automations/{automationId}`
- `GET /automations/{automationId}/runs?limit=&cursor=`

Create includes project, display name, prompt, kind, optional harness,
timezone, exactly one schedule input, and optional enabled state. Patch uses
pointer/optional fields so `false` and empty-harness/default are representable;
schedule replacement is atomic.

List responses include the automation plus a compact latest-run summary. Run
lists are newest-first and cursor paginated. Deletion returns `204`.

Validation maps to the existing `bad_request` envelope, missing rows to
`not_found`, and optimistic/claim conflicts to `conflict`. Request IDs remain
present. DTOs live in `controllers/dto.go`, operations and named schemas are
registered in `apispec/specgen/build.go`, and `npm run api` regenerates both
OpenAPI and frontend types.

Automation mutations rely on direct TanStack Query cache updates plus a short
list refetch interval. Session CDC already refreshes linked run completion.
Phase C does not widen `change_log.event_type`: doing so would require a large,
unrelated rebuild of the mature CDC allowlist and all dependent trigger
compatibility definitions. No manual or parallel CDC emission is introduced.

## CLI

`ao automation` adds:

- `create --project --name --prompt (--rrule | --cron) --timezone [--kind]
  [--harness] [--disabled]`
- `list [--project] [--enabled]`
- `get <id>`
- `update <id>` with explicitly provided mutable flags
- `delete <id>` with the repository's destructive confirmation convention
- `runs <id> [--limit] [--cursor]`

The CLI mirrors wire DTOs locally and uses `commandContext` plus shared daemon
client/error helpers. Human output uses concise tables; supported structured
output conventions remain available. Misuse and local flag validation return
`usageError` (exit 2); daemon/runtime errors exit 1 and retain daemon envelope
details.

## Desktop

A global Automations sidebar destination opens a dedicated route and page.
The primary list shows name, project, humanized schedule and timezone, enabled
state, next occurrence, and current/latest run state. Failure detail is visible
without opening developer tools.

Creation uses a focused dialog with project, name, prompt, kind, harness,
timezone, daily/weekly presets, and a raw RRule fallback. Presets are serialized
to the same API schedule input as raw RRules. Toggle uses optimistic state with
rollback and an accessible error. Delete uses the shared destructive confirm
dialog. Run history expands on demand and remains secondary to the list.

`useAutomations.ts` owns typed list, detail, create, patch, delete, and runs
queries/mutations. The page uses existing shell, form, spacing, control, and
internationalization conventions. It does not introduce daemon logic or a
second recurrence parser into React; humanization is display-only.

## Error handling and safety

- The manager logs automation and run IDs without logging prompt text.
- Stored error messages are bounded and sanitized for user display.
- A single automation failure never stops the ticker or daemon.
- Context cancellation stops new dispatch promptly and leaves recoverable
  durable state.
- Project existence, supported kind/harness, prompt limits, timezone, and
  schedule are validated before mutation.
- Database transactions cover occurrence materialization, claims, and state
  transitions. Runtime launch remains outside a database transaction.
- No network listener, authentication, app-data, lifecycle status, or dirty
  worktree behavior changes.

## Testing and verification

Implementation follows narrow red-green-refactor cycles. Tests assert real
behavior at each boundary:

- Schedule parsing, timezone/DST transitions, cron conversion/rejection, and
  next-occurrence calculation.
- Migration constraints, cascading history deletion, session link behavior,
  due materialization, unique occurrence claims, and lease recovery.
- Duplicate polls and post-spawn retry return one session for a run.
- Three-occurrence catch-up cap, schedule advancement after failure, no replay
  while disabled, and non-overlapping queued dispatch.
- Running-to-completed transition only after durable session termination.
- Controller CRUD, validation/error envelopes, filtering, pagination, and run
  history.
- CLI happy paths, missing arguments, daemon errors, output, and destructive
  confirmation.
- Frontend list/create/toggle/delete/history behavior, optimistic rollback,
  accessible labels, and error states.

After implementation: regenerate sqlc and API artifacts, run the narrow
packages first, then backend build/race checks and frontend typecheck/test/build.
Manual testing uses the real AO Electron desktop app: create a near-future
automation via CLI, observe one scheduled session, verify durable run history
and UI updates, restart the daemon around a due window, and verify catch-up,
non-overlap, and toggle behavior.

## Baseline note

The feature worktree starts from `origin/main` at `04b676c9a`. CI-equivalent
dependency setup makes frontend typechecking pass. Before feature changes,
SQLite/store, session service, HTTP core/spec, CLI, and automation-adjacent
observer packages pass. The repository's full Go suite and some broader
session-manager/controller/daemon/usage packages have pre-existing Windows
failures involving POSIX paths/shells, file modes/locks, host agent discovery,
and environment-sensitive fixtures. Those failures are unrelated to Phase C
and are not repaired in this PR; final verification will distinguish them from
new failures and run the authoritative CI-aligned checks where available.
