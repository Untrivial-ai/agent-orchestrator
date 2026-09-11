# GitHub Multi-Account Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a user keep multiple GitHub CLI accounts and bind one account per AO project without running `gh auth switch` as a product action.

**Architecture:** Discover accounts from `gh auth status --json hosts`. Fetch tokens with `gh auth token --hostname --user`. Store only `{hostname, login}` on `ProjectConfig`. Inject `GH_TOKEN` into session env and pass the same ref through SCM/tracker `context.Context`. Credentials stay in `gh`. Codex/Claude account PRs are out of scope.

**Tech Stack:** Go daemon (Cobra CLI, chi, sqlc/goose unchanged), React 19 settings UI, generated OpenAPI + `frontend/src/api/schema.ts`.

**Spec:** `docs/superpowers/specs/2026-09-11-github-multi-account-design.md`

---

## File plan

| Path | Action | Why |
|------|--------|-----|
| `backend/internal/domain/projectconfig.go` | modify | Add `GitHubAccount GitHubAccountRef` and validation |
| `backend/internal/domain/projectconfig_test.go` | modify | Validation cases |
| `backend/internal/domain/github_account.go` | create | Snapshot types, capability states, login operation |
| `backend/internal/adapters/scm/github/auth.go` | modify | Per-account `gh auth token --hostname --user`, context ref |
| `backend/internal/adapters/scm/github/auth_account_test.go` | create | Cache key, env override, missing user |
| `backend/internal/domain/github_account.go` | create | Ref, validation, `WithGitHubAccount` / `GitHubAccountFrom` context helpers |
| `backend/internal/adapters/scm/github/provider.go` | modify | Identity cache keyed by host+login |
| `backend/internal/service/githubacct/` | create | Catalog, login terminal, logout, ensure |
| `backend/internal/httpd/controllers/github_accounts.go` | create | HTTP routes |
| `backend/internal/httpd/controllers/github_accounts_dto.go` | create | Wire types |
| `backend/internal/httpd/controllers/github_accounts_test.go` | create | HTTP tests |
| `backend/internal/httpd/api.go` | modify | Wire controller |
| `backend/internal/httpd/lan_listener.go` | modify | Block `/api/v1/scm/github/accounts` |
| `backend/internal/httpd/lan_listener_test.go` | modify | LAN 404 cases |
| `backend/internal/httpd/cors.go` | modify | Origin boundary |
| `backend/internal/httpd/apispec/specgen/build.go` | modify | `schemaNames` + operations |
| `backend/internal/observe/scm/observer.go` | modify | Per-project GitHub identity |
| `backend/internal/observe/scm/scoped_identity_test.go` | modify | Two GitHub logins |
| `backend/internal/session_manager/manager.go` | modify | Inject GH_TOKEN |
| `backend/internal/session_manager/github_env_test.go` | create | Env injection tests |
| `backend/internal/daemon/scm_wiring.go` | modify | Keep FallbackTokenSource, now account-aware |
| `backend/internal/daemon/tracker_wiring.go` | modify | Same token helper as SCM |
| `backend/internal/cli/project.go` | modify | `--github-account` |
| `backend/internal/cli/github.go` | create | `ao github accounts` |
| `backend/internal/cli/github_test.go` | create | CLI tests |
| `frontend/src/renderer/hooks/useGitHubAccountsQuery.ts` | create | API hooks |
| `frontend/src/renderer/components/settings/GitHubAccountsSection.tsx` | create | Settings UI |
| `frontend/src/renderer/components/settings/settingsCatalog.tsx` | modify | Catalog entry |
| `frontend/src/renderer/components/ProjectSettingsForm.tsx` | modify | Account picker |
| `frontend/src/renderer/i18n/*.json` | modify | Copy |
| `docs/STATUS.md` | modify | Shipped note after implementation |

**Out of scope:** Codex/Claude account files, `gh auth switch` worker gates, GitLab UI, new SQLite migrations, LAN bind/auth changes, `~/.ao` path rules.

---

### Task 1: Domain ref + ProjectConfig validation

**Files:**
- Create: `backend/internal/domain/github_account.go`
- Modify: `backend/internal/domain/projectconfig.go`
- Test: `backend/internal/domain/projectconfig_test.go`

- [ ] **Step 1: Write the failing validation tests**

Append to `TestProjectConfigValidate`:

