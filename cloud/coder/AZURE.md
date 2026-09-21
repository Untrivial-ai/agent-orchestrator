# Coder flow on Azure

Run cloud sessions with `provider=coder` on **Azure** while the AO control plane
and its database stay on **AWS**. Motivation: dogfood internally and demo on
Azure credits without moving the control plane or data. All AO application code
is unchanged; the work is Azure infrastructure plus a one-line CP config repoint.

## Why this needs almost no code

The AO coder provider (`cloud/internal/sandbox/coder/`) talks to a **Coder API
endpoint** (`AO_CLOUD_CODER_URL`), not to any cloud. And `cloud/coder/main.tf`
is a **Docker** template: it provisions a workspace **container** via a Docker
socket, with the agent reaching Coder over `host.docker.internal`. So it already
runs on any Docker host. "Coder on Azure" is therefore **infra only**: stand up
Coder + Docker on an Azure VM and point `AO_CLOUD_CODER_URL` at it.

## Architecture (single VM, TLS-terminated)

```
App -> CP (AWS eu-north-1) --HTTPS--> Caddy (Azure VM) --> Coder (:3000, unpublished)
          |                                                    |
          |                        Terraform docker provider -> workspace CONTAINER
          v                          (ao-coder-workspace:local, baked ao-worker, mem-capped)
        RDS (AWS, same region as CP)      |
          ^                               |
          +--------- HTTPS (worker token, terminal) <-- AO worker dials the CP's public URL
```

