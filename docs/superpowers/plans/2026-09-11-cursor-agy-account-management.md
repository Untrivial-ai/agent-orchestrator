# Cursor and Agy Account Management Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let Settings → Accounts vault and select multiple Cursor (`cursor-agent`) and Agy (`agy`) credentials, extending the Codex product from PR #4722 without a generic harness-account framework.

**Architecture:** Clone Codex’s filesystem vault, cached/ensure HTTP, login-terminal, LAN block, and CORS origin gate. Cursor isolates per process via `CURSOR_CONFIG_DIR` + `CURSOR_DATA_DIR` (no device-global `~/.cursor`, no stop/resume). Agy activates opaque file-backed credentials into `~/.gemini/antigravity-cli/` and stop/resumes only AO-owned agy **worker** TUI/Chat sessions. Agy reviewers stay out of the switch journal (ADR-0002 private `HOME`); new reviewer launches get vault files under ConfigRoot. SQLite stores only active pointers (and Agy’s switch journal). Secrets never leave the vault.

**Tech Stack:** Go daemon (Cobra CLI is not part of this surface), SQLite + goose/sqlc, chi HTTP + generated OpenAPI, Electron/React Settings UI (`AgentProviderGroup`).

**Spec:** `docs/superpowers/specs/2026-09-11-cursor-agy-account-management-design.md`

**Worktree:** `/home/nurul/.config/superpowers/worktrees/agent-orchestrator/feat-cursor-agy-accounts` on `feat/cursor-agy-accounts`.

---

## File plan

PLANNED FILES (create unless noted modify):

- `docs/superpowers/specs/2026-09-11-cursor-agy-account-management-design.md` — already written
- `docs/superpowers/plans/2026-09-11-cursor-agy-account-management.md` — this file
- `backend/internal/domain/cursor_account.go`
- `backend/internal/domain/agy_account.go`
- `backend/internal/ports/cursor_accounts.go`
- `backend/internal/ports/agy_accounts.go`
- `backend/internal/storage/sqlite/migrations/0136_cursor_account_management.sql`
- `backend/internal/storage/sqlite/migrations/0137_agy_account_management.sql`
- `backend/internal/storage/sqlite/queries/cursor_account_management.sql`
- `backend/internal/storage/sqlite/queries/agy_account_management.sql`
- `backend/internal/storage/sqlite/store/cursor_account_management_store.go` (+ test)
- `backend/internal/storage/sqlite/store/agy_account_management_store.go` (+ test)
- `backend/internal/service/agent/cursor_accounts.go` and siblings listed in Task 3
- `backend/internal/service/agent/agy_accounts.go` and siblings listed in Task 6
- `backend/internal/httpd/controllers/cursor_accounts.go` (+ dto + test)
- `backend/internal/httpd/controllers/agy_accounts.go` (+ dto + test)
- `backend/internal/httpd/controllers/dto.go` (modify)
- `backend/internal/httpd/apispec/specgen/build.go` (modify)
- `backend/internal/httpd/lan_listener.go` (modify) + `lan_listener_test.go` (modify)
- `backend/internal/httpd/cors.go` (modify) + cors tests
- `backend/internal/httpd/api.go` (modify)
- `backend/internal/daemon/daemon.go` (modify)
- `backend/internal/adapters/agent/cursor/cursor.go` (modify) + tests
- `backend/internal/adapters/reviewer/cursor/config.go` (modify) + tests
- `backend/internal/session_manager/agy_account_switch.go` (+ test) — clone `codex_account_switch.go`
- `frontend/src/renderer/components/settings/settingsCatalog.tsx` (modify)
- `frontend/src/renderer/components/settings/CursorAccountsSection.tsx` (+ test, row, details, login panel, hooks)
- `frontend/src/renderer/components/settings/AgyAccountsSection.tsx` (+ test, row, details, login panel, hooks)
- `frontend/src/renderer/i18n/en.json` (modify)
- generated: `backend/internal/storage/sqlite/gen/*` via `npm run sqlc`
- generated: `backend/internal/httpd/apispec/openapi.yaml` + `frontend/src/api/schema.ts` via `npm run api`

OUT OF SCOPE:

- Claude/Cloud Coder account packages
- GitHub SCM identity
- CLI account CRUD
- `backend/internal/observe/**` importing adapters
- Editing shipped migrations before 0136
- Hand-editing sqlc/OpenAPI outputs

Clone map (Codex → Cursor, drop capacity/usage/reset-credit/switch saga):

| Codex | Cursor |
| --- | --- |
| `domain/codex_account.go` | `domain/cursor_account.go` |
| `ports/codex_accounts.go` | `ports/cursor_accounts.go` (status probe, not app-server) |
| `service/agent/codex_accounts.go` | `service/agent/cursor_accounts.go` |
| `service/agent/codex_account_login.go` | `service/agent/cursor_account_login.go` |
| `service/agent/codex_account_catalog.go` | `service/agent/cursor_account_catalog.go` |
| `httpd/controllers/codex_accounts.go` | `httpd/controllers/cursor_accounts.go` |
| `migrations/0124_*.sql` active table only | `0136_cursor_account_management.sql` |

