# AO Coder workspace template

This is the reference Docker-backed Coder template for AO Cloud. Its image
contains release-matched `ao-worker` and `ao` binaries plus the template's
approved coding harness. The control plane verifies both binary hashes before
launching them; if a customer has not updated their template yet, or the image
belongs to an older AO release, bootstrap falls back to the compatible PTY
upload instead of running mismatched code.

## Automatic rebake on deploy (staging)

`scripts/deploy-staging.sh` rebakes and republishes this image automatically
whenever it deploys with `AO_CLOUD_SANDBOX_PROVIDER=coder`. There is no manual
rebake step. It runs `scripts/publish-coder-workspace.sh` with the exact
control-plane image digest it just built, so the baked `ao-worker`/`ao` bytes
are identical to what the reconciler advertises as `AO_WORKER_EXPECTED_SHA256`
(`internal/reconcile/reconciler.go`). That keeps every spawn and resume on the
Coder bootstrap fast path (`internal/sandbox/coder/client.go` `preinstalledCheck`
/ `__AO_PREINSTALLED_MISS__`) instead of the slow, flaky binary upload.

The publish script:

1. builds `coder/Sandbox.Dockerfile` with
   `--build-arg AO_CONTROL_PLANE_IMAGE="$AO_CLOUD_CP_IMAGE"`, which copies
   `/ao-worker` and `/ao` straight out of the control-plane image;
2. verifies the baked SHA-256s equal the control plane's own `/ao-worker` and
   `/ao` (a mismatch fails the deploy rather than silently degrading to the
   upload path);
3. pushes the image to the workspace registry; and
4. publishes a new version of the Coder template pointed at the immutable
   `repo@digest`, using the same Coder URL/token the control plane is deployed
   with.

### Where Coder pulls the image from

By default the script pushes to an ECR repository (`ao-cloud-coder-workspace`,
override with `AO_CLOUD_CODER_WORKSPACE_ECR_REPOSITORY`) and pins the template's
`workspace_image` to the immutable `repo@digest`. **Assumption:** the Coder
provisioner's Docker host can authenticate to and pull from that repository. For
a single-host deployment whose Coder provisioner shares the deploy host's Docker
daemon, set `AO_CLOUD_CODER_WORKSPACE_IMAGE=ao-coder-workspace:local` to build
and reference a local tag and skip the registry round trip.

The token in the Coder secret must belong to a template admin so
`coder templates push` can publish a new version. `AO_CLOUD_CODER_TEMPLATE_NAME`
(default `ao-linux-docker`) must resolve to the same template whose UUID the
control plane is configured with (`AO_CLOUD_CODER_TEMPLATE_ID`).

## Production and other deployments

`scripts/promote-production.sh` is deliberately Docker-free — it promotes the
staging image digests through AWS APIs without building anything. Run the rebake
as an explicit step of the production promote (from `cloud/`, with the promoted
control-plane digest and the production Coder secret):

```bash
AWS_REGION=eu-north-1 \
AO_CLOUD_CP_IMAGE="<promoted control-plane repo@sha256:...>" \
AO_CLOUD_CODER_SECRET_ID=ao-cloud/production/coder \
  ./scripts/publish-coder-workspace.sh
```

The same script covers any bespoke deployment. To do it by hand instead, build
the image from the exact control-plane image being deployed:

```bash
docker build \
  --build-arg AO_CONTROL_PLANE_IMAGE="$AO_CLOUD_CP_IMAGE" \
  --tag ao-coder-workspace:local \
  --file coder/Sandbox.Dockerfile \
  .
```

Run that command from `cloud/`. For a Coder deployment whose provisioner uses a
different Docker host, push the image to an approved registry and pass its
immutable reference to the template (the publish script does this for you, or
set `workspace_image` in `main.tf`). The Coder host must be able to authenticate
to and pull from that registry.

Then publish the directory with the Coder CLI:

```bash
CODER_URL=https://coder.example.com \
CODER_SESSION_TOKEN=... \
coder templates push --yes ao-linux-docker --directory coder \
  --variable workspace_image=<immutable image reference>
```

Configure AO with `/home/coder` as the durable root for this reference
template. A customer's equivalent template can use another mounted path as
long as its AO connection records that exact path.
