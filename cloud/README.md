# AO Cloud

Private AO control-plane service. This foundation contains:

- the 28-table PostgreSQL founding schema;
- WorkOS access-token verification and profile resolution;
- opt-in local email/password authentication for development;
- organization membership checks backed by PostgreSQL row-level security;
- organization-scoped project and session APIs matching the public Cloud contract;
- idempotent project/session creation and cursor pagination;
- explicit per-session mode and denied-command policy;
- durable workspace intent for every session; and
- durable message queues, event replay, and cluster-safe SSE reconnects;
- GitHub App installation verification, repository grants, and durable
  webhook processing; and
- Dockerized local development plus an isolated hosted-staging launcher.

The repository root also contains the authenticated Next.js Cloud UI. It uses
the public `@aoagents/cloud-client` and `@aoagents/product-ui` sources for
contracts, transport, status mapping, board layout, cards, and agent identity.
It supports organization switching, projects, durable sessions, search, chat
history, live event streams, worker turns, and replica-safe workspace files.
The Cloud UI also persists personal GitHub OAuth, installation confirmation,
repository grants, PR/issue synchronization, and sharing behavior. See
[`docs/control-plane.md`](docs/control-plane.md) for durable-state and cluster
behavior, [`docs/deployment.md`](docs/deployment.md) for staging and production
deployments, and [`docs/cloudagent-v1-parity.md`](docs/cloudagent-v1-parity.md)
for the CloudAgent V1 comparison and remaining performance work.

## Environment model

- **Local (`npm run cloud:local`)** uses local email/password auth, local
  PostgreSQL, and the local control-plane container. WorkOS is used only for an
  optional hosted-account session when the user opens GitHub settings; the app
  itself remains on local auth. GitHub App credentials never leave production.
  Each requested session is provisioned as a labeled sibling Docker container
  with a persistent, per-session workspace volume.
- **Staging desktop (`npm run cloud:staging`)** runs the desktop locally against
  `https://staging-api.aoagents.dev`. The hosted staging control plane uses the
  shared WorkOS environment and its own staging database. Future workers run in
  staging, not on the developer's machine.
- **Staging web (`npm run cloud:web:staging`)** runs the private Next.js UI
  against that same staging API. It loads the server-only WorkOS API key and
  client ID at launch from `ao-cloud/staging/workos` in AWS Secrets Manager;
  credentials are never written to the repository or exposed to browser code.
- **Production** uses `https://api.aoagents.dev`, the shared WorkOS environment,
  the production database, and the one production GitHub App. There is no
  supported local-desktop-against-production development command.

GitHub App credentials are rejected outside production because setup, OAuth,
webhook state, installations, and repository grants are durable in the
production database. The web BFF sends only GitHub integration requests to the
production API using the user's hosted WorkOS token. Before creating a local or
staging project it rechecks that the production repository grant is active,
then writes the project to the current environment. Worker execution remains
isolated to that environment, and each checkout obtains a fresh
production-broker grant rather than treating the local project row as
repository authority.

## Hosted ingress

The hosted control planes are publicly reachable through Cloudflare DNS-only
CNAMEs and AWS-managed TLS:

- staging: `https://staging-api.aoagents.dev` →
  `ao-cloud-staging-public`
- production: `https://api.aoagents.dev` →
  `ao-cloud-production-public`

ACM certificates in `eu-north-1` terminate TLS. Both ECS services run two
healthy replicas, `/healthz` reports deployment identity, and `/readyz` verifies
database connectivity plus draining state. Production also exposes
`/github/healthz`, which validates the configured credentials and App identity
with GitHub and returns `503` when GitHub is disabled, misconfigured, or
unavailable. It is diagnostic only and is not an ALB target-health dependency.
Their task security groups accept port
8080 only from the corresponding public ALB security group. CloudWatch
deployment alarms and the operations dashboard use the public ALB target
groups.

The old internal ALBs have no ECS targets and remain only for a short rollback
observation period. Tasks still use public subnets and public IPs; moving them
to private subnets with NAT or VPC endpoints remains infrastructure work.

## Run locally

The default development loop requires Docker with Compose:

```bash
npm run cloud:local
```

