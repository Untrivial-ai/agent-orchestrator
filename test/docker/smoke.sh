#!/usr/bin/env bash
# Smoke-test the headless AO daemon image: build, run, ao status, tear down.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
IMAGE="${AO_DOCKER_IMAGE:-ao-daemon:smoke}"
NAME="ao-daemon-smoke-$$"
LABEL_ARGS=()
if [[ -n "${AO_SESSION_ID:-}" ]]; then
  LABEL_ARGS=(--label "ao.session=${AO_SESSION_ID}")
fi

cleanup() {
  docker rm -f "${NAME}" >/dev/null 2>&1 || true
}
trap cleanup EXIT

echo "==> Building ${IMAGE}"
docker build -f "${ROOT}/docker/Dockerfile" -t "${IMAGE}" "${ROOT}"

echo "==> Starting container ${NAME}"
docker run -d --init --name "${NAME}" "${LABEL_ARGS[@]}" \
  -e AO_DATA_DIR=/ao/data \
  -e AO_RUN_FILE=/ao/data/running.json \
  "${IMAGE}"

echo "==> Waiting for daemon ready"
ready=0
for _ in $(seq 1 60); do
  if docker exec "${NAME}" ao status --json 2>/dev/null | jq -e '.state == "ready"' >/dev/null 2>&1; then
    ready=1
    break
  fi
  sleep 1
done
if [[ "${ready}" -ne 1 ]]; then
  echo "daemon did not become ready" >&2
  docker logs "${NAME}" >&2 || true
  docker exec "${NAME}" ao status >&2 || true
  exit 1
fi

echo "==> ao status"
docker exec "${NAME}" ao status
echo "OK"