```go
{"github account empty ok", ProjectConfig{}, false},
{"github account login only defaults later", ProjectConfig{GitHubAccount: GitHubAccountRef{Login: "octocat"}}, false},
{"github account host without login", ProjectConfig{GitHubAccount: GitHubAccountRef{Hostname: "github.com"}}, true},
{"github account email refused", ProjectConfig{GitHubAccount: GitHubAccountRef{Login: "a@b.com"}}, true},
{"github account scheme refused", ProjectConfig{GitHubAccount: GitHubAccountRef{Hostname: "https://github.com", Login: "octocat"}}, true},
{"github account path refused", ProjectConfig{GitHubAccount: GitHubAccountRef{Hostname: "github.com/foo", Login: "octocat"}}, true},
{"github account token-shaped login refused", ProjectConfig{GitHubAccount: GitHubAccountRef{Login: "gho_notalogin"}}, true},
```

- [ ] **Step 2: Run the test and confirm it fails to compile**

Run: `cd backend && go test ./internal/domain -run TestProjectConfigValidate -count=1`

Expected: FAIL compile, `GitHubAccountRef` undefined.

- [ ] **Step 3: Add types and validation**

`backend/internal/domain/github_account.go`:

```go
package domain

import (
    "fmt"
    "net/url"
    "regexp"
    "strings"
)

const DefaultGitHubHostname = "github.com"

// GitHubAccountRef names one gh-stored account. It is not a credential.
type GitHubAccountRef struct {
    Hostname string `json:"hostname,omitempty"`
    Login    string `json:"login,omitempty"`
}

var gitHubLoginRE = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?$`)

func (r GitHubAccountRef) IsZero() bool {
    return strings.TrimSpace(r.Hostname) == "" && strings.TrimSpace(r.Login) == ""
}

func (r GitHubAccountRef) Normalize() GitHubAccountRef {
    r.Hostname = strings.ToLower(strings.TrimSpace(r.Hostname))
    r.Login = strings.TrimSpace(r.Login)
    if r.Login != "" && r.Hostname == "" {
        r.Hostname = DefaultGitHubHostname
    }
    return r
}

func (r GitHubAccountRef) Validate() error {
    r = r.Normalize()
    if r.IsZero() {
        return nil
    }
    if r.Login == "" {
        return fmt.Errorf("githubAccount.login is required when hostname is set")
    }
    if !gitHubLoginRE.MatchString(r.Login) {
        return fmt.Errorf("githubAccount.login is not a GitHub login")
    }
    if strings.ContainsAny(r.Hostname, "/:@ \\t") || strings.Contains(r.Hostname, "://") {
        return fmt.Errorf("githubAccount.hostname must be a host name")
    }
    if _, err := url.Parse("https://" + r.Hostname); err != nil || r.Hostname == "" {
        return fmt.Errorf("githubAccount.hostname must be a host name")
    }
    return nil
}
```

On `ProjectConfig` add:

```go
GitHubAccount GitHubAccountRef `json:"githubAccount,omitempty"`
```

In `Validate()`, after tracker intake:

```go
if err := c.GitHubAccount.Validate(); err != nil {
    return err
}
```

Call `c.GitHubAccount = c.GitHubAccount.Normalize()` inside `WithDefaults`.

- [ ] **Step 4: Re-run tests**

Run: `cd backend && go test ./internal/domain -run TestProjectConfigValidate -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/domain/github_account.go backend/internal/domain/projectconfig.go backend/internal/domain/projectconfig_test.go
git commit -m "$(cat <<'EOF'
feat(domain): add per-project GitHub account ref

EOF
)"
```

---

### Task 2: Context-aware GitHub token source

**Files:**
- Modify: `backend/internal/domain/github_account.go` (context helpers)
- Modify: `backend/internal/adapters/scm/github/auth.go`
- Test: `backend/internal/adapters/scm/github/auth_account_test.go`

- [ ] **Step 1: Write failing tests**

```go
func TestGHTokenSourceUsesUserAndHostname(t *testing.T) {
    var gotArgs []string
    src := &GHTokenSource{
        GHArgs: func(_ context.Context, hostname, user string) (string, error) {
            gotArgs = []string{hostname, user}
            return "tok-work\n", nil
        },
        TokenTTL: time.Hour,
    }
    ctx := domain.WithGitHubAccount(context.Background(), domain.GitHubAccountRef{Hostname: "github.com", Login: "work"})
    tok, err := src.Token(ctx)
    if err != nil || tok != "tok-work" {
        t.Fatalf("Token = %q, %v", tok, err)
    }
    if gotArgs[0] != "github.com" || gotArgs[1] != "work" {
        t.Fatalf("args = %#v", gotArgs)
    }
}

