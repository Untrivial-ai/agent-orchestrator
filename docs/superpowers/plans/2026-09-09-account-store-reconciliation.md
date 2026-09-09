# Codex Account Initialization and Reconciliation Fix Plan

## Goal

Make Codex account management usable as soon as AO's local account store is ready. Discovery and verification of the device-global Codex account must run independently and retry in the background.

A temporary Codex CLI or app-server failure must not:

- Poison account-management initialization for the daemon lifetime.
- Hide saved AO accounts.
- Mark every saved account as signed out or authentication unknown.
- Block adding an account or managing an inactive account.
- Block ordinary Codex sessions when native Codex authentication still works.
- Require an AO daemon restart before recovery.

Operations that mutate the device-global Codex account must remain fail-closed until AO has conclusively reconciled the current device account.

## Intended Architecture

```text
Daemon starts
   |
   v
Initialize AO Codex account store
   |
   +-- unsafe local-store failure
   |      `--> account management unavailable
   |
   `-- local store ready
          |
          +--> API serves saved accounts
          +--> Add account remains available
          +--> Inactive account management remains available
          +--> Ordinary Codex launch remains available
          `--> Start device reconciliation in background
                    |
                    +--> checking
                    +--> verified
                    +--> temporarily unavailable -> bounded retry
                    `--> safety blocked -> global mutations disabled
                              |
                              `--> verify saved-account auth independently
