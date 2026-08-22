# Cloud development

AO desktop and AO Cloud now evolve in one repository. The local daemon remains
a complete product and does not depend on hosted services. Cloud code lives
under `backend/internal/cloud`, with shared client contracts and presentation
packages remaining transport-independent.

## Current implementation

The first hosted slice contains only:

- a PostgreSQL user, refresh-session, organization, and membership schema;
- forced row-level security for tenant data;
- Google OpenID Connect identity verification;
- short-lived AO access tokens and rotating opaque refresh tokens;
- `/healthz`, `/readyz`, Cloud auth, and current-account HTTP routes; and
- migration and API binaries.

See [cloud-control-plane.md](cloud-control-plane.md) for configuration and the
exact trust boundaries.

The broader OpenAPI document remains the staged contract for projects,
sessions, workers, SCM, terminal streaming, and workspace access. A route in
that contract is not evidence that its hosted handler exists; the foundation
document lists the implemented routes.

## Verification

From the repository root:

```bash
cd backend
go test ./internal/cloud/... ./cmd/ao-cloud ./cmd/ao-cloud-migrate
go build ./...
go vet ./...

cd ..
npm run cloud:check
```

PostgreSQL acceptance requires an isolated database plus separate migration and
runtime roles. Do not point migration tests at a shared environment.

## Implementation order

1. Land this PostgreSQL and authentication foundation.
2. Add AWS infrastructure and a staging deployment for only this service.
3. Add project placement and the Electron-main Google token broker.
4. Add Daytona provisioning and the outbound worker lifecycle.
5. Add durable session events and WAN terminal/workspace transport.
6. Add GitHub App installation, SCM synchronization, and worker checkout grants.

Each stage should remain independently reviewable. The renderer must continue
to consume shared view models and injected actions rather than selecting local
or Cloud transports inside components.
