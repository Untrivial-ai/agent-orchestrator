#!/usr/bin/env bash
set -euo pipefail

REGION="${AWS_REGION:-eu-north-1}"
CLUSTER="${AO_CLOUD_ECS_CLUSTER:-ao-cloud-staging}"
SERVICE="${AO_CLOUD_ECS_SERVICE:-ao-cloud-staging-api}"
CONTROL_REPOSITORY="${AO_CLOUD_ECR_REPOSITORY:-ao-cloud-control-plane}"
WORKER_REPOSITORY="${AO_CLOUD_WORKER_ECR_REPOSITORY:-ao-cloud-worker}"
API_FAMILY="${AO_CLOUD_API_TASK_FAMILY:-ao-cloud-staging-api}"
MIGRATION_FAMILY="${AO_CLOUD_MIGRATION_TASK_FAMILY:-ao-cloud-staging-migrate}"
ROLLBACK_ALARM="${AO_CLOUD_ROLLBACK_ALARM:-ao-cloud-staging-target-5xx}"
RUNTIME_DATABASE_USER="${AO_CLOUD_RUNTIME_DATABASE_USER:-ao_cloud_app}"
NODEOPS_SECRET_ID="${AO_CLOUD_NODEOPS_SECRET_ID:-ao-cloud/staging/nodeops}"
WORKER_SECRET_ID="${AO_CLOUD_WORKER_SECRET_ID:-ao-cloud/staging/worker}"
HEAD_SHA="$(git rev-parse HEAD)"
RELEASE="${1:-$HEAD_SHA}"
IMAGE_TAG="${RELEASE//+/-}-linux-amd64"

# CVE allowlist shared with promote-production.sh. These CRITICAL/HIGH findings
# are unpatched 2026 CVEs in base OS packages (perl, libssh2, expat) that git
# pulls into the worker image; no fix exists in Debian 12 or 13 yet. The worker
# runs inside an ephemeral, single-tenant, isolated CreateOS sandbox that
# already executes untrusted agent code. Keep this list in sync with production
# promote. Remove entries as Debian ships fixes; the worker stage's apt-get
# upgrade then clears them on rebuild. CVE-2026-14456 is a scanner false
# positive for Debian's OpenSSL 3.0: its QUIC listener was introduced in 3.5.
SCAN_CVE_ALLOWLIST="${AO_CLOUD_SCAN_CVE_ALLOWLIST:-CVE-2026-57432 CVE-2026-45186 CVE-2026-12087 CVE-2025-15661 CVE-2026-58051 CVE-2026-7017 CVE-2026-48962 CVE-2026-57433 CVE-2026-66032 CVE-2026-48961 CVE-2026-48959 CVE-2026-66034 CVE-2026-58050 CVE-2026-13221 CVE-2026-14456 CVE-2026-66046 CVE-2026-63076 CVE-2026-53615 CVE-2026-54874 CVE-2026-63072}"

AWS_OPTIONS=(--region "$REGION")
if [[ -n "${AWS_PROFILE:-}" ]]; then
	AWS_OPTIONS+=(--profile "$AWS_PROFILE")
fi

aws_cli() {
	aws "${AWS_OPTIONS[@]}" "$@"
}

if [[ -n "$(git status --porcelain)" ]]; then
	echo "Refusing to deploy a dirty working tree." >&2
	exit 1
fi
if [[ ! "$RELEASE" =~ ^[A-Za-z0-9][A-Za-z0-9._+-]{0,199}$ ]]; then
	echo "Release must be a Git SHA or release tag." >&2
	exit 1
fi
if ! resolved_release="$(git rev-parse "${RELEASE}^{commit}" 2>/dev/null)" ||
	[[ "$resolved_release" != "$HEAD_SHA" ]]; then
	echo "Release must resolve to the current commit ${HEAD_SHA}." >&2
	exit 1
fi

./scripts/verify-ecs-service.py \
	--region "$REGION" \
	--cluster "$CLUSTER" \
	--service "$SERVICE" \
	--alarm "$ROLLBACK_ALARM" >/dev/null

secret_arn() {
	aws_cli secretsmanager describe-secret \
		--secret-id "$1" \
		--query ARN \
		--output text
}