Clone map (Codex → Agy, keep switch journal, drop capacity/usage/reset-credit):

| Codex | Agy |
| --- | --- |
| `domain/codex_account.go` + `codex_account_switch.go` | `domain/agy_account.go` + `agy_account_switch.go` |
| `session_manager/codex_account_switch.go` | `session_manager/agy_account_switch.go` (filter harness `agy` **workers** only; skip reviewer journal) |
| `migrations/0124_*.sql` | `0137_agy_account_management.sql` (active pointer + switch journal **without** reviewer stop/resume columns) |

---

### Task 1: Cursor domain + SQLite active pointer

**Files:**
- Create: `backend/internal/domain/cursor_account.go`
- Create: `backend/internal/storage/sqlite/migrations/0136_cursor_account_management.sql`
- Create: `backend/internal/storage/sqlite/queries/cursor_account_management.sql`
- Create: `backend/internal/storage/sqlite/store/cursor_account_management_store.go`
- Create: `backend/internal/storage/sqlite/store/cursor_account_management_store_test.go`

- [ ] **Step 1: Write the failing store test**

```go
func TestCursorActiveAccountRoundTrip(t *testing.T) {
	store := openTestStore(t) // same helper as codex_account_management_store_test.go
	ctx := context.Background()
	if _, ok, err := store.GetCursorActiveAccount(ctx); err != nil || ok {
		t.Fatalf("empty pointer: ok=%v err=%v", ok, err)
	}
	got, err := store.PutCursorActiveAccount(ctx, domain.CursorActiveAccount{
		AccountID: "11111111-1111-1111-1111-111111111111", Revision: 1,
		ActivatedAt: time.Unix(1, 0).UTC(), UpdatedAt: time.Unix(1, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Revision != 1 || got.AccountID != "11111111-1111-1111-1111-111111111111" {
		t.Fatalf("got %#v", got)
	}
	if _, err := store.PutCursorActiveAccount(ctx, domain.CursorActiveAccount{AccountID: got.AccountID, Revision: 1}); !errors.Is(err, ports.ErrCursorActiveAccountConflict) {
		t.Fatalf("stale revision err = %v", err)
	}
}
```

- [ ] **Step 2: Run it to make sure it fails**

Run: `cd backend && go test ./internal/storage/sqlite/store -run TestCursorActiveAccountRoundTrip`
Expected: FAIL compile (types/methods missing)

- [ ] **Step 3: Add domain types, migration, queries, store**

`0136_cursor_account_management.sql`:

```sql
-- +goose Up
CREATE TABLE cursor_active_account (
    singleton_id INTEGER PRIMARY KEY CHECK (singleton_id = 1),
    account_id TEXT NOT NULL,
    revision INTEGER NOT NULL CHECK (revision >= 1),
    activated_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL
);
-- +goose Down
DROP TABLE IF EXISTS cursor_active_account;
```

Domain: copy `CodexAccountSnapshot` / login operation / `CodexActiveAccount` from `backend/internal/domain/codex_account.go`, rename Cursor, **omit** capacity, usage, reset-credit, and `CodexAccountSwitch`. AuthMethod enum: `browser`, `api_key`, `other`, `unknown`.

Queries: clone `GetCodexActiveAccount` / `PutCodexActiveAccount` / `ClearCodexActiveAccount` from `backend/internal/storage/sqlite/queries/codex_account_management.sql`.

Store: clone `codex_account_management_store.go`, drop switch methods.

- [ ] **Step 4: Generate sqlc and pass the test**

Run:

```bash
npm run sqlc
cd backend && go test ./internal/storage/sqlite/store -run TestCursorActiveAccount -count=1
```

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add backend/internal/domain/cursor_account.go \
  backend/internal/storage/sqlite/migrations/0136_cursor_account_management.sql \
  backend/internal/storage/sqlite/queries/cursor_account_management.sql \
  backend/internal/storage/sqlite/store/cursor_account_management_store.go \
  backend/internal/storage/sqlite/store/cursor_account_management_store_test.go \
  backend/internal/storage/sqlite/gen
git commit -m "$(cat <<'EOF'
feat: add Cursor active-account pointer storage