This builds the same distroless control-plane image used in hosted
environments plus the local worker image, starts PostgreSQL on
`127.0.0.1:54329`, runs migrations with an isolated non-superuser schema-owner
role, grants only runtime DML privileges to the separate non-superuser
`ao_cloud_app` role, disables login for the image-bootstrap superuser, and
exposes the API on
`http://127.0.0.1:8081` (avoiding the desktop daemon's usual port), and leaves
the stack running. Local auth is enabled; point the desktop app at it with
`AO_CLOUD_OFFERING=on AO_CLOUD_CONTROL_PLANE_URL=http://127.0.0.1:8081 npm run dev`
from `frontend/` and register a development email/password account in-app to
sign in. The optional private Next.js Cloud UI on `http://127.0.0.1:3000`
requires the (uninitialized) `private/ao-cloud` submodule and is not needed for
desktop-app testing; when run it receives only an HttpOnly session cookie. The
Docker socket is mounted only into the control-plane container so it can create
sibling workers; worker containers never receive the socket. Local Docker
workers do not auto-pause.

Use `npm run cloud:local:down` to stop containers while retaining data and
`npm run cloud:local:reset` to stop them and delete the local database
directory. PostgreSQL data lives at `~/.ao/cloud/postgres` by default; set
`AO_DATA_DIR` or `AO_CLOUD_LOCAL_POSTGRES_DATA_DIR` to move it while keeping
application state out of OS-default app-data locations. Ports can be changed
with `AO_CLOUD_PORT` and `AO_CLOUD_POSTGRES_PORT`.
`npm run cloud:local:smoke` uses an isolated Compose project and random
loopback ports to verify the complete create/restart/persist/down/reset
lifecycle, including worker replacement with workspace persistence, without
touching normal local Cloud data. It reports a clean skip when Docker is not
available.

To launch the desktop's currently implemented auth-only flow against a hosted
staging deployment:

```bash
npm run cloud:staging
```

The launcher defaults to `https://staging-api.aoagents.dev` and the non-secret
staging WorkOS client ID. `AO_CLOUD_STAGING_URL` and `VITE_WORKOS_CLIENT_ID`
remain available as explicit overrides. The command requires HTTPS, refuses
redirects and production responses, verifies `/readyz` reports
`environment=staging`, and isolates Electron and daemon state under
`~/.ao/staging-desktop` by default. If public ingress is unavailable, it exits
with the failing readiness URL and HTTP/TLS error before Electron starts. The
desktop currently uses the URL for staging preflight and future Cloud API
calls; this branch does not add Cloud project/session UI. WorkOS desktop
authentication continues to use the `ao-app://callback` deep link.

To launch the web UI against hosted staging:

```bash
npm run cloud:web:staging
```

This command requires the `ao-cloud` AWS login profile (override with
`AWS_PROFILE`) and uses `http://127.0.0.1:3000/callback` for WorkOS. That exact
redirect URI is verified and created through the WorkOS API before launch.
AuthKit's encrypted cookie key is generated once under `~/.ao/cloud-web`; app
state and credentials never use an OS-default application-data directory.

The web app intentionally resolves the public TypeScript package sources from
the containing Agent Orchestrator checkout. Develop it through
`private/ao-cloud` as the public repository's submodule; a standalone private
clone does not contain those public packages.

For a direct Go loop, requirements are Go 1.26.5 and PostgreSQL 15 or newer.
Development and test environments can apply embedded Goose migrations at
startup, using `AO_CLOUD_MIGRATION_DATABASE_URL` when set and
`AO_CLOUD_DATABASE_URL` otherwise. Hosted deployments run
`/ao-cloud-migrate` as a one-off task before rolling the API service. Local auth
is disabled unless
`AO_CLOUD_LOCAL_AUTH=true`, cannot run alongside WorkOS, and is rejected when
`AO_CLOUD_ENV` is `staging` or `production`.

For WorkOS, set `AO_CLOUD_WORKOS_ISSUER`, `AO_CLOUD_WORKOS_CLIENT_ID`, and
`AO_CLOUD_WORKOS_API_KEY`. The OIDC verifier validates issuer, signature, token
lifetime, and AuthKit's `client_id` claim. The WorkOS API key is server-only and
resolves profile fields that access tokens may omit. The JWKS URL is derived
for standard WorkOS and custom AuthKit domains;
`AO_CLOUD_WORKOS_JWKS_URL` can override it.

Hosted environments use `AO_CLOUD_ENV=staging` or `production` and must set
`AO_CLOUD_RELEASE` to an immutable image tag or Git SHA. Hosted startup fails
if local authentication is enabled or if the runtime database role is a
superuser or can bypass row-level security. Use a restricted runtime credential
for `AO_CLOUD_DATABASE_URL` and, when necessary, a separate schema-migration
credential for `AO_CLOUD_MIGRATION_DATABASE_URL`.

## Database changes and production promotion

Database migrations in `internal/postgres/migrations` are embedded in the same
immutable control-plane image as the API and migration binary. That image also
packages `/ao-worker` for sandbox upload; a separate worker runtime image
contains the identical binary. The release flow is:

1. Add a forward, backward-compatible Goose migration to the repository.
2. Run the migration and integration tests locally.
3. Deploy the commit with `scripts/deploy-staging.sh`. Staging runs that image's
   migrations before updating its API replicas.
4. Verify the release in staging.
5. Promote it with `scripts/promote-production.sh`. Production uses the exact
   scanned control-plane and worker image digests recorded by staging and runs
   the control-plane image's migrations before updating any production API
   replica.

If a production migration fails, promotion stops and the existing production
API keeps running. Application rollback does not reverse an applied migration,
so migrations must remain compatible with the previous API release.

Only migration code and the tested application artifacts are promoted. NodeOps
and worker settings come from the target environment's `nodeops` and `worker`
Secrets Manager JSON entries; deployment validates every required field before
registering ECS tasks. No provider auto-pause value is set by deployment.
Staging database rows are never copied to production: users, organizations,
projects, sessions, events, credentials, and all other data remain isolated in
their respective databases. The AWS instances are named
`ao-cloud-staging-storage` and `ao-cloud-production-storage` so the environment
boundary is explicit. See [`docs/deployment.md`](docs/deployment.md) for the full
deployment and rollback procedure.

Each push to private `main` runs
`.github/workflows/update-public-submodule.yml`. When the
`AO_PUBLIC_REPO_TOKEN` repository secret is configured with pull-request write
access to `Untrivial-ai/agent-orchestrator`, it opens or refreshes a public PR
that moves the optional `private/ao-cloud` gitlink to that exact private
commit. Without the secret the pointer job is skipped.

If a verified access token contains `org_id`, that WorkOS organization and the
token's role are synchronized into AO membership. Tokens without `org_id`
receive a personal organization.

## API

All resource routes use `/api/cloud/v1`. Project and session creation require an
`Idempotency-Key` header.

| Method | Route | Purpose |
|---|---|---|
| `POST` | `/auth/local/register` | Create a dev user and personal organization |
| `POST` | `/auth/local/login` | Create a revocable dev session |
| `POST` | `/auth/local/logout` | Revoke the current dev session |
| `GET` | `/me` | Return the current user and organization memberships |
| `GET/POST` | `/orgs/{orgId}/projects` | List or create projects |
| `GET` | `/orgs/{orgId}/github/installations` | List connected GitHub App installations |
| `POST` | `/orgs/{orgId}/github/installations/start` | Start a short-lived installation handshake |
| `POST` | `/orgs/{orgId}/github/installations/{id}/sync` | Refresh repository grants |
| `POST` | `/orgs/{orgId}/github/installations/{id}/disconnect` | Revoke AO's installation grants |
| `GET` | `/orgs/{orgId}/github/repositories` | List active and revoked repository grants |
| `POST` | `/orgs/{orgId}/github/projects` | Create a project from an active repository grant |
| `POST` | `/orgs/{orgId}/projects/scratch` | Idempotently create a private repository, project, and orchestrator session |
| `GET/POST` | `/orgs/{orgId}/sessions` | List or create sessions |
| `GET` | `/orgs/{orgId}/sessions/{sessionId}` | Read a session |
| `POST` | `/orgs/{orgId}/sessions/{sessionId}/messages` | Durably queue a message |
| `GET` | `/orgs/{orgId}/sessions/{sessionId}/chat-events` | Replay committed client events |
| `GET` | `/orgs/{orgId}/sessions/{sessionId}/events` | Replay and stream client events over SSE |
| `GET` | `/orgs/{orgId}/sessions/{sessionId}/workspace/files` | List worker-workspace entries |
| `GET/PUT` | `/orgs/{orgId}/sessions/{sessionId}/workspace/file` | Read or write a bounded UTF-8 workspace file |
| `GET` | `/orgs/{orgId}/sessions/{sessionId}/workspace/diff` | Read a bounded worker-workspace git diff |
| `POST` | `/orgs/{orgId}/sessions/{sessionId}/terminal-ticket` | Create a short-lived workspace-terminal ticket |
| `GET` | `/terminal` | Upgrade a single-use ticket to the durable terminal WebSocket |

WorkOS access tokens and local development tokens both use
`Authorization: Bearer <token>`.

GitHub App setup uses random, hashed, expiring state. The setup callback rotates
that state into a separate OAuth PKCE challenge; the encrypted verifier is
deleted after use. AO binds an installation only after GitHub confirms that the
temporary user token can see and administer it: the user must own a personal
installation or be an active admin of the GitHub organization. User OAuth
tokens and installation tokens are never stored. Webhooks are accepted only
after constant-time HMAC-SHA256 verification, deduplicated by GitHub delivery
ID, and processed in per-installation order through leased PostgreSQL inbox
rows with bounded retries. Sync generations prevent slower stale requests from
restoring newer revoked grants. Repository removals, suspension, deletion, and
explicit disconnect revoke grants; project creation checks an active grant in
the same database transaction.

Local and staging scratch creation uses server-only broker routes that are not
part of the public Cloud contract. Production reserves the user's idempotency
key before creating a GitHub repository and returns an encrypted-at-rest,
recoverable opaque capability only to the authenticated BFF. The target
environment validates it with production before encrypting it under its
provider credential key and creating project/session rows. Workers redeem that
stored capability through production for a fresh token restricted to the exact
repository. Revoked, mismatched, or unavailable capabilities fail closed.

The GitHub App needs at least organization **Members: read** permission so AO can
prove that the person binding an organization installation is an active admin.
The `installation` and `installation_repositories` lifecycle webhooks are sent
to every GitHub App automatically and do not appear in the selectable event
list. The connectivity diagnostic does not enforce a permission policy;
least-privilege permissions remain recommended.

The one GitHub App is a production-owned integration. Its global setup,
OAuth-callback, and webhook URLs must point to `https://api.aoagents.dev`.
Staging and local configurations reject GitHub App credentials. Production
loads the App credentials from Secrets Manager through the ECS task definition.
The private web BFF brokers installation and repository requests to production,
so local and staging can manage the one App without copying secrets or callback
state into either environment.

Staging and production intentionally share one WorkOS environment while keeping
their AO databases separate. Desktop login continues to use the
`ao-app://callback` deep link.

## Tenancy

Every organization-owned table has a composite tenant foreign-key path and a
forced PostgreSQL row-level-security policy. Each transaction sets
`ao.user_id` and `ao.org_id`, then verifies an active membership before reading
or writing resources. A caller cannot use another organization's UUID to cross
the boundary.

The service imports shared Go session-status rules from the public AO contract
module. The public OpenAPI document and TypeScript client define account,
project, session, event, and GitHub wire contracts. GitHub integer IDs are
encoded as decimal strings at the HTTP boundary so JavaScript clients do not
lose precision. The `replace` in `go.mod` pins the exact public Go contract
commit under the module's existing canonical import path.

## Hosted monitoring

`scripts/configure-monitoring.py` idempotently enforces the documented log
retention, deployment/health/latency/ECS/RDS alarms, and the `ao-cloud`
CloudWatch dashboard for staging and production:

```bash
AWS_PROFILE=ao-cloud ./scripts/configure-monitoring.py --dry-run
AWS_PROFILE=ao-cloud ./scripts/configure-monitoring.py
```

Set `AO_CLOUD_ALERT_TOPIC_ARN` to attach SNS alarm and recovery notifications.
Without it, alarms still drive deployment rollback and appear in CloudWatch but
do not notify a person. `scripts/verify-ecs-service.py` rejects incomplete
rollouts, mixed task revisions, empty or unhealthy target groups, and
non-`OK` deployment alarms. Deployment and promotion call it before and after
changes.

## Verify

Unit tests do not require PostgreSQL:

```bash
go test ./...
go vet ./...
```

The isolated Compose lifecycle check builds the images, applies fresh
migrations, verifies the HTTP flow and role boundaries, restarts the control
plane, recreates the stack without deleting its data directory, and finally
proves that reset deletes that directory:

```bash
npm run cloud:local:smoke
```

Verify the web application:

```bash
npm run test:web
npm run typecheck:web
npm run build
```

Database integration tests run when a disposable PostgreSQL database is
provided:

```bash
AO_CLOUD_TEST_DATABASE_URL='postgres://localhost/ao_cloud_test?sslmode=disable' \
  go test ./... -count=1
```

The integration suite applies the migration, asserts 35 AO tables and 28
forced-RLS tenant/domain tables,
exercises local and WorkOS-backed principals, checks idempotent project,
session, and message creation, verifies concurrent message retries, durable
cross-replica event delivery/replay and workspace intent, and proves
cross-organization reads are denied. Private CI runs those PostgreSQL tests,
Go vet, deployment fixtures, shell checks, separate control-plane and worker
image builds, an offline image-contract check, and the isolated Compose
lifecycle.
