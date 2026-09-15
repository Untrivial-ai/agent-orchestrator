# Cloud onboarding — implementation plan

Branch: `pr-5023` (Untrivial-ai/agent-orchestrator PR #5023 head `680e1bdb2`, mirrors `main`).
Status: design approved in Discord thread + mockups. Not yet implemented.

## 0. What this fixes

Today, cloud project creation asks for a repo URL/name/branch with no agent key and no
verified GitHub credential. The failure modes discovered by tracing the code:

1. **Gate/server mismatch.** `hasValidAgentConnection` (any valid key) gates the UI nag;
   `agentConnectionAvailable` (that harness's key) gates `createSession`. A Claude key +
   Codex selection passes the first and 422s on the second.
   `frontend/src/renderer/hooks/useProviderConnections.ts:30`,
   `cloud/internal/httpapi/provider_handlers.go:53`.
2. **Unverified GitHub PAT.** `putGitHubPAT` "intentionally does not call GitHub"
   (`cloud/internal/httpapi/provider_handlers.go:235`) — a bad token shows "Connected"
   and fails later inside the sandbox, at clone time.
3. **No repo reachability check at create.** A private repo URL is accepted, project is
   created, checkout fails inside the sandbox with an opaque error.
4. ~~PAT overrides the App~~ — **retracted.** Checked PAT-first ordering in
   checkout/push/git-token against a later commit (`patWriteGrant` +
   `workerRaisePullRequest`, added by the upstream `main` merge) and found an explicit
   comment confirming this is deliberate: *"Prefer the user's PAT when one is
   configured: it can write back to GitHub even where the checkout broker is read-only
   (e.g. staging reaches GitHub through the remote capability broker, whose write
   methods are stubbed). This mirrors the PAT-first read/push grant path."* PAT-first is
   the intended design, repo-wide, not a bug. (I drafted and then reverted a "fix" for
   this — logged here so it isn't tried again without re-reading this note.)
5. ~~No PAT fallback on Raise PR~~ — **retracted, same reason.** `workerRaisePullRequest`
   now has a `patWriteGrant` path (added by the same merge) precisely because the
   deployed remote checkout broker's write methods are stubbed on staging — PAT is the
   only thing that can actually raise a PR there today. Confirmed live: user reports
   "write operations on GitHub — only read has been implemented" on the current
   deployment, matching the code.
6. **No agent defaults on cloud projects.** `CreateProjectInput` has `displayName`,
   `repositoryUrl`, `defaultBranch`, `config` — no agent fields, unlike local's project
   defaults set via `CreateProjectAgentSheet`.
7. **GitHub App is production-only and staging is disabled.**
   `cloud/internal/config/config.go:519` refuses App credentials outside production.
   `curl https://staging-api.aoagents.dev/github/healthz` → `{"status":"disabled"}`.
   Blocks building/testing anything under `/github/*` until a staging App exists —
   **not something this branch can fix; needs a maintainer decision (see §6).**

## 0.5 Demo mode — showing this flow before the backend is reachable

No maintainer/org credentials or DB access are available yet, so the flow below is
wired into the **real** renderer components against a **fake control-plane response
layer**, not a separate mockup page. The seam: `createCloudCpClient({ fetchImpl })`
already accepts any `typeof fetch`
(`frontend/src/renderer/lib/cloud-cp/client.ts:250`) — `createDemoCloudCpFetch()`
(`frontend/src/renderer/lib/cloud-cp/demo-fetch.ts`) wraps the real fetch and
intercepts only the onboarding paths that don't exist on any reachable deployment yet
(`PUT .../provider-connections/agents/{agent}`, `PUT /me/github-pat`,
`POST .../projects` with the reachability check), matching the exact wire contract
implemented in §2.2/§2.3 above. Everything else — sign-in, `/me`, the real org, the
real project list — passes through untouched, so the demo is running on a real WorkOS
session and a real org the whole time.

**Off by default.** Arm it without a rebuild from the Electron devtools console:
`window.__aoSetCloudDemoMode(true)`, then reload the renderer (not the whole app).
Or set `VITE_AO_CLOUD_DEMO=1` before `npm run dev` to have it on from launch. Disarm
with `window.__aoSetCloudDemoMode(false)` + reload.

**To remove once real backend support lands:** delete the matching `case` out of
`demo-fetch.ts` one at a time as each endpoint goes live — nothing above that file
(components, hooks, query keys) needs to change, because the fake responses already
match the real DTOs and error codes.

**What's wired into the real create-project flow now** (`CreateProjectFlow.tsx`'s
`CloudProjectCard`, unchanged local flow):
- Repository step (unchanged fields) → **Next** advances to a new agent step instead
  of creating immediately.
- Agent step (`CloudAgentSetupStep`) — Worker + Orchestrator, using
  `RequiredAgentField` **exported from and shared with local's own agent sheet**
  (`CreateProjectAgentSheet.tsx`), fed cloud provider-connection state mapped into the
  same `AgentInfo` shape local readiness uses (`cloudAgentInfos()`). A provider with
  no valid connection renders exactly as local's "Needs auth" — same component,
  different source of truth, per the original design goal. A not-ready row's "Add a
  coding agent credential" link opens the real `CloudCredentialDialog`
  (store-driven, unmodified).
- `repository_unreachable` from create (real error code, §2.3) sends the user back to
  the repository step with the reason shown and an inline GitHub token field
  revealed; saving the token (`client.putGitHubPAT`, real call/real validation, §2.2)
  automatically retries the same create with the agents already chosen — "Save and
  retry" actually retries, not just navigates back.
- `config: { workerAgent, orchestratorAgent }` rides in `CreateProjectInput.config`
  (already free-form JSON, no DTO/schema change) — the smallest-footprint way to
  carry this until a real `workerAgent`/`orchestratorAgent` field is promoted onto the
  DTO, matching the "config can probably carry this without a schema change" note in
  the original §2.5 sketch.

**Test coverage:** `CreateProjectFlow.test.tsx` gained/updated three cases exercising
this against the real components — advances through both steps and creates with the
chosen agents; blocks a bad URL before the agent step; catches
`repository_unreachable`, reveals the token field, and auto-retries after a real
`putGitHubPAT` call. `npm run typecheck` clean; full `npm run test -- --run` green
except pre-existing unrelated failures (see below).

**Known pre-existing, unrelated failures** on this branch (not touched, not
introduced): `src/landing/scripts/generate-markdown-twins.test.mjs` (missing
`cheerio` dependency) and `src/renderer/i18n/renderer-coverage.test.ts` flags three
hardcoded strings in `components/settings/CloudCredentialsSection.tsx`, a file this
work never opens. Confirmed by isolating: my own change added exactly one new
violation (`"github_pat_…"` placeholder in the new inline token field), fixed by
adding it to that file's existing literal allowlist in
`renderer-coverage.test.ts` — the same allowlist already carries `"web-app"`/`"main"`
for this exact file.

## 1. Scope decision — what ships now vs. later

Maintainer has said: keep the `.git` URL entry for adding a cloud project, no repo
picker yet ("we'll do that eventually"). This plan follows that: **no picker, no
namespace switcher, no org-approval-pending screen** in this pass — those are Lane A
(GitHub App) work, blocked on §6 anyway.

**In scope now (all buildable without the App, on any deployment):**

- A. Fix the gate/server mismatch (finding 1)
- B. Verify the PAT on save (finding 2)
- C. Verify repo reachability at create (finding 3)
- D. Flip PAT precedence to true fallback (finding 4)
- E. Surface the agent-key requirement in the create flow, reusing
  `CreateProjectAgentSheet` (finding 6)
- F. Orchestrator-button "add a coding agent" state (second net for E)
- G. Name the GitHub token scope + link to token creation in the credential UI

**Explicitly out of scope for this pass:**

- Repo picker, namespace switcher, org-approval-pending screen (Lane A / needs App)
- "Connect GitHub" as the primary path (still gated on §6)
- PAT-vs-App resolution for Raise PR (finding 5) — needs a decision first, see §5

## 2. Ordered work items

Each item is independently shippable and testable. Do them in this order — later items
assume earlier ones exist.

### 2.2 — Verify GitHub PAT on save (finding 2) — `cloud/` — ✅ done

Implemented as: `agentCredentialValidator.Validate` gained a `"github"` case reusing
the existing `validateBearerEndpoint` helper (`GET /user`, same 401/403→invalid,
200/429→ok policy as Claude/Codex/Cursor). `putGitHubPAT` now calls it before
`UpsertUserProviderConnection`; an invalid token gets `422 invalid_credential` and is
never stored, GitHub-unreachable gets `502 provider_unavailable`. New tests in
`provider_handlers_github_pat_test.go` cover: accepted, rejected-and-not-stored,
GitHub-unavailable-and-not-stored. `go test ./... ` green.

**File:** `cloud/internal/httpapi/provider_handlers.go`, `putGitHubPAT` (~L235).

**Change:** after decrypting/before storing, one authenticated call to GitHub as that
token (e.g. `GET /user`, or a scoped check against the target repo if the endpoint has
repo context — `putGitHubPAT` today is account-level, so start with `/user`). On
failure, `422` with a specific message ("This token doesn't work — check it hasn't
expired."). Follow the same pattern as `agentCredentialValidator.Validate` for Claude/
OpenAI keys already in this file — same file, opposite policy today; make it consistent.

**Test:** httptest fake GitHub server returning 200/401/403; assert stored vs rejected.

### 2.3 — Verify repo reachability at create (finding 3) — `cloud/` — ✅ done (scoped)

Implemented as an unauthenticated probe of the git smart-HTTP endpoint
(`{repoURL}/info/refs?service=git-upload-pack`) — the same thing `git ls-remote` does
for an HTTPS remote, no git binary needed, one GET. `createProject` calls
`probeRepositoryReachable` before `s.store.CreateProject`; a non-200 answer (git and
GitHub both return 404 for "doesn't exist" and "private, no access" alike, on purpose)
gets `422 repository_unreachable` before any project row is written. A probe that
cannot even run (DNS/connection failure) fails open — an infra hiccup must not block a
legitimate create.

**Scoped down from the original plan:** this does not attempt an *authenticated* probe
using the caller's stored PAT. Doing that needs a new store method to fetch+decrypt a
principal's own PAT outside the worker-session context (today's PAT lookups are all
keyed by session, via `workerGitHubPATGrant`/`patWriteGrant`) — a real postgres-layer
change, not something to bundle silently into this one. Net effect: a private repo with
a *correctly configured* token still gets flagged "unreachable" at create time today
(false rejection), not just an incorrectly-configured one. Follow-up, not done here.

New tests in `resource_handlers_repository_probe_test.go`: reachable → created,
unreachable → 422 and never created, connection error → fails open (loopback only, no
real network call, per AGENTS.md rule 13). `go test ./...` green.

**File:** `cloud/internal/httpapi/resource_handlers.go`, `createProject` (~L146) or a
pre-flight step the frontend calls before submit — pick whichever keeps `createProject`
a single request/response (prefer inline: fail loudly on create rather than adding a
new endpoint).

**Change:** attempt `git ls-remote` (or a GitHub API HEAD, cheaper if the URL is a
github.com URL) using whatever credential is available (PAT if connected, anonymous
otherwise per `AO_CLOUD_ALLOW_ANONYMOUS_GITHUB_CHECKOUT`). On failure, `422` with
"Can't reach this repo — private, or it doesn't exist" (mockup copy) rather than
succeeding and failing later in the sandbox.

**Test:** unreachable URL → 422 before any project row is written; reachable public URL
→ succeeds with no credential; reachable private URL with valid PAT → succeeds.

### 2.4 — Gate/server mismatch fix (finding 1) — `frontend/` + `cloud/`

**Files:** `frontend/src/renderer/hooks/useProviderConnections.ts`,
`cloud/internal/httpapi/provider_handlers.go` (`agentConnectionAvailable` already
correct — the frontend hook is the one to fix).

**Change:** don't fix by adding a second check — fix by construction per §2.5: once the
agent picker only lists agents with a valid connection *for that specific harness*,
`hasValidAgentConnection`'s "any valid" semantics stop mattering for gating session
creation. Keep `hasValidAgentConnection` only for the app-root "you have zero
connections at all" nag (`CloudOnboardingGate`); it's fine there since it's not gating a
specific harness choice.

**Test:** existing `CloudOnboardingGate` tests keep passing; new test on the agent
picker (§2.5) asserting an agent without a connection for its own provider is
unselectable regardless of other providers' state.

### 2.5 — Agent step in cloud create flow, reusing `CreateProjectAgentSheet` (finding 6)

**Files:**
- `cloud/internal/httpd/controllers/dto.go` (or the cloud-specific DTO file) — add
  optional `workerAgent`/`orchestratorAgent` to `CreateProjectInput`. Check whether
  `config` (already free-form JSON) can carry this without a schema change, or whether
  it needs promoting to real fields — real fields are cleaner for the sheet to bind to
  and for validation, so prefer that unless it's a much bigger migration than expected.
- `cloud/internal/httpapi/apispec` or wherever the OpenAPI spec is built for the cloud
  control plane — mirror `backend/internal/httpapi/apispec` pattern if one exists;
  confirm the cloud CP's spec-generation story before touching it (may differ from the
  daemon's).
- `frontend/src/renderer/components/CreateProjectFlow.tsx` — wire
  `CreateProjectAgentSheet` into the cloud path (currently only local's
  `chooseDirectory` flow reaches it).
- `frontend/src/renderer/lib/agent-select-options.ts` — the `agentStatus()` /
  `buildRankedAgentOptions()` logic already renders "Needs auth" style rows; feed it
  cloud provider-connection state (`validationState !== "valid"` → same "needs
  key"/warning tone) instead of the daemon's `AgentInfo.authentication.state`. This is
  the "same component, different source of truth" move from the design — don't build a
  new status-row component.
- `packages/cloud-client/src/types.ts` / `schema.ts` — regenerate if DTOs change (run
  `npm run api` per the repo's API-change loop in AGENTS.md).

**Change (behavioral):** the create flow gains a "Set up agents" step after repo entry,
using the existing local sheet. Each agent row shows ready/needs-key per that harness's
cloud provider connection; picking a not-ready agent opens `CloudCredentialDialog` in
place (already exists, already store-driven via `useCredentialDialogStore`) and returns
to the sheet on save. If only one harness is ready, it's preselected and the picker
doesn't need to be expanded (still show it collapsed with the default, per the mockup).

**Test:** component test — zero connections → both rows "needs key", Create still
enabled; one connection → that harness auto-selected; multiple → ranked by
`agentUsageCompare`, unselectable ones dimmed and non-clickable-to-submit.

### 2.6 — Orchestrator button "add a coding agent" state (finding 6, second net)

**File:** wherever the cloud session/orchestrator start button lives in the renderer
(project detail view — locate via `useCloudProjectsQuery`/`useCloudOrg` usage sites,
likely near `TaskComposer.tsx`'s cloud branch or a sibling "start orchestrator" control).

**Change:** before rendering the start control, check provider-connection state for the
project's configured harness(es) the same way §2.5 does. If none are ready, render "Add
a coding agent to start" (opens `CloudCredentialDialog`) instead of a button that fires
`createSession` and eats a 422.

**Test:** renders the CTA state when connections are empty; renders normal start button
when at least one configured harness is ready.

### 2.7 — Scope guidance + link in the PAT UI (finding 2, cosmetic but cheap)

**File:** `frontend/src/renderer/components/settings/CloudCredentialsSection.tsx` and/or
wherever the PAT field moves to in the create flow per §2.5's dialog reuse.

**Change:** copy from "Optional. Paste a GitHub personal access token with access to
private repositories." to naming the actual scope needed ("Needs `Contents: read and
write`") plus a link to GitHub's fine-grained token creation page pre-filtered if
possible. Check what scope `checkoutBroker`/`workerGitHubPATGrant` actually requires
server-side before finalizing the copy — don't guess a scope name without confirming it
against the git operations the PAT is used for (clone + push).

## 3. What is NOT in this plan (explicitly deferred)

- Repo picker, namespace switcher, "waiting on org approval" screen — all Lane A, all
  blocked on §6.
- "Sign in with GitHub" via WorkOS as an additional identity provider — separate
  decision, doesn't block anything above.
- Auto-chaining sign-in straight into the GitHub install screen — a renderer-only UX
  polish on top of Lane A, not blocking, do after §6 unblocks Lane A work.
- Raise-PR PAT support — already shipped by the upstream merge (`patWriteGrant`), no
  work needed here; findings 4/5 were retracted, see §0.

## 4. Test plan (repo-wide gate, per AGENTS.md)

For each cloud/backend change: narrowest test first
(`cd cloud && go test ./internal/httpapi/... -run <Test>`), then
`cd cloud && go test ./...`, then the full `go test -race ./...` from `backend/` if
anything there is touched (should be none in this plan — all changes are in `cloud/`
and `frontend/`). For frontend: `npm run frontend:typecheck`, then
`cd frontend && npm run test` for the touched components. If DTOs change (§2.5), follow
AGENTS.md's exact API-change loop: dto.go → apispec → `npm run api` →
`go test ./internal/httpd/...` (or cloud's equivalent) → commit `openapi.yaml` +
`schema.ts` together.

## 5. What needs a maintainer, not code

**A separate staging GitHub App.** `AO_CLOUD_GITHUB_APP_ID` (+ slug, client id/secret,
private key) can only be set where `environment == "production"` per config.go:519.
Someone with GitHub org-owner rights needs to:

1. Register a new GitHub App scoped to test/staging use (own callback URLs, own webhook
   secret, installed only on throwaway test orgs — never share production's key).
2. Either relax the config.go guard from "production only" to "never share a key across
   environments" (a small code change, but the *decision* to relax it and the *App
   registration* itself are both maintainer calls, not something a PR can just do), or
   stand up a dedicated staging-like environment where `environment == "production"` is
   true but it's not actually serving real customers.
3. Provide the resulting App ID/key to whoever configures the staging deployment.

Until this exists, every Lane A item (§3's deferred list) is untestable, and this
plan's scope in §1 is exactly "everything that doesn't need it."

**This plan does not require that App to ship §2.2–2.7.** All of it works with the PAT
path only, on any deployment, today.
