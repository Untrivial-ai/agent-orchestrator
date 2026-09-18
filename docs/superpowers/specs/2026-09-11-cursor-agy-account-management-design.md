# Cursor and Agy Multi-Account Design

Date: 2026-09-11
Status: proposed
Branch: `feat/cursor-agy-accounts`

## Problem Statement

Settings → Accounts already manages multiple **Codex** credentials (PR [#4722](https://github.com/Untrivial-ai/agent-orchestrator/pull/4722)). Users who run **Cursor** (`cursor-agent`) and **Agy / Antigravity** (`agy`) still get one signed-in identity per machine (or, for Cursor, one shared AO profile under `{AO_DATA_DIR}/cursor`). A work account and a personal account cannot both live in AO.

This design extends the Codex account-management product — vault, Settings → Accounts UI, loopback-only HTTP, no secrets in SQLite/API/logs — to Cursor and Agy. Isolation mechanics differ per harness because the CLIs do.

## What already exists (do not duplicate)

Checked 2026-09-11:

| Surface | State | Tracking |
| --- | --- | --- |
| Codex coding-agent accounts | Shipped on `main` | [#4722](https://github.com/Untrivial-ai/agent-orchestrator/pull/4722) |
| Claude Code accounts + hot switch | Open PR | [#4813](https://github.com/Untrivial-ai/agent-orchestrator/pull/4813) |
| Cloud Coder accounts | Open PR | [#4787](https://github.com/Untrivial-ai/agent-orchestrator/pull/4787) |
| GitHub SCM multi-account | Closed planning PR; not this work | [#5234](https://github.com/Untrivial-ai/agent-orchestrator/pull/5234) |
| Gate worker `gh auth switch` | Closed, not planned | [#3637](https://github.com/Untrivial-ai/agent-orchestrator/issues/3637) |

No open issue or PR adds Cursor or Agy account vaults.

Canonical Codex architecture: `docs/research/2026-08-31-codex-global-account-management-architecture.md`.

## Approaches considered

### A. Generic “all harnesses” account framework

One `HarnessAccount` package, one HTTP controller, one UI table parameterized by agent id.

Rejected for v1. Codex’s vault, switch journal, and capacity/usage surface are Codex-shaped. Cursor can isolate per process; Agy cannot. A shared framework would hide those differences and fight YAGNI. Clone Codex per harness; share only Settings chrome (`AgentProviderGroup`) and HTTP/LAN/CORS patterns.

### B. Device-global swap for both (copy Codex blindly)

Swap `~/.cursor` and `~/.gemini/antigravity-cli` the way Codex swaps `~/.codex`, then stop/resume AO-owned sessions.

Rejected for **Cursor**. AO already isolates managed Cursor sessions with `CURSOR_DATA_DIR={AO_DATA_DIR}/cursor` and must not mutate the user’s editor profile at `~/.cursor`. Cursor CLI also honors `CURSOR_CONFIG_DIR` and `AGENT_CLI_CREDENTIAL_STORE=file`, so process-scoped homes are available.

Kept for **Agy**. The installed `agy` binary has no documented per-process config-dir env. Community switchers swap `~/.gemini/antigravity-cli`. Setting `HOME` to an account slot would also steal `.ssh` / git config and is forbidden.

### C. Recommended: harness-specific isolation, Codex-shaped product

| Harness | Isolation | Switch | Verify |
| --- | --- | --- | --- |
| Cursor | Per-account dir under `~/.ao/harnesses/cursor/accounts/<uuid>/` as both `CURSOR_CONFIG_DIR` and `CURSOR_DATA_DIR`, with `AGENT_CLI_CREDENTIAL_STORE=file` on pending login/verify | Active pointer for **new** TUI/Chat/reviewer launches. Running sessions keep launch-time env. No stop/resume saga. Never touch `~/.cursor`. | `cursor-agent status --format json` → `isAuthenticated`, `userInfo.email`, `userInfo.userId` (display only) |
| Agy | Opaque vault of file-backed credential files; activate into canonical `~/.gemini/antigravity-cli/` with `GEMINI_FORCE_FILE_STORAGE=true` | Device-global for Agy **workers**; Codex-style stop/resume of AO-owned agy TUI/Chat **workers** only. Reviewers are out of the switch journal (ADR-0002 private `HOME`). | Presence of a safe regular credential file after login; optional display email from a non-token sidecar (`google_accounts.json`) if present. Never parse oauth bytes. Do not treat “binary installed” as authorized once management is enabled. |

This is the v1 design.

## Goals

- Settings → Accounts shows Cursor and Agy `AgentProviderGroup` blocks beside Codex.
- A user can add, reauthenticate, log out, delete, and select an active Cursor account and an active Agy account independently.
- New AO-owned Cursor launches use the active Cursor account’s private dir.
- New AO-owned Agy launches, and the device-global Agy CLI home, use the active Agy account after an explicit switch.
- Secrets never appear in SQLite, API JSON, OpenAPI examples, logs, or telemetry.
- Account-management routes stay on the Loopback Listener, LAN-blocked, and origin-restricted like Codex.

## Non-goals (v1)

- Claude (#4813) or Cloud Coder (#4787) work.
- GitHub/SCM accounts (#5234).
- A generic harness-account framework.
- Per-task / per-session account picker (Codex also omitted this).
- Cursor capacity, usage, or reset-credit (Codex-only provider surface).
- Mutating `~/.cursor` or the Cursor desktop editor’s signed-in account.
- Setting process `HOME` to an Agy account slot.
- CLI CRUD (`ao cursor accounts` / `ao agy accounts`). Login terminals are daemon-invoked, as with Codex.
- Changing Agy’s current “installed binary ⇒ authorized” probe for **Harness Settings** install/login except where account management supplies a better observation for the Accounts surface.

## Boundaries (shared with Codex)

- Each CLI owns its credential format. AO treats credential files as opaque bytes.
- Email / userId are display metadata, not identity. Duplicate emails are valid; each Add account creates a new slot.
- SQLite stores only the active pointer (and, for Agy, the switch journal). Account descriptors live on the filesystem under `~/.ao`.
- File-backed credentials are required for switching. Keyring-backed, env-token, or unsafe credentials remain usable for ordinary launches but cannot be vault-switched (`unmanaged` observation).
- Cached `GET` does no filesystem or native work. `POST .../ensure` is the discovery/refresh path.
- Reconciliation runs on daemon startup, Settings open/focus (ensure), login completion, and switch/activate completion. No polling loop.

## Filesystem layout

```text
<AO_DATA_ROOT>/harnesses/cursor/
├── accounts/<account-uuid>/
│   ├── account.json          # id, label, createdAt — no secrets
│   └── cursor-home/          # opaque CURSOR_CONFIG_DIR + CURSOR_DATA_DIR
├── pending-accounts/<operation-uuid>/
└── (no switch-staging — Cursor activate is a pointer write)

<AO_DATA_ROOT>/harnesses/agy/
├── accounts/<account-uuid>/
│   ├── account.json
│   └── credential-home/      # opaque copies of antigravity-cli credential files
├── pending-accounts/<operation-uuid>/
└── switch-staging/<switch-uuid>/
```

Account directories use mode `0700`; descriptors and credentials use `0600`. Reject symlinks, traversal, unsafe ownership, non-regular files, and hard-linked credentials. Writes use sibling temp files, `fsync`, and atomic rename.

Never log, return, or persist credential bytes. Never project CLI config, caches, sockets, logs, or histories into the API.

Existing Cursor isolation `{AO_DATA_DIR}/cursor` is **not** the vault. On first ensure, if that directory has a safe file-backed credential and the vault is empty, import it as the first managed account (opaque copy into a new slot) and mark it active. Leave the old directory in place so already-running processes keep working. New launches after import use the vault path.

Do not import `~/.cursor`. That is the user’s editor profile.

Agy first ensure: if `~/.gemini/antigravity-cli/` has a safe file-backed credential (or can be forced to file storage on next login) and the vault is empty, import it as the first managed account and make it active — same “external state wins” rule as Codex.

## Cursor runtime

Today `Plugin.AugmentRuntimeEnv` sets `CURSOR_DATA_DIR={dataDir}/cursor`. After this work:

1. If an active managed Cursor account exists, set both `CURSOR_CONFIG_DIR` and `CURSOR_DATA_DIR` to that slot’s `cursor-home`.
2. Else keep today’s `{dataDir}/cursor` fallback.
3. Pending login/verify additionally set `AGENT_CLI_CREDENTIAL_STORE=file`. Ordinary launches after a file-backed login may omit that override so we do not fight a user who later prefers keychain — but the vault slot itself must remain the process home so accounts stay isolated.
4. Seed workspace trust into whichever data dir is used (existing hook/trust behavior, retargeted).
5. Chat ACP (`nativeacp`) already calls `AugmentRuntimeEnv`; no second env path.
6. Cursor reviewer today copies `authInfo` from `~/.cursor/cli-config.json` into a per-reviewer profile. Change it to set `CURSOR_CONFIG_DIR` (and `CURSOR_DATA_DIR` for the reviewer profile) from the **active** Cursor account vault. Stop reading `~/.cursor` for identity.

`CURSOR_API_KEY` (or equivalent daemon-env token) ⇒ account management unsupported (`daemon_token_override`). Ordinary Cursor launches that already treat the env key as authorized stay as they are.

**Activate (not global switch):** `POST /api/v1/agents/cursor/accounts/{accountId}/activate` with `expectedAccountRevision` + `idempotencyKey`. CAS on the SQLite pointer. Confirmation copy: new Cursor sessions use this account; running Cursor sessions keep theirs until the user restarts them. No session stop/resume.

## Agy runtime

Agy has no `AugmentRuntimeEnv` today. Do **not** add `HOME=` isolation.

Login: run `agy` in a pending home that is **not** the user’s real `$HOME`. Force file storage via `GEMINI_FORCE_FILE_STORAGE=true`. The pending process still needs a writable Gemini config path. Implement that by pointing only the Gemini/Antigravity config location at the pending slot — if the binary only honors `HOME` for `~/.gemini`, set `HOME` **for the login terminal process only** to a pending skeleton that contains `.gemini/` and **symlinks** `.ssh`, `.gitconfig` from the real home if those paths exist as the user’s own files. Prefer an env the binary documents; `HOME` for the login PTY is allowed only because that process is short-lived and must not be used for worker sessions. Worker/session processes never get a fake `HOME`.

After verify, copy opaque credential files from the pending `.gemini/antigravity-cli/` into the vault slot. First-account activation (and later switches) atomically install those files into the canonical `~/.gemini/antigravity-cli/` with `GEMINI_FORCE_FILE_STORAGE=true` in AO-owned agy launch env so later writes stay file-backed.

**Switch:** Codex-shaped journal for **worker** TUI/Chat only. Stop/resume only AO-owned agy worker sessions. External Antigravity IDE / CLI processes are outside AO’s process-control boundary; Settings confirmation must say they may need a restart. Phases match Codex (`requested` … `completed` / `failed` / `recovery_required`).

**Reviewers (ADR-0002):** Agy reviewers run with `HOME` rewritten to `~/.ao/reviewer-runtime/<id>/config`. They do **not** read `~/.gemini/antigravity-cli/`, so cloning Codex’s reviewer stop/resume columns would record a credential transaction that never reached the process. v1 leaves running Agy reviewers out of the switch journal. New Agy reviewer launches copy the active vault’s opaque files into `{ConfigRoot}/.gemini/antigravity-cli/` (still under `~/.ao`). Worker sessions never get a fake `HOME`.

`GEMINI_API_KEY`, `GOOGLE_API_KEY`, or `JETSKI_OAUTH_TOKEN` in daemon env ⇒ unmanaged; switching disabled.

**Auth probe:** `backend/internal/adapters/agent/agy/auth.go` currently returns Authorized when the binary exists. Leave Harness Settings install detection as-is if changing it would false-unauthorize users without file-backed tokens. The Accounts surface must **not** list a phantom authorized account from “binary installed”. Verification is: safe credential file in the slot (or global home during reconcile) plus optional display metadata. If no file-backed credential exists, status is `signed_out` / unauthorized, not authorized.

## HTTP

Mirror Codex. Prefixes:

```text
/api/v1/agents/cursor/accounts
/api/v1/agents/cursor/accounts/ensure
/api/v1/agents/cursor/accounts/login-terminal
/api/v1/agents/cursor/accounts/{accountId}/login-terminal
/api/v1/agents/cursor/accounts/{accountId}/logout
/api/v1/agents/cursor/accounts/{accountId}
/api/v1/agents/cursor/accounts/{accountId}/activate
/api/v1/agents/cursor/accounts/login-operations/{operationId}/verify
/api/v1/agents/cursor/accounts/login-operations/{operationId}/cancel
/api/v1/agents/cursor/accounts/events

/api/v1/agents/agy/accounts
/api/v1/agents/agy/accounts/ensure
/api/v1/agents/agy/accounts/login-terminal
/api/v1/agents/agy/accounts/{accountId}/login-terminal
/api/v1/agents/agy/accounts/{accountId}/logout
/api/v1/agents/agy/accounts/{accountId}
/api/v1/agents/agy/accounts/login-operations/{operationId}/verify
/api/v1/agents/agy/accounts/login-operations/{operationId}/cancel
/api/v1/agents/agy/accounts/events
/api/v1/agents/agy/account-switches
/api/v1/agents/agy/account-switches/{switchId}/recover
```

No reset-credit routes. Cursor has activate instead of account-switches. Agy keeps account-switches.

Add all of the above prefixes to `lanControlBlockedPrefixes`. Extend `isCodexAccountPath` to an account-management path helper that includes Cursor and Agy. Model/probe/install routes stay reachable on LAN.

Wire: `dto.go` + `schemaNames` in `backend/internal/httpd/apispec/specgen/build.go`, then `npm run api`. Commit `openapi.yaml` and `frontend/src/api/schema.ts` with the Go changes.

DTOs must not include filesystem paths, env, argv, credential filenames, or native ids beyond what Codex already redacts (handle id + title for the login terminal).

## Persistence

Latest shipped migration is `0135`. Add:

- `0136_cursor_account_management.sql` — `cursor_active_account` singleton (account_id, revision, timestamps). No switch tables.
- `0137_agy_account_management.sql` — `agy_active_account` plus `agy_account_switches` / `agy_account_switch_sessions` cloned from `0124` **without** reviewer stop/resume columns. Same worker phases and CDC triggers into `change_log`.

Do not edit shipped migrations. Do not hand-edit `gen/`. Run `npm run sqlc`.

SQLite never stores email, tokens, paths, or terminal output.

## UI

`frontend/src/renderer/components/settings/settingsCatalog.tsx` section `agents` (label already “Accounts”) currently renders only `CodexAccountsSection`. Change it to a stack of three `AgentProviderGroup` sections: Codex (unchanged), Cursor, Agy.

Reuse login-terminal panel, row, and confirm-dialog patterns from `CodexAccountsSection.tsx`. Cursor switch copy is activate-without-restart. Agy switch copy is Codex-like (AO sessions restart; other Agy clients may need a manual restart).

Harness Settings remains install/login for a missing binary. Accounts is the vault. Do not merge those screens.

No CLI account CRUD.

## Testing

- Vault FS: reject symlink/unsafe/non-regular; atomic write; opaque copy never appears in logs (forbidden-substring tests like Codex).
- Cursor: `AugmentRuntimeEnv` uses active account dir; fallback `{dataDir}/cursor` when no pointer; `CURSOR_API_KEY` ⇒ unmanaged; activate CAS; login verify uses `status --format json` with the pending env; reviewer no longer reads `~/.cursor`.
- Agy: login pending does not leak HOME-skeleton paths in API; switch stop/resume only `agy` sessions; LAN 404 for account routes; model/probe still 200.
- HTTP: cached GET does no native work; CORS origin reject; OpenAPI drift.
- Renderer: add/verify/logout/delete/activate (Cursor) / switch (Agy) against mocked API, matching `CodexAccountsSection.test.tsx`.

## Success criteria

- Two Cursor accounts can be added in Settings; new AO Cursor sessions follow the active one; a running session started on account A stays on A until restart.
- Two Agy accounts can be added; switching updates `~/.gemini/antigravity-cli/` and restarts AO-owned agy **worker** TUI/Chat sessions onto the target account. Running Agy reviewers keep their private gateway credentials until restarted; new reviewer launches receive the active vault files under their ConfigRoot.
- Codex Accounts behavior is unchanged.
- `~/.cursor` is never written by this feature.
- LAN clients cannot hit the new routes.
- `npm run api` artifacts are committed and in sync.