```

The implementation has three separate boundaries:

1. **Account-store initialization** loads and validates AO-owned local state.
2. **Device reconciliation** compares the native device-global Codex account with AO's durable active-account pointer.
3. **Account authentication verification** determines whether each saved account's credential remains usable.

Failure in one boundary must not be reported as failure in another.

## Task 1: Replace Bootstrap With Account-Store Initialization

Primary files:

- `backend/internal/service/agent/codex_accounts.go`
- `backend/internal/service/agent/codex_account_reconcile.go`
- `backend/internal/service/agent/codex_account_service.go`

Refactor the current bootstrap contract so its responsibility is explicit:

- Rename `bootstrap()` to `initializeAccountStore()` or the nearest matching repository convention.
- Rename `WaitCodexAccountBootstrap()` to `WaitCodexAccountStoreReady()`.
- Restrict this phase to AO-owned local work:
  - Validate and create the account-store roots.
  - Clean abandoned pending-login and switch-staging data safely.
  - Load and validate account descriptors.
  - Resolve whether each account has an AO-owned credential without authenticating it remotely.
  - Load the durable active-account pointer and revision.
- Do not call Codex app-server or perform provider authentication here.
- Do not reconcile the device-global account here.
- Do not query capacity information here.
- Do not mutate the global Codex credential here.
- Do not stop, restart, restore, or otherwise alter sessions here.

Use an explicit initialization lifecycle:

- `not_started`
- `initializing`
- `ready`
- `temporarily_failed`
- `safety_blocked`

Concurrency and retry semantics:

- Concurrent callers join one initialization attempt.
- A successful initialization remains latched for the daemon lifetime.
- A transient failure can be retried; it is not cached permanently.
- An unsafe local-store condition remains fail-closed until it is corrected.
- Cancellation of one HTTP request must not cancel daemon-owned initialization for other callers.

Descriptor failure handling:

- One malformed or mismatched account descriptor marks only that account `broken` when it can be isolated safely.
- A missing per-account credential marks only that account `signed_out`.
- Unsafe ownership, traversal, or symlink behavior affecting one account marks that account `broken` when containment is certain.
- An unsafe account-store root or unusable durable database makes account management unavailable.

Tests must cover concurrent callers, cancellation, transient retries, success latching, isolated broken descriptors, missing credentials, and unsafe account-store roots.

Commit:

```text
refactor(codex): separate local account store initialization
```

## Task 2: Model Device Reconciliation Separately

Primary domain file:

- `backend/internal/domain/codex_account.go`

Add an ephemeral device-reconciliation state with these statuses:

- `not_checked`
- `checking`
- `verified`
- `temporarily_unavailable`
- `blocked`

Expose a safe projection containing:

- `status`
- `activeAccountVerified`
- optional safe `reasonCode`
- `retryable`
- optional `attemptedAt`
- optional `verifiedAt`
- optional `nextRetryAt`

Do not expose credentials, provider output, raw filesystem paths, or secret-bearing errors.

The durable `activeAccountId` remains AO's last known pointer. The UI must show an account as **In use** only when:

- Device reconciliation is `verified`.
- The verified global identity matches that account ID.

No database migration is needed for transient reconciliation status. It is daemon runtime state; the durable active-account pointer and revision remain the source of crash-safe account-selection history.

Commit:

```text
feat(codex): model device account reconciliation separately
```

## Task 3: Build a Repeatable Device-Reconciliation Coordinator

Primary file:

- `backend/internal/service/agent/codex_account_reconcile.go`

Implement reconciliation as an independent, repeatable operation:

1. Acquire the existing device-global exclusive account-operation gate.
2. Inspect the canonical Codex credential without mutating it.
3. Read enough structured native account state to determine the device identity, avoiding a credential refresh where a non-refresh identity path is available.
4. Classify the device state as signed out, a known AO account, a new importable account, unmanaged, temporarily unavailable, or unsafe.
5. Re-read the canonical state before committing any AO metadata so a concurrent external login cannot be overwritten or misidentified.
6. Import or select the matching account and compare-and-swap the durable active pointer when safe.
7. Mark reconciliation `verified` and emit the normal account SSE update.

Identity resolution order:

- First use an exact, unique match between the canonical credential and an AO vault credential when the comparison can be performed without leaking or parsing tokens.
- Otherwise use the structured, non-refresh native identity read.
- Never log, return, or persist tokens while resolving identity.

Outcome rules:

- **Conclusive native signed-out state:** clear the active pointer while retaining saved AO accounts.
- **Known global account:** checkpoint/import safe metadata if needed and move the active pointer to it.
- **New valid global account:** verify it in isolation, import it, then move the pointer.
- **Unmanaged or ambiguous global state:** expose a safe unmanaged state and disable device-global switching; do not overwrite the global account.
- **Temporary native read failure:** preserve the current pointer, mark reconciliation temporarily unavailable, and schedule a retry.
- **Global state changed during the read:** abort without mutation and return a retryable reconciliation outcome.

The global device account has priority when it is conclusively identified, but reconciliation must never stop or restart AO sessions merely because it differs from AO's last durable pointer.

Commit:

```text
refactor(codex): make device reconciliation independent
```

## Task 4: Add Automatic Bounded Reconciliation Retries

Add a daemon-owned retry coordinator using an injected clock/timer for deterministic tests.

Recommended backoff:

```text
1s, 2s, 4s, 8s, 16s, 32s maximum, with bounded jitter
```

Retry triggers:

- Daemon startup after local account-store initialization succeeds.
- A retryable reconciliation failure.
- A native/global account-changed signal, where available.
- A saved-account authentication invalidation that may reflect a device change.
- Completion of a successful account login.
- Settings foreground/focus or an explicit account `ensure` request.
- A device-global mutation request such as switch, active logout, or active reauthentication.

Coordinator requirements:

- Use singleflight so only one reconciliation runs at a time.
- Apply a cooldown so UI focus and repeated API calls cannot hammer Codex.
- Own work from the daemon context, not a short-lived HTTP request context.
- Bound every native call with a timeout.
- Reset the backoff immediately after a successful reconciliation.
- Publish status transitions through the existing account event stream.

Retryable examples:

- Codex app-server startup timeout.
- Temporary socket/transport failure.
- Provider response unavailable.
- Global state changed during inspection.

Non-retryable or user-action-required examples:

- Unsupported Codex version/platform.
- Unsafe local account-store ownership.
- Ambiguous duplicate identity.
- Explicit environment-auth override that prevents device-account management.

Commit:

```text
feat(codex): retry device reconciliation safely
```

## Task 5: Split Operation Admission by Safety Requirement

Primary files:

- `backend/internal/ports/codex_account_management.go`
- Codex account service implementation.
- Session-manager Codex account-switch and operation-gate code.

Introduce distinct internal admission methods:

```go
WaitCodexAccountStoreReady(ctx)
EnsureCodexDeviceAccountReconciled(ctx)
```

Operations requiring only the local account store:

- List saved accounts.
- Start Add Account.
- Verify and save a newly added inactive account.
- Reauthenticate an inactive account.
- Log out an inactive account.
- Delete an inactive signed-out account.
- Read cached account and capacity information.

Operations requiring fresh, verified device reconciliation and commit-time identity revalidation:

- Switch the device-global account.
- Log out the active account.
- Reauthenticate the active account and update the canonical credential.
- Automatically activate the first account when the native device is conclusively signed out.

First-account activation rules:

- If the native device is conclusively signed out, the first successfully added account may be activated.
- If device reconciliation is temporarily unavailable, save the account but leave it inactive.
- If another global account already exists, retain that global account and save the new account as inactive.
- When background reconciliation later succeeds, publish the updated active state through SSE.

Switch recovery must depend on the local store and its durable switch saga. It must not run an ordinary reconciliation first when doing so could obscure an incomplete canonical mutation.

Commit:

```text
refactor(codex): gate account operations by device safety
```

## Task 6: Decouple Ordinary Codex Launch Readiness

Primary files:

- `backend/internal/service/agent/codex_operation_gate.go`
- Codex readiness service and tests.

Preserve the durable switch/recovery fence as the safety boundary for launches:

- A nonterminal switch or recovery-required state continues to block a new Codex launch.
- A reconciliation currently holding the exclusive device gate may make a launch wait briefly.
- Once a failed reconciliation releases the gate, the failure itself must not permanently reject future launches.
- If the account manager cannot conclusively report auth because reconciliation is unavailable, return a structured `handled=false` or equivalent result so ordinary native Codex readiness can perform its existing auth check.
- If native Codex reports valid authentication, allow the session to launch.

This work must not create session-switch records, change native session IDs, alter usage bindings, or stop/restart running sessions.

Commit:

```text
fix(codex): keep launches independent from account reconciliation
```

## Task 7: Update HTTP, OpenAPI, and SSE Contracts

Primary files:

- `backend/internal/httpd/controllers/dto.go`
- `backend/internal/httpd/controllers/codex_accounts_dto.go`
- `backend/internal/httpd/apispec/specgen/build.go`

Add the safe `deviceReconciliation` projection to the Codex accounts response.

Contract behavior:

- `GET /accounts` returns cached local accounts even while device reconciliation is checking or temporarily unavailable.
- Account list/read returns `503` only when the AO local account store itself is unavailable or unsafe.
- `POST /accounts/ensure` triggers or joins reconciliation but still returns a `200` account snapshot when the reconciliation attempt is temporarily unavailable.
- Device-global mutations return a safe `503 CODEX_DEVICE_ACCOUNT_UNVERIFIED` when reconciliation cannot establish the current canonical identity.
- Safe error details may include only `reasonCode` and `retryable`.
- SSE emits reconciliation state changes so the UI recovers without a daemon restart or manual reload.
- Raw Codex output, tokens, credentials, and provider errors must remain redacted.

Regenerate and commit both generated artifacts:

```text
npm run api
```

- `backend/internal/httpd/apispec/openapi.yaml`
- `frontend/src/api/schema.ts`

Commit:

```text
feat(api): expose Codex device reconciliation state
```

## Task 8: Make Settings Degrade Gracefully

Primary areas:

- Codex account TanStack Query/state hooks.
- `CodexAccountsSection`.
- `CodexAccountRow`.
- Existing locale files.

UI behavior:

- While checking, show a lightweight reconciliation status without replacing the account list.
- On a temporary failure, show concise copy such as: `Couldn't confirm the active Codex account. Retrying.`
- Keep saved account rows visible.
- Do not mark every account signed out or authentication unknown merely because reconciliation failed.
- Do not hide or disable Add Account because device verification is unavailable.
- Show **In use** only for the reconciled active account.
- Separate saved-account auth labels (`Signed in`, `Signed out`, `Checking`) from device reconciliation status.
- Preserve the last successful usage/capacity snapshot with the existing stale indicator when refresh fails.
- Keep Add, inactive reauthentication, and eligible inactive deletion available.
- Disable only switch and active-account logout/reauthentication until device reconciliation succeeds.
- Trigger an ensure attempt when the subscriptions page gains focus or becomes visible; rely on the backend cooldown and singleflight to deduplicate it.
- Announce checking, degraded, and recovered states accessibly without repeated screen-reader noise.

