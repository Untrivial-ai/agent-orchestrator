# Cloud control-plane foundation

This repository now contains the first hosted AO control-plane slice. It is a
small, independently runnable Go service built around PostgreSQL, Google
identity, and Daytona. A cloud project has a lightweight AO coordinator daemon,
while every orchestrator and worker session is launched in its own Daytona
sandbox. The coordinator continues to expose the existing daemon API, so the
desktop's project, session, SCM, and terminal components remain unchanged.

## Included in this foundation

- `backend/cmd/ao-cloud`: HTTP service with liveness, readiness, Google
  identity exchange, AO session refresh/logout, and current-account routes.
- `backend/cmd/ao-cloud-migrate`: migration-only command using a separate
  privileged database connection.
- `backend/internal/cloud/auth`: Google OpenID Connect verification, short-lived
  AO JWT access tokens, and opaque refresh tokens.
- `backend/internal/cloud/postgres`: users, hashed refresh sessions,
  organizations, memberships, and forced row-level security.
- `backend/internal/cloud/httpapi`: the implemented subset of the public Cloud
  contract.
- `backend/internal/cloud/runtime/daytona`: a coordinator sandbox per cloud
  project plus one isolated sandbox per orchestrator/worker session.
- `backend/internal/adapters/runtime/cloud`: the existing daemon runtime port
  implemented as authenticated control-plane calls. This is the seam that
  makes the ordinary AO lifecycle create remote compute without UI branching.

The existing daemon is unchanged. It remains loopback-only and continues to
own every local project and session.

## HTTP surface

| Route | Authentication | Purpose |
| --- | --- | --- |
| `GET /healthz` | none | Process liveness |
| `GET /readyz` | none | PostgreSQL readiness |
| `POST /api/cloud/v1/auth/google` | Google ID token in JSON | Verify Google identity and issue an AO session |
| `POST /api/cloud/v1/auth/refresh` | Rotating refresh token in JSON | Atomically consume and replace a refresh token |
| `POST /api/cloud/v1/auth/logout` | Refresh token in JSON | Revoke a refresh token |
| `GET /api/cloud/v1/me` | AO bearer access token | Return the current user and live organization memberships |
| `POST /api/cloud/v1/orgs/{orgId}/workspaces` | AO bearer access token | Record intent and asynchronously provision a Daytona AO workspace |
| `GET /api/cloud/v1/orgs/{orgId}/workspaces/{workspaceId}` | AO bearer access token | Poll lifecycle state and obtain a fresh signed AO URL when ready |

Coordinator-only `/api/cloud/internal/v1/workspaces/{workspaceId}/runtimes/*`
routes create, inspect, message, interrupt, and destroy session sandboxes. They
accept a workspace-scoped, 30-day capability with a distinct JWT audience;
neither desktop access tokens nor a capability for another project are valid.

Google establishes identity only. A verified Google hosted-domain claim never
creates, selects, or authorizes an AO organization. First sign-in atomically
creates the user, a personal organization, and its owner membership.

AO access tokens contain the AO user ID but no organization membership. Every
authenticated account read reloads memberships from PostgreSQL, so disabling a
membership takes effect without waiting for an access token to expire.

Refresh tokens are random opaque values. PostgreSQL stores only SHA-256
digests. Rotation deletes the old digest and inserts its replacement in one
transaction, so concurrent replay has at most one winner. A rotated token keeps
the refresh session's original creation time and absolute expiry;
`AO_CLOUD_REFRESH_TOKEN_TTL` therefore caps the entire session lifetime and is
never extended by refresh activity.

## PostgreSQL boundaries

Migration and runtime credentials are intentionally separate:

- `AO_CLOUD_MIGRATION_DATABASE_URL` belongs only to `ao-cloud-migrate` and
  owns schema changes.
- `AO_CLOUD_DATABASE_URL` belongs to `ao-cloud` and must use the restricted
  role named by `AO_CLOUD_RUNTIME_DATABASE_ROLE` during migration.
- Runtime startup rejects roles with `SUPERUSER` or `BYPASSRLS`.

When `AO_CLOUD_RUNTIME_DATABASE_PASSWORD` is set, the migration command creates
the restricted runtime login role if it does not exist. An existing role is
validated but never altered or elevated. It then applies the embedded Goose
migration and grants that role only the tables and tenant helper functions in
this foundation. The AWS deployment generates and stores the runtime password
outside Terraform state.

