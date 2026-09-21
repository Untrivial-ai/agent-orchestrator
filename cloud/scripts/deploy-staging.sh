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
SANDBOX_PROVIDER="${AO_CLOUD_SANDBOX_PROVIDER:-nodeops}"
# Every sandbox provider this control plane serves. Defaults to the single
# primary; a multi-provider CP (for example nodeops,coder) plumbs and preserves
# all their secrets so a deploy never drops an inactive provider's credentials.
PROVIDERS="${AO_CLOUD_SANDBOX_PROVIDERS:-$SANDBOX_PROVIDER}"
NODEOPS_SECRET_ID="${AO_CLOUD_NODEOPS_SECRET_ID:-ao-cloud/staging/nodeops}"
CODER_SECRET_ID="${AO_CLOUD_CODER_SECRET_ID:-ao-cloud/staging/coder}"
WORKER_SECRET_ID="${AO_CLOUD_WORKER_SECRET_ID:-ao-cloud/staging/worker}"
# ECS-on-EC2 (Graviton) sandbox provider. Enabled as an ADDITIONAL provider
# alongside the primary SANDBOX_PROVIDER, opt-in via AO_CLOUD_ECS_ENABLED=1. It
# needs no secrets (the control-plane task role carries the RunTask permissions).
# scripts/provision-ecs-sandbox.sh creates the cluster, capacity provider and the
# task-definition family named here before this is turned on.
ECS_ENABLED="${AO_CLOUD_ECS_ENABLED:-0}"
ECS_SANDBOX_CLUSTER="${AO_CLOUD_ECS_SANDBOX_CLUSTER:-ao-cloud-staging-sandboxes}"
ECS_SANDBOX_TASK_FAMILY="${AO_CLOUD_ECS_SANDBOX_TASK_FAMILY:-ao-cloud-sandbox-worker}"
ECS_SANDBOX_CONTAINER_NAME="${AO_CLOUD_ECS_SANDBOX_CONTAINER_NAME:-worker}"
ECS_SANDBOX_CAPACITY_PROVIDER="${AO_CLOUD_ECS_SANDBOX_CAPACITY_PROVIDER:-ao-cloud-sandbox-graviton}"
ECS_SANDBOX_NAMESPACE="${AO_CLOUD_ECS_SANDBOX_NAMESPACE:-ao-cloud}"
HEAD_SHA="$(git rev-parse HEAD)"
RELEASE="${1:-$HEAD_SHA}"
IMAGE_TAG="${RELEASE//+/-}-linux-amd64"

# CVE allowlist shared with promote-production.sh. These CRITICAL/HIGH findings
# are unpatched 2026 CVEs in base OS packages (perl, libssh2, expat, openssl, util-linux, curl, zlib) that git
# pulls into the worker image; no fix exists in Debian 12 or 13 yet. The worker
# runs inside an ephemeral, single-tenant, isolated provider workspace that
# already executes untrusted agent code. Keep this list in sync with production
# promote. Remove entries as Debian ships fixes; the worker stage's apt-get
# upgrade then clears them on rebuild. CVE-2026-14456 is a scanner false
# positive for Debian's OpenSSL 3.0: its QUIC listener was introduced in 3.5.
# The curl (CVE-2026-8927 cross-proxy Digest leak, CVE-2026-8924 trailing-dot
# super cookie, CVE-2026-8286 STARTTLS reuse) and zlib (CVE-2026-85091 non-blocking
# gzwrite overflow) findings are base OS packages with no Debian fix yet, and the
# worker does not exercise their vulnerable paths (no proxy/cookie/STARTTLS curl
# use; git uses zlib inflate/deflate, not the gz file API). CVE-2026-8286 and
# CVE-2026-85091 are fixed upstream, pending a Debian backport.
SCAN_CVE_ALLOWLIST="${AO_CLOUD_SCAN_CVE_ALLOWLIST:-CVE-2026-57432 CVE-2026-45186 CVE-2026-12087 CVE-2025-15661 CVE-2026-58051 CVE-2026-7017 CVE-2026-48962 CVE-2026-57433 CVE-2026-66032 CVE-2026-48961 CVE-2026-48959 CVE-2026-66034 CVE-2026-58050 CVE-2026-13221 CVE-2026-14456 CVE-2026-66046 CVE-2026-63076 CVE-2026-53615 CVE-2026-54874 CVE-2026-63072 CVE-2026-8927 CVE-2026-8924 CVE-2026-8286 CVE-2026-85091}"