SQLite holds only the singleton Cursor account pointer so credential
bytes stay on the filesystem vault, matching Codex.
EOF
)"
```

---

### Task 2: Cursor vault + status probe

**Files:**
- Create: `backend/internal/ports/cursor_accounts.go`
- Create: `backend/internal/service/agent/cursor_account_catalog.go`
- Create: `backend/internal/service/agent/cursor_account_catalog_test.go`
- Create: `backend/internal/adapters/agent/cursor/status.go`
- Create: `backend/internal/adapters/agent/cursor/status_test.go`

- [ ] **Step 1: Write failing catalog tests**

Cover:

1. Account dir mode `0700`; credential files `0600`; symlink `cursor-home` rejected (`account_unsafe_path`).
2. Descriptor JSON has `id`, `label`, `createdAt` only — a test that marshals `account.json` and fails if keys include `token`, `auth.json`, `path`.
3. Probe: fake `cursor-agent` script that prints `{"isAuthenticated":true,"userInfo":{"email":"a@example.com","userId":"u1"}}` only when `CURSOR_CONFIG_DIR` and `CURSOR_DATA_DIR` equal the slot and `AGENT_CLI_CREDENTIAL_STORE=file`. Observation email is `a@example.com`; raw JSON is not logged (hook a slog handler and forbid `u1` if we decide userId is not display — **userId may be stored in daemon memory as display metadata only, never in SQLite**).

- [ ] **Step 2: Run tests, expect FAIL**

Run: `cd backend && go test ./internal/service/agent -run CursorAccountCatalog -count=1`

- [ ] **Step 3: Implement catalog + status**

Layout:

```text
<AO_DATA_ROOT>/harnesses/cursor/accounts/<uuid>/account.json
<AO_DATA_ROOT>/harnesses/cursor/accounts/<uuid>/cursor-home/
<AO_DATA_ROOT>/harnesses/cursor/pending-accounts/<operation-uuid>/
```

`ports.CursorAccountContext` is `{ Home string }` (the `cursor-home` path). Factory opens a client that runs `cursor-agent status --format json` with env:

```go
env["CURSOR_CONFIG_DIR"] = home
env["CURSOR_DATA_DIR"] = home
env["AGENT_CLI_CREDENTIAL_STORE"] = "file"
```

Parse only `isAuthenticated` (bool) and `userInfo.email` (string). Ignore every other field. Do not retain access tokens if the CLI ever adds them to this payload — add a test that injects `"accessToken":"leak"` and asserts the observation struct has no such field and logs do not contain `leak`.

Treat missing binary as capability `accountManagement=unknown`, not authorized.

- [ ] **Step 4: Pass tests**

Run: `cd backend && go test ./internal/service/agent -run CursorAccountCatalog -count=1 && go test ./internal/adapters/agent/cursor -run Status -count=1`

- [ ] **Step 5: Commit**

```bash
git commit -m "$(cat <<'EOF'
feat: vault Cursor credentials in isolated cursor-home dirs

Pending login and verify force file-backed credentials and never parse
opaque CLI files; status JSON is reduced to email plus auth state.
EOF
)"
```

---

### Task 3: Cursor account service (login, activate, logout, delete)

**Files:**
- Create: `backend/internal/service/agent/cursor_accounts.go`
- Create: `backend/internal/service/agent/cursor_account_login.go`
- Create: `backend/internal/service/agent/cursor_account_service.go`
- Create: `backend/internal/service/agent/cursor_accounts_test.go`
- Modify: `backend/internal/service/agent/service.go` (wire `cursorAccounts` like `codexAccounts`)

- [ ] **Step 1: Write failing service tests** (clone `codex_accounts_test.go` names, Cursor-shaped)

Required cases:

- `TestCachedCursorAccountsPerformsNoFilesystemOrNativeWork` — factory that panics on Open; cached GET must not call it.
- `TestCachedCursorAccountsProjectsOnlySafeActiveLoginMetadata` — forbid substrings `pending-accounts`, `CURSOR_CONFIG_DIR`, `auth.json`, `cursor-home`.
- `TestCursorActivateUpdatesPointerWithoutStoppingSessions` — activate B while a fake session list is non-empty; assert no stop/resume calls (pass a nil/recording switch coordinator that fatals if invoked).
- `TestCursorActivateStaleRevisionConflicts`
- `TestCursorAPIKeyLeavesManagementUnmanaged` — `os.Setenv("CURSOR_API_KEY", "x")` in daemon env observation ⇒ `unmanagedGlobalAccount.reasonCode == "daemon_token_override"` (or Cursor-specific `cursor_api_key_override`); activate returns conflict.
- `TestImportLegacyAOCursorHomeOnFirstEnsure` — seed `{dataDir}/cursor` with a fake authorized status; vault empty; ensure creates one managed account and sets it active; `{dataDir}/cursor` still exists.

Login: `cursor-agent login` in pending home with the three env vars above. Reuse Codex’s trusted shell-terminal opener (`SetCodexAccountLoginTerminal` pattern — add `SetCursorAccountLoginTerminal`).

- [ ] **Step 2: Run, expect FAIL**

Run: `cd backend && go test ./internal/service/agent -run CursorAccount -count=1`

- [ ] **Step 3: Implement manager**

Activate algorithm:

```
acquire mutation
verify target slot authorized
CAS PutCursorActiveAccount(expectedRevision+1)
publish SSE
```

No staging of `~/.cursor`. No session manager involvement.

Bootstrap import: if vault empty and `{dataDir}/cursor` verifies authorized via the status client with that home, opaque-copy into a new uuid slot (copy tree with the same safety checks as Codex credential copy).

Wire in `NewWithDeps`: `CursorAccountRoot`, `CursorPendingRoot`, `CursorLegacyHome` (`filepath.Join(cfg.DataDir, "cursor")`), `CursorAccountState`, `CursorAccounts` factory.

- [ ] **Step 4: Pass tests**

Run: `cd backend && go test ./internal/service/agent -run 'CursorAccount|CachedCursor' -count=1`

- [ ] **Step 5: Commit**

```bash
git commit -m "$(cat <<'EOF'
feat: manage Cursor account login and in-process activate

