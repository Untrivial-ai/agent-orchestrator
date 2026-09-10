#!/usr/bin/env bash
set -euo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source "$repository_root/scripts/lib/docker-local.sh"
if ! ao_docker_available; then
	printf 'SKIP: Docker Engine with Compose is unavailable; local lifecycle smoke test not run.\n'
	exit 0
fi

project_name="ao-cloud-smoke-${PPID}-$$"
state_root="${AO_DATA_DIR:-$HOME/.ao}"
mkdir -p "$state_root"
umask 077
state_directory="$(mktemp -d "${state_root}/cloud-smoke.XXXXXX")"
state_file="${state_directory}/state.json"

free_port() {
	python3 - <<'PY'
import socket

with socket.socket() as listener:
    listener.bind(("127.0.0.1", 0))
    print(listener.getsockname()[1])
PY
}

export AO_CLOUD_PORT="${AO_CLOUD_SMOKE_PORT:-$(free_port)}"
export AO_CLOUD_POSTGRES_PORT="${AO_CLOUD_SMOKE_POSTGRES_PORT:-$(free_port)}"
export AO_CLOUD_LOCAL_POSTGRES_DATA_DIR="$state_directory/postgres"
export AO_CLOUD_PROVIDER_SECRET_KEY
AO_CLOUD_PROVIDER_SECRET_KEY="$(openssl rand -base64 32)"
export AO_CLOUD_WORKER_SIGNING_KEY
AO_CLOUD_WORKER_SIGNING_KEY="$(openssl rand -hex 32)"
export AO_CLOUD_DOCKER_GID
AO_CLOUD_DOCKER_GID="$(ao_docker_socket_gid)"
export AO_CLOUD_DOCKER_WORKER_IMAGE="${project_name}-worker:smoke"
export AO_CLOUD_DEVELOPMENT_SKIP_CREDENTIAL_VALIDATION="true"
# Opt-in low-latency terminal streams (issue #4763). Compose forwards this to
# the control plane, which forwards it to worker containers. Unset keeps the
# fully polled transport under test.
export AO_CLOUD_TERMINAL_STREAM="${AO_CLOUD_TERMINAL_STREAM:-}"
export COMPOSE_PROJECT_NAME="$project_name"

compose() {
	docker compose --project-directory "$repository_root" "$@"
}

cleanup() {
	local status=$?
	if ((status != 0)); then
		compose logs >&2 || true
		local worker
		while IFS= read -r worker; do
			if [[ -n "$worker" ]]; then
				docker logs "$worker" >&2 || true
			fi
		done < <(
			docker ps --all --quiet \
				--filter "label=ao.managed=true" \
				--filter "label=ao.docker.namespace=${project_name}"
		)
	fi
	ao_docker_remove_workers "$project_name" >/dev/null 2>&1 || true
	compose down --volumes --remove-orphans >/dev/null 2>&1 || true
	ao_docker_remove_workspaces "$project_name" >/dev/null 2>&1 || true
	rm -rf "$state_directory"
	return "$status"
}
trap cleanup EXIT

wait_for_ready() {
	local attempts=30
	while ((attempts > 0)); do
		if curl \
			--fail \
			--silent \
			--show-error \
			--max-time 2 \
			"http://127.0.0.1:${AO_CLOUD_PORT}/readyz" >/dev/null 2>&1; then
			return 0
		fi
		attempts=$((attempts - 1))
		sleep 1
	done
	echo "Local AO Cloud did not become ready on 127.0.0.1:${AO_CLOUD_PORT}." >&2
	compose logs >&2
	return 1
}

assert_loopback_port() {
	local service="$1"
	local container_port="$2"
	local expected_port="$3"
	local binding
	binding="$(compose port "$service" "$container_port")"
	if [[ "$binding" != "127.0.0.1:${expected_port}" ]]; then
		echo "${service} port is not loopback-only: ${binding}" >&2
		return 1
	fi
}