provider_secret_arn="$(secret_arn "${AO_CLOUD_PROVIDER_SECRET_ID:-ao-cloud/staging/provider-secret-key}")"
nodeops_secret_arn="$(secret_arn "$NODEOPS_SECRET_ID")"
worker_secret_arn="$(secret_arn "$WORKER_SECRET_ID")"
broker_secret_arn="$(secret_arn "${AO_CLOUD_REPOSITORY_BROKER_SECRET_ID:-ao-cloud/repository-broker}")"
nodeops_settings="$(
	aws_cli secretsmanager get-secret-value \
		--secret-id "$NODEOPS_SECRET_ID" \
		--query SecretString \
		--output text
)"
worker_settings="$(
	aws_cli secretsmanager get-secret-value \
		--secret-id "$WORKER_SECRET_ID" \
		--query SecretString \
		--output text
)"
# Optional per-harness template mapping (JSON object as a string value in the
# nodeops secret). Plain env rather than an ECS secret ref so a missing key
# cannot block container start; empty mapping means every harness uses
# default_rootfs.
rootfs_by_harness="$(jq -r '.rootfs_by_harness // "{}"' <<<"$nodeops_settings")"
./scripts/validate-hosted-settings.py \
	--nodeops <(printf '%s' "$nodeops_settings") \
	--worker <(printf '%s' "$worker_settings")
unset nodeops_settings worker_settings

publish_image() {
	local repository="$1"
	local target="$2"
	PUBLISHED_URI="$(
		aws_cli ecr describe-repositories \
			--repository-names "$repository" \
			--query 'repositories[0].repositoryUri' \
			--output text
	)"
	local registry="${PUBLISHED_URI%%/*}"
	aws_cli ecr get-login-password |
		docker login --username AWS --password-stdin "$registry" >/dev/null
	if ! aws_cli ecr describe-images \
		--repository-name "$repository" \
		--image-ids "imageTag=${IMAGE_TAG}" >/dev/null 2>&1; then
		docker build \
			--platform linux/amd64 \
			--provenance=false \
			--target "$target" \
			--tag "${PUBLISHED_URI}:${IMAGE_TAG}" \
			.
		docker push "${PUBLISHED_URI}:${IMAGE_TAG}"
	fi
	PUBLISHED_DIGEST="$(
		aws_cli ecr describe-images \
			--repository-name "$repository" \
			--image-ids "imageTag=${IMAGE_TAG}" \
			--query 'imageDetails[0].imageDigest' \
			--output text
	)"
	PUBLISHED_IMAGE="${PUBLISHED_URI}@${PUBLISHED_DIGEST}"
}

scan_image() {
	local repository="$1"
	local digest="$2"
	local scan_status=""
	aws_cli ecr start-image-scan \
		--repository-name "$repository" \
		--image-id "imageDigest=${digest}" >/dev/null 2>&1 || true
	for _ in $(seq 1 60); do
		scan_status="$(
			aws_cli ecr describe-image-scan-findings \
				--repository-name "$repository" \
				--image-id "imageDigest=${digest}" \
				--query 'imageScanStatus.status' \
				--output text 2>/dev/null || true
		)"
		case "$scan_status" in
		COMPLETE) break ;;
		FAILED) echo "ECR image scan failed for ${repository}." >&2; exit 1 ;;
		esac
		sleep 2
	done
	if [[ "$scan_status" != "COMPLETE" ]]; then
		echo "ECR image scan did not complete for ${repository}." >&2
		exit 1
	fi

	local findings
	findings="$(
		aws_cli ecr describe-image-scan-findings \
			--repository-name "$repository" \
			--image-id "imageDigest=${digest}" \
			--query 'imageScanFindings.findings[?severity==`CRITICAL`||severity==`HIGH`].{name:name,severity:severity}' \
			--output json
	)"
	if ! FINDINGS="$findings" ALLOWLIST="$SCAN_CVE_ALLOWLIST" python3 - <<'PY'
import json
import os
import sys

findings = json.loads(os.environ.get("FINDINGS") or "[]")
allow = set(os.environ.get("ALLOWLIST", "").split())
blocking = [f for f in findings if f.get("name") not in allow]
for f in blocking:
    sys.stderr.write(f"blocking {f.get('severity')} {f.get('name')}\n")
sys.exit(1 if blocking else 0)
PY
	then
		echo "ECR scan found non-allowlisted critical or high vulnerabilities in ${repository}." >&2
		exit 1
	fi
}

