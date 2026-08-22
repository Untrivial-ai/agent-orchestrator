#!/usr/bin/env bash
set -euo pipefail

umask 077

AWS_REGION="${AWS_REGION:-us-west-2}"
AWS_DEFAULT_REGION="${AWS_DEFAULT_REGION:-$AWS_REGION}"
AWS_RETRY_MODE="${AWS_RETRY_MODE:-standard}"
AWS_MAX_ATTEMPTS="${AWS_MAX_ATTEMPTS:-10}"
ENVIRONMENT="${AO_CLOUD_ENVIRONMENT:-staging}"
GOOGLE_CLIENT_IDS="${AO_CLOUD_GOOGLE_CLIENT_IDS:-}"
ALLOWED_EMAILS="${AO_CLOUD_ALLOWED_EMAILS:-}"
DAYTONA_API_KEY_VALUE="${DAYTONA_API_KEY:-}"
DAYTONA_API_URL_VALUE="${DAYTONA_API_URL:-https://app.daytona.io/api}"
DAYTONA_TARGET_VALUE="${DAYTONA_TARGET:-us}"
GITHUB_TOKEN_BASE64="${AO_CLOUD_GITHUB_TOKEN_BASE64:-}"
SOURCE_COMMIT="${AO_CLOUD_SOURCE_COMMIT:-$(git rev-parse HEAD)}"
IMAGE_TAG="${AO_CLOUD_IMAGE_TAG:-${SOURCE_COMMIT:0:12}}"
ACCOUNT_ID="$(aws sts get-caller-identity --query Account --output text)"
STATE_BUCKET="${AO_CLOUD_TERRAFORM_STATE_BUCKET:-ao-cloud-tfstate-${ACCOUNT_ID}-${AWS_REGION}}"
STATE_KEY="${AO_CLOUD_TERRAFORM_STATE_KEY:-${ENVIRONMENT}/terraform.tfstate}"
TERRAFORM_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/terraform" && pwd)"
PLACEHOLDER_IMAGE="public.ecr.aws/docker/library/busybox:1.36"

export AWS_REGION AWS_DEFAULT_REGION AWS_RETRY_MODE AWS_MAX_ATTEMPTS

if [[ -z "$GOOGLE_CLIENT_IDS" ]]; then
  echo "AO_CLOUD_GOOGLE_CLIENT_IDS is required" >&2
  exit 2
fi
if [[ -z "$ALLOWED_EMAILS" ]]; then
  echo "AO_CLOUD_ALLOWED_EMAILS is required" >&2
  exit 2
fi
for command_name in aws curl git jq openssl terraform; do
  if ! command -v "$command_name" >/dev/null; then
    echo "$command_name is required" >&2
    exit 2
  fi
done

temporary_directory="$(mktemp -d /tmp/ao-cloud-deploy.XXXXXX)"
cleanup() {
  case "$temporary_directory" in
    /tmp/ao-cloud-deploy.*) rm -r -- "$temporary_directory" ;;
  esac
}
trap cleanup EXIT

ensure_state_bucket() {
  if aws s3api head-bucket --bucket "$STATE_BUCKET" >/dev/null 2>&1; then
    return
  fi
  if [[ "$AWS_REGION" == "us-east-1" ]]; then
    aws s3api create-bucket --bucket "$STATE_BUCKET" >/dev/null
  else
    aws s3api create-bucket \
      --bucket "$STATE_BUCKET" \
      --create-bucket-configuration "LocationConstraint=$AWS_REGION" >/dev/null
  fi
  aws s3api put-public-access-block \
    --bucket "$STATE_BUCKET" \
    --public-access-block-configuration \
      'BlockPublicAcls=true,IgnorePublicAcls=true,BlockPublicPolicy=true,RestrictPublicBuckets=true'
  aws s3api put-bucket-versioning \
    --bucket "$STATE_BUCKET" \
    --versioning-configuration Status=Enabled
  aws s3api put-bucket-encryption \
    --bucket "$STATE_BUCKET" \
    --server-side-encryption-configuration \
      '{"Rules":[{"ApplyServerSideEncryptionByDefault":{"SSEAlgorithm":"AES256"}}]}'
  aws s3api put-bucket-tagging \
    --bucket "$STATE_BUCKET" \
    --tagging 'TagSet=[{Key=Project,Value=ao-cloud},{Key=Environment,Value=staging},{Key=ManagedBy,Value=deploy-staging}]'
}

terraform_apply() {
  local image="$1"
  local enabled="$2"
  terraform -chdir="$TERRAFORM_DIR" apply -auto-approve \
    -var "aws_region=$AWS_REGION" \
    -var "environment=$ENVIRONMENT" \
    -var "control_plane_image=$image" \
    -var "deployment_enabled=$enabled"
}

terraform_output() {
  terraform -chdir="$TERRAFORM_DIR" output -raw "$1"
}

