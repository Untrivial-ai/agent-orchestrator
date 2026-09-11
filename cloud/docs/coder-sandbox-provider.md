# Coder sandbox provider

AO Cloud can use a customer-operated Coder deployment as its sandbox compute
provider. AO creates one Coder workspace per AO session, installs `ao-worker`
in the approved template image, launches it through the workspace's existing
`coder_agent`, and keeps the rest of the session lifecycle behind the same
provider-neutral reconciler used by NodeOps. Older templates remain compatible:
AO verifies the baked binaries and falls back to the PTY upload on a hash miss.

The first implementation is deployment-scoped: every AO organization on that
control plane uses the same Coder connection and approved template. It does not
yet decrypt a separate Coder connection per AO organization.

## Customer access required

Use a dedicated, ordinary Coder user such as `ao-integration`; do not grant it a
site-admin role. In Coder Community Edition, that user and its API token must be
able to:

- read the approved template and its active version;
- create workspaces owned by itself from that template;
- read, start, stop, and delete the workspaces it owns; and
- connect to the `coder_agent` terminal for those workspaces.

AO calls these Coder API surfaces:

| Operation | Coder API |
| --- | --- |
| Create | `POST /api/v2/users/{owner}/workspaces` |
| Inspect | `GET /api/v2/workspaces/{id}` |
| Recover by AO session | `GET /api/v2/users/{owner}/workspace/{name}` |
| Start, stop, delete | `POST /api/v2/workspaces/{id}/builds` |
| Launch or repair worker | `GET /api/v2/workspaceagents/{agent}/pty` (WebSocket) |

For a quick Community Edition pilot, an API token with `coder:all` is bounded by
the permissions of this ordinary user and is the simplest setup. On Coder
versions that support composite API-token scopes, the narrower set AO needs is
`coder:workspaces.create`, `coder:workspaces.operate`,
`coder:workspaces.delete`, and `coder:workspaces.access`.

The service plane needs HTTPS and WebSocket connectivity to the Coder URL.
Workspaces need outbound HTTPS connectivity to `AO_CLOUD_PUBLIC_URL`; neither
Coder nor the workspace needs inbound connectivity from the AO desktop app.

## Template contract

The approved template must create a Linux workspace with at least one
`coder_agent` and one persistent volume mount dedicated to workspace state.
Configure that exact mount point with `AO_CLOUD_CODER_DURABLE_ROOT`; it is not
assumed to be `/home/coder`. The root must be an absolute, normalized, non-root
path, must exist as a non-symlink mount point, and must survive Coder `stop` then
`start`. The template must include the `mountpoint` utility so bootstrap can
verify the contract before writing state.

AO derives every stateful path beneath that root:

| State | Path relative to the durable root |
| --- | --- |
| Repository and uncommitted files | `repository` |
| AO worker token, Git helper, sockets, and logs | `.ao/worker` |
| Worker `HOME` | `.ao/home` |
| Claude Code configuration and conversations | `.ao/home/.claude` |
| Codex configuration and conversations | `.ao/home/.codex` |
| AO/Coder restore identity | `.ao/durable-session-id` |

The owner, template ID, template parameters, selected agent, and durable root
are stamped into each session's provider plan. Reconciliation binds the current
deployment-scoped Coder connection credential to that stored profile, so a
later configuration change does not move or recreate existing sessions with
new defaults. Coder workspace responses must match the planned owner,
deterministic workspace name, and template ID before AO adopts the workspace,
accepts a provider ID, or uploads worker credentials. Configure
`AO_CLOUD_CODER_OWNER` as the canonical owner name returned by Coder, not an
alias such as `me`.

A first bootstrap writes a session identity marker; a bootstrap after
stop/start must find the same marker or it fails instead of silently launching
a fresh agent against an empty filesystem.

For normal startup latency, bake `/usr/local/bin/ao-worker` and
`/usr/local/bin/ao` from the exact control-plane image into the approved
workspace image. The reference Dockerfile and Terraform template live under
`cloud/coder/`. At launch AO compares SHA-256 hashes for both executables with
the binaries embedded in its own release. A match transfers only the small,
one-time launch environment; a missing or stale binary uses the full PTY upload
so template and control-plane rollouts do not have to be perfectly atomic.