exercise_api() {
	local mode="$1"
	python3 - "$mode" "$AO_CLOUD_PORT" "$state_file" <<'PY'
import json
import pathlib
import sys
import time
import urllib.error
import urllib.request

mode, port, state_path = sys.argv[1:]
base_url = f"http://127.0.0.1:{port}"
state_file = pathlib.Path(state_path)


def request(method, path, *, body=None, token=None, idempotency_key=None, expected=200):
    headers = {"Accept": "application/json"}
    data = None
    if body is not None:
        headers["Content-Type"] = "application/json"
        data = json.dumps(body).encode()
    if token:
        headers["Authorization"] = f"Bearer {token}"
    if idempotency_key:
        headers["Idempotency-Key"] = idempotency_key
    operation = urllib.request.Request(
        base_url + path,
        data=data,
        headers=headers,
        method=method,
    )
    try:
        response = urllib.request.urlopen(operation, timeout=10)
    except urllib.error.HTTPError as error:
        detail = error.read().decode(errors="replace")
        raise RuntimeError(
            f"{method} {path} returned {error.code}, expected {expected}: {detail}"
        ) from error
    with response:
        if response.status != expected:
            raise RuntimeError(
                f"{method} {path} returned {response.status}, expected {expected}"
            )
        return json.load(response)


def wait_for_running(org_id, session_id, token):
    deadline = time.monotonic() + 90
    last_state = None
    while time.monotonic() < deadline:
        session = request(
            "GET",
            f"/api/cloud/v1/orgs/{org_id}/sessions/{session_id}",
            token=token,
        )["session"]
        last_state = session["runtimeState"]
        if last_state == "running":
            return session
        if last_state == "failed":
            raise RuntimeError(f"worker provisioning failed: {session!r}")
        time.sleep(1)
    raise RuntimeError(f"worker did not become running; last state was {last_state!r}")


def events(org_id, session_id, token):
    return request(
        "GET",
        f"/api/cloud/v1/orgs/{org_id}/sessions/{session_id}/chat-events?after=0&limit=100",
        token=token,
    )["events"]


def wait_for_terminal_turn(org_id, session_id, token, previous):
    terminal_types = {
        "chat.turn_completed",
        "chat.turn_interrupted",
        "chat.turn_aborted",
    }
    deadline = time.monotonic() + 30
    while time.monotonic() < deadline:
        current = events(org_id, session_id, token)
        terminal = [event for event in current if event.get("type") in terminal_types]
        if len(terminal) > previous:
            return len(terminal)
        time.sleep(0.5)
    raise RuntimeError("worker did not durably finish the queued turn")


if mode == "create":
    suffix = str(time.time_ns())
    auth = request(
        "POST",
        "/api/cloud/v1/auth/local/register",
        body={
            "email": f"cloud-smoke-{suffix}@example.com",
            "displayName": "Cloud Smoke",
            "password": "local-smoke-password",
            "orgSlug": f"cloud-smoke-{suffix}",
            "orgName": "Cloud Smoke",
        },
        expected=201,
    )
    token = auth["token"]
    org_id = auth["organizations"][0]["id"]
    request(
        "PUT",
        f"/api/cloud/v1/orgs/{org_id}/provider-connections/agents/claude-code",
        body={
            "credentialType": "api_key",
            "secret": "ao-cloud-smoke-development-only",
        },
        token=token,
    )
    project = request(
        "POST",
        f"/api/cloud/v1/orgs/{org_id}/projects",
        body={
            "displayName": "Persistence Test",
            "repositoryUrl": "https://github.com/octocat/Hello-World",
            "defaultBranch": "main",
            "config": {},
        },
        token=token,
        idempotency_key=f"project-{suffix}",
        expected=201,
    )["project"]
    session = request(
        "POST",
        f"/api/cloud/v1/orgs/{org_id}/sessions",
        body={
            "projectId": project["id"],
            "kind": "orchestrator",
            "harness": "claude-code",
            "displayName": "Persistence Test",
            "prompt": "",
            "mode": "trusted",
        },
        token=token,
        idempotency_key=f"session-{suffix}",
        expected=201,
    )["session"]
    wait_for_running(org_id, session["id"], token)
    workspace_file = request(
        "PUT",
        f"/api/cloud/v1/orgs/{org_id}/sessions/{session['id']}/workspace/file",
        body={"path": ".ao-cloud-smoke-api", "content": "durable-worker-transport\n"},
        token=token,
    )
    if workspace_file.get("content") != "durable-worker-transport\n":
        raise RuntimeError(f"workspace write returned unexpected content: {workspace_file!r}")
    read_back = request(
        "GET",
        f"/api/cloud/v1/orgs/{org_id}/sessions/{session['id']}/workspace/file?path=.ao-cloud-smoke-api",
        token=token,
    )
    if read_back != workspace_file:
        raise RuntimeError(
            f"workspace read did not match the durable write: {read_back!r}"
        )
    listing = request(
        "GET",
        f"/api/cloud/v1/orgs/{org_id}/sessions/{session['id']}/workspace/files?limit=100",
        token=token,
    )
    if ".ao-cloud-smoke-api" not in {item.get("path") for item in listing["items"]}:
        raise RuntimeError(f"workspace listing omitted the written file: {listing!r}")
    request(
        "POST",
        f"/api/cloud/v1/orgs/{org_id}/sessions/{session['id']}/terminal-ticket",
        body={"kind": "agent"},
        token=token,
        expected=201,
    )
    state_file.write_text(
        json.dumps(
            {
                "token": token,
                "orgId": org_id,
                "sessionId": session["id"],
            }
        )
    )
elif mode == "verify":
    state = json.loads(state_file.read_text())
    token = state["token"]
    org_id = state["orgId"]
    session_id = state["sessionId"]
    wait_for_running(org_id, session_id, token)
    request(
        "POST",
        f"/api/cloud/v1/orgs/{org_id}/sessions/{session_id}/terminal-ticket",
        body={"kind": "agent"},
        token=token,
        expected=201,
    )
elif mode == "wake":
    state = json.loads(state_file.read_text())
    token = state["token"]
    org_id = state["orgId"]
    session_id = state["sessionId"]
    woken = request(
        "POST",
        f"/api/cloud/v1/orgs/{org_id}/sessions/wake",
        token=token,
        expected=202,
    ).get("woken", 0)
    if woken < 1:
        raise RuntimeError(f"wake did not resume the paused smoke session: {woken=}")
    wait_for_running(org_id, session_id, token)
    request(
        "POST",
        f"/api/cloud/v1/orgs/{org_id}/sessions/{session_id}/terminal-ticket",
        body={"kind": "agent"},
        token=token,
        expected=201,
    )
else:
    raise RuntimeError(f"unknown smoke-test mode: {mode}")
PY
}