New launches follow the active vault pointer; running sessions keep
their launch env, so Cursor never needs a Codex-style stop/resume saga.
EOF
)"
```

---

### Task 4: Point Cursor launches and reviewers at the active account

**Files:**
- Modify: `backend/internal/adapters/agent/cursor/cursor.go`
- Modify: `backend/internal/adapters/agent/cursor/cursor_test.go`
- Modify: `backend/internal/adapters/reviewer/cursor/config.go` (+ existing tests)
- Modify: `backend/internal/session_manager/manager.go` only if `AugmentRuntimeEnv` needs the active home injected — prefer resolving the path **inside the adapter** via a small port set at daemon wire-up, not by importing the account service into the adapter.

Recommended seam: `ports.CursorLaunchHome` func or `cursor.Plugin.SetLaunchHome(func() string)` set from daemon after agent service bootstrap. Adapter stays free of SQLite.

- [ ] **Step 1: Write failing tests**

Update `TestAugmentRuntimeEnvUsesAODataDir`:

```go
func TestAugmentRuntimeEnvUsesActiveAccountHome(t *testing.T) {
	plugin := New()
	plugin.SetLaunchHome(func() string { return "/ao-data/harnesses/cursor/accounts/aaa/cursor-home" })
	env := map[string]string{}
	plugin.AugmentRuntimeEnv(env, "/ao-data")
	if env["CURSOR_DATA_DIR"] != "/ao-data/harnesses/cursor/accounts/aaa/cursor-home" {
		t.Fatalf("CURSOR_DATA_DIR=%q", env["CURSOR_DATA_DIR"])
	}
	if env["CURSOR_CONFIG_DIR"] != env["CURSOR_DATA_DIR"] {
		t.Fatalf("CONFIG and DATA dirs must match for account isolation")
	}
}

func TestAugmentRuntimeEnvFallsBackToLegacyAOCursorHome(t *testing.T) {
	plugin := New()
	env := map[string]string{}
	plugin.AugmentRuntimeEnv(env, "/ao-data")
	if env["CURSOR_DATA_DIR"] != "/ao-data/cursor" {
		t.Fatalf("CURSOR_DATA_DIR=%q", env["CURSOR_DATA_DIR"])
	}
}
```

Reviewer: `reviewerEnv` must set `CURSOR_CONFIG_DIR` to the launch home (active account) and must **not** read `filepath.Join(home, ".cursor", "cli-config.json")`. Add a test that writes a fake `~/.cursor/cli-config.json` with `"email":"editor@example.com"` and asserts reviewer config does not contain that email.

- [ ] **Step 2: Run, expect FAIL**

Run: `cd backend && go test ./internal/adapters/agent/cursor ./internal/adapters/reviewer/cursor -count=1`

- [ ] **Step 3: Implement env + reviewer**

`AugmentRuntimeEnv`:

```go
func (p *Plugin) AugmentRuntimeEnv(env map[string]string, dataDir string) {
	home := ""
	if p.launchHome != nil {
		home = strings.TrimSpace(p.launchHome())
	}
	if home == "" && strings.TrimSpace(dataDir) != "" {
		home = cursorDataDir(dataDir) // filepath.Join(dataDir, "cursor")
	}
	if home == "" {
		return
	}
	env[cursorDataDirEnv] = home
	env["CURSOR_CONFIG_DIR"] = home
	termtheme.Apply(env, dataDir)
}
```

Keep seeding trust into `CURSOR_DATA_DIR` (existing hook tests).

Daemon: `cursorPlugin.SetLaunchHome(func() string { return agentSvc.CursorLaunchHome() })` returning the active slot’s `cursor-home` or `""`.

- [ ] **Step 4: Pass tests**

Run: `cd backend && go test ./internal/adapters/agent/cursor ./internal/adapters/reviewer/cursor ./internal/service/agent -run Cursor -count=1`

- [ ] **Step 5: Commit**

```bash
git commit -m "$(cat <<'EOF'
feat: launch Cursor workers and reviewers from the active vault home

