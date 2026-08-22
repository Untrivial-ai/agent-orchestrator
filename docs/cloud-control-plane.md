# Cloud control-plane foundation

This repository now contains the first hosted AO control-plane slice. It is a
small, independently runnable Go service built around PostgreSQL and Google
identity. It does not provision sandboxes or implement projects and sessions.

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

The migration command expects the runtime login role to already exist. It
applies the embedded Goose migration and grants that role only the tables and
tenant helper functions in this foundation. It does not create or rotate
database passwords; deployment automation will own that later.

`ao_organizations` and `ao_org_memberships` have forced row-level security.
Tenant reads are authorized through the current AO user. Writes additionally
require an AO-selected organization transaction context, and administrative
updates require an active owner or admin membership.

## Configuration

The API requires:

```bash
export AO_CLOUD_DATABASE_URL='postgres://ao_runtime:...@127.0.0.1:5432/ao_cloud'
export AO_CLOUD_GOOGLE_CLIENT_IDS='desktop-oauth-client.apps.googleusercontent.com'
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

Run migrations and start the service:

```bash
export AO_CLOUD_MIGRATION_DATABASE_URL='postgres://migration_owner:...@127.0.0.1:5432/ao_cloud'
export AO_CLOUD_RUNTIME_DATABASE_ROLE='ao_cloud_runtime'
cd backend
go run ./cmd/ao-cloud-migrate
go run ./cmd/ao-cloud
```

The service intentionally speaks plain HTTP at this stage. A later deployment
PR will place it behind managed TLS; do not expose this listener directly.

## Explicitly not in this PR

- AWS infrastructure or deployment scripts
- Daytona or any sandbox provider
- project, session, lifecycle, worker, event, terminal, or workspace handlers
- GitHub App, SCM synchronization, or credential brokering
- Electron Google PKCE and protected refresh-token custody
- Cloud/local project placement and shared UI wiring

Those pieces should land as separately reviewable follow-ups on top of this
foundation.
