#!/usr/bin/env bash
# Container entrypoint for the headless AO daemon.
# Seeds Connect Mobile state when requested, then execs `ao daemon` as PID 1.
set -euo pipefail

: "${AO_DATA_DIR:=/ao/data}"
: "${AO_RUN_FILE:=${AO_DATA_DIR}/running.json}"
export AO_DATA_DIR AO_RUN_FILE

mkdir -p "${AO_DATA_DIR}"

mobile_cfg="${AO_DATA_DIR}/mobile/config.json"
want_mobile=0
if [[ "${AO_MOBILE_ENABLE:-}" == "1" || "${AO_MOBILE_ENABLE:-}" == "true" ]]; then
  want_mobile=1
fi
if [[ -n "${AO_MOBILE_PASSWORD:-}" ]]; then
  want_mobile=1
fi

if [[ "${want_mobile}" -eq 1 ]]; then
  port="${AO_MOBILE_PORT:-3011}"
  pw="${AO_MOBILE_PASSWORD:-}"

  # Prefer an already-persisted password on restart unless the operator set one.
  if [[ -z "${pw}" && -f "${mobile_cfg}" ]]; then
    existing="$(jq -r '.password // empty' "${mobile_cfg}" 2>/dev/null || true)"
    if [[ -n "${existing}" ]]; then
      pw="${existing}"
    fi
  fi
  if [[ -z "${pw}" ]]; then
    pw="$(tr -dc 'A-Za-z0-9' </dev/urandom | head -c 8 || true)"
    if [[ ${#pw} -ne 8 ]]; then
      echo "ao-docker: failed to generate AO_MOBILE_PASSWORD" >&2
      exit 1
    fi
    echo "ao-docker: generated Connect Mobile password: ${pw}" >&2
  fi

  mkdir -p "${AO_DATA_DIR}/mobile"
  jq -n \
    --arg pw "${pw}" \
    --argjson port "${port}" \
    '{enabled: true, password: $pw, lastPort: $port, securePairing: false}' \
    >"${mobile_cfg}.tmp"
  chmod 600 "${mobile_cfg}.tmp"
  mv "${mobile_cfg}.tmp" "${mobile_cfg}"
  echo "ao-docker: Connect Mobile LAN listener will bind on port ${port}" >&2
fi

exec ao daemon
