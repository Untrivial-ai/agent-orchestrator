#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
state_root="${AO_DATA_DIR:-$HOME/.ao}"
data_directory="${AO_CLOUD_LOCAL_POSTGRES_DATA_DIR:-$state_root/cloud/postgres}"
namespace="${COMPOSE_PROJECT_NAME:-ao-cloud-local}"
source "$root/scripts/lib/docker-local.sh"

case "$data_directory" in
"$state_root"/*) ;;
*)
	echo "Refusing to remove local Cloud data outside ${state_root}: ${data_directory}" >&2
	exit 1
	;;
esac

cd "$root"
ao_docker_load_teardown_keys
ao_docker_remove_workers "$namespace"
docker compose down --remove-orphans
ao_docker_remove_workspaces "$namespace"
rm -rf "$data_directory"
