# AO Coder workspace template

This is the reference Docker-backed Coder template for AO Cloud. Its image
contains release-matched `ao-worker` and `ao` binaries plus the template's
approved coding harness. The control plane verifies both binary hashes before
launching them; if a customer has not updated their template yet, or the image
belongs to an older AO release, bootstrap falls back to the compatible PTY
upload instead of running mismatched code.

Build the image from the exact control-plane image being deployed:

```bash
docker build \
  --build-arg AO_CONTROL_PLANE_IMAGE="$AO_CLOUD_CP_IMAGE" \
  --tag ao-coder-workspace:local \
  --file coder/Sandbox.Dockerfile \
  .
```

Run that command from `cloud/`. For a Coder deployment whose provisioner uses a
different Docker host, push the image to an approved registry and change the
`workspace_image` default in `main.tf` to its immutable reference. The Coder
host must be able to authenticate to and pull from that registry.

Then publish the directory with the Coder CLI:

```bash
CODER_URL=https://coder.example.com \
CODER_SESSION_TOKEN=... \
coder templates push --yes ao-linux-docker --directory coder
```

Configure AO with `/home/coder` as the durable root for this reference
template. A customer's equivalent template can use another mounted path as
long as its AO connection records that exact path.