`ao_organizations` and `ao_org_memberships` have forced row-level security.
Tenant reads are authorized through the current AO user. Writes additionally
require an AO-selected organization transaction context, and administrative
updates require an active owner or admin membership.

## Configuration

The API requires:

```bash
export AO_CLOUD_DATABASE_URL='postgres://ao_runtime:...@127.0.0.1:5432/ao_cloud'
export AO_CLOUD_GOOGLE_CLIENT_IDS='desktop-oauth-client.apps.googleusercontent.com'
export AO_CLOUD_ALLOWED_EMAILS='maintainer@example.com'
export AO_CLOUD_ACCESS_TOKEN_KEY_BASE64="$(openssl rand -base64 32)"
```

Optional settings:

```bash
export AO_CLOUD_ADDR='127.0.0.1:8080'
export AO_CLOUD_ACCESS_TOKEN_ISSUER='ao-cloud'
export AO_CLOUD_ACCESS_TOKEN_AUDIENCE='ao-desktop'
export AO_CLOUD_ACCESS_TOKEN_TTL='15m'
export AO_CLOUD_REFRESH_TOKEN_TTL='720h'
```

Workspace provisioning additionally requires server-side secret injection:

```bash
export DAYTONA_API_KEY='...'
export DAYTONA_API_URL='https://app.daytona.io/api'
export DAYTONA_TARGET='us'
export AO_CLOUD_GITHUB_TOKEN_BASE64='...'
export AO_CLOUD_SANDBOX_AO_BINARY='/ao'
export AO_CLOUD_PUBLIC_URL='https://cloud.example.com'
```

The Electron main process performs Google installed-app PKCE on a temporary
loopback callback and encrypts AO access/refresh tokens with Electron
`safeStorage`. The renderer receives account metadata, never bearer tokens. A
signed Daytona preview URL is held in memory and rebases the existing AO REST,
SSE, and `/mux` transports, so the local and cloud paths render the same React
components. At cloud-project creation, Electron main reads the current Claude
Code credential from the macOS Keychain (or Claude's credential file on other
platforms), sends it only over the authenticated TLS request, and the control
plane passes it in memory to Daytona. It is written directly into that user's
sandbox and is never exposed to the renderer, stored in Postgres, logged, or
kept as a shared deployment secret. The coordinator receives a scoped runtime
capability, not Daytona credentials. On every AO runtime `Create`, it sends the
prepared worktree overlay and launch specification to the control plane. The
control plane clones the repository into fresh compute, overlays that exact
worktree, installs the per-user Claude credential, and starts the agent under a
tmux-backed terminal. Orchestrator and worker launches use the identical path.

Run migrations and start the service:

```bash
export AO_CLOUD_MIGRATION_DATABASE_URL='postgres://migration_owner:...@127.0.0.1:5432/ao_cloud'
export AO_CLOUD_RUNTIME_DATABASE_ROLE='ao_cloud_runtime'
export AO_CLOUD_RUNTIME_DATABASE_PASSWORD='generate-and-store-this-securely'
cd backend
go run ./cmd/ao-cloud-migrate
go run ./cmd/ao-cloud
```

The service intentionally speaks plain HTTP inside its task network. The AWS
staging deployment terminates public TLS at API Gateway and reaches the service
through a VPC link and internal load balancer. See
[`deploy/cloud/README.md`](../deploy/cloud/README.md).

## POC limits

- Provisioning is asynchronous but not yet recovered by a durable reconciler
  after a control-plane process restart.
- GitHub remains a minimum-scope shared staging credential; Claude credentials
  are per-user and are never persisted by the control plane.
- Terminal output currently uses bounded polling through API Gateway. A
  production terminal relay with resize, backpressure, and replay cursors is a
  follow-up; this does not change the frontend terminal contract.
- Prepared worktrees are capped at 24 MiB compressed for this POC. Production
  will move overlays and caches to object storage/prebuilt images.
- Workspace capabilities expire after 30 days; background renewal is deferred.
- The signed coordinator URL injected into each isolated agent sandbox expires
  after 24 hours and is not yet renewed in place. Long-running orchestrators
  lose `ao` CLI connectivity until their session is relaunched.
- A signed URL is refreshed when the desktop polls the workspace, but automatic
  renewal for an already-connected window remains.
- Stop, pause, resume, archive, delete, quotas, billing, and idle reaping remain.

Those production lifecycle pieces should remain separately reviewable follow-ups.
