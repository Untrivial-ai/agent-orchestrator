#!/usr/bin/env bash

ao_docker_available() {
	command -v docker >/dev/null 2>&1 &&
		docker compose version >/dev/null 2>&1 &&
		docker info >/dev/null 2>&1
}

ao_docker_socket_gid() {
	local socket="${1:-/var/run/docker.sock}"
	if [[ "$(uname -s)" == "Darwin" ]]; then
		# Docker Desktop proxies the host socket into its Linux VM as root:root,
		# regardless of the macOS socket's group.
		printf '0\n'
	else
		stat -c '%g' "$socket"
	fi
}

ao_docker_load_teardown_keys() {
	local state_root="${AO_DATA_DIR:-$HOME/.ao}"
	local provider_key_file="$state_root/cloud/provider-secret-key"
	local worker_key_file="$state_root/cloud/worker-signing-key"

	if [[ -z "${AO_CLOUD_PROVIDER_SECRET_KEY:-}" ]]; then
		export AO_CLOUD_PROVIDER_SECRET_KEY
		if [[ -s "$provider_key_file" ]]; then
			AO_CLOUD_PROVIDER_SECRET_KEY="$(<"$provider_key_file")"
		else
			AO_CLOUD_PROVIDER_SECRET_KEY="AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
		fi
	fi
	if [[ -z "${AO_CLOUD_WORKER_SIGNING_KEY:-}" ]]; then
		export AO_CLOUD_WORKER_SIGNING_KEY
		if [[ -s "$worker_key_file" ]]; then
			AO_CLOUD_WORKER_SIGNING_KEY="$(<"$worker_key_file")"
		else
			AO_CLOUD_WORKER_SIGNING_KEY="0000000000000000000000000000000000000000000000000000000000000000"
		fi
	fi
}

ao_docker_remove_workers() {
	local namespace="$1"
	local container_id
	while IFS= read -r container_id; do
		[[ -n "$container_id" ]] || continue
		docker rm --force "$container_id" >/dev/null
	done < <(
		docker ps --all --quiet \
			--filter "label=ao.managed=true" \
			--filter "label=ao.provider=docker" \
			--filter "label=ao.docker.namespace=${namespace}"
	)
}

ao_docker_remove_workspaces() {
	local namespace="$1"
	local volume
	while IFS= read -r volume; do
		[[ -n "$volume" ]] || continue
		docker volume rm "$volume" >/dev/null
	done < <(
		docker volume ls --quiet \
			--filter "label=ao.managed=true" \
			--filter "label=ao.provider=docker" \
			--filter "label=ao.docker.namespace=${namespace}"
	)
}