Add message keys to English and every existing translated locale file.

Commit:

```text
fix(settings): show degraded Codex reconciliation without hiding accounts
```

## Task 9: Regression and Safety Verification

### Backend service tests

- Local account-store initialization succeeds while the global Codex read fails.
- Saved accounts remain visible after reconciliation failure.
- A temporary failure does not clear or change the durable active pointer.
- `activeAccountVerified` is false while reconciliation is unavailable.
- A background retry succeeds without restarting the daemon or sessions.
- A conclusively identified external global account wins over AO's old pointer.
- The pointer is cleared only after a conclusive native signed-out result.
- An inconclusive native result is never translated into signed out.
- Add Account works during reconciliation failure.
- Inactive reauthentication works during reconciliation failure.
- Active reauthentication is safely blocked until reconciliation succeeds.
- Inactive logout/delete follow their existing eligibility rules.
- Active logout is safely blocked until reconciliation succeeds.
- Switching is safely blocked until reconciliation succeeds.
- A global account change during inspection causes no canonical overwrite.
- Reconciliation creates no session-switch record and does not alter usage bindings.

### Session-manager tests

- A launch waits while the exclusive device gate is held and proceeds after a failed reconciliation releases it.
- A durable switch/recovery fence still blocks launches.
- Starting a switch requires fresh reconciliation.
- Switch recovery can initialize the local store without ordinary reconciliation.
- Reconciliation never stops or restarts running sessions.

