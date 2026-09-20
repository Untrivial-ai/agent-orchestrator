# Cloud PR Review Design

## Goal

Bring the existing desktop PR-review experience to cloud sessions without
changing the local daemon or local-session behavior. Cloud reviews run through
the control plane and the existing session worker/terminal transport, so the
same implementation works for Docker, NodeOps, and Coder sandboxes.

The cloud reviewer vocabulary is deliberately limited to `claude-code`,
`cursor`, and `codex`. A session defaults to its current worker harness. A user
may select either of the other supported harnesses; when that CLI is absent
from the live sandbox, the selector offers an **Install** action before the
harness can be selected.

## Scope

- Show the existing Reviews tab for eligible cloud sessions.
- Select and persist a reviewer harness per cloud session.
- Run, cancel, and retry manual reviews for open or draft pull requests.
- Attach the existing terminal UI to the dedicated cloud reviewer terminal.
- Display live status, historical results, and submitted verdicts.
- Persist automatic review, CI-feedback injection, review-feedback injection,
  and terminate-on-merge preferences in the cloud control plane.
- Inspect and install supported reviewer CLIs inside an existing sandbox.
- Preserve all existing local review routes, types, readiness checks, agent
  lists, and lifecycle behavior unchanged.

Inline review-comment resolution/rerequest controls remain available where the
cloud SCM projection supplies the required identities. The implementation must
not fabricate local-only review data when the cloud contract lacks it.

## Chosen Architecture

The cloud control plane owns durable review intent, preferences, and results.
It sends typed requests to the existing worker in the session sandbox. The
worker owns process execution and reports terminal output through the existing
remote terminal transport. Sandbox-provider adapters remain unaware of PR
reviews and harness installation.

This is preferred over provisioning a second sandbox for each review because
the existing checkout, credentials, and terminal channel are already present.
It is preferred over provider-specific implementations because Docker,
NodeOps, and Coder should differ only in how the original sandbox was created.

## Data Model

Add a new migration rather than modifying existing migrations.

Cloud sessions gain:

- `reviewer_harness`, empty to mean the current worker harness;
- `auto_review_enabled`;
- `auto_inject_ci`;
- `auto_inject_review`;
- `terminate_on_pr_merge`.

Review runs retain the harness that actually executed them, the trigger source
(`manual` or `auto`), and their reviewer terminal identifier. Historical runs
must not be relabelled when a session preference changes.

The current one-run-per-commit constraint becomes an active-run constraint:
only one `running` run may exist for a pull request and SHA. Failed or cancelled
runs may be retried against the same SHA and remain in history.

All store reads and writes remain tenant-scoped and use the established row
level security transaction helpers. Session scan lists must be updated and
tested in the same change as new columns.

## Cloud API

The authenticated cloud API gains provider-neutral session routes for:

- reading review state and run history;
- triggering eligible reviews;
- cancelling the active review batch;
- switching the reviewer preference;
- updating cloud session review/feedback/merge preferences;
- inspecting supported reviewer harness availability;
- installing a missing supported reviewer harness.

Requests identify only a session, harness, and typed preferences. The renderer
never submits executable paths, package names, URLs, shell commands, or
installation scripts. The API rejects any harness outside the fixed cloud set.

Cloud responses are adapted to the existing Reviews UI model, including the
reviewer terminal id, running/failed/cancelled/delivered states, trigger source,
verdict, body, target SHA, and harness.

## Reviewer Selection and Credentials

An empty persisted override resolves to the session's worker harness. Since
cloud worker harnesses are themselves restricted to Claude, Cursor, and Codex,
the default is always a supported reviewer.

The selector lists exactly those three entries. Credential readiness and CLI
availability are distinct:

- a valid personal cloud provider connection authorizes the harness;
- a worker inspection confirms whether its binary is present in this sandbox.

A harness can run only when both are true. Missing credentials route to the
existing cloud credential UI. A missing binary exposes **Install** in the
reviewer dropdown. Selecting an already available harness persists immediately;
a missing harness is selected only after installation and verification succeed.

## Harness Installation

The worker implements typed `harness.inspect` and `harness.install` transport
operations for the three cloud harnesses. Installation recipes and versions are
worker-owned constants matching the reviewed cloud images:

- Claude and Codex use fixed npm package names and pinned versions;
- Cursor uses its pinned official Linux archive for the worker architecture.

