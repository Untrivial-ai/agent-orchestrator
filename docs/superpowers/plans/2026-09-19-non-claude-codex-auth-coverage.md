# Non-Claude/Codex Agent Authentication Coverage Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make authentication detection complete and conservative for every registered agent except Claude Code and Codex.

**Architecture:** Preserve device-wide cached readiness and add an optional scoped checker for workspace/model/provider-aware launch decisions. Centralize only genuinely shared local-evidence mechanics in `authutil`; keep official precedence and credential schemas in each adapter. Local evidence yields `configured`, explicit no-auth providers yield `not_applicable`, and only authoritative validation yields `authorized` or `unauthorized`.

**Tech Stack:** Go, Cobra/native CLI probes, JSON/TOML/YAML/dotenv parsers, SQLite read-only queries, AWS SDK v2, Google OAuth2 ADC, generated OpenAPI and TypeScript schema.

**Spec:** `docs/superpowers/specs/2026-09-19-non-claude-codex-auth-coverage-design.md`

## Global Constraints

- Do not modify Claude Code or Codex adapters, tests, credential resolution, or account-management behavior.
- Do not read or write credentials outside documented locations.
- Never log, persist, or return raw credentials.
- Only a successful authoritative probe may return `authorized`.
- Local credential presence returns `configured`.
- Only authoritative rejection returns `unauthorized`.
- Explicitly selected no-auth providers return `not_applicable`.
- Inconclusive, missing, malformed, timed-out, or unsupported evidence returns `unknown`.
- No automated test may call a real provider, keychain, metadata service, cloud CLI, or the user’s home directory.
- Keep the loopback daemon and existing security boundaries unchanged.

---

### Task 1: Complete the five-state port and API contract

**Files:**
- Modify: `backend/internal/ports/agent.go`
- Modify: `backend/internal/domain/agent_readiness.go`
- Modify: `backend/internal/service/agent/readiness_coordinator.go`
- Modify: `backend/internal/service/agent/readiness.go`
- Modify: `backend/internal/service/agent/readiness_coordinator_test.go`
- Modify: `backend/internal/service/agent/catalog_test.go`
- Regenerate: `backend/internal/httpd/apispec/openapi.yaml`
- Regenerate: `frontend/src/api/schema.ts`

**Interfaces:**
- Produces: `ports.AgentAuthStatusNotApplicable`.
- Preserves: `AgentAuthStatusUnavailable` as installation-only compatibility behavior.
- Produces: legacy `Info.AuthStatus` with all five authentication states rather than collapsing `not_applicable` to `authorized`.

- [ ] **Step 1: Write failing contract tests**

Add coordinator and compatibility-projection cases with literal expectations:

```go
status := ports.AgentAuthStatusNotApplicable
// Authentication.State must be domain.AgentAuthenticationNotApplicable.
// EffectiveReadiness must be domain.AgentReadinessReady.
// readinessInfo(snapshot).AuthStatus must remain status, not authorized.
```

Also assert that `Info.AuthStatus` advertises `authorized,unauthorized,unknown,configured,not_applicable` in the generated schema.

- [ ] **Step 2: Verify RED**

Run: `cd backend && go test ./internal/service/agent/... ./internal/httpd/...`

Expected: failures because the port constant and legacy projection do not support `not_applicable`.

- [ ] **Step 3: Implement the contract**

Add:

```go
const AgentAuthStatusNotApplicable AgentAuthStatus = "not_applicable"
```

Map it directly in `checkAuthentication` and `readinessInfo`; update enum tags/descriptions. Do not change Claude or Codex implementations.

- [ ] **Step 4: Regenerate and verify GREEN**

Run: `npm run api`

Run: `cd backend && go test ./internal/service/agent/... ./internal/httpd/...`

