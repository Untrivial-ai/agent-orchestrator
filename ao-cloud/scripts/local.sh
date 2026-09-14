#!/usr/bin/env bash
set -euo pipefail
umask 077

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
data_dir="${AO_DATA_DIR:-$HOME/.ao}/cloud-local"
log_dir="$data_dir/logs"
pid_dir="$data_dir/pids"
env_file="$root/.env.cloud.local"
github_app_id="4475070"
github_app_client_id="Iv23liLaAnXMSyGGzVl4"
github_app_slug="ao-cloud-test"
webhook_path="/api/cloud/v1/github/webhooks"

mkdir -p "$log_dir" "$pid_dir"

usage() {
  cat <<'EOF'
Usage:
  npm run cloud:local [start|stop|clear-db|reset-db]
  npm run cloud:workos
  npm run cloud:workos:gated

start (default) installs dependencies, prepares local configuration, builds the
worker and control-plane images, starts the Compose stack, then starts the web
app.
cloud:local forces local email/password auth. cloud:workos forces WorkOS auth
with local self-serve signup enabled. cloud:workos:gated forces WorkOS auth with
invite-gated signup, matching hosted defaults. WorkOS profiles require the AO
GitHub App private key and start a webhook-only Cloudflare Quick Tunnel.
It remains attached and streams logs until Ctrl-C, which stops the stack while
preserving PostgreSQL data.
stop gracefully stops local worker sandboxes, the control plane, web app, and
local PostgreSQL container while preserving volumes.
clear-db stops the stack and permanently deletes local PostgreSQL data without
starting anything afterward. reset-db is a backwards-compatible alias.
EOF
}

ensure_env() {
  if [[ ! -f "$env_file" ]]; then
    cp "$root/ao-cloud/.env.example" "$env_file"
    echo "Created $env_file"
  fi
  if ! grep -q '^AO_ENCRYPTION_KEY=.\+' "$env_file"; then
    printf '\nAO_ENCRYPTION_KEY=%s\n' "$(openssl rand -hex 32)" >>"$env_file"
  fi
  if ! grep -q '^AO_WORKER_SIGNING_KEY=.\+' "$env_file"; then
    printf 'AO_WORKER_SIGNING_KEY=%s\n' "$(openssl rand -hex 32)" >>"$env_file"
  fi
  if ! grep -q '^WORKOS_COOKIE_PASSWORD=.\+' "$env_file"; then
    printf 'WORKOS_COOKIE_PASSWORD=%s\n' "$(openssl rand -hex 32)" >>"$env_file"
  fi
  if ! grep -q '^AO_GITHUB_APP_WEBHOOK_SECRET=.\+' "$env_file"; then
    printf 'AO_GITHUB_APP_WEBHOOK_SECRET=%s\n' "$(openssl rand -hex 32)" >>"$env_file"
  fi
  if ! grep -q '^AO_GITHUB_APP_STATE_SECRET=.\+' "$env_file"; then
    printf 'AO_GITHUB_APP_STATE_SECRET=%s\n' "$(openssl rand -hex 32)" >>"$env_file"
  fi
  if ! grep -q '^NEXT_PUBLIC_WORKOS_REDIRECT_URI=.\+' "$env_file"; then
    printf 'NEXT_PUBLIC_WORKOS_REDIRECT_URI=http://127.0.0.1:5174/callback\n' >>"$env_file"
  fi
  if ! grep -q '^WORKOS_REDIRECT_URI=.\+' "$env_file"; then
    printf 'WORKOS_REDIRECT_URI=http://127.0.0.1:5174/callback\n' >>"$env_file"
  fi
}

ensure_web_env() {
  local web_env="$root/frontend/src/landing/.env.local"
  if [[ ! -f "$web_env" ]]; then
    printf 'NEXT_PUBLIC_API_URL=http://127.0.0.1:3010\nNEXT_PUBLIC_AO_AUTH_MODE=local\n' >"$web_env"
    echo "Created $web_env"
  fi
}