Because the worker runs as a separate OS user, bootstrap preserves the durable
root's existing mode bits while adding traversal-only (`o+x`) access to that
mount point. It does not make the root listable or readable. Only the derived
`repository` and `.ao` subdirectories are assigned to `ao-worker`.

The normal workspace user must have passwordless `sudo` for the pilot
bootstrap. AO uses it to:

- create the unprivileged `ao-worker` OS user;
- install the release-pinned binaries only when the template hash does not
  match the active control-plane release;
- create and assign only the derived repository and `.ao` directories to the
  worker user; and
- launch the worker, which then dials the AO service plane and sends its own
  heartbeats.

The template is also responsible for the tools an AO task uses: `git`, CA
certificates, and the selected coding-agent harness CLI (for example Claude
Code or Codex). Those tools are baked into AO's NodeOps image today; the Coder
provider deliberately does not mutate a customer's template by installing
third-party harnesses at session startup.

No worker credential is placed in the PTY URL or command. Even on the baked
fast path, AO streams the private launch environment over terminal input,
writes it with mode `0600`, and deletes that environment file when the worker
starts.

For a production rollout, prefer baking the OS user and directories into the
template or a narrowly scoped install helper over unrestricted passwordless
`sudo`. The provider can then be tightened to that contract without changing
the lifecycle API.

## Configuration

Set:

```text
AO_CLOUD_SANDBOX_PROVIDER=coder
AO_CLOUD_CODER_URL=https://coder.customer.example
AO_CLOUD_CODER_TOKEN=<dedicated-user-api-token>
AO_CLOUD_CODER_OWNER=ao-integration
AO_CLOUD_CODER_TEMPLATE_ID=<approved-template-uuid>
AO_CLOUD_CODER_AGENT_NAME=<optional-agent-name>
AO_CLOUD_CODER_PARAMETERS_JSON={"instance_type":"t3.medium","region":"us-west-2"}
AO_CLOUD_CODER_DURABLE_ROOT=<template-persistent-volume-mount>
AO_CLOUD_CODER_WORKER_TOKEN_TTL=15m
```

The ordinary AO worker settings remain required:

```text
AO_CLOUD_PUBLIC_URL=https://api.example.com
AO_CLOUD_WORKER_SIGNING_KEY=<at-least-32-characters>
AO_CLOUD_WORKER_BINARY_PATH=/opt/ao/bin/ao-worker
AO_CLOUD_WORKER_HELPER_BINARY_PATH=/opt/ao/bin/ao
```

Keep `AO_CLOUD_CODER_TOKEN` in the service plane's secret manager. Do not put it
in the desktop app, a workspace environment variable, or AO's provider plan.

For the AWS ECS deployment scripts, store those values in the environment's
Secrets Manager JSON document (`ao-cloud/staging/coder` or
`ao-cloud/production/coder`) using these keys:

```json
{
  "url": "https://coder.customer.example",
  "token": "<dedicated-user-api-token>",
  "owner": "ao-integration",
  "template_id": "<approved-template-uuid>",
  "agent_name": "",
  "parameters_json": "{}",
  "durable_root": "/template-specific/persistent-mount",
  "worker_token_ttl": "15m"
}
```

Before enabling idle pause for a Coder template, run the opt-in lifecycle test
against that exact template and root. It creates a disposable workspace, writes
the durable session marker plus representative repository, Claude, and Codex
state, stops and starts the workspace, requires the original marker during
restore bootstrap, and deletes the workspace:

```bash
cd cloud
CODER_LIVE_URL=https://coder.customer.example \
CODER_LIVE_TOKEN=... \
CODER_LIVE_OWNER=ao-integration \
CODER_LIVE_TEMPLATE_ID=... \
CODER_LIVE_DURABLE_ROOT=/template-specific/persistent-mount \
go test ./internal/sandbox/coder -run TestLiveLifecycle -count=1 -v
```

Set `CODER_LIVE_AGENT_NAME` and `CODER_LIVE_PARAMETERS_JSON` when the approved
template requires them. Do not treat a successful workspace start alone as a
persistence check; the restore bootstrap is the gate.

Grant the environment's ECS execution role `secretsmanager:GetSecretValue` on
that secret, then deploy staging with `AO_CLOUD_SANDBOX_PROVIDER=coder`. The
task-definition renderer removes stale NodeOps values when switching providers.
