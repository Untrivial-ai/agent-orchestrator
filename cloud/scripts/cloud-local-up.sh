#!/usr/bin/env bash
set -euo pipefail

# Interactive, non-self-tearing bring-up of the Docker-backed AO Cloud control
# plane for local development. Unlike scripts/test-cloud-local.sh (a self-tearing
# smoke test that uses random loopback ports and tears everything down on exit),
# this starts the stack on FIXED default ports and LEAVES IT RUNNING so a
# developer can drive the whole cloud flow from the desktop app with zero
# NodeOps. Stop it with cloud-local-down.sh (data retained) or wipe local data
# with cloud-local-reset.sh. Re-running this script is safe (idempotent).

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source "$repository_root/scripts/lib/docker-local.sh"

if ! ao_docker_available; then
	echo "Docker Engine with the Compose plugin is required but was not found." >&2
	echo "Start Docker Desktop (or the Docker daemon) and re-run 'npm run cloud:local'." >&2
	exit 1
fi

# Fixed default ports (overridable via env), unlike the smoke test's random ports.
export AO_CLOUD_PORT="${AO_CLOUD_PORT:-8081}"
export AO_CLOUD_POSTGRES_PORT="${AO_CLOUD_POSTGRES_PORT:-54329}"

# Stable, persisted keys so cloud-local-down.sh / cloud-local-reset.sh (which read
# the same files via ao_docker_load_teardown_keys) match this bring-up. The files
# live at the exact paths those teardown scripts consult.
state_root="${AO_DATA_DIR:-$HOME/.ao}"
cloud_state_directory="$state_root/cloud"
provider_key_file="$cloud_state_directory/provider-secret-key"
worker_key_file="$cloud_state_directory/worker-signing-key"

mkdir -p "$cloud_state_directory"
if [[ ! -s "$provider_key_file" ]]; then
	(umask 077 && openssl rand -base64 32 >"$provider_key_file")
fi
if [[ ! -s "$worker_key_file" ]]; then
	(umask 077 && openssl rand -hex 32 >"$worker_key_file")
fi

export AO_CLOUD_PROVIDER_SECRET_KEY="${AO_CLOUD_PROVIDER_SECRET_KEY:-$(<"$provider_key_file")}"
export AO_CLOUD_WORKER_SIGNING_KEY="${AO_CLOUD_WORKER_SIGNING_KEY:-$(<"$worker_key_file")}"
export AO_CLOUD_DOCKER_GID
AO_CLOUD_DOCKER_GID="$(ao_docker_socket_gid)"
export AO_CLOUD_DEVELOPMENT_SKIP_CREDENTIAL_VALIDATION="true"

compose() {
	docker compose --project-directory "$repository_root" "$@"
}

wait_for_ready() {
	local url="http://127.0.0.1:${AO_CLOUD_PORT}/readyz"
	local attempts="${AO_CLOUD_LOCAL_READY_ATTEMPTS:-90}"
	local attempt=1
	printf 'Waiting for the control plane at %s' "$url"
	while ((attempt <= attempts)); do
		if curl --fail --silent --show-error --max-time 2 "$url" >/dev/null 2>&1; then
			printf ' ready.\n'
			return 0
		fi
		printf '.'
		attempt=$((attempt + 1))
		sleep 1
	done
	printf '\n'
	echo "The control plane did not report ready at ${url} after ${attempts}s." >&2
	compose logs >&2
	return 1
}

printf 'Building the local worker image...\n'
compose --profile worker-image build worker-image
printf 'Building and starting the control plane, PostgreSQL, and migrations...\n'
compose up --build -d
wait_for_ready

# Seed a default dev account so you can just sign in — no register step needed.
# Best-effort and idempotent: a 201 means it was created, anything else means it
# already exists (or the seed was skipped); never fail the bring-up over it.
DEV_EMAIL="dev@local.test"
DEV_PASSWORD="localdevpass123"
seed_dev_account() {
	local code
	code="$(curl -s -o /dev/null -w '%{http_code}' \
		-X POST -H 'Content-Type: application/json' \
		-d "{\"email\":\"${DEV_EMAIL}\",\"displayName\":\"Dev\",\"password\":\"${DEV_PASSWORD}\",\"orgSlug\":\"dev-org\",\"orgName\":\"Dev Org\"}" \
		"http://127.0.0.1:${AO_CLOUD_PORT}/api/cloud/v1/auth/local/register" 2>/dev/null || echo 000)"
	if [ "$code" = "201" ]; then
		echo "Seeded default dev account (${DEV_EMAIL})."
	elif [ "$code" = "000" ]; then
		echo "Note: could not reach the CP to seed a dev account; register in-app if needed." >&2
	else
		echo "Default dev account already exists (${DEV_EMAIL})."
	fi
}
seed_dev_account

cat <<EOF

AO Cloud is up and will stay running (no automatic teardown).

  Control plane : http://127.0.0.1:${AO_CLOUD_PORT}
  PostgreSQL    : 127.0.0.1:${AO_CLOUD_POSTGRES_PORT}

Launch the desktop app against it, from the frontend/ directory:

  AO_CLOUD_OFFERING=on AO_CLOUD_CONTROL_PLANE_URL=http://127.0.0.1:${AO_CLOUD_PORT} npm run dev

A default account is ready — just SIGN IN with:

  Email    : ${DEV_EMAIL}
  Password : ${DEV_PASSWORD}

(No register step needed. You can still use Register in-app for another account.)

Stop it (data retained) : npm run cloud:local:down
Reset it (data deleted) : npm run cloud:local:reset
EOF