AWS_OPTIONS=(--region "$REGION")
if [[ -n "${AWS_PROFILE:-}" ]]; then
	AWS_OPTIONS+=(--profile "$AWS_PROFILE")
fi

aws_cli() {
	aws "${AWS_OPTIONS[@]}" "$@"
}

# providers_has reports whether $1 is one of the comma-separated PROVIDERS.
providers_has() {
	[[ ",${PROVIDERS}," == *",$1,"* ]]
}

if [[ -n "$(git status --porcelain)" ]]; then
	echo "Refusing to deploy a dirty working tree." >&2
	exit 1
fi
if [[ ! "$RELEASE" =~ ^[A-Za-z0-9][A-Za-z0-9._+-]{0,199}$ ]]; then
	echo "Release must be a Git SHA or release tag." >&2
	exit 1
fi
if [[ "$SANDBOX_PROVIDER" != "nodeops" && "$SANDBOX_PROVIDER" != "coder" ]]; then
	echo "AO_CLOUD_SANDBOX_PROVIDER must be nodeops or coder." >&2
	exit 1
fi
IFS=',' read -ra _providers_list <<<"$PROVIDERS"
for _provider in "${_providers_list[@]}"; do
	if [[ "$_provider" != "nodeops" && "$_provider" != "coder" ]]; then
		echo "AO_CLOUD_SANDBOX_PROVIDERS entries must each be nodeops or coder, got: $_provider" >&2
		exit 1
	fi
done
if ! providers_has "$SANDBOX_PROVIDER"; then
	echo "AO_CLOUD_SANDBOX_PROVIDER ($SANDBOX_PROVIDER) must be one of AO_CLOUD_SANDBOX_PROVIDERS ($PROVIDERS)." >&2
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
worker_secret_arn="$(secret_arn "$WORKER_SECRET_ID")"
broker_secret_arn="$(secret_arn "${AO_CLOUD_REPOSITORY_BROKER_SECRET_ID:-ao-cloud/repository-broker}")"
worker_settings="$(
	aws_cli secretsmanager get-secret-value \
		--secret-id "$WORKER_SECRET_ID" \
		--query SecretString \
		--output text
)"
# Resolve, validate, and later plumb the secrets for every provider this control
# plane serves. A multi-provider deploy (nodeops,coder) keeps both providers'
# secrets instead of pruning the inactive one.
if providers_has nodeops; then
	nodeops_secret_arn="$(secret_arn "$NODEOPS_SECRET_ID")"
	nodeops_settings="$(
		aws_cli secretsmanager get-secret-value \
			--secret-id "$NODEOPS_SECRET_ID" \
			--query SecretString \
			--output text
	)"
	./scripts/validate-hosted-settings.py \
		--nodeops <(printf '%s' "$nodeops_settings") \
		--worker <(printf '%s' "$worker_settings")
	# Optional per-harness template mapping. It remains plaintext so a missing
	# optional JSON key cannot prevent the ECS container from starting.
	rootfs_by_harness="$(jq -r '.rootfs_by_harness // "{}"' <<<"$nodeops_settings")"
	unset nodeops_settings
fi
if providers_has coder; then
	coder_secret_arn="$(secret_arn "$CODER_SECRET_ID")"
	coder_settings="$(
		aws_cli secretsmanager get-secret-value \
			--secret-id "$CODER_SECRET_ID" \
			--query SecretString \
			--output text
	)"
	./scripts/validate-hosted-settings.py \
		--coder <(printf '%s' "$coder_settings") \
		--worker <(printf '%s' "$worker_settings")
	unset coder_settings