load_local_env() {
  set -a
  # shellcheck disable=SC1090
  . "$env_file"
  set +a
}

configure_auth_profile() {
  local profile="${1:-local}"
  case "$profile" in
    local)
      AO_CLOUD_AUTH_MODE="local"
      AO_CLOUD_ALLOW_PUBLIC_SIGNUP="false"
      ;;
    workos)
      AO_CLOUD_AUTH_MODE="workos"
      AO_CLOUD_ALLOW_PUBLIC_SIGNUP="true"
      require_workos_env
      ;;
    workos-gated)
      AO_CLOUD_AUTH_MODE="workos"
      AO_CLOUD_ALLOW_PUBLIC_SIGNUP="false"
      require_workos_env
      ;;
    *)
      echo "Unknown auth profile: $profile"
      usage
      exit 2
      ;;
  esac
  WORKOS_REDIRECT_URI="${WORKOS_REDIRECT_URI:-http://127.0.0.1:5174/callback}"
  NEXT_PUBLIC_WORKOS_REDIRECT_URI="${NEXT_PUBLIC_WORKOS_REDIRECT_URI:-http://127.0.0.1:5174/callback}"
  export AO_CLOUD_AUTH_MODE
  export AO_CLOUD_ALLOW_PUBLIC_SIGNUP
  export WORKOS_REDIRECT_URI
  export NEXT_PUBLIC_WORKOS_REDIRECT_URI
}