ensure_state_bucket
terraform -chdir="$TERRAFORM_DIR" init -reconfigure \
  -backend-config="bucket=$STATE_BUCKET" \
  -backend-config="key=$STATE_KEY" \
  -backend-config="region=$AWS_REGION" \
  -backend-config="encrypt=true" \
  -backend-config="use_lockfile=true"

if ! terraform -chdir="$TERRAFORM_DIR" state show aws_codebuild_project.control_plane >/dev/null 2>&1; then
  terraform_apply "$PLACEHOLDER_IMAGE" false
fi

database_secret_arn="$(terraform_output database_secret_arn)"
application_secret_arn="$(terraform_output application_secret_arn)"
master_secret_arn="$(terraform -chdir="$TERRAFORM_DIR" output -raw rds_master_secret_arn)"
database_address="$(terraform_output database_address)"
database_name="$(terraform_output database_name)"
runtime_user="$(terraform_output database_runtime_user)"

master_secret="$(aws secretsmanager get-secret-value \
  --secret-id "$master_secret_arn" \
  --query SecretString \
  --output text)"
owner_user="$(jq -er '.username' <<<"$master_secret")"
owner_password="$(jq -er '.password' <<<"$master_secret")"

existing_database_secret="$(aws secretsmanager get-secret-value \
  --secret-id "$database_secret_arn" \
  --query SecretString \
  --output text 2>/dev/null || true)"
runtime_password="$(jq -r '.runtimePassword // empty' <<<"${existing_database_secret:-{}}" 2>/dev/null || true)"
if [[ -z "$runtime_password" ]]; then
  runtime_password="$(openssl rand -base64 48 | tr -d '\n')"
fi
owner_password_encoded="$(jq -rn --arg value "$owner_password" '$value|@uri')"
runtime_password_encoded="$(jq -rn --arg value "$runtime_password" '$value|@uri')"
migration_url="postgres://${owner_user}:${owner_password_encoded}@${database_address}:5432/${database_name}?sslmode=require"
runtime_url="postgres://${runtime_user}:${runtime_password_encoded}@${database_address}:5432/${database_name}?sslmode=require"
jq -n \
  --arg migrationUrl "$migration_url" \
  --arg runtimeUrl "$runtime_url" \
  --arg runtimePassword "$runtime_password" \
  '{migrationUrl:$migrationUrl,runtimeUrl:$runtimeUrl,runtimePassword:$runtimePassword}' \
  >"$temporary_directory/database.json"
aws secretsmanager put-secret-value \
  --secret-id "$database_secret_arn" \
  --secret-string "file://$temporary_directory/database.json" >/dev/null

existing_application_secret="$(aws secretsmanager get-secret-value \
  --secret-id "$application_secret_arn" \
  --query SecretString \
  --output text 2>/dev/null || true)"
access_token_key="$(jq -r '.accessTokenKeyBase64 // empty' <<<"${existing_application_secret:-{}}" 2>/dev/null || true)"
if [[ -z "$access_token_key" ]]; then
  access_token_key="$(openssl rand -base64 32 | tr -d '\n')"
fi
if [[ -z "$DAYTONA_API_KEY_VALUE" ]]; then
  DAYTONA_API_KEY_VALUE="$(jq -r '.daytonaApiKey // empty' <<<"${existing_application_secret:-{}}" 2>/dev/null || true)"
fi
if [[ -z "$GITHUB_TOKEN_BASE64" ]]; then
  GITHUB_TOKEN_BASE64="$(jq -r '.githubTokenBase64 // empty' <<<"${existing_application_secret:-{}}" 2>/dev/null || true)"
fi
if [[ -z "$DAYTONA_API_KEY_VALUE" || -z "$GITHUB_TOKEN_BASE64" ]]; then
  echo "DAYTONA_API_KEY and AO_CLOUD_GITHUB_TOKEN_BASE64 are required for workspace provisioning" >&2
  exit 2
fi
jq -n \
  --arg googleClientIds "$GOOGLE_CLIENT_IDS" \
  --arg allowedEmails "$ALLOWED_EMAILS" \
  --arg accessTokenKeyBase64 "$access_token_key" \
  --arg daytonaApiKey "$DAYTONA_API_KEY_VALUE" \
  --arg daytonaApiUrl "$DAYTONA_API_URL_VALUE" \
  --arg daytonaTarget "$DAYTONA_TARGET_VALUE" \
  --arg githubTokenBase64 "$GITHUB_TOKEN_BASE64" \
  '{googleClientIds:$googleClientIds,allowedEmails:$allowedEmails,accessTokenKeyBase64:$accessTokenKeyBase64,daytonaApiKey:$daytonaApiKey,daytonaApiUrl:$daytonaApiUrl,daytonaTarget:$daytonaTarget,githubTokenBase64:$githubTokenBase64}' \
  >"$temporary_directory/application.json"