session_id() {
	python3 - "$state_file" <<'PY'
import json
import pathlib
import sys

print(json.loads(pathlib.Path(sys.argv[1]).read_text())["sessionId"])
PY
}

org_id() {
	python3 - "$state_file" <<'PY'
import json
import pathlib
import sys

print(json.loads(pathlib.Path(sys.argv[1]).read_text())["orgId"])
PY
}

wait_for_worker() {
	local session="$1"
	local previous="${2:-}"
	local attempts=90
	local container_id
	while ((attempts > 0)); do
		container_id="$(
			docker ps --quiet \
				--filter "label=ao.managed=true" \
				--filter "label=ao.provider=docker" \
				--filter "label=ao.docker.namespace=${project_name}" \
				--filter "label=ao.session_id=${session}"
		)"
		if [[ -n "$container_id" && "$container_id" != "$previous" ]]; then
			printf '%s\n' "$container_id"
			return 0
		fi
		attempts=$((attempts - 1))
		sleep 1
	done
	echo "Worker container for session ${session} did not appear." >&2
	compose logs control-plane >&2
	return 1
}

wait_for_worker_stopped() {
	local container_id="$1"
	local attempts=90
	local running
	while ((attempts > 0)); do
		running="$(docker inspect --format '{{.State.Running}}' "$container_id" 2>/dev/null || true)"
		if [[ "$running" != true ]]; then
			return 0
		fi
		attempts=$((attempts - 1))
		sleep 1
	done
	echo "Worker container ${container_id} did not stop for the pause test." >&2
	compose logs control-plane >&2
	return 1
}

assert_workspace_marker() {
	local container_id="$1"
	local marker
	marker="$(docker exec "$container_id" bash -c 'cat /workspace/repository/.ao-cloud-smoke')"
	if [[ "$marker" != "persistent-workspace" ]]; then
		echo "Worker workspace marker did not survive container replacement." >&2
		return 1
	fi
}

