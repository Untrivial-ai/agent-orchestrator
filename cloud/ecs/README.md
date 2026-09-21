# ECS-on-EC2 (Graviton) sandbox provider

A self-hosted sandbox provider that runs each cloud session's worker as an ECS
task on a Graviton (arm64) EC2 fleet. It exists to dogfood sandboxing internally
on our AWS credits instead of paying NodeOps, while keeping container-level
isolation and fast starts.

## What it is (and is not)

- **Container isolation, not microVM.** Each session is one ECS task (one
  container) on a shared EC2 host. This is the isolation NodeOps gives via
  Firecracker microVMs traded for far lower operational effort. Good enough for
  internal dogfooding; not the boundary you would put fully untrusted multi-tenant
  code behind.
- **No idle-pause.** A task runs until it is terminated. There is no resume: an
  ECS task cannot be restarted once stopped, and the container filesystem does not
  survive. Users terminate their own sessions when done. The idle-pause scanner
  explicitly skips ECS sandboxes (`RunningSandboxSessions` filters `provider <>
  'ecs'`).
- **Ephemeral workspace.** No EFS, no durable volume. If a task's worker crashes,
  repair re-provisions a fresh task (a fresh clone), it does not resume.

## How it maps onto the provider interface

The provider (`cloud/internal/sandbox/ecs`) mirrors the **docker** provider, not
NodeOps/Coder: the worker binary is baked into the task image as its entrypoint,
so the container's main process is the worker. It implements `Provider` and
`Recreator`, not `Bootstrapper` (there is no exec/upload step).

| Interface method | ECS behavior |
| --- | --- |
| `Create` | `RunTask` with the per-session bootstrap env as a container override, tags for ownership, `startedBy = sessionID` for correlation |
| `Get` | `DescribeTasks` (with TAGS), ownership-checked; task `lastStatus` mapped to AO states; a MISSING task is `ErrNotFound` |
| `FindBySession` | `ListTasks` filtered by `startedBy` + desiredStatus RUNNING, then `DescribeTasks`; adopts a running task after a control-plane crash |
| `Start` | readiness check; a running task is a no-op, a non-running task errors so the reconciler recreates it |
| `Stop` / `Pause` | `StopTask` (terminal and destructive; there is no idle-pause) |
| `Resume` | alias of `Start` |
| `Delete` | `StopTask`, idempotent on a missing task |
| `Recreate` | `StopTask` the old task, `RunTask` a new one with a fresh single-use ticket |

State mapping: `RUNNING → running`; `PROVISIONING/PENDING/ACTIVATING →
provisioning`; `STOPPING/DEACTIVATING/DEPROVISIONING → deleting`; `STOPPED →
deleted` (a stopped task is terminal in this model, so the reconciler completes a
deletion promptly and re-provisions fresh on a resume instead of attempting an
impossible restart); anything unrecognized → `provisioning`.

Networking is **bridge**: the worker only makes outbound calls to the control
plane, GitHub and npm, so no per-task ENI is needed. That is what lets a single
host pack many sessions (bridge networking has no ENI-per-task ceiling).

## The cross-architecture rule (important)

Sandboxes run on Graviton (**arm64**). The control plane and the worker binary it
serves for self-update are **amd64**. A cross-architecture self-update would
replace the baked arm64 worker with an amd64 one and break the box. Therefore:

- The reconciler never advertises `AO_WORKER_EXPECTED_SHA256` /
  `AO_WORKER_HELPER_EXPECTED_SHA256` to an ECS session
  (`reconcile/reconciler.go`).
- The baked arm64 worker is authoritative and is **rebuilt from the same commit
  on every release** (`build-ecs-sandbox-image.sh`, run automatically by
  `deploy-staging.sh` when `AO_CLOUD_ECS_ENABLED=1`), the same way the coder image
  is rebaked.
- `verify-ecs-sandbox-image.sh` checks arch/entrypoint/agents but does **not**
  compare bytes against the amd64 control-plane image.

## Configuration (control plane)

All non-secret; the provider authenticates to AWS with the control-plane task
role, so there are no provider secrets.

| Env | Meaning |
| --- | --- |
| `AO_CLOUD_SANDBOX_PROVIDERS` | include `ecs` (e.g. `coder,ecs`) |
| `AO_CLOUD_ECS_REGION` | region of the sandbox cluster |
| `AO_CLOUD_ECS_CLUSTER` | ECS cluster name or ARN |
| `AO_CLOUD_ECS_TASK_DEFINITION` | task-definition family (RunTask uses the latest active revision) |
| `AO_CLOUD_ECS_CONTAINER_NAME` | container to inject the bootstrap env into (default `worker`) |
| `AO_CLOUD_ECS_CAPACITY_PROVIDER` | capacity provider name; empty falls back to the EC2 launch type |
| `AO_CLOUD_ECS_NAMESPACE` | ownership-tag scope (default `ao-cloud`) |
| `AO_CLOUD_ECS_WORKER_TOKEN_TTL` | bootstrap token TTL (default 15m) |

The control-plane task role needs `ecs:RunTask`, `ecs:StopTask`,
`ecs:DescribeTasks`, `ecs:ListTasks`, `ecs:TagResource` on the sandbox cluster and
`iam:PassRole` for the two sandbox roles. `provision-ecs-sandbox.sh` attaches
exactly this.

## Bring it up

```sh
# 1. One-time infra (idempotent). Provide your VPC specifics and the CP task role.
AWS_PROFILE=ao-cloud \
AO_CLOUD_ECS_SUBNETS=subnet-aaa,subnet-bbb \
AO_CLOUD_ECS_VPC_ID=vpc-xxxx \
AO_CLOUD_CP_TASK_ROLE_NAME=ao-cloud-staging-task \
  ./scripts/provision-ecs-sandbox.sh

# 2. Build + register the arm64 worker image (also run automatically by deploy).
AWS_PROFILE=ao-cloud ./scripts/build-ecs-sandbox-image.sh

# 3. Deploy the control plane with ECS enabled as an extra provider.
AWS_PROFILE=ao-cloud AO_CLOUD_ECS_ENABLED=1 ./scripts/deploy-staging.sh
```

`AO_CLOUD_SANDBOX_PROVIDER` stays `coder` (or `nodeops`); ECS is added on top via
`AO_CLOUD_SANDBOX_PROVIDERS`. 11_x sessions that do not need this isolation keep
running on `coder` (EC2 workspaces).

## Scaling and cost

- Instances are Graviton memory-optimized (`r7g.xlarge` by default); the ECS
  managed-scaling capacity provider grows/shrinks the ASG to the target capacity.
- Density comes from bridge networking (no ENI ceiling) and packing many tasks per
  host. Right-size `AO_CLOUD_ECS_INSTANCE_TYPE`, `AO_CLOUD_ECS_ASG_MAX` and
  `AO_CLOUD_ECS_TARGET_CAPACITY` to your session load. `ASG_MIN=0` lets the fleet
  scale to zero when idle.
- Because there is no idle-pause, remind users to terminate sessions; an abandoned
  session holds a task (and its share of a host) until terminated.

## Known limitations

- No workspace persistence across a task's life; a crash re-provisions fresh.
- A crash-looping image re-provisions until the session is terminated (bounded
  only by manual termination); the arm64 image is validated at build time to keep
  this from happening in practice.
- Container isolation only; not for fully untrusted multi-tenant workloads.
- Disabling ECS later means clearing the injected `AO_CLOUD_SANDBOX_PROVIDERS` /
  `AO_CLOUD_ECS_*` env from the control-plane task (they persist across deploys).