func TestGHTokenSourceCachesPerAccount(t *testing.T) {
    calls := 0
    src := &GHTokenSource{
        GHArgs: func(context.Context, string, string) (string, error) {
            calls++
            return "tok", nil
        },
        TokenTTL: time.Hour,
    }
    a := domain.WithGitHubAccount(context.Background(), domain.GitHubAccountRef{Login: "a"})
    b := domain.WithGitHubAccount(context.Background(), domain.GitHubAccountRef{Login: "b"})
    _, _ = src.Token(a)
    _, _ = src.Token(a)
    _, _ = src.Token(b)
    if calls != 2 {
        t.Fatalf("calls = %d, want 2", calls)
    }
}

func TestGHTokenSourceEnvOverrideIgnoresAccount(t *testing.T) {
    t.Setenv("AO_GITHUB_TOKEN", "")
    t.Setenv("GITHUB_TOKEN", "")
    src := FallbackTokenSource{
        EnvTokenSource{EnvVars: []string{"AO_GITHUB_TOKEN"}},
        &GHTokenSource{GHArgs: func(context.Context, string, string) (string, error) {
            t.Fatal("gh should not run when env token is set")
            return "", nil
        }},
    }
    t.Setenv("AO_GITHUB_TOKEN", "env-tok")
    tok, err := src.Token(domain.WithGitHubAccount(context.Background(), domain.GitHubAccountRef{Login: "work"}))
    if err != nil || tok != "env-tok" {
        t.Fatalf("Token = %q, %v", tok, err)
    }
}
```

Keep `TestGHTokenSourceUsesInjectedHook` passing by leaving the old `GH` func as the no-account path.

- [ ] **Step 2: Run tests, expect compile failure**

Run: `cd backend && go test ./internal/adapters/scm/github -run 'TestGHTokenSource' -count=1`

- [ ] **Step 3: Implement context + GHArgs**

Put context helpers on the domain ref so `observe/scm` never imports `adapters/scm/github`:

```go
// in backend/internal/domain/github_account.go
type githubAccountCtxKey struct{}

func WithGitHubAccount(ctx context.Context, ref GitHubAccountRef) context.Context {
    ref = ref.Normalize()
    if ref.IsZero() {
        return ctx
    }
    return context.WithValue(ctx, githubAccountCtxKey{}, ref)
}

func GitHubAccountFrom(ctx context.Context) (GitHubAccountRef, bool) {
    ref, ok := ctx.Value(githubAccountCtxKey{}).(GitHubAccountRef)
    return ref, ok && !ref.IsZero()
}
```

`GHTokenSource.Token` calls `domain.GitHubAccountFrom(ctx)`.

Extend `GHTokenSource` with:

```go
// GHArgs, when set, is the test hook for `gh auth token` with optional
// --hostname and --user. Production leaves this nil.
GHArgs func(ctx context.Context, hostname, user string) (string, error)
```

Cache `map[string]cachedToken` keyed by `hostname+"\x00"+user`.

`ghAuthToken` becomes:

```go
func ghAuthTokenFor(ctx context.Context, hostname, user string) (string, error) {
    args := []string{"auth", "token"}
    if hostname != "" && hostname != domain.DefaultGitHubHostname {
        args = append(args, "--hostname", hostname)
    }
    if user != "" {
        args = append(args, "--user", user)
    }
    out, err := aoprocess.CommandContext(ctx, "gh", args...).Output()
    if err != nil {
        return "", err
    }
    return string(out), nil
}
```

For `github.com` + user, still pass `--user` so the non-active account is used. Pass `--hostname github.com` only when needed; `gh` defaults the host. Prefer always passing `--user` when login is set.

- [ ] **Step 4: Run tests**

Run: `cd backend && go test ./internal/adapters/scm/github -run 'TestGHTokenSource' -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/domain/github_account.go backend/internal/adapters/scm/github/auth.go backend/internal/adapters/scm/github/auth_account_test.go
git commit -m "$(cat <<'EOF'
feat(scm): fetch GitHub tokens for a named gh account