### HTTP tests

- Account list and ensure return `200` with a degraded reconciliation projection.
- Unsafe device-global mutations return the safe `503` code.
- SSE reports checking, unavailable, and verified transitions.
- Responses and logs redact provider output and credentials.
- Route/spec parity and generated API drift checks pass.

### Frontend tests

- Accounts remain visible during temporary reconciliation failure.
- Add Account remains enabled.
- No unverified account receives the **In use** badge.
- Reconciliation failure does not falsely show every account as signed out.
- Degraded and recovered statuses are accessible.
- Local-only and device-global actions have the correct enabled states.
- SSE recovery updates the UI without reload.
- Repeated focus events do not clear cached account rows.

### Verification commands

Run narrow tests first:

```bash
cd backend
go test ./internal/service/agent/... -run 'Codex.*(Bootstrap|Store|Reconciliation|Account)'
go test ./internal/session_manager/... -run 'Codex.*(Admission|Account|Switch)'
go test ./internal/httpd/... -run Codex
```

Regenerate and verify contracts and frontend behavior:

```bash
npm run api
npm run frontend:typecheck
npm --prefix frontend test -- src/renderer/hooks/codex-accounts-state.test.ts src/renderer/components/settings/CodexAccountsSection.test.tsx
```

Run the broader repository checks before completion:

```bash
cd backend && go build ./... && go test ./...
npm run lint
npm run frontend:typecheck
npm --prefix frontend run build
```

## Acceptance Scenarios

### Temporary native failure on daemon startup

1. AO loads the local account store.
2. Global Codex inspection times out.
3. Settings still displays saved accounts.
4. Add Account remains usable.
5. Ordinary Codex sessions can launch if native authentication succeeds.
6. The daemon retries reconciliation automatically.
7. On success, SSE updates the active badge without restarting sessions or the daemon.

### Device account differs from AO's last pointer

1. AO shows reconciliation as checking and does not claim either account is currently in use.
2. AO conclusively identifies the native account.
3. AO imports or matches it and advances the durable pointer safely.
4. Existing sessions remain untouched.

### Device state cannot be verified safely

1. Saved-account reads and isolated account login continue to work.
2. Operations that would overwrite the canonical device credential remain blocked.
3. AO preserves the last durable pointer and canonical credential.
4. A retry or explicit ensure can recover the state later.

## Explicit Non-Goals

- Do not restart existing sessions when the external device account differs from AO's previous active pointer.
- Do not change the existing live-session account-propagation policy in this fix.
- Do not redesign usage/capacity collection.
- Do not add new persistent reconciliation tables unless implementation evidence proves ephemeral state is insufficient.
- Do not weaken the durable switch/recovery safety fence.
- Do not treat a temporary provider failure as proof that an account is signed out.
