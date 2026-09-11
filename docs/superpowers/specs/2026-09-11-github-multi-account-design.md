# GitHub Multi-Account Design

Date: 2026-09-11
Status: proposed
Branch: `feat/github-multi-account`

## Problem Statement

Agent Orchestrator is a GitHub-centric loop: add project, spawn session, observe PR, merge. GitHub identity is still process-global. The daemon, SCM observer, issue tracker, and `ao doctor` all resolve one token:

1. `AO_GITHUB_TOKEN`
2. `GITHUB_TOKEN` (daemon environment)
3. `gh auth token` (the currently active `gh` account)

A user with a work account and a personal account cannot bind different identities to different projects. The observer's author filter then treats the other account's PRs as foreign. Workers inherit whatever `gh` considers active, and `gh auth switch` is a machine-global mutation.

## What already exists (do not duplicate)

GitHub was checked on 2026-09-11:

| Surface | State | Tracking |
| --- | --- | --- |
| Codex coding-agent accounts | Shipped on `main` | PR [#4722](https://github.com/Untrivial-ai/agent-orchestrator/pull/4722) |
| Claude Code accounts + hot switch | Open PR | [#4813](https://github.com/Untrivial-ai/agent-orchestrator/pull/4813) |
| Cloud Coder accounts | Open PR | [#4787](https://github.com/Untrivial-ai/agent-orchestrator/pull/4787) |
| Gate worker `gh auth switch` | Closed, not planned | [#3637](https://github.com/Untrivial-ai/agent-orchestrator/issues/3637) |

This design is **GitHub/SCM identity**, not Codex/Claude credential vaults and not worker command gating. #3637 stays closed: AO will not intercept `gh auth switch`. It will make the switch unnecessary for the product loop.

`domain.ProjectConfig` already records the intended follow-up:

> Settings whose consumers do not yet exist (tracker/SCM per-project config) are intentionally absent and land in focused follow-up PRs alongside the code that reads them.

This is that PR series.

## Approaches considered

### A. Device-global `gh auth switch` (Codex-shaped)

AO would treat one GitHub login as the machine-wide active account and call `gh auth switch` when the user picks another.

Rejected. Switching is global and persists after the session. #3637 documents a worker doing exactly this and leaving the human's `gh` on the wrong account. It also cannot express "project A is work, project B is personal" at the same time.

### B. Isolated `GH_CONFIG_DIR` vault (copy Codex credential homes)

AO would copy `gh` config/credentials into `~/.ao/harnesses/github/accounts/<id>/` and point sessions at a private config dir.

Rejected for v1. It duplicates GitHub CLI's credential store, creates a secret-handling surface AO does not need, and fights SSH git remotes plus the user's existing `gh` setup. Codex needed a vault because Codex owns `auth.json`. GitHub CLI already owns multi-account state.

### C. Recommended: leave credentials in `gh`, bind per project, never switch

`gh` already stores multiple accounts per host. AO discovers them with `gh auth status --json hosts` (never `--show-token`). It fetches a specific account with `gh auth token --hostname <host> --user <login>`. A project optionally binds `{hostname, login}`. Sessions receive `GH_TOKEN` / `GITHUB_TOKEN` / `GH_HOST` for that account. The SCM observer and tracker use the same token for that project's API calls. AO never runs `gh auth switch`.

This is the v1 design.

## Goals

- A user can add a second GitHub account through Settings without changing the machine's active `gh` account.
- A project can bind one GitHub account. Empty binding keeps today's behavior (active `gh` account / env token).
- SCM observation, tracker intake, PR actions, and worker `gh`/`git` HTTPS credential helper calls for that project use the bound account.
- Tokens never appear in SQLite, API JSON, logs, telemetry, or OpenAPI examples.
- Account-management HTTP routes stay loopback-only and are blocked on the LAN listener, matching Codex.

## Non-goals (v1)

- Claude or Codex account work (existing PR / shipped).
- Per-session GitHub account override (project is the grain).
- GitLab multi-account UI (GitLab already has host-scoped tokens in daemon config; a later PR can add a picker).
- Worker sandboxing of `gh auth switch` (#3637, not planned).
- Copying, parsing, or storing credential files.
- Changing the machine's active `gh` account as a product action.
- SSH key inventory or per-account SSH identity files. SSH remotes keep using the user's SSH agent. `GH_TOKEN` still authenticates `gh` API calls.
- Fine-grained PAT onboarding as a separate flow. `gh auth login` remains the interactive path.
- Copilot's separate token chain (`COPILOT_GITHUB_TOKEN` → `GH_TOKEN` → `GITHUB_TOKEN` → `gh`). That adapter stays as it is.

## Existing surfaces to extend, not replace

Onboarding already has `GET/POST /api/v1/system/github-auth*` (`systemcheck.OpenGitHubAuthTerminal`, `GitHubOnboardingNotice`). That flow signs in the **active** `gh` account so the daemon has any GitHub identity at all.

v1 Add account in Settings should reuse that shell-terminal implementation (same `gh auth login`, same trusted auth-terminal class) rather than copying Codex login as a second PTY stack. After login it must rediscover **all** `gh` accounts and restore the previously active login if `gh` switched. The onboarding notice stays the empty-state path; Settings is the multi-account path.

Comments on `EnvTokenSource` call `AO_GITHUB_TOKEN` “project-scoped.” That is the **daemon process** environment today, not SQLite project config. This design does not change that override. Per-project binding is `ProjectConfig.githubAccount`, not a new `AO_GITHUB_TOKEN` per project.

Chat/TUI runtimes overlay session env on full `os.Environ()`. Injected `GH_TOKEN` / `GITHUB_TOKEN` must win over inherited daemon env for that session. Preview servers already strip daemon credentials (`previewEnvironment`); they must keep stripping GitHub tokens.

## User experience

### Settings → GitHub

New global settings catalog item, visually cloned from the Codex accounts block (`AgentProviderGroup` + rows), not a new visual language.

- List discovered accounts: host, login, active-in-gh flag, last verification state.
- **Add account** opens an inline `gh auth login` shell terminal (same shell-terminal pattern as Codex / harness auth).
- After login, if `gh` made the new account active, AO restores the previously active account with `gh auth switch --user <previous>` **only as crash recovery for the login flow**, then rediscovers. The Settings UI must say AO does not keep a device-global GitHub switch; project binding is the selector.
- **Sign in again** reauthenticates one account.
- **Log out** wraps `gh auth logout --hostname --user` for an account that is not bound to any project. Bound accounts must be unbound first.
- Daemon-wide `AO_GITHUB_TOKEN` / `GITHUB_TOKEN` shows an unmanaged override banner: listing still works for display if `gh` is logged in, but project binding cannot override the env token. Mirror Codex's "switching disabled when credential overrides are present."

### Project settings → General

A GitHub account dropdown under the repo row:

- Default option: "Active GitHub CLI account" (empty binding).
- One option per discovered account (`login@hostname`).
- Saving writes `ProjectConfig.githubAccount`.
- Scratch projects omit the control (no SCM).

## Architecture

```text
Settings UI / CLI
        |
        v
GitHubAccounts service  --discover-->  gh auth status --json hosts
        |               --token----->  gh auth token --hostname --user
        |               --login----->  shellterm: gh auth login
        v
ProjectConfig.githubAccount { hostname, login }
        |
        +--> Session Manager injects GH_TOKEN, GITHUB_TOKEN, GH_HOST
        +--> SCM GitHub Client authorize(ctx) reads account from context
        +--> Tracker GitHub token source reads the same ref
```

### Domain

```go
// GitHubAccountRef identifies one gh-stored account. It is display metadata
// and a token-fetch key, not a credential.
type GitHubAccountRef struct {
    Hostname string `json:"hostname,omitempty"`
    Login    string `json:"login,omitempty"`
}

type ProjectConfig struct {
    // ...existing fields...
    GitHubAccount GitHubAccountRef `json:"githubAccount,omitempty"`
}
```

Validation:

- Both hostname and login empty: OK (inherit active `gh` / env token).
- Login set and hostname empty: hostname defaults to `github.com`.
- Hostname set and login empty: invalid.
- Hostname must be a DNS host (no scheme, path, userinfo, or whitespace).
- Login must be a GitHub login (`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?$` — refuse tokens, emails, and paths).

No new SQLite table. Project config is already a JSON blob. Account catalog is derived from `gh` on ensure/list.

### Token source

Extend `backend/internal/adapters/scm/github/auth.go` (and the tracker twin) so `GHTokenSource` accepts hostname + user. Cache key is `hostname\x00login`. `Token(ctx)` reads an optional account ref from context; empty ref keeps `gh auth token` with no flags (today's behavior).

Env precedence is unchanged and process-wide:

1. `AO_GITHUB_TOKEN`
2. `GITHUB_TOKEN` in the **daemon** environment
3. `gh auth token [--hostname] [--user]`

When (1) or (2) is set, project bindings are ignored for token fetch. The API reports `accountManagement` as unsupported with reason `daemon_token_override`.

`Client.authorize` already takes `context.Context`. The observer and tracker set the account on that context from the project's config before calling the provider. Do not put tokens in the context.

`WithGitHubAccount` / `GitHubAccountFrom` live in `domain` (or `ports`), not in `adapters/scm/github`. The observer package must not import a provider adapter. The GitHub token source reads the domain ref from context.

### SCM observer

Today `identityKey("github", host)` collapses every GitHub host to `"github"`, and `AuthenticatedIdentity` is cached for the provider lifetime as a single login. That is the foreign-PR filter bug for the second account.

v1 changes:

- Replace GitHub's collapsed `identityKey("github", host) == "github"` with an account-aware key: `github|<hostname>|<login>` (login from the project's `GitHubAccount`, or the active `gh` login when unbound). GitLab stays `gitlab|<host>`.
- `resolveIdentities` must not collapse two GitHub projects on `github.com` into one `seen` entry. Key identity resolution by `(provider, host, projectID)` / account key, not by repo host alone.
- `discoverNewPRs` groups listings by `(repoKey, accountKey)`. Two projects that share a repo with different bound logins get two `ListPRsByRepo` calls, each with that account on `ctx`. Author filtering uses the session's account identity, not `identities[identityKey(repo.Provider, repo.Host)]` as it is today.
- Provider API calls for that session/repo go out with the matching context account.
- `AuthenticatedIdentity` cache key includes hostname+login. Do not keep a single process-wide GitHub identity.
- GitLab host-scoping is unchanged.

### Session environment

`runtimeEnv` / `spawnEnv` currently copies `ProjectConfig.Env` then pins AO internals. After that, if the project has a usable GitHub binding (and no daemon env override):

- Set `GH_TOKEN` and `GITHUB_TOKEN` to the fetched token.
- Set `GH_HOST` when hostname is not `github.com`.
- Treat these keys as protected, same as `AO_SESSION_ID`: project `Env` cannot override them.

Do not set `GH_CONFIG_DIR`. Do not put the token in session metadata, SQLite, or logs. Chat controller env uses the same helper as TUI spawn.

`gh`'s git credential helper honors `GH_TOKEN` for HTTPS remotes. SSH remotes are out of scope.

### HTTP API

Loopback routes, Codex-shaped:

| Method | Path | Purpose |
| --- | --- | --- |
| GET | `/api/v1/scm/github/accounts` | Cached catalog |
| POST | `/api/v1/scm/github/accounts/ensure` | Rediscover |
| POST | `/api/v1/scm/github/accounts/login-terminal` | Add account |
| POST | `/api/v1/scm/github/accounts/{login}/login-terminal` | Reauthenticate (`login` is path-escaped; host query required if not github.com) |
| POST | `/api/v1/scm/github/accounts/login-operations/{operationId}/verify` | Verify login |
| POST | `/api/v1/scm/github/accounts/login-operations/{operationId}/cancel` | Cancel |
| POST | `/api/v1/scm/github/accounts/{login}/logout` | Logout if unbound |
| GET | `/api/v1/scm/github/accounts/events` | SSE catalog updates |

Path identity is `login` plus `hostname` query (default `github.com`). Do not invent AO UUIDs; `gh` already keys accounts by host+login.

Wire types live in `backend/internal/httpd/controllers/dto.go` (or a sibling `github_accounts_dto.go`). Register `schemaNames` in `apispec/specgen/build.go`. Run `npm run api`.

LAN: add `/api/v1/scm/github/accounts` to `lanControlBlockedPrefixes`. CORS: treat these paths like Codex account paths (`isCodexAccountPath` or a renamed shared helper).

Project binding uses the existing `PUT /api/v1/projects/{id}/config` / settings update. No new project route.

### CLI

Thin HTTP only:

- `ao github accounts` → GET catalog
- `ao github accounts --refresh` → POST ensure
- `ao project set-config` grows `--github-account login[@hostname]`

Doctor continues to probe one token; when multiple accounts exist, mention the active login and that project bindings are configured in settings.

### Frontend

- `GitHubAccountsSection` on a new Settings catalog id `github`, cloned from Codex row/group chrome.
- Hooks: `useGitHubAccountsQuery`, `useGitHubAccountActions` (login terminal, verify, logout).
- Project settings: account `<select>` in `ProjectSettingsForm` + `packages/product-ui` general view only if the control can stay presentational. Prefer keeping data fetching in the renderer form and passing the control into `ProjectGeneralSettingsView` as a slot so product-ui stays free of the daemon client.
- i18n keys in every `frontend/src/renderer/i18n/*.json` locale.

## Security

- Never log token stdout. `gh auth token` output is trimmed into memory and used as a bearer; tests inject a fake `GH` func.
- Never persist tokens in SQLite, change_log payloads, or session records.
- API snapshots expose `hostname`, `login`, `activeInGh`, `state`, `reasonCode`, `reason`. No token, no `gho_`/`ghp_` prefixes, no hosts.yml paths.
- Login terminal is a trusted authentication terminal (existing shellterm class), scoped to the originating app launch.
- Logout refuses if any project binds that account (`GITHUB_ACCOUNT_IN_USE`).
- After add-account login, restore the previous active `gh` user if it changed. Record a daemon log line without account tokens if restore fails; surface `recovery_required` in the catalog.
- Do not add unauthenticated routes. Do not bind a new listener.

## Testing seams

Prefer httptest, injected `GH` hooks, and fake stores. No live GitHub network in unit tests.

Minimum coverage:

- `GHTokenSource` with `--user`/`--hostname`, cache keyed by account, env override wins.
- `ProjectConfig.Validate` githubAccount cases.
- Observer: two GitHub projects, two logins, foreign-author filter uses the project login; shared repo with two accounts lists twice.
- Session env: bound account injects `GH_TOKEN`; project `Env` cannot override; daemon `AO_GITHUB_TOKEN` suppresses injection.
- HTTP: list/ensure/login/logout, LAN 404, origin boundary.
- Frontend: Settings list, add-account terminal, project picker save payload.
- CLI: set-config round-trip of `githubAccount`.

## Done when

From `AGENTS.md`:

```bash
cd backend && go test ./internal/adapters/scm/github/... ./internal/observe/scm/... ./internal/httpd/... ./internal/domain/... ./internal/session_manager/... ./internal/cli/... ./internal/service/project/...
npm run api
npm run frontend:typecheck
```

Full `npm run lint` before merge. Manual gate: two real `gh` accounts, bind A to project 1 and B to project 2, confirm observer and `gh api user` inside each worker match the binding, and that `gh auth status` on the host still shows the original active account.

## Further notes

- GitHub Enterprise: hostname is first-class because `gh` is already multi-host.
- Enterprise API base URL stays the GitHub client's current host derivation; do not invent a second host table.
- Mobile: account mutation stays loopback-only. Project config read remains on the app API so a phone can see which login a project uses, not add accounts.
