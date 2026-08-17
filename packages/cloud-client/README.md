# `@aoagents/cloud-client`

Runtime-neutral TypeScript contracts and a small fetch-based client for AO
Cloud's public API. The package defines the client boundary; this repository
does not implement the Cloud routes.

```ts
import { createCloudClient } from "@aoagents/cloud-client";

const cloud = createCloudClient({
  baseUrl: "https://cloud.example.com",
  getAccessToken: () => authSession.getAccessToken(),
  fetch,
});

const sessions = await cloud.listSessions(orgId, { limit: 50 });
```

The caller owns authentication and token refresh. `createCloudClient` asks for
an access token immediately before a user request. `createWorkerClient` does the
same for every authenticated worker request. It also exposes the unauthenticated
one-time bootstrap exchange:

```ts
import { createWorkerClient } from "@aoagents/cloud-client";

let workerToken: string | null = null;
const worker = createWorkerClient({
  baseUrl: "https://cloud.example.com",
  getWorkerToken: () => workerToken,
  fetch,
});

const bootstrap = await worker.bootstrap({
  bootstrapToken: oneTimeTicket,
  version: workerVersion,
  capabilities,
});
workerToken = bootstrap.workerToken;

const heartbeat = await worker.heartbeat({ version: workerVersion, capabilities });
workerToken = heartbeat.workerToken;
```

Keep bootstrap, worker, agent-credential, and checkout-grant secrets only in
memory and never log them. Secret-bearing client requests use `cache:
"no-store"`; the credential and checkout-grant responses also require the
server's `Cache-Control: no-store`.

The source contract is `contracts/cloud/openapi.yaml`. Run `npm run generate`
from this directory after changing it. The generated `src/schema.ts` file is
committed so consumers do not need an OpenAPI toolchain.

The worker client matches the control plane's bootstrap, heartbeat, event,
fenced turn, credential, checkout-grant, child orchestration, workspace
transport, and terminal transport routes. It intentionally excludes worker
provisioning, database details, secret storage, and local daemon routes.