One Azure VM runs, all as `--restart unless-stopped` containers (survive
reboots): **Caddy** (auto Let's Encrypt TLS) in front of **Coder**, plus Coder's
**Postgres**. The Coder provisioner uses the VM's own Docker socket, so each
workspace is a memory-capped container on that VM. The **database is untouched**
- workspaces never talk to it; only the CP does, and CP↔RDS stays intra-region
on AWS.

**Region:** provision in **Sweden Central** (next to AWS `eu-north-1`) so the
workspace↔CP terminal relay is intra-Europe (~10-30ms). Farther regions add
noticeable terminal lag because the terminal relays through the CP.

## Provision

```bash
cd cloud
# 1. VM + Docker + Caddy(TLS) + Coder + Postgres + admin + long-lived API token
AO_AZURE_LOCATION=swedencentral ./scripts/provision-coder-azure.sh
#    -> prints CODER_URL (https://<fqdn>); stores PGPW/CODER_ADMIN_PW/CODER_TOKEN on the VM

# 2. build the workspace image (bakes the release-matched worker) + publish the
#    template with per-workspace limits
AO_CLOUD_CP_IMAGE=<acct>.dkr.ecr.eu-north-1.amazonaws.com/ao-cloud-control-plane:<tag>-linux-amd64 \
  ./scripts/provision-coder-azure-template.sh
#    -> builds ao-coder-workspace:local, publishes ao-linux-docker, stores CODER_TEMPLATE_ID
```

Both scripts are parameterized (see the env vars at the top of each) and safe to
re-run. Retrieve the token/template id from the VM when wiring the CP:

```bash
az vm run-command invoke -g ao-coder-azure -n ao-coder-azure-vm --command-id RunShellScript \
  --scripts '. /etc/ao-coder.env; echo "$CODER_TOKEN"; echo "$CODER_TEMPLATE_ID"' --query 'value[0].message' -o tsv
```

## Wire a control plane at it

```
AO_CLOUD_CODER_URL=https://<fqdn>
AO_CLOUD_CODER_TOKEN=<CODER_TOKEN from the VM>
AO_CLOUD_CODER_TEMPLATE_ID=<CODER_TEMPLATE_ID from the VM>
AO_CLOUD_CODER_OWNER=aoadmin
AO_CLOUD_CODER_AGENT_NAME=main
```

A session created with `provider=coder` then provisions a workspace container on
the Azure VM, and its baked `ao-worker` dials the CP's public URL as usual. An
orchestrator's workers **inherit its provider**, so a coder orchestrator spawns
coder workers - both land on Azure.

> **Guardrail:** the coder config is a single endpoint per CP, so **every** coder
> session on a given CP goes wherever its `AO_CLOUD_CODER_URL` points. Repointing
> a CP that other people use (e.g. shared staging) moves *their* coder workspaces
> too. Point only a CP you control at this Azure Coder.

## Hardening status (for a customer handover)

Handled + validated live in `swedencentral`:

- **TLS** - Caddy fronts Coder with a Let's Encrypt cert on a stable FQDN; the
  plain `:3000` port is unpublished and closed at the NSG. The CP↔Coder token
  never crosses the internet in cleartext.
- **Per-workspace limits** - each workspace container gets a hard memory cap
  (`workspace_memory_mb`, default 4096) + CPU weight (`workspace_cpu_shares`), so
  one workspace cannot OOM the shared VM. Optional vars in `main.tf`, no-op for
  the existing single-tenant (AWS) deployments.
- **Restart resilience** - Caddy/Coder/Postgres are `--restart unless-stopped`
  and recover automatically on a VM reboot (verified: workspace provisions again
  post-reboot).
- **NSG** - steady state is inbound **80/443 only**. SSH (22) is needed only for
  the one-time template build; restrict it with `AO_AZURE_SSH_SOURCE` at
  provision time, or close it afterward (done on the live deployment - day-2
  admin is via `az vm run-command`, no SSH). Re-open 22 briefly only to rebake
  the image.
- **Worker SHA fast path** - the workspace image bakes `/ao-worker` from
  `AO_CLOUD_CP_IMAGE`; the CP you wire up must run that same image or the coder
  bootstrap falls back to the slow PTY upload. Re-run
  provision-coder-azure-template.sh with the new `AO_CLOUD_CP_IMAGE` whenever the
  CP image changes (the Azure analogue of publish-coder-workspace.sh on AWS).

Deferred (production-scale, **not** blockers for a bounded customer test):

- **Coder's Postgres -> Azure Database for PostgreSQL.** It is a container with a
  persistent volume today; it survives reboots (verified). Migrate before real
  production for managed backups/HA.
- **Push the workspace image to ACR.** It is a local tag on the one VM; it
  survives reboots but is lost if the VM is *recreated*. Push to ACR before you
  scale to more than one VM or want VM-recreation resilience.
- **Multi-VM autoscale.** One VM (4 vCPU / 16 GB) holds a handful of memory-
  capped workspaces; add hosts / a bigger VM only at higher concurrency.
- **Rotate the Coder admin token.** The CP↔Coder token in `/etc/ao-coder.env` is
  admin-scoped and long-lived (`CODER_MAX_ADMIN_TOKEN_LIFETIME=8760h`); rotate it
  (`coder tokens create` + update the `ao-cloud/staging/coder` secret) periodically
  or on suspicion of VM compromise.
- **SSH host-key pinning.** provision-coder-azure-template.sh uses
  `StrictHostKeyChecking=no`; for a rebake over an untrusted network, pin the host
  key first (it carries a short-lived, read-only ECR pull token via stdin).

## VM-per-workspace variant (mirrors eleven_x's AWS dev-kit)

The default template above runs each workspace as a **container** on the shared
Coder VM. To instead mirror eleven_x's AWS EC2-per-workspace dev-kit - **one
dedicated Azure VM per workspace, running the AO worker + harness NATIVELY (no
Docker, no container)** - use `cloud/coder-azure-vm/main.tf` (an `azurerm`
template that boots from a baked image). This is what the live staging deployment
is pointed at.

Setup (on top of the base Coder from provision-coder-azure.sh):

1. **Give Coder Azure creds** so its `azurerm` provider can create VMs. Simplest
   for the container is a scoped service principal (avoids container->IMDS):
   `az ad sp create-for-rbac --name ao-coder-azurerm --role Contributor --scopes
   <rg>`, then re-run the coder container with `ARM_CLIENT_ID/ARM_CLIENT_SECRET/
   ARM_TENANT_ID/ARM_SUBSCRIPTION_ID` env (persisted in /etc/ao-coder.env).
2. **Bake the native VM image** (`ao-coder-workspace-image`): a temp Ubuntu VM
   installs node + **all AO harnesses natively** (`@anthropic-ai/claude-code`,
   `@openai/codex`, `cursor-agent`) + `gh`, extracts the `ao-worker` and `ao` Go
   binaries from the control-plane image, adds a `coder` user with NOPASSWD sudo,
   then Docker is removed - so nothing runs in a container. Then
   `az vm deallocate/generalize` + `az image create --hyper-v-generation V2`. All
   three harnesses are baked into the ONE shared image (matching `cloud/Dockerfile`,
   which is what lets nodeops/ecs sessions switch harness mid-session with zero
   install); the harness chosen at project setup only decides which binary the
   worker launches, not what is installed. To refresh harness versions (or add a
   new harness) without a full rebuild, run
   `cloud/scripts/bake-coder-azure-image.sh`, which bakes FROM the current image
   and only adds/updates harnesses, then re-point the template at the new image.
3. **Publish the template:** `coder templates push ao-azure-vm -d <dir>
   --variable subnet_id=<subnet> --variable image_id=<managed-image-id>`.
4. **Repoint** `ao-cloud/staging/coder` `template_id` at it and redeploy the CP
   (parameters_json stays `{}`; owner `aoadmin`).

Each workspace VM is `Standard_D2s_v5` (~2 vCPU/8 GB, form-tunable via `vm_size`),
boots from the baked image, and cloud-init starts the **coder agent as a native
systemd service** (`coder-agent.service`, `Restart=always`, so it survives
stop/start). The control plane then bootstraps the baked `ao-worker` natively
over the agent - exactly like eleven_x's EC2 setup. No Docker install, no image
pull at boot.

**Durable root + stop/start persistence:** the AO worker bootstrap refuses unless
the durable root (`/home/coder`) is a real MOUNTED directory (`mountpoint -q`) -
matching eleven_x's dev-kit, which mounts a persistent disk there. So the template
attaches a persistent per-workspace managed disk (`azurerm_managed_disk.durable`,
`count = 1` so it survives a Coder **stop**; the attachment is `count = start_count`)
and cloud-init formats+mounts it at `/home/coder` before the agent starts. Coder
`Pause` maps to `Stop` (`sandbox/coder/client.go`), which destroys the VM but keeps
the disk, so on `Resume` a fresh VM re-mounts the same disk and `/home/coder`
workspace state persists. Only `Delete`/terminate (e.g. an orchestrator reaping a
finished child) tears the disk down.

**Follow-ups for this variant** (not blockers): move the baked image into a Compute
Gallery for multi-region/versioning; and give each workspace VM an NSG that only
allows outbound (the agent dials out).

## Teardown

```bash
az group delete -n ao-coder-azure --yes --no-wait
```
