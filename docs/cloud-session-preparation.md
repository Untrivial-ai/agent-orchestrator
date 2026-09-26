# Cold-start session preparation

This change prepares a new cloud worker when the task composer opens. It does
not add a warm pool or the shared browser viewer. Browser commands and the
viewer are reviewed separately in PR #5543.

## User flow

1. Open **New task** in a cloud project. Once a valid harness and execution
   configuration are selected, the editable composer starts a hidden,
   promptless session that requests fresh compute.
2. Enter the task. Closing and reopening a compatible composer within its
   two-minute grace period reuses the preparation. Duplicate composer windows
   attach to the same server-side preparation rather than requesting workers
   independently.
3. Submit. A generation-fenced, idempotent commit makes the prepared session
   visible and saves the prompt. A preparation failure falls back to ordinary
   session creation when appropriate; drafts stay available on failure.
4. Follow progress while the worker connects, downloads the repository,
   restores saved work, and starts the harness. Follow-up instructions are
   saved in order. Execution waits for a complete workspace.
5. An abandoned attachment stops renewing. Expiry requests deletion, fences
   stale callbacks, and leaves unresolved provider creations tracked until
   they can be found and removed.

There is no fixed startup-time promise. Provisioning, image availability,
repository size, restore data, and credentials affect the measured result.

## Boundaries

| Area | Responsibility |
| --- | --- |
| Composer and pending session | Immediate input, stable mutation keys, retryable drafts, lifecycle progress |
| Preparation API and Postgres | User-scoped compatibility identity, attachments, leases, commit/detach/renew, quota |
| Reconciler | Wake notifications, generation fencing, durable provider-creation cleanup |
| Worker | Parallel credential/terminal preparation, branch-focused blobless checkout, execution gate |
| Local task delegation | Synchronous submit guard, durable idempotency and recovery so the shared composer cannot duplicate local work |

The server derives compatibility from the project, checkout branch, harness,
provider connection and execution configuration. Client instance IDs identify
attachments, not distinct workers. A visible but inactive composer eventually
stops renewing; opening a composer is not an unlimited keep-alive.

Postgres migrations 00042 through 00045 belong to this change. SQLite migrations
0156 through 0158 support local submission idempotency and recovery. Apply them
through the normal application migration path. Do not renumber deployed
migrations or roll them back against live sessions.

Both independent PRs carry the same small Docker test-support changes: resource
labels, first-start database readiness, architecture-aware local builds, and
the local Docker sign-in entry. These are shared test prerequisites, not a
dependency on the other feature.

## Automated checks

From the repository root:

```sh
cloud/scripts/test-cloud-local.sh
cloud/scripts/test-cloud-local.sh --measure-startup
cloud/scripts/test-cloud-cold-start-variations.sh --local
cloud/scripts/measure-cloud-cold-start.sh --self-test
cloud/scripts/test-cloud-cold-start-variations.sh --self-test
cd cloud && GOWORK=off go test -race -count=1 ./...
```

The Docker scripts use fresh workers and clean up their own resources. Image
build time is outside the session-start timing. Browser readiness is deliberately
not part of the preparation-only measurements.

Also run the complete backend and frontend suites, both frontend typechecks,
and the cloud-client generation/typecheck/test/build checks. A disposable
Postgres URL in `AO_TEST_DATABASE_URL` enables the persistence/recovery tests;
an unset URL skips those tests and is not equivalent coverage.

## Manual desktop review

Use the real desktop app and Docker provider with an isolated data directory,
not a browser-only renderer. Follow `.agents/skills/ao-desktop-dev/SKILL.md`.
Run `npm run cloud:local`, point the desktop at its printed control-plane URL,
sign in through local auth, and choose a public throwaway repository. Real task
execution requires a valid harness credential; placeholder credentials prove
only the infrastructure and terminal path.

1. Open New task without submitting. Confirm exactly one new worker begins
   provisioning. Type immediately and verify focus stays in the composer.
2. Close and reopen within two minutes, keeping or reselecting the same
   harness/model configuration. Confirm the same preparation/session and worker
   identity. Open a second composer and confirm no duplicate worker.
3. Submit once, then rapidly repeat the submit action. Confirm one durable
   session and one initial prompt. Enter a follow-up during checkout and verify
   it executes only after the workspace is ready, in submission order.
4. Close all composers and let the grace period expire. Confirm the hidden
   session is terminated and resources are eventually removed. Repeat with
   a delayed provider response and confirm it cannot revive the preparation.
5. Exercise unavailable credentials, checkout failure, reconnect, and worker
   replacement. Confirm actionable progress/errors and retained drafts, without
   duplicate delivery. Verify ordinary local task submission still works.

Record provider, architecture, image revision, repository, startup milestones,
and cleanup outcome with any regression report. Docker checks do not establish
hosted Coder or NodeOps latency or authentication.

## Historical design and evidence

The cold-start and reconnect-grace specs under `docs/superpowers/specs/` record
the original design work. Their combined-branch test results are historical,
not evidence that this split branch has passed. Current split verification is
recorded in the [split validation report](superpowers/reports/2026-09-26-cloud-preparation-validation.md).
See the [native desktop evidence](screenshots/cloud-preparation/README.md) for
the fresh close/reopen verification and its limits.
