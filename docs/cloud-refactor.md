# Cloud-shared refactor

This refactor makes AO's stable product language, client contracts, and presentation
layer reusable by the future private `ao-cloud` repository. Public AO remains a
complete local product; no hosted control-plane, database, provisioner, or worker
implementation belongs here.

For the optional private checkout workflow and recommended implementation
sequence, see [cloud-development.md](cloud-development.md).

## Public boundaries

| Boundary | Owns | Does not own |
| --- | --- | --- |
| `backend/pkg/contract` | Session/PR facts, stack positions, and pure status derivation | Local durable records, stores, runtime ports, provider payloads |
| `backend/pkg/agentruntime` | Claude Code, Codex, and Cursor command/restore policy plus process-group lifecycle | Worker orchestration, credentials, leases, persistence, provider installation |
| `contracts/cloud` | Authenticated, organization-scoped HTTP contract and client-visible event schemas | Route implementation, authorization policy, persistence |
| `packages/cloud-client` | Handwritten typed Cloud client over generated OpenAPI schema types, auth injection, errors, pagination, replay cursors | Refresh-token storage, Electron, React, worker RPC |
| `packages/product-ui` | Semantic view models, pure presentation logic, and portable board/composer/inspector React views | Electron bridges, loopback API calls, native BrowserView, daemon lifecycle |
| `frontend/src/renderer` | Desktop controllers and adapters for the local daemon and Electron | Cloud control-plane implementation |

The dependency direction is one way:

```text
local daemon DTOs ──mapper──┐
                            ├── product-ui models and views
Cloud API DTOs ────mapper───┘

desktop renderer ──uses── product-ui
future ao-cloud ───uses── product-ui + cloud-client + Go contracts
```

Shared views receive data and actions from their host. They do not select a
transport internally. The local daemon client never switches into a Cloud client.

## Shared readiness

“Shared-ready” means the public contract, client type, rule, or view exists now.
It does not imply that the private Cloud control plane already implements the
route or supplies the host data.

| Area | Public/shared now | Still owned by the future Cloud implementation |
| --- | --- | --- |
| Status and stacks | Go facts, deterministic derivation, PR stack rules | Mapping hosted observations into the shared facts |
| Agents | Identity, capability vocabulary, installation/auth/org availability, Cloud list contract | Runtime probing, image availability, org policy, provider authentication |
| Projects | Cloud DTOs/client plus controlled setup/settings sections and validation used by local AO | Private Cloud implements authorized persistence and GitHub project import; update/archive flows and Cloud-specific list/import UI remain |
| Sessions and chat | Session/message/event DTOs, replay rules, reconnecting client, board/composer/inspector views | Private Cloud implements durable creation, messages, replay, and replica-safe SSE; reconciliation, worker event ingestion, and execution remain |
| PRs and reviews | Raw PR/CI/review/mergeability/AO-review models, read routes, client methods, reusable inspector presentation | GitHub observation, stale-head enforcement, review execution and storage |
| Workspace and terminal | File/diff shapes, workspace requests, terminal-ticket and WebSocket contracts | Sandbox RPC, ticket issuance, filesystem confinement and terminal transport |
| Authentication | WorkOS desktop token custody, token-free account projection, and account/membership client contract | Private Cloud implements hosted token validation and organization authorization; the Cloud organization-picker UI remains |

Project/workspace database and service structs are intentionally not shared Go
types. Public clients exchange OpenAPI DTOs; each backend maps its private domain
model to those DTOs.

## File map

- `backend/pkg/contract`: Go facts, agent/SCM vocabulary, and deterministic
  status/stack rules.
- `backend/pkg/agentruntime`: Linux-worker-facing command construction, native
  restore identity, approval-policy mapping, and child process lifecycle.
- `contracts/cloud/openapi.yaml`: source of truth for the hosted client API.
- `packages/cloud-client/src/schema.ts`: generated OpenAPI types.
- `packages/cloud-client/src/client.ts`: fetch, bearer auth, pagination, SSE, and
  terminal-ticket helpers.
