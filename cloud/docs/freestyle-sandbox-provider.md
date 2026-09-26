# Freestyle sandbox provider

AO Cloud can run its worker in a Freestyle VM. The worker connects outbound to
`AO_CLOUD_PUBLIC_URL`; AO does not publish an inbound port on the VM. Freestyle
VMs keep their filesystem and memory while paused, so AO maps its normal
idle `Stop` action to Freestyle pause and resumes the same VM on activity.
Deleting an AO sandbox deletes the VM.

## Prepare the provider

1. Choose a Freestyle snapshot with the Linux tools and coding harnesses AO
   sessions need. `freestyle/ubuntu` is the default; for useful coding sessions,
   publish a snapshot with the desired tools installed.
2. Supply an API key for the Freestyle account that owns the snapshot. AO keeps
   this key in its control plane and never sends it to a worker or browser.
3. Set `AO_CLOUD_SANDBOX_PROVIDER=freestyle`, or include `freestyle` in
   `AO_CLOUD_SANDBOX_PROVIDERS` for a control plane that also serves other
   providers. Set `AO_CLOUD_FREESTYLE_URL`, `AO_CLOUD_FREESTYLE_API_KEY`,
   `AO_CLOUD_FREESTYLE_SNAPSHOT_ID`, and
   `AO_CLOUD_FREESTYLE_WORKER_TOKEN_TTL` as shown in `.env.example`.
4. Set `AO_CLOUD_PUBLIC_URL` to an origin reachable from the VM and configure
   `AO_CLOUD_WORKER_SIGNING_KEY`. Package the Linux `ao-worker` and `ao` helper
   binaries at `AO_CLOUD_WORKER_BINARY_PATH` and
   `AO_CLOUD_WORKER_HELPER_BINARY_PATH`. Hosted images already include them.

Hosted staging and production load these values from a JSON Secrets Manager
entry named `ao-cloud/<environment>/freestyle`:

```json
{
  "url": "https://api.freestyle.sh",
  "api_key": "<Freestyle API key>",
  "snapshot_id": "freestyle/ubuntu",
  "worker_token_ttl": "15m"
}
```

The staging deploy and production promotion scripts validate the secret and
inject its fields into the control-plane task. Production promotion requires
the provider set already verified in staging. The Freestyle account and
snapshot may differ by environment.

## Lifecycle and access

Each AO session gets a deterministic `ao-<session-id>` VM slug and session and
organization metadata. On a retried create, AO checks that metadata before
adopting the VM. The create request explicitly allows outbound public traffic
so the worker can reach the control plane and GitHub; it declares no inbound
firewall rule. Provider idle timeout and auto-delete are disabled so AO owns
the pause and deletion decisions, subject to any limits of the Freestyle plan.

The control plane uploads its release's worker and helper binaries through the
Freestyle file API, creates an unprivileged `ao-worker` Linux user, and launches
the worker with a short-lived AO bootstrap ticket. Session files, worker state,
and coding harness homes live under `/workspace`, which survives pause and
resume. A stopped VM can also be started, but Freestyle stop discards guest
memory; AO's normal idle path uses pause to retain it.

Freestyle snapshots determine VM resources. The AO session's existing generic
CPU, memory, and disk fields do not resize a snapshot restore. Account-level
VM and resource quotas still apply.