exercise_browser_proxy() {
	local container_id="$1"
	docker exec -d "$container_id" node -e '
const http = require("http");
http.createServer((request, response) => {
  if (request.url === "/assets/app.js") {
    response.writeHead(200, {"Content-Type": "application/javascript"});
    response.end("window.vmBrowserSmoke = true;");
    return;
  }
  response.writeHead(200, {"Content-Type": "text/html; charset=utf-8"});
  response.end("<!doctype html><html><head><title>VM browser smoke</title></head><body><script src=\"/assets/app.js\"></script><a href=\"docs/start\">Docs</a><p>vm-browser-smoke</p></body></html>");
}).listen(3000, "127.0.0.1");
'
	python3 - "$AO_CLOUD_PORT" "$state_file" <<'PY'
import base64
import json
import pathlib
import sys
import time
import urllib.error
import urllib.request

port, state_path = sys.argv[1:]
state = json.loads(pathlib.Path(state_path).read_text())
origin = "http://localhost:3000"
origin_token = base64.urlsafe_b64encode(origin.encode()).decode().rstrip("=")
prefix = (
    f"/api/cloud/v1/orgs/{state['orgId']}/sessions/{state['sessionId']}"
    f"/browser/{origin_token}/"
)
base_url = f"http://127.0.0.1:{port}"


def get(path):
    request = urllib.request.Request(
        base_url + path,
        headers={"Authorization": f"Bearer {state['token']}"},
        method="GET",
    )
    with urllib.request.urlopen(request, timeout=10) as response:
        return response.headers, response.read().decode(errors="replace")


deadline = time.monotonic() + 15
while True:
    try:
        headers, document = get(prefix)
        break
    except urllib.error.URLError:
        if time.monotonic() >= deadline:
            raise
        time.sleep(0.25)

if not headers.get_content_type() == "text/html":
    raise RuntimeError(f"browser proxy returned the wrong content type: {headers!r}")
if "vm-browser-smoke" not in document:
    raise RuntimeError(f"browser proxy did not reach the VM-local server: {document!r}")
if f'<base href="{prefix}">' not in document:
    raise RuntimeError(f"browser proxy did not anchor relative VM links: {document!r}")
if f'src="{prefix}assets/app.js"' not in document:
    raise RuntimeError(f"browser proxy did not rewrite VM asset URLs: {document!r}")

asset_headers, asset = get(prefix + "assets/app.js")
if asset_headers.get_content_type() != "application/javascript":
    raise RuntimeError(f"browser asset returned the wrong content type: {asset_headers!r}")
if asset != "window.vmBrowserSmoke = true;":
    raise RuntimeError(f"browser proxy did not return the VM asset: {asset!r}")
PY
}

compose --profile worker-image build worker-image
docker build \
	--build-arg "BASE_IMAGE=${AO_CLOUD_DOCKER_WORKER_IMAGE}" \
	--file "$repository_root/test/Dockerfile.worker-smoke" \
	--tag "$AO_CLOUD_DOCKER_WORKER_IMAGE" \
	"$repository_root"
compose up --build -d
wait_for_ready
assert_loopback_port control-plane 8080 "$AO_CLOUD_PORT"
assert_loopback_port postgres 5432 "$AO_CLOUD_POSTGRES_PORT"

role_state="$(
	compose exec -T \
		-e PGPASSWORD=ao_cloud_local_owner \
		postgres \
		psql \
		--username ao_cloud_owner \
		--dbname ao_cloud \
		--tuples-only \
		--no-align \
		--command \
		"SELECT rolname || ':' || rolsuper || ':' || rolbypassrls || ':' || rolcanlogin
		 FROM pg_roles
		 WHERE rolname IN ('ao_cloud_app', 'ao_cloud_bootstrap', 'ao_cloud_owner')
		 ORDER BY rolname"
)"
expected_role_state="$(
	cat <<'EOF'
ao_cloud_app:false:false:true
ao_cloud_bootstrap:true:true:false
ao_cloud_owner:false:false:true
EOF
)"
if [[ "$role_state" != "$expected_role_state" ]]; then
	echo "Unexpected local PostgreSQL role state:" >&2
	echo "$role_state" >&2
	exit 1
fi

exercise_api create
session="$(session_id)"
org="$(org_id)"
first_worker="$(wait_for_worker "$session")"
docker exec "$first_worker" ao list >/dev/null
exercise_browser_proxy "$first_worker"
spawn_output="$(
	docker exec "$first_worker" ao spawn \
		--name "Delegated smoke" \
		--agent claude-code \
		--prompt "Wait for a control-plane message"
)"
child_session="$(printf '%s\n' "$spawn_output" | awk '/^spawned / { print $2 }')"
if [[ -z "$child_session" ]]; then
	echo "AO orchestration CLI did not return a child session id: ${spawn_output}" >&2
	exit 1