require_workos_env() {
  local missing=()
  [[ -n "${WORKOS_CLIENT_ID:-}" ]] || missing+=("WORKOS_CLIENT_ID")
  [[ -n "${WORKOS_API_KEY:-}" ]] || missing+=("WORKOS_API_KEY")
  [[ -n "${WORKOS_COOKIE_PASSWORD:-}" ]] || missing+=("WORKOS_COOKIE_PASSWORD")
  if (( ${#missing[@]} > 0 )); then
    cat <<EOF
WorkOS local auth needs these values in $env_file:

WORKOS_CLIENT_ID=client_...
WORKOS_API_KEY=sk_...
WORKOS_COOKIE_PASSWORD=$(openssl rand -hex 32)
WORKOS_REDIRECT_URI=http://127.0.0.1:5174/callback
NEXT_PUBLIC_WORKOS_REDIRECT_URI=http://127.0.0.1:5174/callback

Configure these values in the WorkOS dashboard:
Redirect URI:       http://127.0.0.1:5174/callback
App homepage URL:   http://127.0.0.1:5174
Initiate login URI: http://127.0.0.1:5174/auth/workos/sign-in
Sign-out URI:       http://127.0.0.1:5174/auth
Allowed web origin: http://127.0.0.1:5174

Missing: ${missing[*]}
EOF
    exit 1
  fi
}

configure_local_github() {
  AO_GITHUB_AUTH_MODE="local-gh"
  AO_GITHUB_APP_ID=""
  AO_GITHUB_APP_CLIENT_ID=""
  AO_GITHUB_APP_SLUG=""
  AO_GITHUB_APP_PRIVATE_KEY_PATH=""
  AO_GITHUB_APP_WEBHOOK_SECRET=""
  AO_GITHUB_APP_STATE_SECRET=""
  export AO_GITHUB_AUTH_MODE
  export AO_GITHUB_APP_ID AO_GITHUB_APP_CLIENT_ID AO_GITHUB_APP_SLUG
  export AO_GITHUB_APP_PRIVATE_KEY_PATH AO_GITHUB_APP_WEBHOOK_SECRET AO_GITHUB_APP_STATE_SECRET

  if ! command -v gh >/dev/null 2>&1; then
    AO_GITHUB_AUTH_MODE=""
    AO_LOCAL_GITHUB_TOKEN=""
    export AO_GITHUB_AUTH_MODE AO_LOCAL_GITHUB_TOKEN
    echo "GitHub repository access is disabled: install and authenticate the gh CLI."
    return
  fi

  local token
  if ! token="$(gh auth token 2>/dev/null)" || [[ -z "$token" ]]; then
    AO_GITHUB_AUTH_MODE=""
    AO_LOCAL_GITHUB_TOKEN=""
    export AO_GITHUB_AUTH_MODE AO_LOCAL_GITHUB_TOKEN
    echo "GitHub repository access is disabled: run 'gh auth login'."
    return
  fi
  AO_LOCAL_GITHUB_TOKEN="$token"
  AO_GITHUB_AUTH_MODE="local-gh"
  export AO_LOCAL_GITHUB_TOKEN
  export AO_GITHUB_AUTH_MODE
  echo "Using the host gh authentication token for local GitHub access."
}

require_github_app_env() {
  AO_GITHUB_APP_ID="${AO_GITHUB_APP_ID:-$github_app_id}"
  AO_GITHUB_APP_CLIENT_ID="${AO_GITHUB_APP_CLIENT_ID:-$github_app_client_id}"
  AO_GITHUB_APP_SLUG="${AO_GITHUB_APP_SLUG:-$github_app_slug}"
  export AO_GITHUB_APP_ID AO_GITHUB_APP_CLIENT_ID AO_GITHUB_APP_SLUG

  local missing=()
  [[ "${AO_GITHUB_APP_ID:-}" == "$github_app_id" ]] || missing+=("AO_GITHUB_APP_ID=$github_app_id")
  [[ "${AO_GITHUB_APP_CLIENT_ID:-}" == "$github_app_client_id" ]] || missing+=("AO_GITHUB_APP_CLIENT_ID=$github_app_client_id")
  [[ "${AO_GITHUB_APP_SLUG:-}" == "$github_app_slug" ]] || missing+=("AO_GITHUB_APP_SLUG=$github_app_slug")
  [[ -n "${AO_GITHUB_APP_WEBHOOK_SECRET:-}" ]] || missing+=("AO_GITHUB_APP_WEBHOOK_SECRET")
  [[ -n "${AO_GITHUB_APP_STATE_SECRET:-}" ]] || missing+=("AO_GITHUB_APP_STATE_SECRET")
  if (( ${#missing[@]} > 0 )); then
    echo "GitHub App mode is missing or has invalid values in $env_file:"
    printf '  %s\n' "${missing[@]}"
    exit 1
  fi
  if (( ${#AO_GITHUB_APP_WEBHOOK_SECRET} < 32 )); then
    echo "AO_GITHUB_APP_WEBHOOK_SECRET must be at least 32 characters."
    exit 1
  fi
  if (( ${#AO_GITHUB_APP_STATE_SECRET} < 32 )); then
    echo "AO_GITHUB_APP_STATE_SECRET must be at least 32 characters."
    exit 1
  fi
  if [[ "$AO_GITHUB_APP_WEBHOOK_SECRET" == "$AO_GITHUB_APP_STATE_SECRET" ]]; then
    echo "AO_GITHUB_APP_WEBHOOK_SECRET and AO_GITHUB_APP_STATE_SECRET must be independent."
    exit 1
  fi

  local private_key_path="${AO_GITHUB_APP_PRIVATE_KEY_PATH:-$data_dir/github-app.private-key.pem}"
  if [[ "$private_key_path" != /* ]]; then
    echo "AO_GITHUB_APP_PRIVATE_KEY_PATH must be an absolute path under $data_dir."
    exit 1
  fi
  if [[ ! -f "$private_key_path" || ! -r "$private_key_path" ]]; then
    echo "GitHub App private key is missing or unreadable: $private_key_path"
    echo "Generate it in GitHub, save it under $data_dir, and run: chmod 600 '$private_key_path'"
    exit 1
  fi
  if [[ -L "$private_key_path" ]]; then
    echo "GitHub App private key must not be a symbolic link: $private_key_path"
    exit 1
  fi
  local resolved_data_dir resolved_key_dir
  resolved_data_dir="$(cd "$data_dir" && pwd -P)"
  resolved_key_dir="$(cd "$(dirname "$private_key_path")" && pwd -P)"
  if [[ "$resolved_key_dir" != "$resolved_data_dir" ]]; then
    echo "GitHub App private key must be stored directly under $resolved_data_dir."
    exit 1
  fi
  local key_mode
  key_mode="$(stat -f '%Lp' "$private_key_path" 2>/dev/null || stat -c '%a' "$private_key_path" 2>/dev/null || true)"
  if [[ -z "$key_mode" ]] || (( (8#$key_mode & 077) != 0 )); then
    echo "GitHub App private key must not grant group/other access. Run: chmod 600 '$private_key_path'"
    exit 1
  fi
  if ! openssl pkey -in "$private_key_path" -noout >/dev/null 2>&1; then
    echo "GitHub App private key is not a readable PEM private key: $private_key_path"
    exit 1
  fi
  if ! command -v cloudflared >/dev/null 2>&1; then
    echo "cloudflared is required for cloud:workos GitHub webhooks."
    echo "Install it with: brew install cloudflared"
    exit 1
  fi

  AO_GITHUB_AUTH_MODE="github-app"
  AO_LOCAL_GITHUB_TOKEN=""
  AO_GITHUB_APP_PRIVATE_KEY_PATH="$private_key_path"
  export AO_GITHUB_AUTH_MODE AO_LOCAL_GITHUB_TOKEN AO_GITHUB_APP_PRIVATE_KEY_PATH
  export AO_GITHUB_APP_WEBHOOK_SECRET AO_GITHUB_APP_STATE_SECRET
}

configure_github_profile() {
  local profile="$1"
  case "$profile" in
    local) configure_local_github ;;
    workos|workos-gated) require_github_app_env ;;
  esac
}

running_pid() {
  local name="$1"
  local pid_file="$pid_dir/$name.pid"
  [[ -f "$pid_file" ]] && kill -0 "$(cat "$pid_file")" 2>/dev/null
}

start_process() {
  local name="$1"
  local command="$2"
  local log_file="$log_dir/$name.log"
  if running_pid "$name"; then
    echo "$name is already running (pid $(cat "$pid_dir/$name.pid"))."
    return
  fi
  rm -f "$pid_dir/$name.pid"
  (
    cd "$root"
    exec bash -lc "$command"
  ) >>"$log_file" 2>&1 &
  echo $! >"$pid_dir/$name.pid"
  echo "Started $name (log: $log_file)."
}

stop_process() {
  local name="$1"
  local pid_file="$pid_dir/$name.pid"
  if [[ -f "$pid_file" ]]; then
    local pid
    pid="$(cat "$pid_file")"
    if kill -0 "$pid" 2>/dev/null; then
      kill_tree "$pid"
      # Reap the process started by start_process so it cannot remain as a
      # zombie after its descendants have been terminated.
      wait "$pid" 2>/dev/null || true
      echo "Stopped $name."
    fi
    rm -f "$pid_file"
  fi
}

kill_tree() {
  local pid="$1"
  local child
  for child in $(pgrep -P "$pid" 2>/dev/null || true); do
    kill_tree "$child"
  done
  kill "$pid" 2>/dev/null || true
}

stop_local_sandboxes() {
  local ids
  ids="$(docker ps --quiet --filter label=ao.managed=true)"
  if [[ -z "$ids" ]]; then
    return
  fi
  docker stop --time 15 $ids || true
  echo "Stopped local AO worker sandboxes."
}

stop_stack() {
  # Stop sandboxes first so their worker processes can handle SIGTERM while the
  # control plane is still available for their final lifecycle events.
  stop_local_sandboxes
  stop_process sandbox-events
  stop_process cloudflared
  stop_process webhook-relay
  stop_process web
  stop_process control-plane
  (
    cd "$root"
    docker compose --env-file "$env_file" -f ao-cloud/docker-compose.local.yml stop
  )
}

start_failure_cleanup_armed=false
start_failure_cleanup_running=false

run_start_failure_cleanup() {
  if [[ "$start_failure_cleanup_armed" != "true" || "$start_failure_cleanup_running" == "true" ]]; then
    return
  fi
  start_failure_cleanup_running=true
  start_failure_cleanup_armed=false
  trap - EXIT INT TERM
  set +e
  stop_stack
}

handle_start_failure_exit() {
  local status=$?
  run_start_failure_cleanup
  exit "$status"
}

handle_start_failure_signal() {
  local signal="$1"
  local status=1
  case "$signal" in
    INT) status=130 ;;
    TERM) status=143 ;;
  esac
  run_start_failure_cleanup
  exit "$status"
}

arm_start_failure_cleanup() {
  start_failure_cleanup_armed=true
  start_failure_cleanup_running=false
  trap 'handle_start_failure_exit' EXIT
  trap 'handle_start_failure_signal INT' INT
  trap 'handle_start_failure_signal TERM' TERM
}

handoff_start_cleanup_to_stream() {
  start_failure_cleanup_armed=false
  trap - EXIT
}

start_github_webhook_tunnel() {
  rm -f "$log_dir/webhook-relay.log" "$log_dir/cloudflared.log"
  start_process webhook-relay "exec node '$root/ao-cloud/scripts/webhook-relay.mjs'"
  sleep 1
  if ! running_pid webhook-relay; then
    echo "Webhook relay failed to start. Recent log:"
    tail -n 50 "$log_dir/webhook-relay.log"
    exit 1
  fi

  start_process cloudflared "exec cloudflared tunnel --no-autoupdate --url http://127.0.0.1:3011"
  local tunnel_url=""
  for _ in {1..30}; do
    tunnel_url="$(awk 'match($0, /https:\/\/[[:alnum:]-]+\.trycloudflare\.com/) { print substr($0, RSTART, RLENGTH); exit }' "$log_dir/cloudflared.log")"
    if [[ -n "$tunnel_url" ]]; then
      break
    fi
    if ! running_pid cloudflared; then
      break
    fi
    sleep 1
  done
  if [[ -z "$tunnel_url" ]]; then
    echo "cloudflared did not provide a Quick Tunnel URL. Recent log:"
    tail -n 50 "$log_dir/cloudflared.log"
    stop_process cloudflared
    stop_process webhook-relay
    exit 1
  fi
  echo
  echo "GitHub webhook URL: ${tunnel_url}${webhook_path}"
  echo "Paste this URL into the $github_app_slug GitHub App and activate webhooks."
}

clear_database() {
  stop_stack
  cd "$root"
  docker compose --env-file "$env_file" -f ao-cloud/docker-compose.local.yml down --volumes
  echo "Deleted local AO Cloud PostgreSQL data."
}

stream_logs() {
  touch "$log_dir/control-plane.log" "$log_dir/web.log" "$log_dir/sandbox-events.log"
  echo
  echo "Streaming local Cloud logs. Press Ctrl-C to stop the local stack."
  local stream_pids=()
  stream_file() {
    local label="$1"
    local file="$2"
    tail -n 0 -F "$file" 2>/dev/null | awk -v label="$label" '
      /ObjectMultiplex - orphaned/ { next }
      /Next.js inferred your workspace root/ { next }
      /Detected additional lockfiles/ { next }
      /^   \* .*package-lock\.json/ { next }
      /"method":"OPTIONS"/ { next }
      /"method":"GET".*"status":200/ &&
        /\/api\/cloud\/v1\/(me|projects|sessions|provider-connections|repositories)/ { next }
      { print "[" label "] " $0; fflush() }
    ' &
    stream_pids+=("$!")
  }
  stream_file CP "$log_dir/control-plane.log"
  stream_file WEB "$log_dir/web.log"
  stream_file DOCKER "$log_dir/sandbox-events.log"
  trap 'kill "${stream_pids[@]}" 2>/dev/null || true; stop_stack; exit 0' INT TERM
  handoff_start_cleanup_to_stream
  wait "${stream_pids[@]}"
}

start() {
  local auth_profile="${1:-local}"
  ensure_env
  ensure_web_env
  load_local_env
  configure_auth_profile "$auth_profile"
  configure_github_profile "$auth_profile"
  cd "$root"
  npm install
  npm --prefix frontend/src/landing install
  npm run cloud:build-image
  arm_start_failure_cleanup
  docker compose --env-file "$env_file" -f ao-cloud/docker-compose.local.yml up --build -d
  : >"$log_dir/control-plane.log"
  start_process control-plane "docker compose --env-file '$env_file' -f ao-cloud/docker-compose.local.yml logs --no-log-prefix --follow control-plane"
  for _ in {1..30}; do
    if curl --fail --silent http://127.0.0.1:3010/readyz >/dev/null; then
      break
    fi
    sleep 1
  done
  if ! curl --fail --silent http://127.0.0.1:3010/readyz >/dev/null; then
    echo "Control plane did not become ready. Recent logs:"
    tail -n 100 "$log_dir/control-plane.log"
    exit 1
  fi
  if [[ "$auth_profile" != "local" ]]; then
    start_github_webhook_tunnel
  fi
  : >"$log_dir/web.log"
  local web_workos_env=""
  if [[ "$auth_profile" == "local" ]]; then
    # The local auth profile does not need WorkOS, but App Router route
    # modules still load the WorkOS SDK during Next.js startup. Set fallbacks
    # inside the child shell, after .env.cloud.local has been sourced, so
    # empty values in that file cannot overwrite them.
    web_workos_env='export WORKOS_CLIENT_ID="${WORKOS_CLIENT_ID:-client_local_dummy}" WORKOS_API_KEY="${WORKOS_API_KEY:-sk_test_local_dummy}"'
  fi
  start_process web "set -a; . '$env_file'; set +a; export AO_CLOUD_AUTH_MODE='$AO_CLOUD_AUTH_MODE' AO_CLOUD_ALLOW_PUBLIC_SIGNUP='$AO_CLOUD_ALLOW_PUBLIC_SIGNUP' WORKOS_REDIRECT_URI='$WORKOS_REDIRECT_URI' NEXT_PUBLIC_API_URL=http://127.0.0.1:3010 NEXT_PUBLIC_WEB_URL=http://127.0.0.1:5174 NEXT_PUBLIC_AO_AUTH_MODE='$AO_CLOUD_AUTH_MODE' NEXT_PUBLIC_WORKOS_REDIRECT_URI='$NEXT_PUBLIC_WORKOS_REDIRECT_URI'; $web_workos_env; exec npm run cloud:web"
  start_process sandbox-events "exec docker events --filter label=ao.managed=true --format '{{.Time}} {{.Action}} {{.Actor.Attributes.name}}'"
  for _ in {1..90}; do
    if curl --fail --silent http://127.0.0.1:5174/ >/dev/null; then
      break
    fi
    sleep 1
  done
  if ! curl --fail --silent http://127.0.0.1:5174/ >/dev/null; then
    echo "Web app did not become ready. Recent logs:"
    tail -n 100 "$log_dir/web.log"
    exit 1
  fi
  echo
  echo "✓ AO Cloud is ready."
  echo "→ Visit site: http://127.0.0.1:5174"
  echo "→ Press Ctrl-C to stop the local stack and preserve its database."
  stream_logs
}

main() {
  case "${1:-start}" in
    start) start local ;;
    workos) start workos ;;
    workos-gated) start workos-gated ;;
    stop) stop_stack ;;
    clear-db|reset-db) clear_database ;;
    -h|--help|help) usage ;;
    *)
      usage
      exit 2
      ;;
  esac
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  main "$@"
fi