Expected: PASS; both generated artifacts contain `not_applicable` for agent auth status.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/ports/agent.go backend/internal/domain/agent_readiness.go backend/internal/service/agent/readiness_coordinator.go backend/internal/service/agent/readiness.go backend/internal/service/agent/readiness_coordinator_test.go backend/internal/service/agent/catalog_test.go backend/internal/httpd/apispec/openapi.yaml frontend/src/api/schema.ts
git commit -m "feat: complete agent auth status contract"
```

### Task 2: Add scoped authentication without contaminating global readiness

**Files:**
- Modify: `backend/internal/ports/agent.go`
- Modify: `backend/internal/ports/session.go`
- Modify: `backend/internal/service/session/service.go`
- Modify: `backend/internal/service/session/service_test.go`
- Modify: `backend/internal/session_manager/agent_switching.go`
- Modify: `backend/internal/session_manager/agent_switching_test.go`
- Modify: `backend/internal/daemon/lifecycle_wiring.go`
- Modify: `backend/internal/daemon/wiring_test.go`

**Interfaces:**
- Produces: `ports.AgentAuthCheck{WorkingDir string, DataDir string, Config ports.AgentConfig, Env map[string]string, Args []string, Interactive bool}`.
- Produces: `ports.AgentScopedAuthChecker.AuthStatusFor(context.Context, ports.AgentAuthCheck)`.
- Preserves: `AgentAuthChecker.AuthStatus(context.Context)` and the agent-ID-only cache.

- [ ] **Step 1: Write failing scoped-check tests**

Create a fake implementing both interfaces and assert:

```go
type scopedFake struct { got ports.AgentAuthCheck }
func (f *scopedFake) AuthStatusFor(_ context.Context, in ports.AgentAuthCheck) (ports.AgentAuthStatus, error) {
	f.got = in
	return ports.AgentAuthStatusNotApplicable, nil
}
```

Cover fresh spawn and agent switch. The scoped fake must receive the resolved workspace, AO data directory, effective model/provider config, launch environment, final command arguments, and interactive-mode fact. Assert the device-wide cached snapshot is unchanged.

- [ ] **Step 2: Verify RED**

Run: `cd backend && go test ./internal/service/session ./internal/session_manager ./internal/daemon`

Expected: compile failure because scoped auth types do not exist.

- [ ] **Step 3: Implement optional scoped dispatch**

Add the two types to the port. At launch/switch, prefer `AgentScopedAuthChecker`; fall back to existing readiness only when scoped checking is unavailable. Treat only scoped `unauthorized` as a hard rejection; `configured`, `unknown`, and `not_applicable` allow the authoritative launch to proceed.

- [ ] **Step 4: Verify GREEN**

Run: `cd backend && go test ./internal/service/session ./internal/session_manager ./internal/daemon`

Expected: PASS, including isolation between two workspaces selecting different providers.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/ports/agent.go backend/internal/ports/session.go backend/internal/service/session/service.go backend/internal/service/session/service_test.go backend/internal/session_manager/agent_switching.go backend/internal/session_manager/agent_switching_test.go backend/internal/daemon/lifecycle_wiring.go backend/internal/daemon/wiring_test.go
git commit -m "feat: add workspace-scoped agent auth checks"
```

### Task 3: Build shared local-auth evidence utilities

**Files:**
- Create: `backend/internal/adapters/agent/authutil/evidence.go`
- Create: `backend/internal/adapters/agent/authutil/files.go`
- Create: `backend/internal/adapters/agent/authutil/dotenv.go`
- Create: `backend/internal/adapters/agent/authutil/command.go`
- Create: `backend/internal/adapters/agent/authutil/keychain.go`
- Create: `backend/internal/adapters/agent/authutil/evidence_test.go`
- Create: `backend/internal/adapters/agent/authutil/files_test.go`
- Create: `backend/internal/adapters/agent/authutil/dotenv_test.go`
- Create: `backend/internal/adapters/agent/authutil/command_test.go`
- Create: `backend/internal/adapters/agent/authutil/keychain_test.go`

**Interfaces:**
- Produces: `Evidence{Status ports.AgentAuthStatus, Source string}` and deterministic `FirstDefinitive` reduction.
- Produces: injected `Environment`, `FileSystem`, `Clock`, `GOOS`, and `CommandRunner` dependencies.
- Produces: bounded file/JSON helpers, upward search, dotenv decoding, expiry checks, and generic-password reads.

- [ ] **Step 1: Write failing utility tests**

Cover these literal behaviors:

```go
// configured outranks unknown; authoritative unauthorized outranks configured;
// a successful live result outranks every local result; not_applicable is used
// only when the selected provider is explicitly no-auth.
```

Fixtures must cover regular versus symlink/non-regular files, the size limit, `export KEY="quoted # value"`, inline comments, upward search order, Unix/RFC3339 expiry, command timeout, macOS-only keychain execution, fixed service/account arguments, and secret-free errors.

- [ ] **Step 2: Verify RED**

Run: `cd backend && go test ./internal/adapters/agent/authutil`

Expected: package does not exist.

- [ ] **Step 3: Implement minimal utilities**

Use concrete dependencies rather than package-global mutable runners:

```go
type Dependencies struct {
	Getenv func(string) string
	ReadFile func(string) ([]byte, error)
	Run func(context.Context, string, ...string) ([]byte, error)
	Now func() time.Time
	GOOS string
}
```

Never expose secret values in `Evidence.Source` or errors.

- [ ] **Step 4: Verify GREEN**

Run: `cd backend && go test ./internal/adapters/agent/authutil -race`

Expected: PASS with no process-global runner races.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/adapters/agent/authutil
git commit -m "feat: add shared local auth evidence utilities"
```

### Task 4: Add AWS, Google ADC, and Azure chain evidence

**Files:**
- Create: `backend/internal/adapters/agent/authutil/cloud.go`
- Create: `backend/internal/adapters/agent/authutil/cloud_test.go`
- Modify: `backend/go.mod` only if the existing AWS/OAuth dependencies do not expose the required loaders
- Modify: `backend/go.sum` only through `go mod tidy`

**Interfaces:**
- Produces: `AWSEvidence`, `GoogleADCEvidence`, and `AzureEvidence` returning `configured` or `unknown`, never `authorized`.

- [ ] **Step 1: Write failing cloud-chain tests**

Test AWS environment pairs, bearer token, named/default shared profiles, SSO profile/cache, web identity, and role/metadata loader injection. Test ADC service-account, authorized-user, external-account and well-known default paths, rejecting empty/malformed/incomplete JSON. Test Azure API key, service-principal/managed-identity configuration, and injected `az account get-access-token` output. Every test must use temporary paths and fake loaders.

- [ ] **Step 2: Verify RED**

Run: `cd backend && go test ./internal/adapters/agent/authutil -run 'AWS|GoogleADC|Azure'`

Expected: undefined helpers.

- [ ] **Step 3: Implement bounded evidence loaders**

Return `configured` after a chain yields usable local credentials or a token; return `unknown` on loader timeout/failure. Do not perform a provider entitlement call here.

- [ ] **Step 4: Verify GREEN**

Run: `cd backend && go test ./internal/adapters/agent/authutil -race`

Expected: PASS without network access.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/adapters/agent/authutil backend/go.mod backend/go.sum
git commit -m "feat: detect cloud credential chain evidence"
```

