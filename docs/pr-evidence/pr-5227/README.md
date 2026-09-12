# PR #5227 verification evidence

2026-09-12, macOS arm64. Node 24; Go 1.26.5 (selected by the checked-in Go
workspace); golangci-lint 2.12.2.

## Native end-to-end scope

The actual Electron app and preload ran against this checkout's Go daemon and
SQLite database, with isolated state under `~/.ao/dev/pr-5227`. Only the external
provider processes were scripted. No renderer bridge or daemon API was mocked,
and no real credentials, paid requests, or billing changes were used.

Both Claude/ACP and Codex exercise:

- Quota failure with original multiline details and a safe clickable link.
- One terminal error presentation; superseded retry progress is hidden.
- Reload from durable conversation state.
- Retry progress, recovery, and a successful follow-up.
- User cancellation settling as interrupted, without a terminal error.
- Retry A → recovery → retry B → final failure: A remains a separate historical
  diagnostic, and only B is superseded, including after reload.
- Native auth-required metadata setting the existing account state and generic
  sign-in banner, with only one copy of the provider's explanation.

Evidence:

- [Claude quota outcome](claude-code-quota.png)
- [Codex retry in progress](codex-retrying.png)
- [Codex auth outcome and action banner](codex-auth.png)
- Durable snapshots: [Claude](claude-code-snapshot.json), [Codex](codex-snapshot.json)
- [Fixture and reproduction instructions](../../../test/fixtures/chat-provider-failures/README.md)

These are fault-injection results, not live-provider integration certification.
Live Claude evidence from the original PR remains linked in its description.

## Local validation

- Frontend and renderer-E2E TypeScript checks passed.
- Full frontend suite: **292 files, 4,168 tests passed; 7 skipped**.
- Full renderer smoke suite: **59 passed** (single-worker rerun).
- Go build and vet passed; pinned lint reports **0 issues**.
- ACP, Codex, ports, and chat-service full race suites passed after the final fixes.
- API and SQL regeneration produced no drift.
- Native CLI E2E passed on macOS.
- Product UI tests (126), cloud-client tests (21), both package typechecks and
  dry-run packaging passed. Cloud-client generation had no drift.
- macOS helper/repair tests, both helper architectures, Swift state tests,
  landing icon check, optional-private-submodule test, and E2E gate tests passed.

The initial full race run hit timing failures under contention in fake/pi agents,
conpty/tmux runtimes, and session manager. Their complete package race reruns
passed at lower concurrency. SQLite's initial full-package run exceeded the
15-minute deadline; its complete isolated race rerun passed in 715.6 seconds.
The initial frontend run encountered a Node/Electron native-module ABI mismatch
and two timing failures; rebuilding for Node and rerunning the complete suite
produced the green result above.

Docker's server did not respond locally, so container smoke requires CI.
Windows/Linux-native checks likewise require their CI runners. Check the PR's
current commit checks; original-head CI results are not evidence for this update.

## Deliberate 80/20 boundary

No schema migration, error taxonomy, new dependency, global incident registry,
or automatic retry mechanism. Adapters use native signals and preserve unknown
provider prose. Existing turn, account, activity, and retry owners remain in charge.