aws secretsmanager put-secret-value \
  --secret-id "$application_secret_arn" \
  --secret-string "file://$temporary_directory/application.json" >/dev/null

repository_url="$(terraform_output ecr_repository_url)"
repository_name="${repository_url#*/}"
if ! aws ecr describe-images \
  --repository-name "$repository_name" \
  --image-ids "imageTag=$IMAGE_TAG" >/dev/null 2>&1; then
  codebuild_project="$(terraform_output codebuild_project)"
  build_id="$(aws codebuild start-build \
    --project-name "$codebuild_project" \
    --environment-variables-override \
      "name=SOURCE_COMMIT,value=$SOURCE_COMMIT,type=PLAINTEXT" \
      "name=IMAGE_TAG,value=$IMAGE_TAG,type=PLAINTEXT" \
    --query 'build.id' \
    --output text)"
  while true; do
    build_status="$(aws codebuild batch-get-builds \
      --ids "$build_id" \
      --query 'builds[0].buildStatus' \
      --output text)"
    case "$build_status" in
      SUCCEEDED) break ;;
      FAILED|FAULT|STOPPED|TIMED_OUT)
        aws codebuild batch-get-builds \
          --ids "$build_id" \
          --query 'builds[0].logs.deepLink' \
          --output text >&2
        exit 1
        ;;
    esac
    sleep 10
  done
fi

image_digest="$(aws ecr describe-images \
  --repository-name "$repository_name" \
  --image-ids "imageTag=$IMAGE_TAG" \
  --query 'imageDetails[0].imageDigest' \
  --output text)"
image_reference="${repository_url}@${image_digest}"

# Staging intentionally scales to zero while its migration task runs. The
# production deployment will use an expand/migrate/contract rollout instead.
terraform_apply "$image_reference" false

cluster="$(terraform_output ecs_cluster)"
migration_task="$(terraform_output migration_task_definition_arn)"
security_group="$(terraform_output ecs_security_group_id)"
subnets="$(terraform -chdir="$TERRAFORM_DIR" output -json public_subnet_ids | jq -r 'join(",")')"
run_task_output="$(aws ecs run-task \
  --cluster "$cluster" \
  --task-definition "$migration_task" \
  --launch-type FARGATE \
  --network-configuration \
    "awsvpcConfiguration={subnets=[$subnets],securityGroups=[$security_group],assignPublicIp=ENABLED}")"
task_arn="$(jq -r '.tasks[0].taskArn // empty' <<<"$run_task_output")"
if [[ -z "$task_arn" ]]; then
  jq '.failures' <<<"$run_task_output" >&2
  exit 1
fi
aws ecs wait tasks-stopped --cluster "$cluster" --tasks "$task_arn"
migration_exit_code="$(aws ecs describe-tasks \
  --cluster "$cluster" \
  --tasks "$task_arn" \
  --query 'tasks[0].containers[0].exitCode' \
  --output text)"
if [[ "$migration_exit_code" != "0" ]]; then
  aws logs tail "/ao-cloud/$ENVIRONMENT/control-plane" --since 15m >&2 || true
  exit 1
fi

terraform_apply "$image_reference" true

service="$(terraform_output ecs_service)"
aws ecs update-service \
  --cluster "$cluster" \
  --service "$service" \
  --force-new-deployment >/dev/null
aws ecs wait services-stable --cluster "$cluster" --services "$service"
api_url="$(terraform_output api_url)"
api_url="${api_url%/}"

for attempt in $(seq 1 60); do
  if curl --silent --show-error --fail "$api_url/readyz" >"$temporary_directory/ready.json"; then
    break
  fi
  if [[ "$attempt" == "60" ]]; then
    echo "control plane did not become ready" >&2
    exit 1
  fi
  sleep 5
done
jq -e '.status == "ready"' "$temporary_directory/ready.json" >/dev/null
curl --silent --show-error --fail "$api_url/healthz" | jq -e '.status == "ok"' >/dev/null

invalid_google_status="$(curl --silent --output "$temporary_directory/google.json" --write-out '%{http_code}' \
  --header 'Content-Type: application/json' \
  --data '{"idToken":"invalid"}' \
  "$api_url/api/cloud/v1/auth/google")"
[[ "$invalid_google_status" == "401" ]]
jq -e '.code == "INVALID_GOOGLE_ID_TOKEN"' "$temporary_directory/google.json" >/dev/null

for route in refresh logout; do
  status="$(curl --silent --output "$temporary_directory/$route.json" --write-out '%{http_code}' \
    --header 'Content-Type: application/json' \
    --data '{"refreshToken":"malformed"}' \
    "$api_url/api/cloud/v1/auth/$route")"
  [[ "$status" == "401" ]]
  jq -e '.code == "INVALID_REFRESH_TOKEN"' "$temporary_directory/$route.json" >/dev/null
done

echo "AO Cloud staging is ready at $api_url"