Managed sessions keep using AO-owned dirs instead of ~/.cursor, and
reviewers stop copying the desktop editor's cli-config identity.
EOF
)"
```

---

### Task 5: Cursor HTTP, OpenAPI, LAN, CORS

**Files:**
- Create: `backend/internal/httpd/controllers/cursor_accounts.go`
- Create: `backend/internal/httpd/controllers/cursor_accounts_dto.go`
- Create: `backend/internal/httpd/controllers/cursor_accounts_test.go`
- Modify: `backend/internal/httpd/controllers/dto.go`
- Modify: `backend/internal/httpd/apispec/specgen/build.go`
- Modify: `backend/internal/httpd/api.go`
- Modify: `backend/internal/httpd/lan_listener.go`
- Modify: `backend/internal/httpd/lan_listener_test.go`
- Modify: `backend/internal/httpd/cors.go` (+ tests if present)
- Modify: `backend/internal/daemon/daemon.go`

- [ ] **Step 1: Write failing controller + LAN tests**

Clone `codex_accounts_test.go` without reset-credit and without `account-switches`. Add:

```go
func TestActivateCursorAccountAccepted(t *testing.T) { /* POST /accounts/{id}/activate */ }
```

LAN test in `lan_listener_test.go`: extend the blocked-path list:

```go
"/api/v1/agents/cursor/accounts",
"/api/v1/agents/cursor/accounts/events",
"/api/v1/agents/cursor/accounts/acct/activate",
```

Keep Codex model routes 200. Cursor probe/install must remain unblocked (`POST /api/v1/agents/cursor/probe` is not an accounts prefix).

CORS: rename `isCodexAccountPath` → `isAccountManagementPath` and include Cursor prefixes. Origin mismatch ⇒ 403 `ORIGIN_FORBIDDEN`.

DTO field names: `CursorAccountsResponse` with `activeAccountId`, `accountRevision`, `accounts`, `capabilities`, `unmanagedGlobalAccount`, `activeLogin`. No `currentSwitch`. No capacity.

- [ ] **Step 2: Run, expect FAIL**

Run: `cd backend && go test ./internal/httpd/... -count=1`

- [ ] **Step 3: Implement routes and regenerate API**

Routes (chi, under `/api/v1`):

```
GET    /agents/cursor/accounts
POST   /agents/cursor/accounts/ensure
POST   /agents/cursor/accounts/login-terminal
POST   /agents/cursor/accounts/{accountId}/login-terminal
POST   /agents/cursor/accounts/{accountId}/logout
DELETE /agents/cursor/accounts/{accountId}
POST   /agents/cursor/accounts/{accountId}/activate
POST   /agents/cursor/accounts/login-operations/{operationId}/verify
POST   /agents/cursor/accounts/login-operations/{operationId}/cancel
GET    /agents/cursor/accounts/events   // RegisterStreams, no request timeout
```

`lanControlBlockedPrefixes` add `/api/v1/agents/cursor/accounts`.

Add `schemaNames` entries for every new `ControllersCursor*` type, plus `projectOperations()` rows cloned from Codex (skip reset-credit and switch ops; add `activateCursorAccount`).

Run:

```bash
npm run api
cd backend && go test ./internal/httpd/... -count=1
```

Expected: PASS, including spec drift.

Daemon `agentDeps` gets Cursor roots under `cfg.StateDir/harnesses/cursor/{accounts,pending-accounts}`.

- [ ] **Step 4: Commit including generated spec/types**

```bash
git add backend/internal/httpd backend/internal/daemon \
  backend/internal/httpd/apispec/openapi.yaml frontend/src/api/schema.ts
git commit -m "$(cat <<'EOF'
feat: expose loopback Cursor account-management HTTP

