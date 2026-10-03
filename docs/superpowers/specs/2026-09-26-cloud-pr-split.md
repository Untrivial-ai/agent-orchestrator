# Separate preparation and browser reviews

Date: 2026-09-26

## Decision

Keep PR #5543 for shared browser sessions and VM DevTools. Move cold-start
preparation into a separate PR against main. Neither feature requires the other
branch to build. Preserve the existing PR history with a scoped removal commit,
not a force-push. Preserve the original combined tree on a local backup branch.

## Ownership

| Shared browser | Cold-start preparation |
| --- | --- |
| Shared browser contract and CLI verbs | Composer preparation and pending input |
| Worker browser service and Chromium image | Branch-focused checkout and readiness gate |
| Viewer relay, tickets, permissions and input | Preparation API, leases, reuse and commit |
| Desktop browser surface and VM DevTools | Startup progress, ordered messages and retries |
| Browser regression and native inspector tests | Provider cleanup, late-result fencing and delegation idempotency |

Mixed files are split at the changed block, not assigned wholesale. This
includes worker startup, HTTP route registration, generated client contracts,
session rendering, translations and telemetry. Each branch keeps only its
feature's API endpoints and schemas.

Small identical prerequisites remain in both branches: Docker resource labels,
first-start database readiness, architecture-aware test builds, and the local
Docker sign-in entry. These are intentionally merge-compatible infrastructure.
Existing combined design records stay with the original browser PR as historical
context; the current runbooks define each PR's actual scope.

## Acceptance

1. Each branch builds and passes its relevant complete regression suites on its
   own. A compile-only check is recorded separately from executed tests.
2. Preparation-only tests do not start Chromium or depend on viewer endpoints.
   Browser tests create ordinary sessions and do not call preparation endpoints.
3. API generation matches each branch's contract. PostgreSQL tests run against
   disposable databases, without preparation schema in the browser-only branch.
4. Real desktop evidence and manual test instructions identify the branch and
   distinguish local Docker coverage from hosted-provider coverage.
5. Both PR descriptions use independently measured net diff counts, list shared
   prerequisites and limitations, and link each other's scope after publication.

## Merge considerations

Shared insertion points may need ordinary merge conflict resolution if one PR
lands first. Retain both features when resolving: browser wiring must not
replace readiness gating, and preparation must not remove browser controls.
The combined snapshot is the reference for checking accidental omissions.

No provider credential, scratch application data, local database or handoff
file belongs in either PR.