fi
unset worker_settings

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

# Coder bakes ao-worker into its workspace image, so a release that changes the
# worker must rebake that image or every spawn and resume falls back to the slow
# PTY upload (internal/sandbox/coder/client.go preinstalledCheck). Rebuild and
# publish it here, before the rollout, from the exact control-plane digest so the
# baked binaries are byte-identical to what the reconciler advertises
# (AO_WORKER_EXPECTED_SHA256). The nodeops path is unaffected: its template is
# published out of band by scripts/publish-nodeops-template.sh.
if providers_has coder; then
	AWS_REGION="$REGION" \
		AO_CLOUD_CP_IMAGE="$control_image" \
		AO_CLOUD_CODER_SECRET_ID="$CODER_SECRET_ID" \
		AO_CLOUD_CODER_WORKSPACE_IMAGE_TAG="${IMAGE_TAG}" \
		./scripts/publish-coder-workspace.sh
fi

# The ECS sandbox worker runs on Graviton and does not self-update, so its arm64
# image must be rebuilt from this exact commit and a fresh task-definition
# revision registered before the rollout. The control plane runs the family's
# latest revision (it stores the family name, not a pinned revision), so this
# stays in step with each release without touching the control-plane env.
if [[ "$ECS_ENABLED" == "1" ]]; then
	AWS_REGION="$REGION" \
		AO_CLOUD_WORKER_ECR_REPOSITORY="$WORKER_REPOSITORY" \
		AO_CLOUD_ECS_TASK_FAMILY="$ECS_SANDBOX_TASK_FAMILY" \
		AO_CLOUD_ECS_CONTAINER_NAME="$ECS_SANDBOX_CONTAINER_NAME" \
		./scripts/build-ecs-sandbox-image.sh "$RELEASE"
