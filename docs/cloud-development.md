# Cloud development

## TL;DR

AO stays a complete public local product. The private `ao-cloud` repository
implements the hosted control plane and can be checked out at
`private/ao-cloud` as an optional submodule for developers who have access.
Public builds, tests, and contributors must not depend on that checkout.

The public repository owns stable contracts, generated client types, reusable
product UI, and local desktop behavior. The private repository owns tenant data,
authorization, hosted execution, infrastructure, and secrets.

## Using the private checkout

After the optional submodule is added, an authorized developer initializes it
with:

```bash
gh auth setup-git
git -c submodule.private/ao-cloud.update=checkout \
  submodule update --init private/ao-cloud
```

Developers without access do nothing. A normal clone, build, and test of public
AO—including `git clone --recursive`—continues to work because the submodule is
configured with `update = none`. Only the explicit opt-in command above fails
without private repository permission.

The private web app consumes the public packages through the containing
workspace. Install the public workspace first, then the private app:

```bash
npm ci
npm --prefix private/ao-cloud ci
```

Do not copy those package sources into the private repository. The private
package links point to `packages/cloud-client` and `packages/product-ui`, while
its build scripts compile the public packages before building the Next.js app.

Work in the two repositories remains separate:

1. Commit and push private implementation changes from `private/ao-cloud`.
2. In the public repository, stage the `private/ao-cloud` path to record the
   known-compatible private commit.
3. Review the private code and the public submodule-pointer update in separate
   pull requests.

Never put credentials in `.gitmodules`. Public and fork CI must not initialize
the private submodule. A future integration job should use a scoped GitHub App
token with read access to `ao-cloud`.

## Foundation implemented in public AO

- Stable Go facts and pure rules for agents, sessions, status, PRs, reviews, and
  stack position.
- Shared Claude Code, Codex, and Cursor launch/restore policy plus Linux worker
  process lifecycle primitives.
- Organization-scoped account, project, session-policy, event, and GitHub
  OpenAPI contracts with generated TypeScript schema types, including the
  durable worker turn, credential, and checkout-grant boundary.
- A typed Cloud client for bearer authentication, pagination, idempotent writes,
  cursor-safe reconnecting SSE, GitHub App flows, terminal tickets, and
  workspace reads.
- Reusable board, composer, inspector, project-settings, agent, and SCM
  presentation. The Cloud app now consumes the shared board, session-card,
  status, and agent-identity exports instead of maintaining a second copy.
- WorkOS desktop authentication with token custody in Electron main and a
  token-free renderer account projection.

These are shared-ready boundaries, not a hosted Cloud implementation.

## Session browser (Stage 1)

Cloud sandbox sessions run an in-VM browser service ("browserd") so `ao
browser <verb>` works inside a session exactly as it does on desktop: same
verb tree (`backend/pkg/clibrowser` is the shared implementation), same wire
contract (`backend/pkg/browsercontract`), same JSON result shapes and
untrusted-content markers.

How it fits together:

- `/ao-worker` starts browserd before the agent terminal opens: it mints the
  per-session HMAC capability, binds a loopback-only HTTP listener on
  `127.0.0.1`, and exports `AO_BROWSER_CAPABILITY` and `AO_BROWSER_API_URL`
  into the agent environment (and the worker environment, so workspace shell
  terminals inherit them). A browserd failure logs and continues; the worker
  never dies for it.
- The supervised headless Chromium (Debian `chromium` package) and the pinned
  agent-browser engine (`/usr/local/lib/ao/agent-browser`, sha256-pinned per
  arch in `cloud/Dockerfile`, kept in sync with
  `frontend/scripts/prepare-agent-browser.mjs`) start lazily on the first
  browser verb. Chromium binds CDP to loopback only; its port is discovered
  from `<profile>/DevToolsActivePort`.
- Everything stays inside the VM: agent verbs never touch the control plane,
  and screenshots (up to 5 MiB) never leave the sandbox.
- The browser profile lives under `<dataDir>/browser/<sessionID>/profile` and
  dies with the sandbox. After 30 minutes with no browser command the
  supervised Chromium stops; the next verb starts it again.

Verb support: everything the desktop supports except `network-*`,
`devtools-*`, and `unhighlight` (they return
`BROWSER_ACTION_UNSUPPORTED_CLOUD`; network capture is planned for a later
stage). `open localhost:3000` and bare hostnames work, so VM-local dev
servers are first-class targets.

Try it locally with the docker provider stack
(`cloud/scripts/test-cloud-local.sh`):