EOF
)"
```

---

### Task 3: GitHub accounts catalog service

**Files:**
- Create: `backend/internal/service/githubacct/service.go`
- Create: `backend/internal/service/githubacct/status.go`
- Test: `backend/internal/service/githubacct/service_test.go`

- [ ] **Step 1: Write catalog tests against a fake `gh auth status --json`**

Fixture (no tokens):

```json
{
  "hosts": {
    "github.com": [
      {"user": "personal", "active": true, "state": "success"},
      {"user": "work", "active": false, "state": "success"}
    ]
  }
}
```

Field names must match real `gh auth status --json hosts` on the implementer's `gh`. If the live schema uses `login` instead of `user`, decode both with `json.RawMessage` fallbacks. Tests inject the bytes; they never call the real binary.

Assertions:

- Two snapshots, `personal` marked `ActiveInGh`.
- `ensure` is idempotent.
- Malformed JSON returns `GITHUB_ACCOUNT_STATUS_INVALID` without leaking the payload into the API error details.

- [ ] **Step 2: Run, expect package missing**

Run: `cd backend && go test ./internal/service/githubacct -count=1`

- [ ] **Step 3: Implement discovery**

Parse only display fields. If a token-like string appears in a decoded field, drop it (`strings.HasPrefix(v, "gho_")` etc) and keep login empty → `broken`.

Capabilities:

```go
type Capabilities struct {
    AccountRead       domain.CodexCapabilityObservation // reuse observation shape or a tiny SCM copy
    AccountManagement domain.CodexCapabilityObservation
}
```

Prefer a small `domain.GitHubAccountCapabilities` in `github_account.go` rather than reusing Codex types in the GitHub service.

When `os.Getenv("AO_GITHUB_TOKEN")` or `GITHUB_TOKEN` is set in the daemon, `AccountManagement.State = unsupported`, `ReasonCode = daemon_token_override`.

- [ ] **Step 4: Tests pass**

Run: `cd backend && go test ./internal/service/githubacct -count=1`

- [ ] **Step 5: Commit**

```bash
git commit -m "$(cat <<'EOF'
feat(github): discover gh CLI accounts without reading tokens

EOF
)"
```

---

### Task 4: Login terminal, restore-active, logout

**Files:**
- Modify: `backend/internal/service/githubacct/login.go`
- Test: `backend/internal/service/githubacct/login_test.go`

Reuse `shellterm` the way Codex login does (`backend/internal/service/agent/codex_account_login.go`). Command argv is exactly `gh auth login --hostname <host> --git-protocol ssh` (or https if the existing accounts on that host use https). Do not pass tokens on argv.

After verify:

1. Rediscover accounts.
2. If the previously active login is no longer active, run `gh auth switch --hostname --user <previous>` once. This is login-flow recovery only.
3. If restore fails, catalog `recovery_required` with a safe reason string.

Logout: `gh auth logout --hostname --user`. Before that, list projects; if any `GitHubAccount` matches, return `apierr.Conflict("GITHUB_ACCOUNT_IN_USE", ...)`.

Tests use fake shellterm + fake gh runner. Assert switch is invoked only when active login changed.

- [ ] **Commit**

```bash
git commit -m "$(cat <<'EOF'
feat(github): add account login without leaving gh switched

EOF
)"
```

---

### Task 5: HTTP API, LAN block, OpenAPI

**Files:**
- Create: `backend/internal/httpd/controllers/github_accounts.go`
- Create: `backend/internal/httpd/controllers/github_accounts_dto.go`
- Create: `backend/internal/httpd/controllers/github_accounts_test.go`
- Modify: `backend/internal/httpd/api.go`
- Modify: `backend/internal/httpd/lan_listener.go`
- Modify: `backend/internal/httpd/lan_listener_test.go`
- Modify: `backend/internal/httpd/cors.go`
- Modify: `backend/internal/httpd/apispec/specgen/build.go`

Routes (chi, under `/api/v1` via existing Register):

```go
r.Get("/scm/github/accounts", c.list)
r.Post("/scm/github/accounts/ensure", c.ensure)
r.Post("/scm/github/accounts/login-terminal", c.openLoginTerminal)
r.Post("/scm/github/accounts/{login}/login-terminal", c.openReauthTerminal)
r.Post("/scm/github/accounts/{login}/logout", c.logout)
r.Post("/scm/github/accounts/login-operations/{operationId}/verify", c.verify)
r.Post("/scm/github/accounts/login-operations/{operationId}/cancel", c.cancel)
```

Register streams: `GET /scm/github/accounts/events`.

LAN: append `"/api/v1/scm/github/accounts"` to `lanControlBlockedPrefixes`. Add the same paths to the existing LAN test table that covers Codex accounts.

CORS: extend `isCodexAccountPath` into `isLoopbackAccountPath` that also matches `/api/v1/scm/github/accounts`. Update `TestCodexAccountOriginBoundaryBlocksPreviewAndAllowsRenderers` with a GitHub path row.

Add `schemaNames` entries for every new controller type. Then:

```bash
npm run api
cd backend && go test ./internal/httpd/...
```

Commit `openapi.yaml` and `frontend/src/api/schema.ts` with the Go changes.

```bash
git commit -m "$(cat <<'EOF'
feat(api): add loopback GitHub account catalog routes

