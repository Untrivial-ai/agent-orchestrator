#!/usr/bin/env bash
set -euo pipefail

AWS_PROFILE="${AWS_PROFILE:-ao-cloud}"
AWS_REGION="${AWS_REGION:-eu-north-1}"
NODEOPS_SECRET_ID="${AO_CLOUD_NODEOPS_SECRET_ID:-ao-cloud/staging/nodeops}"
# HARNESS selects the template flavor: one of claude-code | codex | cursor for
# a slim single-agent template (Sandbox.base.Dockerfile + the matching
# nodeops/harness/*.Dockerfile layer), or "all" for the legacy all-agents
# template built from Sandbox.Dockerfile.
HARNESS="${AO_CLOUD_NODEOPS_HARNESS:-all}"
case "$HARNESS" in
    all) harness_suffix="baked" ;;
    claude-code) harness_suffix="claude" ;;
    codex) harness_suffix="codex" ;;
    cursor) harness_suffix="cursor" ;;
    *) echo "AO_CLOUD_NODEOPS_HARNESS must be all|claude-code|codex|cursor, got: $HARNESS" >&2; exit 1 ;;
esac
TEMPLATE_NAME="${AO_CLOUD_NODEOPS_TEMPLATE_NAME:-ao-worker-$(date +%Y%m%d)-${harness_suffix}-v1}"
DOCKERFILE="${AO_CLOUD_NODEOPS_DOCKERFILE:-nodeops/Sandbox.Dockerfile}"
BASE_DOCKERFILE="${AO_CLOUD_NODEOPS_BASE_DOCKERFILE:-nodeops/Sandbox.base.Dockerfile}"
# Control-plane image to bake the worker binaries from. Using the image (not a
# fresh local build) guarantees the baked bytes hash-match exactly what that
# control plane uploads, which is what lets its bootstrap fast-path skip the
# uploads. Republish the template whenever a deploy changes the worker; an
# out-of-date template still works, it just falls back to the upload path.
CP_IMAGE="${AO_CLOUD_CP_IMAGE:-}"
ARTIFACTS_BUCKET="${AO_CLOUD_ARTIFACTS_BUCKET:-ao-cloud-staging-artifacts}"

if [[ ! -f "$DOCKERFILE" ]]; then
    echo "NodeOps template Dockerfile not found: $DOCKERFILE" >&2
    exit 1
fi
if [[ -z "$CP_IMAGE" ]]; then
    echo "Set AO_CLOUD_CP_IMAGE to the control-plane image (tag or digest) whose worker binaries should be baked." >&2
    exit 1
fi

# Extract the exact worker binaries the control plane serves.
workdir="$(mktemp -d)"
trap 'rm -rf "$workdir"' EXIT
container="$(docker create "$CP_IMAGE")"
docker cp "$container:/ao-worker" "$workdir/ao-worker" >/dev/null
docker cp "$container:/ao" "$workdir/ao" >/dev/null
docker rm "$container" >/dev/null
worker_sha="$(shasum -a 256 "$workdir/ao-worker" | cut -d' ' -f1)"
helper_sha="$(shasum -a 256 "$workdir/ao" | cut -d' ' -f1)"
echo "Baking ao-worker ${worker_sha:0:12} and ao ${helper_sha:0:12} from $CP_IMAGE"

# Stage the binaries where the NodeOps builder can fetch them. The template
# build takes only dockerfile text (no build context), so a short-lived
# presigned URL is the transport. Keys are content-addressed.
if ! AWS_PROFILE="$AWS_PROFILE" aws s3api head-bucket --bucket "$ARTIFACTS_BUCKET" --region "$AWS_REGION" >/dev/null 2>&1; then
    AWS_PROFILE="$AWS_PROFILE" aws s3api create-bucket \
        --bucket "$ARTIFACTS_BUCKET" --region "$AWS_REGION" \
        --create-bucket-configuration LocationConstraint="$AWS_REGION" >/dev/null
    AWS_PROFILE="$AWS_PROFILE" aws s3api put-public-access-block \
        --bucket "$ARTIFACTS_BUCKET" --region "$AWS_REGION" \
        --public-access-block-configuration BlockPublicAcls=true,IgnorePublicAcls=true,BlockPublicPolicy=true,RestrictPublicBuckets=true >/dev/null
    echo "Created artifacts bucket $ARTIFACTS_BUCKET"
