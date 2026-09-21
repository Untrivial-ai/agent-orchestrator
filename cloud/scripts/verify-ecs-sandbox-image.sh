#!/usr/bin/env bash
set -euo pipefail

# Verifies the arm64 ECS sandbox worker image. Unlike verify-image-contract.sh
# this does NOT compare the baked /ao-worker against the control-plane image: the
# control plane is amd64 and the sandbox is arm64, so byte-identity cannot hold.
# The baked arm64 worker is authoritative instead (the ECS provider never
# advertises a self-update hash to it), so this check confirms the image is the
# right architecture, runs the worker as a non-root entrypoint, and ships every
# agent a session may launch.

if [[ "$#" -ne 1 ]]; then
	echo "usage: $0 WORKER_IMAGE" >&2
	exit 2
fi

worker_image="$1"

WORKER_INSPECT="$(docker image inspect "$worker_image")" python3 - <<'PY'
import json
import os

image = json.loads(os.environ["WORKER_INSPECT"])[0]
platform = f'{image.get("Os", "")}/{image.get("Architecture", "")}'
if platform != "linux/arm64":
    raise SystemExit(f"sandbox image platform is {platform}, expected linux/arm64")
if image.get("Config", {}).get("Entrypoint") != ["/ao-worker"]:
    raise SystemExit("sandbox image has an unexpected entrypoint")
if image.get("Config", {}).get("User") in ("", "0", "root"):
    raise SystemExit("sandbox image must run as a non-root user")
PY

# The agents-present check runs the arm64 image; on an amd64 host it needs binfmt
# emulation. Skip it explicitly with AO_CLOUD_ECS_SKIP_RUNTIME_CHECK=1 on a CI
# runner without emulation, but never skip it on the release build.
if [[ "${AO_CLOUD_ECS_SKIP_RUNTIME_CHECK:-0}" != "1" ]]; then
	if ! docker run --rm --entrypoint /bin/sh "$worker_image" -c \
		'command -v claude >/dev/null &&
		 command -v codex >/dev/null &&
		 command -v cursor-agent >/dev/null &&
		 command -v gh >/dev/null &&
		 command -v ao >/dev/null'; then
		echo "Sandbox image must contain Claude Code, Codex, Cursor Agent, GitHub CLI, and the AO orchestration CLI." >&2
		exit 1
	fi
fi

printf 'Verified arm64 sandbox worker %s\n' "$worker_image"