- `packages/product-ui/src`: portable models, formatting, session/SCM views, and
  controlled project setup/settings presentations used by local AO.
- `frontend/src/renderer/components/SessionsBoardAdapters.tsx` and the desktop
  controller components: local-daemon data/action adapters for shared views.
- `frontend/src/main/cloud-auth.ts` and `frontend/src/shared/cloud-account.ts`:
  WorkOS token custody and the token-free renderer projection.

## Behavior that must not regress

| Area | Required behavior | Primary coverage |
| --- | --- | --- |
| Projects | Register/import/archive projects and workspaces; validate config; preserve scratch and multi-repo behavior | project service, controllers, CLI, renderer project tests |
| Sessions | Spawn/restore/rename/pin/kill; one controller epoch; TUI/Chat handoff and restart recovery | session manager, lifecycle, session service and renderer tests |
| Status | Derive from facts; activity precedence; Chat signal exemption; multi-PR worst-wins and stack suppression | contract and session status/stack tests |
| Agents | Stable identity/capabilities; installation, auth, model, and mode availability remain host-specific | agent catalog, adapters, controller and composer tests |
| Chat/events | Ordered turns/messages/activities; idempotent sends; replay and targeted invalidation | chat service, conversation controllers, CDC and chat UI tests |
| Workspace | Worktree safety; merge-base comparison; rename/delete/binary/untracked files; confinement and truncation | workspace adapters, session workspace and files UI tests |
| Terminal | Attach/replay/input/resize/reconnect; TUI controller and session shell remain distinct | terminal mux, runtime and xterm tests |
| SCM/reviews | Multi-PR summaries; current-head checks; reviews/comments; mergeability and stale-head guards | SCM observer, PR/review services, inspector tests |
| Daemon/CLI | Loopback listener remains unauthenticated; CLI stays a thin HTTP client; errors retain codes/request IDs | HTTP, API spec, CLI and daemon tests |
| Desktop UI | Board, sidebar, inspector, composer, chat, files, terminal and native shell retain current appearance and behavior | renderer Vitest and Playwright suites |
| WorkOS | Tokens remain in Electron main; renderer receives account identity only; sign-in never gates local AO | Cloud auth, preload, hook and sidebar tests |

## Import rules

- Public Go contract packages cannot import `backend/internal/*`.
- Cloud project, workspace, persistence, and authorization models stay private.
  Hosted handlers map those models to the public OpenAPI DTOs instead of sharing
  database or service-layer structs with the local daemon.
- `product-ui` cannot import Electron, `window.ao`, renderer stores, local generated
  API types, or either local/Cloud API client.
- `cloud-client` accepts `baseUrl`, `fetch`, and access-token providers from its host.
  It never stores or refreshes refresh tokens.
- Local SQLite CDC events and Cloud transcript/control events remain separate.
- Agent identity metadata is shared; installed/authenticated/organization-allowed
  availability is supplied by the host.
- Browser, clipboard, notifications, file staging, terminal transport, and external
  navigation are injected capabilities.

## Explicitly deferred/private

- PostgreSQL schemas, migrations, RLS, and tenant persistence.
- Hosted route handlers, WorkOS token validation, organization authorization.
- Reconciliation, queues, leases, retries, provisioning, warm pools, and images.
- Worker provisioning, bootstrap, heartbeats, terminal transport, and
  workspace RPC implementation.
- GitHub secrets/webhooks/token brokering, sharing policy implementation, billing,
  infrastructure, deployments, observability, and backups.
- Release-pairing/version-policy machinery beyond stable contract shapes.

## Verification

Run focused tests while moving each boundary, then finish with:

```bash
npm run lint
npm run shared:check
npm run frontend:typecheck
cd frontend && npm run typecheck:e2e && npm test && npm run test:e2e
cd ../backend && go build ./... && go vet ./... && go test -race ./...
```

When an API artifact changes, regenerate it from its source and verify no drift.
Frontend changes must also be rendered through `ao preview` before handoff.

Final review checks the branch diff against `feat/auth` for unnecessary wrappers,
comments, defensive clutter, casts, duplicate types, dead code, and private concerns
leaking into public packages.
