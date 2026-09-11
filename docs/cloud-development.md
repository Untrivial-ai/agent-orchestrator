# Cloud control-plane development

The Cloud control plane is vendored in this repository under [`cloud/`](../cloud/).
It is the source used by staging and production deployments; the historical
private `ao-cloud` checkout and submodule flow are no longer part of the
architecture.

## Local development

From the repository root, run:

```bash
npm run cloud:local
```

This starts the local Compose control plane, PostgreSQL, worker image, and the
Cloud web UI. The local API listens on `http://127.0.0.1:8081`; local Cloud
sessions and credentials are separate from hosted staging sessions.

To run the local lifecycle smoke test:

```bash
npm run cloud:local:smoke
```

Use `npm run cloud:local:down` to stop the stack while retaining data, or
`npm run cloud:local:reset` to remove its database.

## Staging

Staging is deployed from `cloud/`, not from another checkout:

```bash
cloud/scripts/deploy-staging.sh
```

The deploy requires the configured AO Cloud AWS profile and Docker. Verify the
staging readiness endpoint after deployment before testing the Electron client.

## Contracts and shared packages

The public Cloud API contract is [`contracts/cloud/openapi.yaml`](../contracts/cloud/openapi.yaml).
After changing it, regenerate the shared TypeScript client:

```bash
npm --prefix packages/cloud-client run generate
npm --prefix packages/cloud-client run typecheck
```

Cloud API handlers, persistence, migrations, worker transport, and reconciliation
code belong in `cloud/`. Do not add new integration pointers to the retired
private repository.