publish_image "$CONTROL_REPOSITORY" control-plane
control_image="$PUBLISHED_IMAGE"
control_image_digest="$PUBLISHED_DIGEST"
publish_image "$WORKER_REPOSITORY" worker
worker_image="$PUBLISHED_IMAGE"
worker_image_digest="$PUBLISHED_DIGEST"
docker pull "$control_image" >/dev/null
docker pull "$worker_image" >/dev/null
./scripts/verify-image-contract.sh "$control_image" "$worker_image"
scan_image "$CONTROL_REPOSITORY" "$control_image_digest"
scan_image "$WORKER_REPOSITORY" "$worker_image_digest"

register_task_definition() {
	local family="$1"
	local container_name="$2"
	local source payload
	source="$(aws_cli ecs describe-task-definition --task-definition "$family" --include TAGS)"
	local render_args=(
		--family "$family"
		--container "$container_name"
		--image "$control_image"
		--release "$RELEASE"
		--environment staging
		--log-group /ao-cloud/staging/control-plane
		--region "$REGION"
	)
	if [[ "$container_name" == "control-plane" ]]; then
		render_args+=(
			--worker-image "$worker_image"
			--set-environment "AO_CLOUD_PUBLIC_URL=${AO_CLOUD_PUBLIC_URL:-https://staging-api.aoagents.dev}"
			--set-environment "AO_CLOUD_TERMINAL_STREAM=${AO_CLOUD_TERMINAL_STREAM:-}"
			--set-environment AO_CLOUD_REPOSITORY_BROKER_URL=https://api.aoagents.dev
			--set-environment AO_CLOUD_ALLOW_ANONYMOUS_GITHUB_CHECKOUT=true
			--set-environment "AO_CLOUD_NODEOPS_ROOTFS_BY_HARNESS=${rootfs_by_harness}"
			--set-secret "AO_CLOUD_PROVIDER_SECRET_KEY=${provider_secret_arn}"
			--set-secret "AO_CLOUD_REPOSITORY_BROKER_TOKEN=${broker_secret_arn}:auth_token::"
			--set-secret "AO_CLOUD_ENV_CONTROL_TOKEN=${broker_secret_arn}:staging_control_token::"
			--set-secret "AO_CLOUD_NODEOPS_BASE_URL=${nodeops_secret_arn}:base_url::"
			--set-secret "AO_CLOUD_NODEOPS_API_KEY=${nodeops_secret_arn}:api_key::"
			--set-secret "AO_CLOUD_NODEOPS_DEFAULT_SHAPE=${nodeops_secret_arn}:default_shape::"
			--set-secret "AO_CLOUD_NODEOPS_DEFAULT_ROOTFS=${nodeops_secret_arn}:default_rootfs::"
			--set-secret "AO_CLOUD_NODEOPS_INGRESS=${nodeops_secret_arn}:ingress::"
			--set-secret "AO_CLOUD_NODEOPS_SSH_KEY_PATH=${nodeops_secret_arn}:ssh_key_path::"
			--set-secret "AO_CLOUD_NODEOPS_REGION=${nodeops_secret_arn}:region::"
			--set-secret "AO_CLOUD_NODEOPS_WORKER_TOKEN_TTL=${nodeops_secret_arn}:worker_token_ttl::"
			--set-secret "AO_CLOUD_WORKER_SIGNING_KEY=${worker_secret_arn}:signing_key::"
			--set-secret "AO_CLOUD_MAX_ACTIVE_SANDBOXES_PER_ORG=${worker_secret_arn}:max_active_sandboxes_per_org::"
			--set-secret "AO_CLOUD_SANDBOX_RECONCILE_INTERVAL=${worker_secret_arn}:sandbox_reconcile_interval::"
			--set-secret "AO_CLOUD_SANDBOX_STARTUP_TIMEOUT=${worker_secret_arn}:sandbox_startup_timeout::"
			--set-secret "AO_CLOUD_WORKER_HEARTBEAT_TIMEOUT=${worker_secret_arn}:worker_heartbeat_timeout::"
		)
	else
		render_args+=(--runtime-database-user "$RUNTIME_DATABASE_USER")
	fi
	payload="$(printf '%s' "$source" | ./scripts/render-task-definition.py "${render_args[@]}")"
	aws_cli ecs register-task-definition \
		--cli-input-json "$payload" \
		--query 'taskDefinition.taskDefinitionArn' \
		--output text
}