fi
AWS_PROFILE="$AWS_PROFILE" aws s3 cp --region "$AWS_REGION" --only-show-errors \
    "$workdir/ao-worker" "s3://$ARTIFACTS_BUCKET/worker/$worker_sha/ao-worker"
AWS_PROFILE="$AWS_PROFILE" aws s3 cp --region "$AWS_REGION" --only-show-errors \
    "$workdir/ao" "s3://$ARTIFACTS_BUCKET/worker/$helper_sha/ao"
worker_url="$(AWS_PROFILE="$AWS_PROFILE" aws s3 presign --region "$AWS_REGION" \
    --expires-in 3600 "s3://$ARTIFACTS_BUCKET/worker/$worker_sha/ao-worker")"
helper_url="$(AWS_PROFILE="$AWS_PROFILE" aws s3 presign --region "$AWS_REGION" \
    --expires-in 3600 "s3://$ARTIFACTS_BUCKET/worker/$helper_sha/ao")"

# Append the bake layer to the base Dockerfile text. sha256 checks make a
# corrupted fetch fail the template build instead of shipping a broken worker.
bake_layer="$(cat <<EOF

RUN curl --fail --location --silent --show-error -o /usr/local/bin/ao-worker '$worker_url' && \\
    echo "$worker_sha  /usr/local/bin/ao-worker" | sha256sum -c - && \\
    chmod 0755 /usr/local/bin/ao-worker && \\
    curl --fail --location --silent --show-error -o /usr/local/bin/ao '$helper_url' && \\
    echo "$helper_sha  /usr/local/bin/ao" | sha256sum -c - && \\
    chmod 0755 /usr/local/bin/ao
EOF
)"

secret="$(AWS_PROFILE="$AWS_PROFILE" aws secretsmanager get-secret-value \
    --region "$AWS_REGION" \
    --secret-id "$NODEOPS_SECRET_ID" \
    --query SecretString \
    --output text)"
base_url="$(jq -r '.base_url // empty' <<<"$secret")"
api_key="$(jq -r '.api_key // empty' <<<"$secret")"
unset secret

if [[ -z "$base_url" || -z "$api_key" ]]; then
    echo "NodeOps secret is missing base_url or api_key." >&2
    exit 1
fi

composed="$workdir/Dockerfile"
if [[ "$HARNESS" == "all" ]]; then
    cat "$DOCKERFILE" > "$composed"
else
    harness_layer="nodeops/harness/$HARNESS.Dockerfile"
    if [[ ! -f "$BASE_DOCKERFILE" || ! -f "$harness_layer" ]]; then
        echo "Missing $BASE_DOCKERFILE or $harness_layer" >&2
        exit 1
    fi
    cat "$BASE_DOCKERFILE" > "$composed"
    printf '\n' >> "$composed"
    cat "$harness_layer" >> "$composed"
fi
printf '%s\n' "$bake_layer" >> "$composed"

payload="$(jq -n \
    --arg name "$TEMPLATE_NAME" \
    --rawfile dockerfile "$composed" \
    '{name: $name, dockerfile: $dockerfile}')"
response="$(curl --fail --silent --show-error \
    -X POST "$base_url/v1/templates" \
    -H "X-Api-Key: $api_key" \
    -H "Content-Type: application/json" \
    --data-binary "$payload")"
template_id="$(jq -r '.data.id // empty' <<<"$response")"
if [[ -z "$template_id" ]]; then
    echo "NodeOps did not return a template id." >&2
    jq . <<<"$response" >&2
    exit 1
fi

echo "Submitted NodeOps template $TEMPLATE_NAME ($template_id)."
while true; do
    response="$(curl --fail --silent --show-error \
        "$base_url/v1/templates/$template_id" \
        -H "X-Api-Key: $api_key")"
    status="$(jq -r '.data.status // empty' <<<"$response")"
    case "$status" in
        ready)
            echo "NodeOps template $TEMPLATE_NAME is ready."
            break
            ;;
        failed)
            echo "NodeOps template $TEMPLATE_NAME failed to build." >&2
            curl --fail --silent --show-error \
                "$base_url/v1/templates/$template_id/logs" \
                -H "X-Api-Key: $api_key" >&2 || true
            exit 1
            ;;
        pending|building)
            printf 'Template status: %s\n' "$status"
            sleep 5
            ;;
        *)
            echo "Unexpected NodeOps template status: $status" >&2
            exit 1
            ;;
    esac
done