### Task 5: Make generic native probing conservative

**Files:**
- Modify: `backend/internal/adapters/agent/authprobe/authprobe.go`
- Modify: `backend/internal/adapters/agent/authprobe/authprobe_test.go`

**Interfaces:**
- Produces: adapter-specific structured parsers as the only positive `authorized` source.
- Preserves: bounded execution and conservative explicit-negative recognition.

- [ ] **Step 1: Write failing parser tests**

Assert that `"credentials found"`, `"logged in"`, and `{ "authenticated": true }` no longer become generically authorized, while explicit false/signed-out forms remain unauthorized and unfamiliar output remains unknown.

- [ ] **Step 2: Verify RED**

Run: `cd backend && go test ./internal/adapters/agent/authprobe`

Expected: positive generic phrases currently return authorized.

- [ ] **Step 3: Remove generic positive authorization**

Keep `CLIStatus` for execution/negative fallback. Add no provider-specific strings to this shared package.

- [ ] **Step 4: Verify GREEN and dependent packages**

Run: `cd backend && go test ./internal/adapters/agent/authprobe ./internal/adapters/agent/...`

Expected: authprobe passes; adapter failures identify tests that must be migrated in subsequent tasks.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/adapters/agent/authprobe
git commit -m "fix: require adapter-specific auth validation"
```

### Task 6: Correct Crush credential resolution

**Files:**
- Modify: `backend/internal/adapters/agent/crush/auth.go`
- Modify: `backend/internal/adapters/agent/crush/auth_test.go`
- Modify: `backend/internal/service/agentauth/plans.go`
- Modify: `backend/internal/service/agentauth/plans_test.go`

**Interfaces:**
- Produces: global/scoped Crush config resolver following official precedence.
- Consumes: `authutil` cloud and no-auth evidence.

- [ ] **Step 1: Write failing tests**

Add table cases for Hyper/Copilot/OpenAI OAuth under `providers.<id>.oauth`, stored `api_key`, top-level config `env`, global data/config/crushrc/project precedence, AWS profile/SSO, Google ADC, Azure Entra ID, local provider, malformed ADC, and complete environment isolation including `AZURE_OPENAI_API_KEY`. All local cases expect `configured`; selected local expects `not_applicable`.

- [ ] **Step 2: Verify RED**

Run: `cd backend && go test ./internal/adapters/agent/crush ./internal/service/agentauth -run 'Crush|crush'`

- [ ] **Step 3: Implement resolver and copy**

Implement `AuthStatusFor`; make `AuthStatus` delegate with an empty scope. Replace file-nonempty ADC logic with `authutil.GoogleADCEvidence`. Update guidance to mention Hyper, GitHub Copilot, and `crush login openai`.

- [ ] **Step 4: Verify GREEN**

Run: `cd backend && go test ./internal/adapters/agent/crush ./internal/service/agentauth`

- [ ] **Step 5: Commit**

```bash
git add backend/internal/adapters/agent/crush backend/internal/service/agentauth/plans.go backend/internal/service/agentauth/plans_test.go
git commit -m "fix: resolve Crush authentication sources"
```

### Task 7: Correct Qwen settings, provider, and ADC resolution

**Files:**
- Modify: `backend/internal/adapters/agent/qwen/auth.go`
- Modify: `backend/internal/adapters/agent/qwen/auth_test.go`

**Interfaces:**
- Produces: `AuthStatusFor` with official user/system/default/project precedence and active provider validation.

- [ ] **Step 1: Write failing tests**

Cover macOS/Linux/Windows system paths, both override variables, upward `.qwen/settings.json`, `.qwen/.env`, `.env`, `selectedType`, model/provider/base URL pairing, custom `envKey`, every missing documented variable from the audit, Vertex ADC plus project/model, CLI-scoped OpenAI inputs, unrelated recursive `apiKey`, dotenv quoting/export/comments, and discontinued OAuth cache. Expect configured/not-applicable/unknown exactly per the spec.

- [ ] **Step 2: Verify RED**

Run: `cd backend && go test ./internal/adapters/agent/qwen`

- [ ] **Step 3: Implement structured Qwen resolution**

Replace recursive key search with typed settings structures. Resolve only the selected auth type/provider and require provider-specific endpoint fields. Do not treat `oauth_creds.json` as readiness.

- [ ] **Step 4: Verify GREEN**

Run: `cd backend && go test ./internal/adapters/agent/qwen -race`

- [ ] **Step 5: Commit**

```bash
git add backend/internal/adapters/agent/qwen
git commit -m "fix: resolve Qwen authentication precedence"
```

### Task 8: Correct OpenCode and Kilo credential stores

**Files:**
- Modify: `backend/internal/adapters/agent/opencode/opencode.go`
- Modify: `backend/internal/adapters/agent/opencode/opencode_test.go`
- Modify: `backend/internal/adapters/agent/kilocode/auth.go`
- Modify: `backend/internal/adapters/agent/kilocode/auth_test.go`

**Interfaces:**
- Produces: provider-aware configured/no-auth results for both related OpenCode-derived stores.

- [ ] **Step 1: Write failing OpenCode tests**

Assert env, `auth.json`, database rows, and `auth list` are configured rather than authorized; Bedrock profiles/`~/.aws/credentials` are configured; selected free/local provider is not applicable; undocumented database paths do not override official sources.

- [ ] **Step 2: Write failing Kilo tests**

Cover OAuth `access`/`refresh`/`expires`, expired entries, `credential` table, active-account join, `KILO_DB`, injected `kilo db path`, `KILO_AUTH_CONTENT`, selected `options.apiKey`, omitted zero-environment output, unrelated provider credentials, and free models.

- [ ] **Step 3: Verify RED**

Run: `cd backend && go test ./internal/adapters/agent/opencode ./internal/adapters/agent/kilocode`

- [ ] **Step 4: Implement and verify GREEN**

Use read-only SQLite queries that check payload and expiry. Do not rely on `KILO_DATA_DIR`. Run: `cd backend && go test ./internal/adapters/agent/opencode ./internal/adapters/agent/kilocode -race`

- [ ] **Step 5: Commit**

```bash
git add backend/internal/adapters/agent/opencode backend/internal/adapters/agent/kilocode
git commit -m "fix: validate OpenCode and Kilo credential stores"
```

### Task 9: Correct Grok and Kimi configuration schemas

**Files:**
- Modify: `backend/internal/adapters/agent/grok/auth.go`
- Modify: `backend/internal/adapters/agent/grok/auth_test.go`
- Modify: `backend/internal/adapters/agent/kimi/auth.go`
- Modify: `backend/internal/adapters/agent/kimi/auth_test.go`

- [ ] **Step 1: Write failing Grok tests**

Cover `GROK_AUTH_PATH`, valid/malformed inline `GROK_AUTH`, stored `key`, deployment env/config keys, per-model `api_key`/`env_key`, and absence of stale `access_token`/`GROK_API_KEY` proof.

- [ ] **Step 2: Write failing Kimi tests**

Cover `api_key_env`, `KIMI_MODEL_NAME` plus `KIMI_MODEL_API_KEY`, Vertex ADC, exact custom Authorization header, current-versus-legacy mode, and rejection of undocumented current variables. Change all unvalidated local expectations to configured.

- [ ] **Step 3: Verify RED**

Run: `cd backend && go test ./internal/adapters/agent/grok ./internal/adapters/agent/kimi`

- [ ] **Step 4: Implement and verify GREEN**

Decode typed TOML/JSON provider structures and select only the effective model/provider. Run: `cd backend && go test ./internal/adapters/agent/grok ./internal/adapters/agent/kimi -race`

- [ ] **Step 5: Commit**

```bash
git add backend/internal/adapters/agent/grok backend/internal/adapters/agent/kimi
git commit -m "fix: parse Grok and Kimi auth configuration"
```

### Task 10: Add Copilot and Muse keychain detection

**Files:**
- Modify: `backend/internal/adapters/agent/copilot/auth.go`
- Modify: `backend/internal/adapters/agent/copilot/auth_test.go`
- Modify: `backend/internal/adapters/agent/muse/auth.go`
- Modify: `backend/internal/adapters/agent/muse/auth_test.go`

- [ ] **Step 1: Write failing Copilot tests**

Test exact precedence across three env variables, macOS `copilot-cli` keychain, `gh auth token`, and config fallback. Assert session event logs never count. Keychain lock/error must fall through without becoming unauthorized.

- [ ] **Step 2: Write failing Muse tests**

Test `storage:"keychain"` metadata, successful/locked/missing Muse keychain entries, file OAuth/API key, `META_API_KEY`, and absence of settings-file proof.

- [ ] **Step 3: Verify RED**

Run: `cd backend && go test ./internal/adapters/agent/copilot ./internal/adapters/agent/muse`

- [ ] **Step 4: Implement and verify GREEN**

Use fixed service/account selectors through injected `authutil` keychain reads; return configured for successful reads. Run: `cd backend && go test ./internal/adapters/agent/copilot ./internal/adapters/agent/muse -race`

- [ ] **Step 5: Commit**

```bash
git add backend/internal/adapters/agent/copilot backend/internal/adapters/agent/muse
git commit -m "feat: detect Copilot and Muse keychain credentials"
```

### Task 11: Parse Cursor and Kiro structured native status

**Files:**
- Modify: `backend/internal/adapters/agent/cursor/auth.go`
- Modify: `backend/internal/adapters/agent/cursor/auth_test.go`
- Modify: `backend/internal/adapters/agent/kiro/auth.go`
- Create: `backend/internal/adapters/agent/kiro/auth_test.go`

- [ ] **Step 1: Write failing Cursor tests**

Assert the command is `agent status --format json`; valid signed-in JSON, signed-out JSON, malformed JSON, timeout, and `CURSOR_API_KEY` configured behavior. Assert `cli-config.json/authInfo` is ignored.

- [ ] **Step 2: Write failing Kiro tests**

Assert browser-session probe runs before API-key fallback, JSON is parsed structurally, signed-out is unauthorized, malformed/unknown version is unknown, and `KIRO_API_KEY` is configured only for non-interactive scope.

- [ ] **Step 3: Verify RED**

Run: `cd backend && go test ./internal/adapters/agent/cursor ./internal/adapters/agent/kiro`

- [ ] **Step 4: Implement and verify GREEN**

Use adapter-owned response structs; do not pass JSON to generic substring matching. Run: `cd backend && go test ./internal/adapters/agent/cursor ./internal/adapters/agent/kiro -race`

- [ ] **Step 5: Commit**

```bash
git add backend/internal/adapters/agent/cursor backend/internal/adapters/agent/kiro
git commit -m "fix: parse Cursor and Kiro auth status"
```

### Task 12: Make Amp’s live usage probe authoritative

**Files:**
- Modify: `backend/internal/adapters/agent/amp/auth.go`
- Modify: `backend/internal/adapters/agent/amp/auth_test.go`

- [ ] **Step 1: Write failing tests**

Cover probe-first ordering, live success, explicit rejection, timeout, `sgamp_` token shape, short-lived/invalid token rejection, exact `secrets.json` fields, unrelated token-like keys, empty/unrecognized secrets, JSONC settings behavior, and removal of undocumented settings auth keys.

- [ ] **Step 2: Verify RED**

Run: `cd backend && go test ./internal/adapters/agent/amp`

- [ ] **Step 3: Implement minimal behavior**

Always attempt `amp usage --no-color` first. Return authorized/unauthorized only from its documented result; otherwise use a configured local fallback.

- [ ] **Step 4: Verify GREEN**

Run: `cd backend && go test ./internal/adapters/agent/amp -race`

- [ ] **Step 5: Commit**

```bash
git add backend/internal/adapters/agent/amp
git commit -m "fix: prioritize Amp live auth validation"
```

### Task 13: Validate Auggie sessions and scoped session input

**Files:**
- Modify: `backend/internal/adapters/agent/auggie/auth.go`
- Modify: `backend/internal/adapters/agent/auggie/auth_test.go`

- [ ] **Step 1: Write failing tests**

Cover valid/malformed `AUGMENT_SESSION_AUTH`, required `accessToken`, `tenantURL`, scopes, expiry, refreshable versus non-refreshable expiry, service-account session, scoped inline/path `--augment-session-json`, and missing evidence.

- [ ] **Step 2: Verify RED**

Run: `cd backend && go test ./internal/adapters/agent/auggie`

- [ ] **Step 3: Implement typed session parsing**

Remove recursive token search. Return configured only for a structurally usable, non-expired or refreshable session.

- [ ] **Step 4: Verify GREEN**

Run: `cd backend && go test ./internal/adapters/agent/auggie -race`

- [ ] **Step 5: Commit**

```bash
git add backend/internal/adapters/agent/auggie
git commit -m "fix: validate Auggie session credentials"
```

### Task 14: Resolve Droid and Cline scoped provider settings

**Files:**
- Modify: `backend/internal/adapters/agent/droid/auth.go`
- Modify: `backend/internal/adapters/agent/droid/auth_test.go`
- Modify: `backend/internal/adapters/agent/cline/auth.go`
- Modify: `backend/internal/adapters/agent/cline/auth_test.go`

- [ ] **Step 1: Write failing Droid tests**

Cover browser-login state, global/project settings precedence, legacy custom models, scoped settings path, static key, helper-based evidence without executing arbitrary helpers, AWS chain, active model selection, invalid provider/base URL, and trusted keyless endpoints.

- [ ] **Step 2: Write failing Cline tests**

Cover all three directory/path overrides, scoped config/data/key inputs, nested `settings.auth.apiKey`, AWS/ADC/Azure/SAP evidence, local providers, selected entry, and `cline-pass` lookup.

- [ ] **Step 3: Verify RED**

Run: `cd backend && go test ./internal/adapters/agent/droid ./internal/adapters/agent/cline`

- [ ] **Step 4: Implement and verify GREEN**

Implement typed active-provider resolution and use cloud helpers. Treat `apiKeyHelper`/trusted endpoint configuration as configured or not applicable without executing arbitrary shell. Run: `cd backend && go test ./internal/adapters/agent/droid ./internal/adapters/agent/cline -race`

- [ ] **Step 5: Commit**

```bash
git add backend/internal/adapters/agent/droid backend/internal/adapters/agent/cline
git commit -m "fix: resolve Droid and Cline provider auth"
```

### Task 15: Replace Agy and Continue installed-only checks

**Files:**
- Modify: `backend/internal/adapters/agent/agy/auth.go`
- Modify: `backend/internal/adapters/agent/agy/auth_test.go`
- Modify: `backend/internal/adapters/agent/continueagent/auth.go`
- Modify: `backend/internal/adapters/agent/continueagent/auth_test.go`

- [ ] **Step 1: Write failing Agy tests**

Test keyring success/failure, Gemini mode plus `GEMINI_API_KEY`, missing half of that pair, and explicit rejection of Google key/ADC/dotenv evidence.

- [ ] **Step 2: Write failing Continue tests**

Test browser-login state, `CONTINUE_API_KEY`, local Anthropic key, selected config YAML provider key/env reference, malformed config, and installed-without-evidence unknown.

- [ ] **Step 3: Verify RED**

Run: `cd backend && go test ./internal/adapters/agent/agy ./internal/adapters/agent/continueagent`

- [ ] **Step 4: Implement and verify GREEN**

Return configured for every local success; neither adapter has a documented provider-validation command. Run: `cd backend && go test ./internal/adapters/agent/agy ./internal/adapters/agent/continueagent -race`

- [ ] **Step 5: Commit**

```bash
git add backend/internal/adapters/agent/agy backend/internal/adapters/agent/continueagent
git commit -m "fix: detect Agy and Continue credentials"
```

### Task 16: Resolve Goose secrets and provider metadata

**Files:**
- Modify: `backend/internal/adapters/agent/goose/auth.go`
- Modify: `backend/internal/adapters/agent/goose/auth_test.go`

- [ ] **Step 1: Write failing tests**

Cover keyring, `GOOSE_PATH_ROOT`, Windows AppData, `secrets.yaml`, supported OAuth caches, AWS/ADC/Azure evidence, custom `api_key_env`, command credential configuration, incomplete hard-coded env coverage, active provider selection, Ollama/LM Studio, and rejection of `config.yaml` secrets.

- [ ] **Step 2: Verify RED**

Run: `cd backend && go test ./internal/adapters/agent/goose`

- [ ] **Step 3: Implement metadata-driven resolution**

Read the active provider and its declared auth mechanism; do not treat any unrelated provider credential as readiness. Command credentials are configured evidence without executing arbitrary commands.

- [ ] **Step 4: Verify GREEN**

Run: `cd backend && go test ./internal/adapters/agent/goose -race`

- [ ] **Step 5: Commit**

```bash
git add backend/internal/adapters/agent/goose
git commit -m "fix: resolve Goose provider credentials"
```

### Task 17: Keep Devin status authoritative and add portable fallbacks

**Files:**
- Modify: `backend/internal/adapters/agent/devin/auth.go`
- Modify: `backend/internal/adapters/agent/devin/auth_test.go`

- [ ] **Step 1: Write failing tests**

Cover live `devin auth status` success/rejection/unknown, XDG/default Unix/Windows credential paths, malformed TOML, `devin_api_url` without credentials, conservative `DEVIN_API_KEY`, and ACP-only `WINDSURF_API_KEY`.

- [ ] **Step 2: Verify RED**

Run: `cd backend && go test ./internal/adapters/agent/devin`

- [ ] **Step 3: Implement precedence**

Run official status first. Use file/env evidence only as configured fallback and never infer credential field names beyond the documented/observed typed schema.

- [ ] **Step 4: Verify GREEN**

Run: `cd backend && go test ./internal/adapters/agent/devin -race`

- [ ] **Step 5: Commit**

```bash
git add backend/internal/adapters/agent/devin
git commit -m "fix: add Devin credential fallbacks"
```

### Task 18: Resolve active providers for Vibe and Autohand

**Files:**
- Modify: `backend/internal/adapters/agent/vibe/auth.go`
- Create: `backend/internal/adapters/agent/vibe/auth_test.go`
- Modify: `backend/internal/adapters/agent/autohand/auth.go`
- Modify: `backend/internal/adapters/agent/autohand/auth_test.go`

- [ ] **Step 1: Write failing Vibe tests**

Cover global/project TOML precedence, active model/provider, provider-specific env, compatible dotenv syntax, unrelated provider keys, default `MISTRAL_API_KEY`, rejected `VIBE_CODE_API_KEY`, and local llamacpp.

- [ ] **Step 2: Write failing Autohand tests**

Cover four config formats, `AUTOHAND_HOME`, scoped config, provider/extension keys, documented env list, bare helper, AWS/ADC/Azure, local providers, account expiry, and separation between account and model credentials.

- [ ] **Step 3: Verify RED**

Run: `cd backend && go test ./internal/adapters/agent/vibe ./internal/adapters/agent/autohand`

- [ ] **Step 4: Implement and verify GREEN**

Use real TOML/YAML decoders and `authutil` dotenv/cloud helpers. Do not execute arbitrary API-key helpers. Run: `cd backend && go test ./internal/adapters/agent/vibe ./internal/adapters/agent/autohand -race`

- [ ] **Step 5: Commit**

```bash
git add backend/internal/adapters/agent/vibe backend/internal/adapters/agent/autohand
git commit -m "fix: resolve Vibe and Autohand active providers"
```

### Task 19: Add Kimchi subscription and project configuration evidence

**Files:**
- Modify: `backend/internal/adapters/agent/kimchi/auth.go`
- Modify: `backend/internal/adapters/agent/kimchi/auth_test.go`

- [ ] **Step 1: Write failing tests**

Cover global/project precedence, API/service key, subscription upstream OAuth, endpoint/key pairing, malformed endpoint, unrelated provider, revoked response through an injected official probe, and absence unknown.

- [ ] **Step 2: Verify RED**

Run: `cd backend && go test ./internal/adapters/agent/kimchi`

- [ ] **Step 3: Implement selected endpoint/provider resolution**

Return configured for local key/subscription evidence. Return authorized/unauthorized only when the injected official validation path succeeds/rejects.

- [ ] **Step 4: Verify GREEN**

Run: `cd backend && go test ./internal/adapters/agent/kimchi -race`

- [ ] **Step 5: Commit**

```bash
git add backend/internal/adapters/agent/kimchi
git commit -m "fix: resolve Kimchi subscription authentication"
```

### Task 20: Add Pi OAuth, cloud chains, and provider checks

**Files:**
- Modify: `backend/internal/adapters/agent/pi/auth.go`
- Modify: `backend/internal/adapters/agent/pi/auth_test.go`

- [ ] **Step 1: Write failing tests**

Cover OAuth `access`/`refresh`/`expires`, provider-scoped env entries, every missing variable listed in the audit, AWS chain, Vertex ADC, custom `models.json`, local providers, selected-provider matching, `GEMINI_API_KEY` acceptance and `GOOGLE_API_KEY` rejection.

- [ ] **Step 2: Write provider-command tests**

Assert exact `pi auth check --provider <id>` invocation and authorized/unauthorized/unknown parsing with an injected runner.

- [ ] **Step 3: Verify RED**

Run: `cd backend && go test ./internal/adapters/agent/pi`

- [ ] **Step 4: Implement and verify GREEN**

Prefer the provider-specific native check when scope resolves a provider; fall back to configured local evidence. Run: `cd backend && go test ./internal/adapters/agent/pi -race`

- [ ] **Step 5: Commit**

```bash
git add backend/internal/adapters/agent/pi
git commit -m "fix: resolve Pi provider authentication"
```

### Task 21: Correct Prime Agent runtime and credential resolution

**Files:**
- Modify: `backend/internal/adapters/agent/primeagent/auth.go`
- Modify: `backend/internal/adapters/agent/primeagent/auth_test.go`

- [ ] **Step 1: Write failing tests**

Cover AO runtime config before `~/.prime/agent`, Copilot variables, `MOONSHOT_API_KEY`, Bedrock skip-auth, web identity without role ARN, scoped API key, expired OAuth, unresolved `!command`/env references, unrelated recursive keys, and absence of unsupported PI/Google aliases.

- [ ] **Step 2: Verify RED**

Run: `cd backend && go test ./internal/adapters/agent/primeagent`

- [ ] **Step 3: Implement typed active-provider resolution**

Pass AO data/runtime location through scoped auth input. Replace recursive JSON scans with provider-specific entries and expiry/reference checks.

- [ ] **Step 4: Verify GREEN**

Run: `cd backend && go test ./internal/adapters/agent/primeagent -race`

- [ ] **Step 5: Commit**

```bash
git add backend/internal/adapters/agent/primeagent
git commit -m "fix: align Prime Agent auth with runtime config"
```

### Task 22: Correct OMP credential precedence and no-auth providers

**Files:**
- Modify: `backend/internal/adapters/agent/omp/auth.go`
- Modify: `backend/internal/adapters/agent/omp/auth_test.go`

- [ ] **Step 1: Write failing database tests**

Cover enabled/disabled rows, supported type/payload, expiry, selected-provider match, `disabledProviders`, malformed rows, and unrelated-provider rows.

- [ ] **Step 2: Write failing non-database tests**

Cover provider env/dotenv, `models.yml` API key, scoped API key, broker/cache evidence, undocumented `auth.json` fallback, and `auth:none` local provider.

- [ ] **Step 3: Verify RED**

Run: `cd backend && go test ./internal/adapters/agent/omp`

- [ ] **Step 4: Implement and verify GREEN**

Use read-only SQLite and typed YAML. Local rows/files return configured; selected no-auth returns not applicable. Run: `cd backend && go test ./internal/adapters/agent/omp -race`

- [ ] **Step 5: Commit**

```bash
git add backend/internal/adapters/agent/omp
git commit -m "fix: validate OMP provider credentials"
```

### Task 23: Make Aider workspace and provider aware

**Files:**
- Modify: `backend/internal/adapters/agent/aider/auth.go`
- Modify: `backend/internal/adapters/agent/aider/auth_test.go`

- [ ] **Step 1: Write failing path/precedence tests**

Cover workspace/git-root dotenv and config, `AIDER_ENV_FILE`, scoped env/config paths, and proof that daemon CWD is ignored.

- [ ] **Step 2: Write failing provider tests**

Cover documented LiteLLM variables from the audit, Bedrock AWS chain, Vertex project/location/ADC, Azure, Copilot token, LM Studio, optional Ollama key, keyless Ollama, provider/key pairing, and malformed YAML list entries containing `=`.

- [ ] **Step 3: Verify RED**

Run: `cd backend && go test ./internal/adapters/agent/aider`

- [ ] **Step 4: Implement and verify GREEN**

Resolve typed YAML and dotenv according to Aider precedence and only for the selected model provider. Run: `cd backend && go test ./internal/adapters/agent/aider -race`

- [ ] **Step 5: Commit**

```bash
git add backend/internal/adapters/agent/aider
git commit -m "fix: resolve Aider workspace authentication"
```

### Task 24: Enforce adapter-wide status and environment isolation

**Files:**
- Modify: `backend/internal/adapters/agent/agy/auth_test.go`
- Modify: `backend/internal/adapters/agent/aider/auth_test.go`
- Modify: `backend/internal/adapters/agent/amp/auth_test.go`
- Modify: `backend/internal/adapters/agent/auggie/auth_test.go`
- Modify: `backend/internal/adapters/agent/autohand/auth_test.go`
- Modify: `backend/internal/adapters/agent/cline/auth_test.go`
- Modify: `backend/internal/adapters/agent/continueagent/auth_test.go`
- Modify: `backend/internal/adapters/agent/copilot/auth_test.go`
- Modify: `backend/internal/adapters/agent/crush/auth_test.go`
- Modify: `backend/internal/adapters/agent/cursor/auth_test.go`
- Modify: `backend/internal/adapters/agent/devin/auth_test.go`
- Modify: `backend/internal/adapters/agent/droid/auth_test.go`
- Modify: `backend/internal/adapters/agent/goose/auth_test.go`
- Modify: `backend/internal/adapters/agent/grok/auth_test.go`
- Modify: `backend/internal/adapters/agent/kilocode/auth_test.go`
- Modify: `backend/internal/adapters/agent/kimchi/auth_test.go`
- Modify: `backend/internal/adapters/agent/kimi/auth_test.go`
- Modify: `backend/internal/adapters/agent/kiro/auth_test.go`
- Modify: `backend/internal/adapters/agent/muse/auth_test.go`
- Modify: `backend/internal/adapters/agent/omp/auth_test.go`
- Modify: `backend/internal/adapters/agent/opencode/opencode_test.go`
- Modify: `backend/internal/adapters/agent/pi/auth_test.go`
- Modify: `backend/internal/adapters/agent/primeagent/auth_test.go`
- Modify: `backend/internal/adapters/agent/qwen/auth_test.go`
- Modify: `backend/internal/adapters/agent/vibe/auth_test.go`
- Modify: `backend/internal/adapters/agent/registry/registry_test.go`
- Do not modify: `backend/internal/adapters/agent/claudecode/*`
- Do not modify: `backend/internal/adapters/agent/codex/*`

- [ ] **Step 1: Add a registry-wide status conformance test**

For every included adapter, assert that injected local evidence cannot produce authorized and a no-evidence isolated home cannot accidentally inherit the developer environment. Use explicit env-name inventories owned by each adapter rather than four-variable cleanup helpers.

- [ ] **Step 2: Run the test against the complete inherited environment**

Run: `cd backend && go test ./internal/adapters/agent/... -count=1`

Expected before cleanup: at least the known inherited-variable regression or a local-evidence authorized expectation fails.

- [ ] **Step 3: Complete isolation helpers**

Use `t.Setenv` for every supported variable, set HOME/USERPROFILE/XDG roots to temporary directories, and inject OS commands. Do not mutate package-global runners in parallel tests.

- [ ] **Step 4: Verify GREEN under repetition and race detection**

Run: `cd backend && go test ./internal/adapters/agent/... -count=5`

Run: `cd backend && go test -race ./internal/adapters/agent/...`

Expected: PASS without depending on machine credentials.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/adapters/agent
git commit -m "test: isolate agent authentication checks"
```

### Task 25: Regenerate contracts, update backend documentation, and run full verification

**Files:**
- Modify: `docs/STATUS.md` if it currently describes auth coverage/status semantics
- Regenerate: `backend/internal/httpd/apispec/openapi.yaml`
- Regenerate: `frontend/src/api/schema.ts`

- [ ] **Step 1: Update repository auth documentation**

Update an existing repository auth-coverage section if one exists; otherwise add a focused section to `docs/STATUS.md`. For every included adapter, list only implemented sources, identify authoritative versus configured evidence, and document no-auth behavior. Treat `/Users/nikhilachale/Desktop/agent-binary-auth-checks.html` as audit input only; it is outside the repository and must not be added to the PR. Mark Claude Code and Codex as unchanged by this PR.

- [ ] **Step 2: Regenerate the API contract**

Run: `npm run api`

Expected: OpenAPI and TypeScript expose all five authentication states without unrelated drift.

- [ ] **Step 3: Run focused and backend-wide verification**

Run: `cd backend && go test ./internal/adapters/agent/... ./internal/service/agent/... ./internal/session_manager/... ./internal/httpd/...`

Run: `cd backend && go build ./... && go test ./... && go test -race ./... && go vet ./...`

- [ ] **Step 4: Run repository CI-equivalent checks**

Run: `npm run lint`

Run: `npm run frontend:typecheck`

Run: `cd frontend && npm run build`

Run when Docker is available: `npx @redwoodjs/agent-ci run --all`

Record any unavailable Docker/native-runner gap exactly; do not claim it passed locally.

- [ ] **Step 5: Review the diff for scope and secrets**

Run: `git diff --check`

Run: `git diff --stat`

Run: `git diff -- backend/internal/adapters/agent/claudecode backend/internal/adapters/agent/codex`

Expected: no Claude/Codex diff, no credentials, no generated drift, and no frontend recovery implementation.

- [ ] **Step 6: Commit**

```bash
git add docs/STATUS.md backend/internal/httpd/apispec/openapi.yaml frontend/src/api/schema.ts
git commit -m "docs: document agent authentication coverage"
```

## PR Handoff Checklist

- [ ] Confirm every included adapter has tests for configured, unknown, malformed input, and test isolation.
- [ ] Confirm adapters with authoritative probes have authorized and unauthorized tests.
- [ ] Confirm adapters with local providers have not-applicable tests.
- [ ] Confirm project-aware adapters have two-workspace isolation tests.
- [ ] Confirm Claude Code and Codex have no behavioral diff.
- [ ] Confirm `openapi.yaml` and `schema.ts` were regenerated together.
- [ ] Follow `.agents/skills/pr-description/SKILL.md` before publishing the PR description.
- [ ] Verify remote CI after pushing; do not publish or deploy as validation.
