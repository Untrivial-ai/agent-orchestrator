#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source "$root/scripts/lib/docker-local.sh"
namespace="${COMPOSE_PROJECT_NAME:-ao-cloud-local}"

cd "$root"
ao_docker_load_teardown_keys
if ao_docker_available; then
	ao_docker_remove_workers "$namespace"
fi
docker compose down --remove-orphans
