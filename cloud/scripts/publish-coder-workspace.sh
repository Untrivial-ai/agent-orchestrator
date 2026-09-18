#!/usr/bin/env bash
set -euo pipefail

# Rebuild and publish the approved Coder workspace image so its baked ao-worker
# and ao binaries hash-match the exact control-plane image being deployed, then
# push the Coder template that references that image.
#
# Why this exists: coder/Sandbox.Dockerfile bakes /ao-worker and /ao straight
# out of the control-plane image (FROM ${AO_CONTROL_PLANE_IMAGE} AS ao-release).
# The reconciler advertises AO_WORKER_EXPECTED_SHA256 = sha256 of that same
# /ao-worker (reconciler.go:1195), and the Coder bootstrap fast path
# (internal/sandbox/coder/client.go preinstalledCheck / __AO_PREINSTALLED_MISS__)
# only skips the slow, flaky multi-megabyte PTY upload when the baked SHA-256
# matches. So a deploy that ships a new ao-worker MUST rebake this image, or
# every spawn AND every resume falls back to the upload path. Running this as
# part of the deploy (see deploy-staging.sh) removes the manual rebake step.
#
# Run from the cloud/ directory, like publish-nodeops-template.sh.

AWS_REGION="${AWS_REGION:-eu-north-1}"
CODER_SECRET_ID="${AO_CLOUD_CODER_SECRET_ID:-ao-cloud/staging/coder}"

# Control-plane image (tag or, preferably, an immutable @sha256 digest) whose
# worker binaries get baked in. Passing the same reference the control plane is
# deployed with is what makes the SHAs line up.
CP_IMAGE="${AO_CLOUD_CP_IMAGE:-}"

# Coder template name to publish. It must resolve, on the target deployment, to
# the same template whose UUID the control plane is configured with
# (AO_CLOUD_CODER_TEMPLATE_ID). The reference template's name is ao-linux-docker.
TEMPLATE_NAME="${AO_CLOUD_CODER_TEMPLATE_NAME:-ao-linux-docker}"

DOCKERFILE="${AO_CLOUD_CODER_DOCKERFILE:-coder/Sandbox.Dockerfile}"
TEMPLATE_DIR="${AO_CLOUD_CODER_TEMPLATE_DIR:-coder}"
# Terraform variable in coder/main.tf that selects the container image.
IMAGE_VARIABLE="${AO_CLOUD_CODER_IMAGE_VARIABLE:-workspace_image}"

# Where Coder pulls the workspace image from. Default: an ECR repository
# (mirrors how the control-plane and worker images ship). The built image is
# pushed there and the template is pointed at the immutable repo@digest, so the
# Coder provisioner's Docker host must be able to authenticate to and pull from
# this repository. For a single-host deployment whose Coder provisioner shares
# THIS Docker daemon, set AO_CLOUD_CODER_WORKSPACE_IMAGE to a local tag such as
# ao-coder-workspace:local instead (see below) to skip the registry round trip.
WORKSPACE_REPOSITORY="${AO_CLOUD_CODER_WORKSPACE_ECR_REPOSITORY:-ao-cloud-coder-workspace}"

# Full image reference override. When set, the image is built and tagged as this
# reference and used verbatim as the template's image variable; ECR resolution
# is skipped. Combine with AO_CLOUD_CODER_WORKSPACE_PUSH=true to also `docker
# push` it to a non-ECR registry.
WORKSPACE_IMAGE_OVERRIDE="${AO_CLOUD_CODER_WORKSPACE_IMAGE:-}"
WORKSPACE_PUSH="${AO_CLOUD_CODER_WORKSPACE_PUSH:-}"

# Mutable tag for the ECR push. Unique-per-release is best; the template is
# pinned to the resulting immutable digest, not this tag.
IMAGE_TAG="${AO_CLOUD_CODER_WORKSPACE_IMAGE_TAG:-coder-workspace-$(date +%Y%m%d%H%M%S)}"

# Set to false to build+publish the image but skip `coder templates push` (for
# deployments that publish the template through a separate change-controlled
# path). Default publishes the template.
PUSH_TEMPLATE="${AO_CLOUD_CODER_PUSH_TEMPLATE:-true}"

if [[ -z "$CP_IMAGE" ]]; then
    echo "Set AO_CLOUD_CP_IMAGE to the control-plane image (tag or digest) whose worker binaries should be baked." >&2
    exit 1
fi
if [[ ! -f "$DOCKERFILE" ]]; then
    echo "Coder workspace Dockerfile not found: $DOCKERFILE (run from the cloud/ directory)" >&2
    exit 1
fi
if [[ ! -f "$TEMPLATE_DIR/main.tf" ]]; then
    echo "Coder template directory not found: $TEMPLATE_DIR/main.tf (run from the cloud/ directory)" >&2
    exit 1
fi

AWS_OPTIONS=(--region "$AWS_REGION")
if [[ -n "${AWS_PROFILE:-}" ]]; then
    AWS_OPTIONS+=(--profile "$AWS_PROFILE")
fi
aws_cli() {
    aws "${AWS_OPTIONS[@]}" "$@"
}

# sha256 of one path inside an image, without executing the image: create a
# throwaway container, copy the file out, hash it, discard the container. This
# is arch-agnostic (no emulation) and matches verify-image-contract.sh.
image_file_sha() {
    local image="$1" src="$2" tmp container sha
    tmp="$(mktemp)"
    container="$(docker create "$image")"
    docker cp "$container:$src" "$tmp" >/dev/null
    docker rm "$container" >/dev/null
    sha="$(shasum -a 256 "$tmp" | cut -d' ' -f1)"
    rm -f "$tmp"
    printf '%s' "$sha"
}