fi

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
		--sandbox-provider "$SANDBOX_PROVIDER"
		--sandbox-providers "$PROVIDERS"
	)
	if [[ "$container_name" == "control-plane" ]]; then
		render_args+=(
			--worker-image "$worker_image"
			--set-environment "AO_CLOUD_PUBLIC_URL=${AO_CLOUD_PUBLIC_URL:-https://staging-api.aoagents.dev}"
			--set-environment AO_CLOUD_TERMINAL_STREAM=1
			--set-environment AO_CLOUD_TERMINAL_RELAY=1
			--set-environment AO_CLOUD_REPOSITORY_BROKER_URL=https://api.aoagents.dev
			--set-environment AO_CLOUD_ALLOW_ANONYMOUS_GITHUB_CHECKOUT=true
			--set-secret "AO_CLOUD_PROVIDER_SECRET_KEY=${provider_secret_arn}"
			--set-secret "AO_CLOUD_REPOSITORY_BROKER_TOKEN=${broker_secret_arn}:auth_token::"
			--set-secret "AO_CLOUD_ENV_CONTROL_TOKEN=${broker_secret_arn}:staging_control_token::"
			--set-secret "AO_CLOUD_WORKER_SIGNING_KEY=${worker_secret_arn}:signing_key::"
			--set-secret "AO_CLOUD_MAX_ACTIVE_SANDBOXES_PER_ORG=${worker_secret_arn}:max_active_sandboxes_per_org::"
			--set-secret "AO_CLOUD_SANDBOX_RECONCILE_INTERVAL=${worker_secret_arn}:sandbox_reconcile_interval::"
			--set-secret "AO_CLOUD_SANDBOX_STARTUP_TIMEOUT=${worker_secret_arn}:sandbox_startup_timeout::"
			--set-secret "AO_CLOUD_WORKER_HEARTBEAT_TIMEOUT=${worker_secret_arn}:worker_heartbeat_timeout::"
		)
		if [[ "$ECS_ENABLED" == "1" ]]; then
			# ECS is an additional, secretless provider (the control-plane task role
			# carries the RunTask permissions). The render script stamps
			# AO_CLOUD_SANDBOX_PROVIDERS from the secret-bearing providers only, so
			# this override re-adds it with ecs appended (keeping every secret
			# provider), makes ecs the default new sessions land on (our own infra),
			# and passes the ECS provider config. SANDBOX_PROVIDER stays the
			# secret-plumbing primary so coder keeps working for 11x.
			render_args+=(
				--set-environment "AO_CLOUD_SANDBOX_PROVIDERS=${PROVIDERS},ecs"
				--set-environment "AO_CLOUD_SANDBOX_DEFAULT_PROVIDER=ecs"
				--set-environment "AO_CLOUD_ECS_REGION=${REGION}"
				--set-environment "AO_CLOUD_ECS_CLUSTER=${ECS_SANDBOX_CLUSTER}"
				--set-environment "AO_CLOUD_ECS_TASK_DEFINITION=${ECS_SANDBOX_TASK_FAMILY}"
				--set-environment "AO_CLOUD_ECS_CONTAINER_NAME=${ECS_SANDBOX_CONTAINER_NAME}"
				--set-environment "AO_CLOUD_ECS_CAPACITY_PROVIDER=${ECS_SANDBOX_CAPACITY_PROVIDER}"
				--set-environment "AO_CLOUD_ECS_NAMESPACE=${ECS_SANDBOX_NAMESPACE}"
			)
		fi
		if providers_has coder; then
			render_args+=(
				--set-secret "AO_CLOUD_CODER_URL=${coder_secret_arn}:url::"
				--set-secret "AO_CLOUD_CODER_TOKEN=${coder_secret_arn}:token::"
				--set-secret "AO_CLOUD_CODER_OWNER=${coder_secret_arn}:owner::"
				--set-secret "AO_CLOUD_CODER_TEMPLATE_ID=${coder_secret_arn}:template_id::"
				--set-secret "AO_CLOUD_CODER_AGENT_NAME=${coder_secret_arn}:agent_name::"
				--set-secret "AO_CLOUD_CODER_PARAMETERS_JSON=${coder_secret_arn}:parameters_json::"
				--set-secret "AO_CLOUD_CODER_DURABLE_ROOT=${coder_secret_arn}:durable_root::"
				--set-secret "AO_CLOUD_CODER_WORKER_TOKEN_TTL=${coder_secret_arn}:worker_token_ttl::"
			)
		fi
		if providers_has nodeops; then
			render_args+=(
				--set-environment "AO_CLOUD_NODEOPS_ROOTFS_BY_HARNESS=${rootfs_by_harness}"
				--set-secret "AO_CLOUD_NODEOPS_BASE_URL=${nodeops_secret_arn}:base_url::"
				--set-secret "AO_CLOUD_NODEOPS_API_KEY=${nodeops_secret_arn}:api_key::"
				--set-secret "AO_CLOUD_NODEOPS_DEFAULT_SHAPE=${nodeops_secret_arn}:default_shape::"
				--set-secret "AO_CLOUD_NODEOPS_DEFAULT_ROOTFS=${nodeops_secret_arn}:default_rootfs::"
				--set-secret "AO_CLOUD_NODEOPS_INGRESS=${nodeops_secret_arn}:ingress::"
				--set-secret "AO_CLOUD_NODEOPS_SSH_KEY_PATH=${nodeops_secret_arn}:ssh_key_path::"
				--set-secret "AO_CLOUD_NODEOPS_REGION=${nodeops_secret_arn}:region::"
				--set-secret "AO_CLOUD_NODEOPS_WORKER_TOKEN_TTL=${nodeops_secret_arn}:worker_token_ttl::"
			)
		fi
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
	--desired-count 1 \
	--health-check-grace-period-seconds 60 \
	--deployment-configuration \
	"{\"maximumPercent\":100,\"minimumHealthyPercent\":0,\"deploymentCircuitBreaker\":{\"enable\":true,\"rollback\":true},\"alarms\":{\"alarmNames\":[\"${ROLLBACK_ALARM}\"],\"enable\":true,\"rollback\":true}}" \
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