Account routes stay LAN-blocked and origin-gated like Codex so mobile
and preview browsers cannot read or mutate Cursor credentials.
EOF
)"
```

---

### Task 6: Agy domain + vault + switch journal

**Files:**
- Create: `backend/internal/domain/agy_account.go`
- Create: `backend/internal/domain/agy_account_switch.go` (clone `codex_account_switch.go`)
- Create: `backend/internal/ports/agy_accounts.go`
- Create: `backend/internal/storage/sqlite/migrations/0137_agy_account_management.sql`
- Create: `backend/internal/storage/sqlite/queries/agy_account_management.sql`
- Create: `backend/internal/storage/sqlite/store/agy_account_management_store.go` (+ test)
- Create: `backend/internal/service/agent/agy_account_catalog.go` (+ test)

- [ ] **Step 1: Write failing tests**

Store: same round-trip as Cursor **plus** one-active-switch unique index (clone Codex switch store tests).

Catalog: opaque copy of files named `antigravity-oauth-token` and `jetski-standalone-oauth-token` if present as regular files; skip other names except a display-only `google_accounts.json` whose JSON `email` / `active_account` string may populate `AccountEmail`. A fixture token file containing `secret-token-value` must never appear in `fmt.Sprintf("%v", snapshot)` or slog output.

Unsafe: symlink credential rejected.

- [ ] **Step 2: Run, expect FAIL**

Run: `cd backend && go test ./internal/storage/sqlite/store -run Agy -count=1`

- [ ] **Step 3: Implement migration 0137 from 0124, minus reviewer columns**

Replace `codex_` table/trigger names with `agy_`. Keep the worker phase CHECK list and CDC `session_updated` payloads. Drop every `reviewer_*` column from `agy_account_switch_sessions` — Agy reviewers use ADR-0002 `HOME=ConfigRoot` and must not be journaled as if they consumed `~/.gemini/antigravity-cli`.

`npm run sqlc`

Canonical global dir helper:

```go
func agyGlobalCLIDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".gemini", "antigravity-cli")
}
```

Never set worker `HOME` to a vault slot.

- [ ] **Step 4: Pass tests and commit**

```bash
git commit -m "$(cat <<'EOF'
feat: add Agy account vault and switch journal storage

Agy credentials stay opaque files under ~/.ao; SQLite records only the
active pointer and Codex-shaped switch phases.
EOF
)"
```

---

### Task 7: Agy login, global activate, AO-owned stop/resume

**Files:**
- Create: `backend/internal/service/agent/agy_accounts.go`
- Create: `backend/internal/service/agent/agy_account_login.go`
- Create: `backend/internal/service/agent/agy_account_service.go`
- Create: `backend/internal/service/agent/agy_accounts_test.go`
- Create: `backend/internal/session_manager/agy_account_switch.go`
- Create: `backend/internal/session_manager/agy_account_management_test.go`
- Modify: `backend/internal/adapters/agent/agy/auth.go` only if Accounts verification needs a probe helper in the adapter; do **not** change Harness Settings to unauthorized-when-installed without a file token.
- Modify: `backend/internal/service/agent/service.go`, `backend/internal/daemon/daemon.go`

- [ ] **Step 1: Write failing tests**

- Cached GET does no native work; API projection forbids `GEMINI_FORCE_FILE_STORAGE`, `antigravity-oauth-token`, `credential-home`, real home paths.
- Login terminal env includes `GEMINI_FORCE_FILE_STORAGE=true`. If the login PTY uses a skeleton `HOME`, API metadata still omits that path. Worker launch env in session_manager tests must **not** include a replaced `HOME`.
- `TestAgySwitchStopsOnlyAgyWorkerSessions` — fixture with one `agy` TUI, one `cursor` TUI, and one running Agy reviewer; only the agy **worker** is stopped/resumed; cursor worker and Agy reviewer are untouched (no reviewer journal rows).
- `TestNewAgyReviewerLaunchInstallsActiveVaultIntoConfigRoot` — reviewer env `HOME` is `{dataDir}/reviewer-runtime/<id>/config`; after launch prep, `{ConfigRoot}/.gemini/antigravity-cli/` contains the opaque active-account files. Worker env still has no replaced `HOME`.
- `TestAgySwitchConfirmationDoesNotClaimExternalClientsAreStopped`
- Env `GEMINI_API_KEY` or `GOOGLE_API_KEY` or `JETSKI_OAUTH_TOKEN` ⇒ unmanaged, switch conflict.
- First ensure imports file-backed `~/.gemini/antigravity-cli` into the vault when empty (use `t.TempDir` + fake home via a test hook `agyGlobalCLIDir` variable).
- Verify: missing credential file ⇒ `unauthorized` / login status `unauthorized`, **not** authorized because `agy` exists.

- [ ] **Step 2: Run, expect FAIL**

Run: `cd backend && go test ./internal/service/agent -run Agy -count=1 && go test ./internal/session_manager -run AgyAccount -count=1`

- [ ] **Step 3: Implement**

Login command remains `agy` (Harness auth plan already uses that). Pending skeleton:

```
pending/<op>/home/.gemini/antigravity-cli/   # writable, mode 0700
pending/<op>/home/.ssh -> <real>/.ssh        # symlink only if real path is a directory owned by the user
```

Worker/session `AugmentRuntimeEnv`: add on `agy.Plugin` setting `GEMINI_FORCE_FILE_STORAGE=true` only — not `HOME`. After a committed switch, the canonical CLI dir already holds the active files.

Switch coordinator: copy `session_manager/codex_account_switch.go`, filter harness id `agy` **workers** (TUI/Chat). Do not record or stop Agy reviewers. Wire `agentSvc.SetAgyAccountSwitchCoordinator(sessMgr)` and `sessMgr.SetAgyAccountSwitchObserver(agentSvc.PublishAgyAccounts)`.

New Agy reviewer launches: after `reviewgateway.PrepareHostTrustedEnvironment`, opaque-copy the active vault into `filepath.Join(env.ConfigRoot, ".gemini", "antigravity-cli")` so ADR-0002 `HOME=ConfigRoot` sees the account. Do not point reviewer `HOME` at the vault or at `~/.gemini`.

Atomic activate: stage files in `harnesses/agy/switch-staging/<id>/`, fsync, rename into `agyGlobalCLIDir()`, then verify the vault slot still matches (size/mtime/bytes equality) before committing the pointer. External write during the transaction ⇒ `recovery_required`, same as Codex.

- [ ] **Step 4: Pass tests**

Run: `cd backend && go test ./internal/service/agent ./internal/session_manager ./internal/adapters/agent/agy -count=1`

- [ ] **Step 5: Commit**

```bash
git commit -m "$(cat <<'EOF'
feat: switch Agy accounts by activating file-backed CLI credentials