# Resolve the target image reference and log in where a push is needed. Logging
# in first also lets `docker build`/`docker create` pull the control-plane image
# from the same registry when it is not already present locally.
push_image="false"
if [[ -n "$WORKSPACE_IMAGE_OVERRIDE" ]]; then
    build_tag="$WORKSPACE_IMAGE_OVERRIDE"
    workspace_image_ref="$WORKSPACE_IMAGE_OVERRIDE"
    if [[ "$WORKSPACE_PUSH" == "true" ]]; then
        push_image="true"
    fi
else
    if ! repository_uri="$(
        aws_cli ecr describe-repositories \
            --repository-names "$WORKSPACE_REPOSITORY" \
            --query 'repositories[0].repositoryUri' \
            --output text 2>/dev/null
    )"; then
        repository_uri="$(
            aws_cli ecr create-repository \
                --repository-name "$WORKSPACE_REPOSITORY" \
                --image-scanning-configuration scanOnPush=true \
                --query 'repository.repositoryUri' \
                --output text
        )"
        echo "Created workspace image repository $WORKSPACE_REPOSITORY"
    fi
    registry="${repository_uri%%/*}"
    aws_cli ecr get-login-password |
        docker login --username AWS --password-stdin "$registry" >/dev/null
    build_tag="${repository_uri}:${IMAGE_TAG}"
    push_image="true"
fi

# The exact hashes the control plane advertises (its /ao-worker and /ao).
cp_worker_sha="$(image_file_sha "$CP_IMAGE" /ao-worker)"
cp_helper_sha="$(image_file_sha "$CP_IMAGE" /ao)"
echo "Control-plane advertises ao-worker ${cp_worker_sha:0:12} and ao ${cp_helper_sha:0:12} (from $CP_IMAGE)"

# Build the workspace image against the exact control-plane image. The Dockerfile
# copies /ao-worker and /ao from it, so the baked bytes are identical to what the
# control plane serves.
docker build \
    --platform linux/amd64 \
    --provenance=false \
    --build-arg "AO_CONTROL_PLANE_IMAGE=${CP_IMAGE}" \
    --file "$DOCKERFILE" \
    --tag "$build_tag" \
    .

# Fail loudly if the bake did not reproduce the control-plane hashes: a mismatch
# means every spawn/resume would silently fall back to the slow PTY upload.
baked_worker_sha="$(image_file_sha "$build_tag" /usr/local/bin/ao-worker)"
baked_helper_sha="$(image_file_sha "$build_tag" /usr/local/bin/ao)"
if [[ "$baked_worker_sha" != "$cp_worker_sha" || "$baked_helper_sha" != "$cp_helper_sha" ]]; then
    echo "Baked worker/helper SHA does not match the control-plane image." >&2
    echo "  ao-worker baked=${baked_worker_sha} expected=${cp_worker_sha}" >&2
    echo "  ao        baked=${baked_helper_sha} expected=${cp_helper_sha}" >&2
    exit 1
fi
echo "Baked worker ${baked_worker_sha:0:12} and helper ${baked_helper_sha:0:12} match the control plane."

if [[ "$push_image" == "true" ]]; then
    docker push "$build_tag" >/dev/null
fi

# Prefer the immutable digest for the template so the workspace image cannot
# drift out from under an already-published template version.
if [[ -z "$WORKSPACE_IMAGE_OVERRIDE" ]]; then
    image_digest="$(
        aws_cli ecr describe-images \
            --repository-name "$WORKSPACE_REPOSITORY" \
            --image-ids "imageTag=${IMAGE_TAG}" \
            --query 'imageDetails[0].imageDigest' \
            --output text
    )"
    workspace_image_ref="${repository_uri}@${image_digest}"
fi
echo "Coder workspace image: $workspace_image_ref"

if [[ "$PUSH_TEMPLATE" != "true" ]]; then
    echo "Skipping coder templates push (AO_CLOUD_CODER_PUSH_TEMPLATE != true)."
    exit 0
fi

if ! command -v coder >/dev/null 2>&1; then
    echo "The 'coder' CLI is required to push the template. Install it, or set AO_CLOUD_CODER_PUSH_TEMPLATE=false to publish the image only." >&2
    exit 1
fi

# Reuse the deployment's Coder credentials (same URL/token the control plane
# uses) to publish a new version of the template pointed at the rebaked image.
# The token's user must be a template admin on the target deployment.
secret="$(
    aws_cli secretsmanager get-secret-value \
        --secret-id "$CODER_SECRET_ID" \
        --query SecretString \
        --output text
)"
coder_url="$(jq -r '.url // empty' <<<"$secret")"
coder_token="$(jq -r '.token // empty' <<<"$secret")"
unset secret
if [[ -z "$coder_url" || -z "$coder_token" ]]; then
    echo "Coder secret $CODER_SECRET_ID is missing url or token." >&2
    exit 1
fi

CODER_URL="$coder_url" CODER_SESSION_TOKEN="$coder_token" \
    coder templates push "$TEMPLATE_NAME" \
        --directory "$TEMPLATE_DIR" \
        --variable "${IMAGE_VARIABLE}=${workspace_image_ref}" \
        --yes

echo "Published Coder template $TEMPLATE_NAME with workspace image $workspace_image_ref"
