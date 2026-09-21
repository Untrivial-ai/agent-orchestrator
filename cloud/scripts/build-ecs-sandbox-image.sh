#!/usr/bin/env bash
set -euo pipefail

# Builds the arm64 (Graviton) AO worker image for the ECS-on-EC2 sandbox provider
# and registers a fresh revision of the sandbox task definition that points at it.
#
# Why a separate image from the amd64 worker deploy-staging.sh builds: ECS
# sandboxes run on Graviton, and the ECS worker does not self-update (the control
# plane and its served worker binary are amd64, so a cross-architecture heal
# would corrupt the box). The baked arm64 worker is therefore authoritative and
# must be rebuilt from the same commit on every release, exactly the way
# publish-coder-workspace.sh rebakes the coder image.
#
# Run scripts/provision-ecs-sandbox.sh once first to create the cluster, roles and
# the task-definition family this script updates.

REGION="${AWS_REGION:-eu-north-1}"
WORKER_REPOSITORY="${AO_CLOUD_WORKER_ECR_REPOSITORY:-ao-cloud-worker}"
SANDBOX_TASK_FAMILY="${AO_CLOUD_ECS_TASK_FAMILY:-ao-cloud-sandbox-worker}"
SANDBOX_CONTAINER_NAME="${AO_CLOUD_ECS_CONTAINER_NAME:-worker}"
HEAD_SHA="$(git rev-parse HEAD)"
RELEASE="${1:-$HEAD_SHA}"
IMAGE_TAG="${RELEASE//+/-}-linux-arm64"

AWS_OPTIONS=(--region "$REGION")
if [[ -n "${AWS_PROFILE:-}" ]]; then
	AWS_OPTIONS+=(--profile "$AWS_PROFILE")
fi
aws_cli() { aws "${AWS_OPTIONS[@]}" "$@"; }

if [[ -n "$(git status --porcelain)" ]]; then
	echo "Refusing to build from a dirty working tree." >&2
	exit 1
fi

repository_uri="$(
	aws_cli ecr describe-repositories \
		--repository-names "$WORKER_REPOSITORY" \
		--query 'repositories[0].repositoryUri' \
		--output text
)"
registry="${repository_uri%%/*}"
aws_cli ecr get-login-password | docker login --username AWS --password-stdin "$registry" >/dev/null

if ! aws_cli ecr describe-images \
	--repository-name "$WORKER_REPOSITORY" \
	--image-ids "imageTag=${IMAGE_TAG}" >/dev/null 2>&1; then
	# buildx cross-builds arm64 from any host; the agents-present verification
	# below needs an arm64-capable runtime (native on Apple Silicon, or binfmt).
	docker buildx build \
		--platform linux/arm64 \
		--provenance=false \
		--target worker \
		--tag "${repository_uri}:${IMAGE_TAG}" \
		--push \
		.
fi

digest="$(
	aws_cli ecr describe-images \
		--repository-name "$WORKER_REPOSITORY" \
		--image-ids "imageTag=${IMAGE_TAG}" \
		--query 'imageDetails[0].imageDigest' \
		--output text
)"
worker_image="${repository_uri}@${digest}"

docker pull "$worker_image" >/dev/null
./scripts/verify-ecs-sandbox-image.sh "$worker_image"

# Register a new revision of the sandbox task definition with the fresh image,
# preserving everything else the family already carries (roles, networkMode,
# resource reservations, log configuration).
source_task="$(
	aws_cli ecs describe-task-definition \
		--task-definition "$SANDBOX_TASK_FAMILY" \
		--query taskDefinition \
		--output json
)"
payload="$(
	SOURCE_TASK="$source_task" \
		WORKER_IMAGE="$worker_image" \
		CONTAINER_NAME="$SANDBOX_CONTAINER_NAME" \
		python3 - <<'PY'
import json
import os

task = json.loads(os.environ["SOURCE_TASK"])
container_name = os.environ["CONTAINER_NAME"]
keep = {
    "family", "taskRoleArn", "executionRoleArn", "networkMode",
    "containerDefinitions", "volumes", "placementConstraints",
    "requiresCompatibilities", "cpu", "memory", "runtimePlatform",
}
payload = {key: value for key, value in task.items() if key in keep and value not in (None, [], "")}
containers = payload["containerDefinitions"]
target = next((c for c in containers if c["name"] == container_name), None)
if target is None:
    raise SystemExit(f"sandbox task family has no container named {container_name!r}")
target["image"] = os.environ["WORKER_IMAGE"]
print(json.dumps(payload))
PY
)"
task_arn="$(
	aws_cli ecs register-task-definition \
		--cli-input-json "$payload" \
		--query 'taskDefinition.taskDefinitionArn' \
		--output text
)"

printf 'Built arm64 sandbox worker %s\nRegistered task definition %s\n' \
	"$worker_image" "$task_arn"
printf 'Set AO_CLOUD_ECS_TASK_DEFINITION=%s\n' "${task_arn##*/}"