AO stops and resumes only Agy worker TUI/Chat sessions it owns; the
device-global antigravity-cli home is updated atomically without rewriting
worker HOME. Reviewers stay on ADR-0002 ConfigRoot copies.
EOF
)"
```

---

### Task 8: Agy HTTP, OpenAPI, LAN, CORS

**Files:** same HTTP files as Task 5, Agy-named, plus `account-switches` routes.

- [ ] **Step 1: Write failing controller + LAN tests**

Blocked prefixes:

```
/api/v1/agents/agy/accounts
/api/v1/agents/agy/account-switches
```

Do **not** block `/api/v1/agents/agy/probe` or install.

- [ ] **Step 2: Implement routes, `schemaNames`, `npm run api`**

Routes match Codex minus reset-credit. Include `StartAgyAccountSwitchRequest` (`targetAccountId`, `expectedAccountRevision`, `idempotencyKey`).

- [ ] **Step 3: Pass `go test ./internal/httpd/...` and commit**

```bash
git commit -m "$(cat <<'EOF'
feat: expose loopback Agy account-management HTTP

LAN and CORS origin gates cover Agy accounts and switches so credential
mutations stay on the unauthenticated loopback listener only.
EOF
)"
```

---

### Task 9: Settings → Accounts UI for Cursor and Agy

**Files:**
- Modify: `frontend/src/renderer/components/settings/settingsCatalog.tsx`
- Create: `frontend/src/renderer/components/settings/CursorAccountsSection.tsx` (+ `CursorAccountRow.tsx`, `CursorAccountDetails.tsx`, `CursorAccountLoginTerminalPanel.tsx`, tests)
- Create: `frontend/src/renderer/hooks/useCursorAccountsQuery.ts` (+ actions + state helpers, tests)
- Create: `frontend/src/renderer/components/settings/AgyAccountsSection.tsx` (+ row/details/login panel, tests)
- Create: `frontend/src/renderer/hooks/useAgyAccountsQuery.ts` (+ actions + state helpers, tests)
- Modify: `frontend/src/renderer/i18n/en.json`
- Modify: `CodexAccountsSection` **only** if the catalog wrapper needs a fragment; do not restyle Codex.

- [ ] **Step 1: Write failing renderer tests**

Clone `CodexAccountsSection.test.tsx`:

- Cursor: ensure on mount; add account opens login terminal; verify completed focuses the new card; activate posts `{ expectedAccountRevision, idempotencyKey }` to `/api/v1/agents/cursor/accounts/{accountId}/activate`; logout/delete; no reset-credit control rendered; switch dialog text includes “running sessions keep their current account”.
- Agy: switch posts `/api/v1/agents/agy/account-switches`; dialog text includes that other Agy clients may need a restart; no reset-credit.

- [ ] **Step 2: Run, expect FAIL**

Run: `cd frontend && npx vitest run src/renderer/components/settings/CursorAccountsSection.test.tsx src/renderer/components/settings/AgyAccountsSection.test.tsx`

- [ ] **Step 3: Implement sections**

`settingsCatalog.tsx` `agents` render:

```tsx
render: (_t, titleHidden) => (
	<>
		<CodexAccountsSection titleHidden={titleHidden} />
		<div className="mt-4">
			<CursorAccountsSection titleHidden={titleHidden} />
		</div>
		<div className="mt-4">
			<AgyAccountsSection titleHidden={titleHidden} />
		</div>
	</>
),
```

Use `AgentProviderGroup` with `provider="cursor"` and `provider="agy"`. Reuse `CodexAccountLoginTerminalPanel` if it is already generic (shell handle + verify/cancel); otherwise copy it to keep Codex untouched.

i18n keys: `settings.cursorAccounts.*` and `settings.agyAccounts.*` parallel to `settings.codexAccounts.*` minus plan/capacity/reset strings.

DESIGN.md: clone agent-orchestrator web look; shadcn primitives; no new accent.

- [ ] **Step 4: Typecheck**

Run: `npm run frontend:typecheck`

- [ ] **Step 5: Commit**

```bash
git commit -m "$(cat <<'EOF'
feat: add Cursor and Agy account groups to Settings