api_task="$(register_task_definition "$API_FAMILY" control-plane)"
migration_task="$(register_task_definition "$MIGRATION_FAMILY" migration)"
network_configuration="$(
	aws_cli ecs describe-services \
		--cluster "$CLUSTER" \
		--services "$SERVICE" \
		--query 'services[0].networkConfiguration' \
		--output json
)"
migration_result="$(
	aws_cli ecs run-task \
		--cluster "$CLUSTER" \
		--launch-type FARGATE \
		--platform-version LATEST \
		--task-definition "$migration_task" \
		--network-configuration "$network_configuration" \
		--started-by "deploy-${RELEASE:0:28}" \
		--tags \
			key=Project,value=ao-cloud \
			key=Environment,value="${AO_CLOUD_DEPLOY_ENVIRONMENT:-staging}" \
			"key=Release,value=${RELEASE}"
)"
migration_arn="$(
	MIGRATION_RESULT="$migration_result" python3 - <<'PY'
import json
import os

result = json.loads(os.environ["MIGRATION_RESULT"])
if result.get("failures") or not result.get("tasks"):
    raise SystemExit("ECS refused to start the migration task")
print(result["tasks"][0]["taskArn"])
PY
)"
cleanup_migration() {
	if [[ -n "${migration_arn:-}" && "${migration_complete:-false}" != "true" ]]; then
		aws_cli ecs stop-task \
			--cluster "$CLUSTER" \
			--task "$migration_arn" \
			--reason "AO Cloud deployment interrupted before migration completion" \
			>/dev/null 2>&1 || true
		echo "Stopped incomplete migration task ${migration_arn}." >&2
	fi
}
trap cleanup_migration EXIT
aws_cli ecs wait tasks-stopped --cluster "$CLUSTER" --tasks "$migration_arn"
migration_exit="$(
	aws_cli ecs describe-tasks \
		--cluster "$CLUSTER" \
		--tasks "$migration_arn" \
		--query 'tasks[0].containers[0].exitCode' \
		--output text
)"
if [[ "$migration_exit" != "0" ]]; then
	aws_cli ecs describe-tasks \
		--cluster "$CLUSTER" \
		--tasks "$migration_arn" \
		--query 'tasks[0].{reason:stoppedReason,containerReason:containers[0].reason}' \
		--output json >&2
	exit 1
fi
migration_complete=true

aws_cli ecs update-service \
	--cluster "$CLUSTER" \
	--service "$SERVICE" \
	--task-definition "$api_task" \
	--desired-count 2 \
	--health-check-grace-period-seconds 60 \
	--deployment-configuration \
	"{\"maximumPercent\":200,\"minimumHealthyPercent\":100,\"deploymentCircuitBreaker\":{\"enable\":true,\"rollback\":true},\"alarms\":{\"alarmNames\":[\"${ROLLBACK_ALARM}\"],\"enable\":true,\"rollback\":true}}" \
	>/dev/null
aws_cli ecs wait services-stable --cluster "$CLUSTER" --services "$SERVICE"

deployed_task="$(
	aws_cli ecs describe-services \
		--cluster "$CLUSTER" \
		--services "$SERVICE" \
		--query 'services[0].taskDefinition' \
		--output text
)"
if [[ "$deployed_task" != "$api_task" ]]; then
	echo "ECS rolled back instead of deploying ${api_task}." >&2
	exit 1
fi
verification_error=""
for _ in $(seq 1 18); do
	if verification_error="$(
		./scripts/verify-ecs-service.py \
			--region "$REGION" \
			--cluster "$CLUSTER" \
			--service "$SERVICE" \
			--alarm "$ROLLBACK_ALARM" \
			--expected-task-definition "$api_task" \
			--expected-control-image "$control_image" \
			--expected-worker-image "$worker_image" 2>&1
	)"; then
		verification_error=""
		break
	fi
	sleep 10
done
if [[ -n "$verification_error" ]]; then
	echo "$verification_error" >&2
	exit 1
fi
trap - EXIT

printf 'Deployed release %s\nControl-plane digest: %s\nWorker digest: %s\nTask definition: %s\n' \
	"$RELEASE" \
	"$control_image_digest" \
	"$worker_image_digest" \
	"$api_task"