fi
wait_for_worker "$child_session" >/dev/null
docker exec "$first_worker" ao send "$child_session" "Report smoke status" >/dev/null
attempts=30
while ((attempts > 0)); do
	message_forwarded="$(
		compose exec -e "PGOPTIONS=-c ao.org_id=${org}" -T postgres \
			psql -U ao_cloud_owner -d ao_cloud -Atc \
			"SELECT EXISTS (SELECT 1 FROM ao_turns WHERE session_id = '${child_session}' AND state = 'completed') OR EXISTS (SELECT 1 FROM ao_worker_requests WHERE session_id = '${child_session}' AND kind = 'terminal.input' AND status = 'succeeded')"
	)"
	if [[ "$message_forwarded" == "t" ]]; then
		break
	fi
	attempts=$((attempts - 1))
	sleep 1
done
if [[ "$message_forwarded" != "t" ]]; then
	echo "Orchestrator message was not forwarded into the child agent PTY." >&2
	exit 1
fi
docker exec "$first_worker" ao kill "$child_session" >/dev/null

if [[ "${AO_CLOUD_TERMINAL_STREAM:-}" == "1" ]]; then
	# The delivery above must have ridden the stream push, not the transport
	# poll: the pushed request is completed by the control plane, and the
	# stream never leaves failed input rows behind.
	stream_pushed="$(
		compose exec -e "PGOPTIONS=-c ao.org_id=${org}" -T postgres \
			psql -U ao_cloud_owner -d ao_cloud -Atc \
			"SELECT NOT EXISTS (
				SELECT 1 FROM ao_worker_requests
				WHERE session_id = '${child_session}'
				  AND kind = 'terminal.input' AND status = 'failed'
			)"
	)"
	if [[ "$stream_pushed" != "t" ]]; then
		echo "Terminal stream left failed input requests behind." >&2
		exit 1
	fi
	if compose logs control-plane 2>/dev/null | grep -q "claim terminal input for push"; then
		echo "Control plane logged terminal stream push failures." >&2
		exit 1
	fi
	echo "terminal stream assertions passed"
fi
docker exec "$first_worker" bash -c \
	'printf "%s\n" persistent-workspace > /workspace/repository/.ao-cloud-smoke'

# Docker workers cannot be resumed with their one-time bootstrap ticket, so a
# wake recreates the worker container while retaining its workspace volume. The
# control-plane path is the same user-visible pause -> wake transition used by
# the hosted provider, and must preserve both the workspace and the new agent
# terminal interaction lease.
compose exec -e "PGOPTIONS=-c ao.org_id=${org}" -T postgres \
	psql -U ao_cloud_owner -d ao_cloud -v ON_ERROR_STOP=1 -c \
	"UPDATE ao_sandboxes
	 SET desired_state = 'paused', reconcile_after = now(), interactive_until = NULL, updated_at = now()
	 WHERE org_id = '${org}' AND session_id = '${session}'" >/dev/null
wait_for_worker_stopped "$first_worker"
exercise_api wake
resumed_worker="$(wait_for_worker "$session" "$first_worker")"
assert_workspace_marker "$resumed_worker"
interactive_after_wake="$(
	compose exec -e "PGOPTIONS=-c ao.org_id=${org}" -T postgres \
		psql -U ao_cloud_owner -d ao_cloud -Atc \
		"SELECT interactive_until > now()
		 FROM ao_sandboxes
		 WHERE org_id = '${org}' AND session_id = '${session}'"
)"
if [[ "$interactive_after_wake" != t ]]; then
	echo "Agent-terminal wake did not reserve an interactive lease." >&2
	exit 1
fi

first_worker="$resumed_worker"
docker rm --force "$first_worker" >/dev/null
replacement_worker="$(wait_for_worker "$session" "$first_worker")"
assert_workspace_marker "$replacement_worker"
exercise_api verify

compose restart control-plane >/dev/null
wait_for_ready
exercise_api verify

ao_docker_remove_workers "$project_name"
compose down --remove-orphans >/dev/null
compose up -d
wait_for_ready
restarted_worker="$(wait_for_worker "$session")"
assert_workspace_marker "$restarted_worker"
exercise_api verify

ao_docker_remove_workers "$project_name"
compose down --volumes --remove-orphans >/dev/null
ao_docker_remove_workspaces "$project_name"
if docker volume inspect "${project_name}_ao-cloud-postgres" >/dev/null 2>&1; then
	echo "cloud:local:reset semantics left the PostgreSQL volume behind." >&2
	exit 1
fi

trap - EXIT
rm -rf "$state_directory"
printf 'AO Cloud local lifecycle smoke test passed.\n'
