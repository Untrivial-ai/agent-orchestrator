#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
temp_dir="$(mktemp -d)"
spawned_pid_file="$temp_dir/spawned-pids"
touch "$spawned_pid_file"
cleanup_tests() {
  while read -r pid; do
    kill "$pid" 2>/dev/null || true
  done <"$spawned_pid_file"
  rm -rf "$temp_dir"
}
trap cleanup_tests EXIT

export AO_DATA_DIR="$temp_dir/data"
mkdir -p "$AO_DATA_DIR/cloud-local" "$temp_dir/bin"
openssl genpkey \
  -algorithm RSA \
  -pkeyopt rsa_keygen_bits:2048 \
  -out "$AO_DATA_DIR/cloud-local/github-app.private-key.pem" \
  >/dev/null 2>&1
chmod 600 "$AO_DATA_DIR/cloud-local/github-app.private-key.pem"
printf '#!/usr/bin/env sh\nexit 0\n' >"$temp_dir/bin/cloudflared"
chmod +x "$temp_dir/bin/cloudflared"
export PATH="$temp_dir/bin:$PATH"

# shellcheck source=local.sh
source "$script_dir/local.sh"

assert_lf_shell_checkout() {
  local script
  for script in \
    ao-cloud/scripts/local.sh \
    ao-cloud/docker/worker-entrypoint.sh \
    cloud/scripts/cloud-local-up.sh; do
    if [[ "$(git -C "$root" check-attr eol -- "$script")" != "$script: eol: lf" ]]; then
      echo "expected $script to be checked out with LF line endings" >&2
      exit 1
    fi
  done
}

assert_lf_shell_checkout

export AO_GITHUB_APP_PRIVATE_KEY_PATH="$AO_DATA_DIR/cloud-local/github-app.private-key.pem"
export AO_GITHUB_APP_WEBHOOK_SECRET="aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
export AO_GITHUB_APP_STATE_SECRET="bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
export AO_LOCAL_GITHUB_TOKEN="must-be-cleared"
unset AO_GITHUB_APP_ID AO_GITHUB_APP_CLIENT_ID AO_GITHUB_APP_SLUG

gh_marker="$temp_dir/gh-called"
gh() {
  : >"$gh_marker"
  printf 'local-test-token'
}

require_github_app_env
[[ "$AO_GITHUB_APP_ID" == "4475070" ]]
[[ "$AO_GITHUB_APP_CLIENT_ID" == "Iv23liLaAnXMSyGGzVl4" ]]
[[ "$AO_GITHUB_APP_SLUG" == "ao-cloud-test" ]]
[[ "$AO_GITHUB_AUTH_MODE" == "github-app" ]]
[[ -z "$AO_LOCAL_GITHUB_TOKEN" ]]
[[ ! -e "$gh_marker" ]]
bash -c '[[ "$AO_GITHUB_APP_ID" == 4475070 && "$AO_GITHUB_APP_CLIENT_ID" == Iv23liLaAnXMSyGGzVl4 && "$AO_GITHUB_APP_SLUG" == ao-cloud-test ]]'

for override in \
  "AO_GITHUB_APP_ID=999" \
  "AO_GITHUB_APP_CLIENT_ID=Iv_wrong" \
  "AO_GITHUB_APP_SLUG=wrong"; do
  if (
    export "$override"
    require_github_app_env >/dev/null 2>&1
  ); then
    echo "require_github_app_env accepted wrong local test identity: $override" >&2
    exit 1
  fi
done

AO_GITHUB_APP_ID="should-be-cleared"
AO_GITHUB_APP_CLIENT_ID="should-be-cleared"
AO_GITHUB_APP_SLUG="should-be-cleared"
AO_GITHUB_APP_PRIVATE_KEY_PATH="/should-be-cleared"
AO_GITHUB_APP_WEBHOOK_SECRET="should-be-cleared"
AO_GITHUB_APP_STATE_SECRET="should-be-cleared"
configure_local_github >/dev/null
[[ "$AO_GITHUB_AUTH_MODE" == "local-gh" ]]
[[ "$AO_LOCAL_GITHUB_TOKEN" == "local-test-token" ]]
[[ -z "$AO_GITHUB_APP_ID" ]]
[[ -z "$AO_GITHUB_APP_CLIENT_ID" ]]
[[ -z "$AO_GITHUB_APP_SLUG" ]]
[[ -z "$AO_GITHUB_APP_PRIVATE_KEY_PATH" ]]
[[ -z "$AO_GITHUB_APP_WEBHOOK_SECRET" ]]
[[ -z "$AO_GITHUB_APP_STATE_SECRET" ]]

