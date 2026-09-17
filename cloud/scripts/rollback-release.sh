#!/usr/bin/env bash
set -euo pipefail

environment="${1:-}"
target_task="${2:-}"
region="${AWS_REGION:-eu-north-1}"
if [[ "$environment" != "staging" && "$environment" != "production" ]]; then
	echo "Usage: rollback-release.sh <staging|production> <task-definition>" >&2
	exit 2
fi
if [[ -z "$target_task" ]]; then
	echo "A target task-definition ARN or revision is required." >&2
	exit 2
fi
if [[ "$environment" == "production" &&
	"${AO_CLOUD_APPROVE_PRODUCTION_ROLLBACK:-}" != "1" ]]; then
	echo "Set AO_CLOUD_APPROVE_PRODUCTION_ROLLBACK=1 to approve production rollback." >&2
	exit 1
fi

cluster="ao-cloud-${environment}"
service="ao-cloud-${environment}-api"
family="ao-cloud-${environment}-api"
alarm="ao-cloud-${environment}-target-5xx"
aws_options=(--region "$region")
if [[ -n "${AWS_PROFILE:-}" ]]; then
	aws_options+=(--profile "$AWS_PROFILE")
fi
aws_cli() {
	aws "${aws_options[@]}" "$@"
}

./scripts/verify-ecs-service.py \
	--region "$region" \
	--cluster "$cluster" \
	--service "$service" \
	--alarm "$alarm" >/dev/null

target_source="$(
	aws_cli ecs describe-task-definition \
		--task-definition "$target_task" \
		--query taskDefinition \
		--output json
)"
target_arn="$(
	SOURCE="$target_source" FAMILY="$family" python3 - <<'PY'
import json
import os

source = json.loads(os.environ["SOURCE"])
if source["family"] != os.environ["FAMILY"]:
    raise SystemExit("target task definition belongs to the wrong environment")
container = next(
    item for item in source["containerDefinitions"]
    if item["name"] == "control-plane"
)
if "@sha256:" not in container["image"]:
    raise SystemExit("target task definition is not digest pinned")
print(source["taskDefinitionArn"])
PY
)"
current_arn="$(
	aws_cli ecs describe-services \
		--cluster "$cluster" \
		--services "$service" \
		--query 'services[0].taskDefinition' \
		--output text
)"
if [[ "$current_arn" == "$target_arn" ]]; then
	echo "${environment} already runs ${target_arn}."
	exit 0
fi

printf 'Environment: %s\nCurrent: %s\nTarget: %s\n' \
	"$environment" "$current_arn" "$target_arn"
aws_cli ecs update-service \
	--cluster "$cluster" \
	--service "$service" \
	--task-definition "$target_arn" \
	--deployment-configuration \
	"{\"maximumPercent\":200,\"minimumHealthyPercent\":100,\"deploymentCircuitBreaker\":{\"enable\":true,\"rollback\":true},\"alarms\":{\"alarmNames\":[\"${alarm}\"],\"enable\":true,\"rollback\":true}}" \
	>/dev/null
aws_cli ecs wait services-stable --cluster "$cluster" --services "$service"
./scripts/verify-ecs-service.py \
	--region "$region" \
	--cluster "$cluster" \
	--service "$service" \
	--alarm "$alarm" \
	--expected-task-definition "$target_arn" >/dev/null
printf 'Rolled %s back to %s. Database migrations were not reversed.\n' \
	"$environment" "$target_arn"