```bash
docker exec "$(docker ps -q --filter label=ao.provider=docker | head -1)" \
  ao browser open https://example.com
docker exec "$(docker ps -q --filter label=ao.provider=docker | head -1)" \
  ao browser snapshot --interactive
```

Full manual instructions, including running the same production service on a
CreateOS sandbox VM without any control plane: `docs/cloud-browser-stage1.md`.

## Private implementation status

The private repository now contains:

- the 28-table PostgreSQL schema, tenant keys, forced RLS, organizations,
  memberships, projects, sessions, turns/events, and future execution,
  sharing, and GitHub records;
- WorkOS access-token validation, organization authorization, idempotent
  project/session/message APIs, durable workspace intent, and cross-replica
  event replay/SSE;
- secure GitHub App installation, OAuth verification, repository grants,
  synchronization, disconnect, project import, and durable webhook processing;
- an authenticated Next.js Cloud app for organization selection, project and
  durable-session creation, search, chat history, and live event replay;
- non-root control-plane and migration images;
- separate staging and production RDS/ECS/ALB/secrets/logging environments; and
- migration-first staging deployment plus exact-digest production promotion
  with scanning, health checks, automatic rollback, guarded manual rollback,
  CloudWatch alarms, and an operations dashboard.

The public submodule pointer records the private `main` commit known to be
compatible with this public branch. It is a development reference only; public
builds and releases still do not initialize or package the private repository.

## Environment modes

1. **Local:** `npm run cloud:local` builds and starts the Docker-backed control
   plane, PostgreSQL, and worker image with email/password local auth on
   `http://127.0.0.1:8081`, then leaves the stack running for the desktop app.
   WorkOS is used only for an optional hosted-account session when managing
   GitHub; the app itself remains on local auth and never loads GitHub App
   credentials. Docker workers are shipped and working: the control plane uses
   the `docker` sandbox provider (`AO_CLOUD_SANDBOX_PROVIDER=docker`) to
   provision each session as a labeled sibling container with a persistent
   per-session workspace volume, and the Docker socket is mounted only into the
   control-plane container. Point the desktop app at it with
   `AO_CLOUD_OFFERING=on AO_CLOUD_CONTROL_PLANE_URL=http://127.0.0.1:8081 npm run dev`
   from `frontend/`, then register a local email/password account in-app. Stop
   the stack with `npm run cloud:local:down` (data retained) or
   `npm run cloud:local:reset` (also deletes the local database directory);
   `npm run cloud:local:smoke` runs the isolated lifecycle smoke test. The
   optional private Next.js Cloud UI at `http://127.0.0.1:3000` requires the
   `private/ao-cloud` submodule and is not needed for desktop-app testing.
2. **Staging:** `npm run cloud:staging` runs the desktop locally against
   `https://staging-api.aoagents.dev`, the hosted staging database, and the
   shared WorkOS environment. `npm run cloud:web:staging` runs the private web
   app against the same API, loading server-only AuthKit credentials from AWS
   Secrets Manager. Future staging workers run remotely.
3. **Production:** `https://api.aoagents.dev` uses the production database, the
   same WorkOS environment, and the production-owned GitHub App. There is no
   local-desktop-against-production development command.

GitHub App credentials remain disabled outside production. The private web BFF
brokers GitHub installation and repository requests to the production API using
the user's WorkOS session, then rechecks active repository access before writing
a project into the current environment. Callback and webhook state therefore
stay in production. Worker checkout still needs a short-lived,
environment-scoped broker grant before execution can be enabled.

## Private implementation still required

1. **Hosted execution plane:** queues, leases, reconciliation, provisioning,
   sandbox images, workers, heartbeats, terminal transport, and workspace RPC.
   These already ship for local development through the `docker` sandbox
   provider (exercised end to end by `npm run cloud:local:smoke`); the remaining
   work is the hosted (NodeOps) execution plane.
2. **Cloud app completion:** files, terminal, review, and a production web
   deployment. Worker/orchestrator execution controls remain hidden until the
   execution plane exists.
3. **SCM completion:** personal GitHub OAuth, scoped installation-token brokering for workers,
   PR/issue/check/review synchronization, and stale-head guards.
4. **Operations:** retire the empty internal ALBs after observation, move tasks
   to private subnets, configure SNS alarm notifications, and complete billing,
   backup restore drills, incident controls, and compatibility policy.

## Recommended implementation order

1. Add provisioning and the worker protocol for real hosted sessions.
2. Add worker token brokering and SCM synchronization.
3. Add terminal/files, sharing, review synchronization, and remaining operations
   hardening.

See [cloud-refactor.md](cloud-refactor.md) for package ownership, import rules,
and the detailed public/private boundary.
