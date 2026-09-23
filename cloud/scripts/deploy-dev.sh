#!/usr/bin/env bash
# Deploys the current checkout to the shared DEV control plane
# (https://dev-api.aoagents.dev). The dev environment is sacrificial: any
# branch, any provider config, no ancestry checks. It shares nothing with
# staging or production except the NodeOps account and the WorkOS app —
# its database, task families, service, secrets, and alarm are all its own.
#
# Usage: AWS_PROFILE=ao-cloud ./cloud/scripts/deploy-dev.sh
# (run from the cloud/ directory or repo root; requires Docker)
set -euo pipefail

export AO_CLOUD_ECS_CLUSTER="ao-cloud-dev"
export AO_CLOUD_ECS_SERVICE="ao-cloud-dev-api"
export AO_CLOUD_API_TASK_FAMILY="ao-cloud-dev-api"
export AO_CLOUD_MIGRATION_TASK_FAMILY="ao-cloud-dev-migrate"
export AO_CLOUD_ROLLBACK_ALARM="ao-cloud-dev-target-5xx"
export AO_CLOUD_RUNTIME_DATABASE_USER="ao_dev_app"
export AO_CLOUD_WORKER_SECRET_ID="ao-cloud/dev/worker"
export AO_CLOUD_PROVIDER_SECRET_ID="ao-cloud/dev/provider-secret-key"
export AO_CLOUD_PUBLIC_URL="https://dev-api.aoagents.dev"
export AO_CLOUD_DEPLOY_ENVIRONMENT="dev"

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$script_dir/.."
exec ./scripts/deploy-staging.sh "$@"