EOF
)"
```

---

### Task 6: SCM observer per-project identity

**Files:**
- Modify: `backend/internal/observe/scm/observer.go`
- Modify: `backend/internal/observe/scm/scoped_identity_test.go`
- Modify: `backend/internal/adapters/scm/github/provider.go`
- Modify: `backend/internal/daemon/scm_wiring.go`

- [ ] **Step 1: Test two GitHub projects with two logins**

Mirror `TestPoll_TwoGitLabHostsIdentityResolution`, but both repos are `github.com` with different `ProjectConfig.GitHubAccount.Login`. Fake store must expose config (extend `fakeStore.projects` or session lookup). Foreign authors must be filtered per project login.

- [ ] **Step 2: Implement — change the observer keys, not only the GitHub client cache**

`observe/scm` must stay provider-neutral: import `domain`, not `adapters/scm/github`.

Required observer edits in `observer.go` (today these all collapse GitHub to one identity):

1. `identityKey(provider, host)` for GitHub cannot stay `"github"`. Use `github|<hostname>|<login>` where login comes from the session project's `GitHubAccount` (or the unbound active login). GitLab remains `gitlab|<host>`.
2. `resolveIdentities` `seen` map must key GitHub by account, not by host. Two projects on `github.com` with logins `work` and `personal` are two identity lookups.
3. `discoverNewPRs` currently groups `byRepo` / `repos` with `prKey(repo, 0)` and filters authors with `identities[identityKey(repo.Provider, repo.Host)]`. Change listing to group by `(repoKey, accountKey)`. Call `ListPRsByRepo(domain.WithGitHubAccount(ctx, ref), repo, ...)` once per group. Filter `pr.Author` against **that group's** identity, not a single github.com identity.
4. Shared repo + two bound accounts ⇒ two list calls. Add a test; do not skip it.

Provider-side: GitHub `AuthenticatedIdentity` cache key is hostname+login. The observer sets `domain.WithGitHubAccount` on ctx; the adapter reads it.

```go
ref := project.Config.GitHubAccount.Normalize()
ctx = domain.WithGitHubAccount(ctx, ref)
id, err := scoped.AuthenticatedIdentityForProvider(ctx, "github", ref.Hostname)
```

- [ ] **Step 3: Tests**

Run: `cd backend && go test ./internal/observe/scm -count=1`

- [ ] **Commit**

```bash
git commit -m "$(cat <<'EOF'
fix(scm): attribute GitHub PRs using the project's bound account

EOF
)"
```

---

### Task 7: Session env injection

**Files:**
- Modify: `backend/internal/session_manager/manager.go`
- Test: `backend/internal/session_manager/github_env_test.go`

Inject after `runtimeEnv` builds the map, before browser capability issuance.

```go
func (m *Manager) applyGitHubAccountEnv(ctx context.Context, project domain.ProjectID, env map[string]string) error {
    rec, ok, err := m.store.GetProject(ctx, string(project))
    // bind from rec.Config.GitHubAccount
    tok, err := m.githubTokens.Token(domain.WithGitHubAccount(ctx, ref))
    env["GH_TOKEN"] = tok
    env["GITHUB_TOKEN"] = tok
    if ref.Hostname != "" && ref.Hostname != domain.DefaultGitHubHostname {
        env["GH_HOST"] = ref.Hostname
    }
    return nil
}
```

Protected: if `ProjectConfig.Env` already had `GH_TOKEN`, overwrite. Tests: `TestSpawnEnvProjectVarsCannotOverrideInternal` style.

Skip injection when ref is zero or daemon env override is set.

Wire a `TokenSource` on Manager in daemon wiring; tests inject a stub.

Run: `cd backend && go test ./internal/session_manager -run GitHub -count=1`

```bash
git commit -m "$(cat <<'EOF'
feat(session): inject bound GitHub tokens into worker env

