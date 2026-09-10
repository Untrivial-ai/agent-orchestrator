#!/usr/bin/env bash
set -euo pipefail

readonly default_staging_url="https://staging-api.aoagents.dev"
readonly default_workos_client_id="client_01KZ3VRKC374HS91XGRDPT3671"

staging_url="${AO_CLOUD_STAGING_URL:-$default_staging_url}"
if [[ "$staging_url" != https://* ]]; then
	echo "AO_CLOUD_STAGING_URL must be the hosted staging HTTPS origin." >&2
	exit 1
fi
staging_url="${staging_url%/}"

export VITE_WORKOS_CLIENT_ID="${VITE_WORKOS_CLIENT_ID:-$default_workos_client_id}"

response_file="$(mktemp)"
error_file="$(mktemp)"
trap 'rm -f "$response_file" "$error_file"' EXIT
if ! curl \
	--fail \
	--silent \
	--show-error \
	--max-time 10 \
	--proto '=https' \
	--tlsv1.2 \
	--output "$response_file" \
	"${staging_url}/readyz" 2>"$error_file"; then
	detail="$(tr '\n' ' ' <"$error_file")"
	echo "Staging control plane readiness failed at ${staging_url}/readyz: ${detail}" >&2
	exit 1
fi

python3 - "$response_file" <<'PY'
import json
import pathlib
import sys

try:
    payload = json.loads(pathlib.Path(sys.argv[1]).read_text())
except (OSError, json.JSONDecodeError) as error:
    raise SystemExit(f"The staging readiness response is invalid: {error}") from error
if payload.get("status") != "ready":
    raise SystemExit("The staging control plane is not ready.")
if payload.get("environment") != "staging":
    raise SystemExit(
        f"Refusing to launch against {payload.get('environment')!r}; expected staging."
    )
PY

public_repository="${AO_PUBLIC_REPOSITORY:-$(git rev-parse --show-superproject-working-tree)}"
if [[ -z "$public_repository" || ! -f "$public_repository/frontend/package.json" ]]; then
	echo "Set AO_PUBLIC_REPOSITORY to an Agent Orchestrator checkout containing frontend/." >&2
	exit 1
fi

export AO_CLOUD_API_BASE_URL="$staging_url"
export VITE_AO_CLOUD_API_BASE_URL="$staging_url"
export AO_DATA_DIR="${AO_CLOUD_STAGING_DATA_DIR:-$HOME/.ao/staging-desktop}"

printf 'Launching AO desktop against staging API %s\n' "$staging_url"
printf 'Isolated desktop state: %s\n' "$AO_DATA_DIR"
exec npm --prefix "$public_repository/frontend" run dev
