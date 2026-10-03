# Cloud development

Cloud's Go control plane, worker, Docker development stack, deployment scripts,
and tests now live in this public repository under `cloud/`. The desktop Cloud
client lives in `frontend/`; its generated contract and shared presentation
packages live in `contracts/cloud/`, `packages/cloud-client/`, and
`packages/product-ui/`. The optional `private/ao-cloud` submodule is for a
separate web app. It is not required to build or test the desktop or control
plane. For end-user setup, see the [Cloud sessions guide](../frontend/src/docs/content/guides/cloud.mdx).

## Run the local stack

From the repository root, install workspace dependencies and start the Docker
stack:

```bash
npm ci
npm run cloud:local
```

This starts PostgreSQL, the Cloud API on `http://127.0.0.1:8081`, and Docker
worker support with local email/password authentication. From `frontend/`,
launch the desktop against it:

```bash
AO_CLOUD_OFFERING=on AO_CLOUD_CONTROL_PLANE_URL=http://127.0.0.1:8081 npm run dev
```

Create a development account in the app. Local Cloud auth and hosted WorkOS
auth are separate; do not use production credentials in the local stack.
`npm run cloud:local:down` stops containers and keeps data;
`npm run cloud:local:reset` also removes the local Cloud database. The local
smoke test uses isolated resources: `npm run cloud:local:smoke`. All four
commands run from the repository root and require Docker/Compose.

## Check changes

Run `npm run cloud:check` at the root for the generated Cloud client contract,
typecheck, tests, and build. Run `cd cloud && go test ./...` for the control
plane. For desktop integration, run `npm run frontend:typecheck` at the root
and the frontend build/tests prescribed by the affected workflow. See
[`cloud/README.md`](../cloud/README.md) and
[`cloud/docs/control-plane.md`](../cloud/docs/control-plane.md) for the current
environment and durable-state model. Do not infer a hosted capability from a
shared DTO alone; check the handler, renderer gate, and selected environment.

## Hosted environments

`npm run cloud:staging` runs an isolated desktop against staging and requires
the staging control plane to pass its readiness preflight. Production uses a
different database, secrets, and GitHub App authority. The deployment scripts
in `cloud/scripts/` are release operations, not development checks. The
[deployment runbook](../cloud/docs/deployment.md) describes immutable image
builds, migrations, staging verification, production promotion, and rollback.
Only the designated release operator should run those scripts.

The optional web app in `private/ao-cloud` consumes the public packages. An
authorized developer may initialize that submodule separately; a normal public
clone and CI do not need its contents. The original split design is retained
as [historical architecture context](cloud-refactor.md), not as the current
implementation plan.