EOF
)"
```

---

### Task 8: Tracker uses the same account ref

**Files:**
- Modify: `backend/internal/daemon/tracker_wiring.go`
- Modify: `backend/internal/adapters/tracker/github` token source if it cannot read context

Issue intake is per-project. The list/get calls already have a project id in the service. Thread `domain.WithGitHubAccount` from tracker intake into the GitHub tracker token fetch. Add a unit test that two projects do not share a cached token.

Do not spawn `gh auth token` at daemon boot (existing `TestWiring_NewMultiTracker_NeverTypedNilWhenNoGitHubToken` / lazy probe tests must stay green).

```bash
git commit -m "$(cat <<'EOF'
feat(tracker): use the project's GitHub account for issue intake

EOF
)"
```

---

### Task 9: CLI

**Files:**
- Create: `backend/internal/cli/github.go`
- Create: `backend/internal/cli/github_test.go`
- Modify: `backend/internal/cli/project.go`
- Modify: `backend/internal/cli/root.go` (register command)
- Modify: `docs/cli/README.md`

`ao github accounts` GET `/api/v1/scm/github/accounts`. `--refresh` POST ensure. Table columns: LOGIN, HOST, ACTIVE_IN_GH, STATE.

`ao project set-config --github-account work` and `--github-account work@github.example.com`. Mirror DTO in `projectConfig`:

```go
GitHubAccount *gitHubAccountRef `json:"githubAccount,omitempty"`
```

Tests: httptest daemon, missing args → exit 2 `usageError`, daemon error envelope preserved.

```bash
git commit -m "$(cat <<'EOF'
feat(cli): list GitHub accounts and set project binding

EOF
)"
```

---

### Task 10: Settings UI + project picker

**Files:**
- Create: `frontend/src/renderer/hooks/useGitHubAccountsQuery.ts`
- Create: `frontend/src/renderer/hooks/useGitHubAccountActions.ts`
- Create: `frontend/src/renderer/components/settings/GitHubAccountsSection.tsx`
- Create: `frontend/src/renderer/components/settings/GitHubAccountsSection.test.tsx`
- Modify: `frontend/src/renderer/components/settings/settingsCatalog.tsx`
- Modify: `frontend/src/renderer/stores/ui-store.ts` (`GlobalSettingsSection` union)
- Modify: `frontend/src/renderer/components/ProjectSettingsForm.tsx`
- Modify: `frontend/src/renderer/i18n/en.json` and every other locale file with the same keys

Clone Codex structure:

- `AgentProviderGroup` with `provider="github"` (or a GitHub mark if AgentAvatar has no github harness — use the existing Github icon from product-ui).
- Add account button, login terminal panel (reuse `CodexAccountLoginTerminalPanel` only if it is already generic; otherwise copy the thin terminal embed used by `HarnessSettingsSection` auth).
- Project form: `SettingsOptionMenu` of accounts; save `githubAccount: { hostname, login }` or omit.

Tests: picker writes the config payload; add-account calls login-terminal; override banner when capabilities.accountManagement.state === `"unsupported"`.

Run: `cd frontend && npm run typecheck` and the new vitest files.

```bash
git commit -m "$(cat <<'EOF'
feat(desktop): add GitHub account settings and project binding

EOF
)"
```

---

### Task 11: Docs + STATUS

**Files:**
- Modify: `docs/STATUS.md` (backend shipped bullet)
- Modify: `docs/cli/README.md` if not done in Task 9

One paragraph: GitHub accounts are discovered from the GitHub CLI, bound per project, and never switched device-globally.

```bash
git commit -m "$(cat <<'EOF'
docs: record GitHub multi-account support

EOF
)"
```

---

## Self-review

| Spec requirement | Task |
| --- | --- |
| Discover `gh` accounts, no token in API | 3, 5 |
| `gh auth token --user` | 2 |
| Never product-level `gh auth switch` | 4 (restore-only) |
| Per-project bind on ProjectConfig | 1, 9, 10 |
| Observer author filter per login | 6 |
| Session GH_TOKEN | 7 |
| Tracker | 8 |
| LAN / CORS / OpenAPI | 5 |
| Settings UI cloned from Codex chrome | 10 |
| No Codex/Claude duplication | file plan out of scope |
| No SQLite secrets / no new migration | Task 1 JSON field only |

Placeholders scanned: none remaining. Types use `GitHubAccountRef` / `hostname` / `login` consistently.

## Execution

Implement task-by-task in this worktree. After each backend task run the focused `go test` command in that task. After API changes run `npm run api`. Before the PR is marked ready:

```bash
cd backend && go test ./...
cd frontend && npm run typecheck
npm run lint
```