Accounts stays the Codex-shaped vault UI; Cursor activates in-process
and Agy uses a confirmed global switch with the same row chrome.
EOF
)"
```

---

### Task 10: Wire daemon, readiness, and forbidden-leak sweep

**Files:**
- Modify: `backend/internal/daemon/daemon.go`
- Modify: `backend/internal/service/agent/readiness.go` (invalidate Cursor/Agy on harness probe like Codex)
- Modify: `backend/internal/httpd/api.go` if Task 5/8 left a TODO
- Tests: leak substring tables on all three account HTTP fixtures

- [ ] **Step 1: Write a shared leak test or extend existing ones**

Forbidden in JSON for Cursor and Agy responses:

```
auth.json, credential-home, cursor-home, pending-accounts, CODEX_HOME,
CURSOR_CONFIG_DIR, CURSOR_DATA_DIR, AGENT_CLI_CREDENTIAL_STORE,
GEMINI_FORCE_FILE_STORAGE, antigravity-oauth-token, HOME=, /Users/, /home/
```

(Allow `/home/` only if you cannot avoid — prefer omitting paths entirely so this list stays strict.)

- [ ] **Step 2: Implement daemon Deps fields and Warm*Accounts on boot** (mirror `WarmCodexAccounts`)

- [ ] **Step 3: Run focused + httpd + agent tests**

```bash
cd backend && go test ./internal/daemon ./internal/httpd ./internal/service/agent ./internal/session_manager ./internal/adapters/agent/cursor ./internal/adapters/agent/agy ./internal/adapters/reviewer/cursor -count=1
```

- [ ] **Step 4: Commit**

```bash
git commit -m "$(cat <<'EOF'
fix: wire Cursor and Agy account managers into daemon startup

Bootstrap and readiness invalidation follow the Codex managers so
Settings ensure sees a warm cache without polling.
EOF
)"
```

---

### Task 11: Full verification

- [ ] **Step 1: Regenerated artifacts are clean**

```bash
npm run sqlc
npm run api
git diff --exit-code backend/internal/storage/sqlite/gen backend/internal/httpd/apispec/openapi.yaml frontend/src/api/schema.ts
```

If diff: commit `chore: regenerate sqlc and OpenAPI for Cursor/Agy accounts`.

- [ ] **Step 2: Backend tests named in AGENTS.md for this area**

```bash
cd backend && go test ./internal/httpd/... ./internal/service/agent ./internal/session_manager ./internal/storage/sqlite/store ./internal/adapters/agent/cursor ./internal/adapters/agent/agy ./internal/adapters/reviewer/cursor
```

- [ ] **Step 3: Frontend**

```bash
cd frontend && npm run typecheck
npx vitest run src/renderer/components/settings src/renderer/hooks/useCursorAccountsQuery.ts src/renderer/hooks/useAgyAccountsQuery.ts src/renderer/hooks/useCodexAccountsQuery.test.tsx
```

Codex account tests must still pass (no regression).

- [ ] **Step 4: Desktop visual check when implementing (not required for this planning PR)**

Use `.claude/skills/ao-desktop-dev` with `AO_DATA_DIR` isolated. Open Settings → Accounts. Confirm Codex, Cursor, and Agy groups render. Do not use `ao preview` as a substitute for the desktop Accounts modal if the skill is available.

---

## Spec coverage

| Spec requirement | Task |
| --- | --- |
| Cursor vault + file store + status JSON | 2, 3 |
| Cursor activate without stop/resume; no `~/.cursor` writes | 3, 4 |
| Legacy `{AO_DATA_DIR}/cursor` import | 3 |
| Reviewer stops reading `~/.cursor` | 4 |
| Cursor HTTP + LAN + CORS + OpenAPI | 5 |
| Agy opaque vault + 0137 switch journal | 6 |
| Agy global activate + AO-only worker stop/resume; no worker HOME; reviewers out of journal | 7 |
| Agy HTTP + LAN + CORS | 8 |
| Settings three `AgentProviderGroup`s | 9 |
| Secrets never in SQLite/API/logs | 1–8, 10 |
| No generic framework; no Claude/GitHub | file plan OUT OF SCOPE |

## Placeholder scan

No TBD/TODO implementation holes. Cursor login HOME is not used; Agy login may use a pending skeleton HOME for the PTY only, specified in Task 7.

## Type consistency

- Pointer type: `CursorActiveAccount` / `AgyActiveAccount` with `AccountID`, `Revision`.
- Activate (Cursor): `expectedAccountRevision`, `idempotencyKey`.
- Switch (Agy): `targetAccountId`, `expectedAccountRevision`, `idempotencyKey`.
- Routes live under `/api/v1/agents/{cursor|agy}/accounts` and Agy `/account-switches`.