assert_process_stopped() {
  local pid="$1"
  for _ in {1..20}; do
    if ! kill -0 "$pid" 2>/dev/null; then
      return
    fi
    sleep 0.05
  done
  echo "cleanup left process $pid running" >&2
  exit 1
}

prepare_start_harness() {
  test_scenario="$1"
  test_case_dir="$2"
  data_dir="$test_case_dir/data"
  log_dir="$data_dir/logs"
  pid_dir="$data_dir/pids"
  env_file="$test_case_dir/test.env"
  test_compose_stop_marker="$test_case_dir/compose-stop"
  test_started_file="$test_case_dir/started"
  mkdir -p "$log_dir" "$pid_dir"
  : >"$env_file"
  : >"$test_started_file"

  ensure_env() { :; }
  ensure_web_env() { :; }
  load_local_env() { :; }
  configure_auth_profile() {
    if [[ "$1" == "local" ]]; then
      AO_CLOUD_AUTH_MODE="local"
    else
      AO_CLOUD_AUTH_MODE="workos"
    fi
    AO_CLOUD_ALLOW_PUBLIC_SIGNUP="false"
    WORKOS_REDIRECT_URI="http://127.0.0.1:5174/callback"
    NEXT_PUBLIC_WORKOS_REDIRECT_URI="http://127.0.0.1:5174/callback"
  }
  configure_github_profile() { :; }
  npm() { :; }
  sleep() { :; }
  curl() {
    local url="${*: -1}"
    case "$test_scenario:$url" in
      cp:*) return 1 ;;
      tunnel:*3010/readyz) return 0 ;;
      web:*3010/readyz) return 0 ;;
      web:*5174/) return 1 ;;
      success:*) return 0 ;;
      *) return 1 ;;
    esac
  }
  docker() {
    if [[ "${1:-}" == "ps" ]]; then
      return
    fi
    if [[ "${1:-}" == "stop" ]]; then
      return
    fi
    if [[ " $* " == *" compose "* && " $* " == *" stop "* ]]; then
      printf 'stop\n' >>"$test_compose_stop_marker"
    fi
  }
  start_process() {
    local name="$1"
    local pid
    touch "$log_dir/$name.log"
    /bin/sleep 300 &
    pid=$!
    printf '%s\n' "$pid" >>"$spawned_pid_file"
    printf '%s %s\n' "$name" "$pid" >>"$test_started_file"
    printf '%s\n' "$pid" >"$pid_dir/$name.pid"
  }
}

assert_failed_start_cleaned_up() {
  local scenario="$1"
  local profile="local"
  local case_dir="$temp_dir/failure-$scenario"
  mkdir -p "$case_dir"
  if [[ "$scenario" == "tunnel" ]]; then
    profile="workos"
  fi

  if (
    prepare_start_harness "$scenario" "$case_dir"
    start "$profile"
  ) >/dev/null 2>&1; then
    echo "$scenario startup unexpectedly succeeded" >&2
    exit 1
  fi

  [[ "$(wc -l <"$case_dir/compose-stop" | tr -d ' ')" == "1" ]]
  while read -r name pid; do
    assert_process_stopped "$pid"
    [[ ! -e "$case_dir/data/pids/$name.pid" ]]
  done <"$case_dir/started"

  if awk '$1 == "cloudflared" { found=1 } END { exit !found }' "$case_dir/started"; then
    local tunnel_pid
    tunnel_pid="$(awk '$1 == "cloudflared" { print $2 }' "$case_dir/started")"
    assert_process_stopped "$tunnel_pid"
  fi
}

assert_failed_start_cleaned_up cp
assert_failed_start_cleaned_up tunnel
assert_failed_start_cleaned_up web

success_dir="$temp_dir/success"
mkdir -p "$success_dir"
(
  prepare_start_harness success "$success_dir"
  stream_logs() {
    [[ "$start_failure_cleanup_armed" == "true" ]]
    [[ -n "$(trap -p EXIT)" ]]
    trap ':' INT TERM
    handoff_start_cleanup_to_stream
    [[ "$start_failure_cleanup_armed" == "false" ]]
    [[ -z "$(trap -p EXIT)" ]]
    : >"$success_dir/stream-owned"
  }
  start local
  [[ -e "$success_dir/stream-owned" ]]
  [[ ! -e "$success_dir/compose-stop" ]]
  stop_stack
)

echo "local runner tests passed"