Installation targets a writable AO-owned user tools directory inside the
sandbox and never requires `sudo`. The worker updates only the environment used
for terminals it launches; it does not mutate host or provider configuration.
Downloads are bounded, use HTTPS, and are unpacked without a shell pipeline.
The worker verifies the canonical binary with a bounded version command before
reporting success.

Only one installation per session/harness may run at once. Installation status
is returned as `missing`, `installing`, `ready`, or `failed`, with a bounded
diagnostic message. A worker restart may lose an in-progress job, but the next
inspection derives truth from the installed binary rather than trusting stale
UI state.

Docker normally reports all three ready. NodeOps and Coder can install a second
or third harness without provider-specific code.

## Review Lifecycle

Triggering a review snapshots every eligible open/draft PR head. For each PR,
the control plane creates a durable run and opens a dedicated agent terminal in
the existing sandbox using the effective reviewer harness and its cloud
credential environment. The reviewer prompt tells the agent to inspect the
target diff and submit a typed verdict through the existing worker review
submission endpoint.

The review terminal is independent of the worker's main conversation. Its
output is available through the same ticketed remote mux used by other cloud
terminals. The terminal id is stable for the run and is returned to the UI.

Submission validates run ownership and state, submits the GitHub review through
the control-plane credential broker, records the result, closes the terminal,
and refreshes the pull-request review state.

Cancel closes all running reviewer terminals for the session and atomically
marks their runs cancelled. Retry creates new runs for the same SHA after a
failed or cancelled attempt. Duplicate concurrent triggers return the existing
running state rather than opening duplicate terminals.

## Automatic Controls

Automatic review observes a newly discovered eligible PR head and uses the same
trigger path with `trigger_source=auto`; it does not maintain a second review
implementation. Automatic review is disabled while the chosen harness lacks a
credential or binary and surfaces the reason in review state.

Automatic CI and review feedback controls determine whether newly observed
failing-check and review-feedback summaries are injected into the owning cloud
worker session. Injection is idempotent per observed provider fact and travels
through the existing cloud message/turn boundary.

Terminate-on-merge is evaluated by the cloud PR observer. A newly observed
merged PR requests normal session teardown once; the reconciler continues to
own actual sandbox termination.

## Frontend Integration

Shared presentation components remain shared. Data access branches only at the
cloud boundary:

- local sessions continue using the generated loopback daemon client;
- cloud sessions use the typed cloud control-plane client and cloud query keys.

The Reviews tab, status cards, result history, run/cancel/retry actions, and
terminal presentation retain the current local visual language. Cloud-specific
selection data is passed into `ReviewerSelect`; its existing local readiness
and management behavior is not changed.

Reviewer terminal targets carry enough cloud context for `TerminalPane` to
request a cloud ticket and connect through the cloud mux. No local handle or
loopback terminal route is used for a cloud reviewer.

The CI/review/merge switches call cloud preference routes for cloud sessions
and their existing daemon routes for local sessions.

## Failure Handling

- Reject unsupported or unavailable harnesses before creating a run.
- Resolve durable runs to `failed` if terminal creation or prompt delivery
  fails; never leave a run permanently `running` after enqueue failure.
- Preserve the control-plane request id and stable error code in UI errors.
- Treat a missing/disconnected worker as unavailable, not as proof the session
  is dead.
- A cancellation race with a submitted verdict is resolved by conditional
  state transitions; exactly one terminal outcome wins.
- Installation failure leaves the prior reviewer preference intact and keeps
  the Install action available for retry.

## Testing and Verification

Tests must cover:

- migrations, session scanning, defaults, and tenant isolation;
- rejection of every reviewer outside Claude, Cursor, and Codex;
- default-to-worker resolution and explicit override persistence;
- trigger deduplication plus failed/cancelled same-SHA retry;
- terminal open/input/close requests and terminal-id reporting;
- submission, cancellation, authorization, and failure transitions;
- harness inspection/install allowlists, bounded execution, verification, and
  concurrent-install fencing;
- cloud API/client routing and preservation of all local request paths;
- cloud-only reviewer options and Install-button states;
- Docker, NodeOps, and Coder behavior through the common worker protocol;
- automatic feedback idempotency and terminate-on-merge intent.

Verification runs focused Go and Vitest suites first, then cloud Go tests,
frontend typecheck/build, generated cloud API drift checks, repository lint,
and the Docker cloud smoke flow. NodeOps/Coder live provisioning is reported as
a verification gap unless provider credentials are available; their common
protocol and image contracts remain covered locally.
