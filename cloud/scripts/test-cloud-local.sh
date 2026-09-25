#!/usr/bin/env bash
set -euo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source "$repository_root/scripts/lib/docker-local.sh"

measure_startup=false
case "${1:-}" in
	"") ;;
	--measure-startup) measure_startup=true ;;
	*)
		echo "Usage: $0 [--measure-startup]" >&2
		exit 2
		;;
esac

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

export AO_CLOUD_SMOKE_DOCKERFILE="$repository_root/Dockerfile"
if ! docker buildx version >/dev/null 2>&1; then
	AO_CLOUD_SMOKE_DOCKERFILE="$state_directory/Dockerfile.classic"
	python3 - "$repository_root/Dockerfile" "$AO_CLOUD_SMOKE_DOCKERFILE" <<'PY'
import pathlib
import sys

source, destination = map(pathlib.Path, sys.argv[1:])
text = source.read_text()
text = text.replace(
    "RUN --mount=type=cache,target=/go/pkg/mod go mod download",
    "RUN go mod download",
)
text = text.replace(
    "RUN --mount=type=cache,target=/go/pkg/mod \\\n"
    "    --mount=type=cache,target=/root/.cache/go-build \\\n"
    "    CGO_ENABLED=0",
    "RUN CGO_ENABLED=0",
)
if "--mount=type=cache" in text:
    raise SystemExit("classic-builder Dockerfile still contains cache mounts")
destination.write_text(text)
PY
fi

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
export AO_CLOUD_DOCKER_EXTRA_LABELS_JSON
AO_CLOUD_DOCKER_EXTRA_LABELS_JSON="{\"ao.session\":\"${AO_SESSION_ID:-local}\"}"
export AO_CLOUD_DEVELOPMENT_SKIP_CREDENTIAL_VALIDATION="true"
# Opt-in low-latency terminal streams (issue #4763). Compose forwards this to
# the control plane, which forwards it to worker containers. Unset keeps the
# fully polled transport under test.
export AO_CLOUD_TERMINAL_STREAM="${AO_CLOUD_TERMINAL_STREAM:-}"
# The relay is independently opt-in so the smoke suite can exercise either
# the existing durable stream or the live-forward + durable-mirror path.
export AO_CLOUD_TERMINAL_RELAY="${AO_CLOUD_TERMINAL_RELAY:-}"
export COMPOSE_PROJECT_NAME="$project_name"

case "$(uname -m)" in
	x86_64) export AO_CLOUD_SMOKE_TARGET_ARCH=amd64 ;;
	aarch64|arm64) export AO_CLOUD_SMOKE_TARGET_ARCH=arm64 ;;
	*)
		echo "Unsupported smoke-test architecture: $(uname -m)" >&2
		exit 1
		;;
esac
export AO_CLOUD_SMOKE_BUILD_PLATFORM="linux/${AO_CLOUD_SMOKE_TARGET_ARCH}"

compose() {
	docker compose \
		--project-directory "$repository_root" \
		--file "$repository_root/compose.yaml" \
		--file "$repository_root/compose.smoke.yaml" \
		"$@"
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
import uuid
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
        if error.code == expected:
            return json.loads(detail)
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


def wait_for_startup(org_id, session_id, token, started, milestones):
    wanted = {
        "sandbox.provisioning": "sandboxProvisioningMs",
        "worker.connected": "workerConnectedMs",
        "worker.ready": "workerReadyMs",
        "checkout.started": "checkoutStartedMs",
        "checkout.completed": "checkoutCompletedMs",
        "restore.started": "restoreStartedMs",
        "restore.completed": "restoreCompletedMs",
        "workspace.ready": "workspaceReadyMs",
        "agent.launch_started": "agentLaunchStartedMs",
        "agent.ready": "agentReadyMs",
    }
    deadline = time.monotonic() + 90
    last_state = None
    while time.monotonic() < deadline:
        session = request(
            "GET",
            f"/api/cloud/v1/orgs/{org_id}/sessions/{session_id}",
            token=token,
        )["session"]
        last_state = session["runtimeState"]
        now_ms = round((time.monotonic() - started) * 1000)
        if last_state == "running":
            milestones.setdefault("runtimeRunningMs", now_ms)
        if last_state == "failed":
            raise RuntimeError(f"worker provisioning failed: {session!r}")
        for event in events(org_id, session_id, token):
            key = wanted.get(event.get("type"))
            if key:
                milestones.setdefault(key, now_ms)
        if last_state == "running" and "agentReadyMs" in milestones:
            return session
        time.sleep(0.1)
    raise RuntimeError(
        f"startup milestones did not complete; state={last_state!r}, milestones={milestones!r}"
    )


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


def wait_for_agent_terminal_ticket(org_id, session_id, token):
    deadline = time.monotonic() + 90
    while time.monotonic() < deadline:
        try:
            return request(
                "POST",
                f"/api/cloud/v1/orgs/{org_id}/sessions/{session_id}/terminal-ticket",
                body={"kind": "agent"},
                token=token,
                expected=201,
            )
        except RuntimeError as error:
            detail = str(error)
            if "returned 409" not in detail or '"code":"WORKER_UNAVAILABLE"' not in detail:
                raise
            time.sleep(0.25)
    raise RuntimeError("agent terminal did not become available")


def visible_session_ids(org_id, project_id, token):
    page = request(
        "GET",
        f"/api/cloud/v1/orgs/{org_id}/sessions?projectId={project_id}&limit=100",
        token=token,
    )
    return [item["id"] for item in page["items"]]


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
    connection = request(
        "PUT",
        f"/api/cloud/v1/orgs/{org_id}/provider-connections/agents/claude-code",
        body={
            "credentialType": "api_key",
            "secret": "ao-cloud-smoke-development-only",
        },
        token=token,
    )["providerConnection"]
    state_file.write_text(json.dumps({
        "token": token,
        "orgId": org_id,
        "harness": connection["provider"],
    }))
elif mode == "prepare":
    state = json.loads(state_file.read_text())
    token = state["token"]
    org_id = state["orgId"]
    prompt = f"prepared-session-smoke-{time.time_ns()}"
    commit_key = f"commit-preparation-{time.time_ns()}"
    client_instance_id = str(uuid.uuid4())
    started = time.time()
    prepared = request(
        "POST",
        f"/api/cloud/v1/orgs/{org_id}/session-preparations",
        body={
            "projectId": state["projectId"],
            "harness": state["harness"],
            "provider": "docker",
            "clientInstanceId": client_instance_id,
        },
        token=token,
        idempotency_key=f"prepare-session-{time.time_ns()}",
        expected=201,
    )
    session = prepared["session"]
    preparation = prepared["preparation"]
    if preparation["leaseSeconds"] != 120 or not preparation["expiresAt"] or preparation["generation"] < 1:
        raise RuntimeError(f"invalid preparation lease: {preparation!r}")
    if prepared["disposition"] != "created" or prepared["claimId"] != session["id"]:
        raise RuntimeError(f"invalid created preparation response: {prepared!r}")
    reused_client_instance_id = str(uuid.uuid4())
    reused = request(
        "POST",
        f"/api/cloud/v1/orgs/{org_id}/session-preparations",
        body={
            "projectId": state["projectId"],
            "harness": state["harness"],
            "provider": "docker",
            "clientInstanceId": reused_client_instance_id,
        },
        token=token,
        idempotency_key=f"reuse-preparation-{time.time_ns()}",
        expected=201,
    )
    if reused["disposition"] != "reused" or reused["session"]["id"] != session["id"]:
        raise RuntimeError(f"compatible preparation did not reuse one session: {reused!r}")
    if session["id"] in visible_session_ids(org_id, state["projectId"], token):
        raise RuntimeError("uncommitted preparation appeared in the session list")
    state.update({
        "preparationId": session["id"],
        "preparationPrompt": prompt,
        "preparationCommitKey": commit_key,
        "preparationClientInstanceId": client_instance_id,
        "preparationGeneration": preparation["generation"],
        "preparationExpiresAt": preparation["expiresAt"],
        "preparationStartedAt": started,
    })
    state_file.write_text(json.dumps(state))
elif mode == "renew-preparation":
    state = json.loads(state_file.read_text())
    renewed = request(
        "POST",
        f"/api/cloud/v1/orgs/{state['orgId']}/sessions/{state['preparationId']}/renew-preparation",
        body={
            "clientInstanceId": state["preparationClientInstanceId"],
            "generation": state["preparationGeneration"],
        },
        token=state["token"],
    )["preparation"]
    if renewed["leaseSeconds"] != 120:
        raise RuntimeError(f"invalid renewed lease: {renewed!r}")
    if renewed["expiresAt"] <= state["preparationExpiresAt"]:
        raise RuntimeError(
            f"renewal did not extend expiry: {state['preparationExpiresAt']} -> {renewed['expiresAt']}"
        )
    state["preparationExpiresAt"] = renewed["expiresAt"]
    state_file.write_text(json.dumps(state))
elif mode == "prepare-ready":
    state = json.loads(state_file.read_text())
    milestones = {}
    wait_for_startup(
        state["orgId"],
        state["preparationId"],
        state["token"],
        time.monotonic(),
        milestones,
    )
    state["preparationReadyMs"] = round(
        (time.time() - state["preparationStartedAt"]) * 1000
    )
    state_file.write_text(json.dumps(state))
    print(json.dumps({"preparationReadyMs": state["preparationReadyMs"]}))
elif mode == "commit-preparation":
    state = json.loads(state_file.read_text())
    token = state["token"]
    org_id = state["orgId"]
    session_id = state["preparationId"]
    body = {
        "displayName": "Prepared session smoke",
        "prompt": state["preparationPrompt"],
        "clientInstanceId": state["preparationClientInstanceId"],
        "generation": state["preparationGeneration"],
    }
    first = request(
        "POST",
        f"/api/cloud/v1/orgs/{org_id}/sessions/{session_id}/commit-preparation",
        body=body,
        token=token,
        idempotency_key=state["preparationCommitKey"],
    )["session"]
    repeated = request(
        "POST",
        f"/api/cloud/v1/orgs/{org_id}/sessions/{session_id}/commit-preparation",
        body=body,
        token=token,
        idempotency_key=state["preparationCommitKey"],
    )["session"]
    if first["id"] != session_id or repeated["id"] != session_id:
        raise RuntimeError("preparation commit changed the session identity")
    listed = visible_session_ids(org_id, state["projectId"], token)
    if listed.count(session_id) != 1:
        raise RuntimeError(f"committed preparation list count was {listed.count(session_id)}")
    matching_messages = [
        event
        for event in events(org_id, session_id, token)
        if event.get("type") == "chat.user_message"
        and event.get("payload", {}).get("text") == state["preparationPrompt"]
    ]
    if len(matching_messages) != 1:
        raise RuntimeError(
            f"preparation prompt was not durable exactly once: {matching_messages!r}"
        )
elif mode == "delete-preparation":
    state = json.loads(state_file.read_text())
    request(
        "DELETE",
        f"/api/cloud/v1/orgs/{state['orgId']}/sessions/{state['preparationId']}",
        token=state["token"],
        expected=202,
    )
elif mode == "renew-committed-preparation":
    state = json.loads(state_file.read_text())
    error = request(
        "POST",
        f"/api/cloud/v1/orgs/{state['orgId']}/sessions/{state['preparationId']}/renew-preparation",
        body={
            "clientInstanceId": state["preparationClientInstanceId"],
            "generation": state["preparationGeneration"],
        },
        token=state["token"],
        expected=409,
    )
    if error.get("code") != "PREPARATION_COMMITTED":
        raise RuntimeError(f"unexpected committed renewal error: {error!r}")
elif mode == "cancel-preparation":
    state = json.loads(state_file.read_text())
    token = state["token"]
    org_id = state["orgId"]
    client_instance_id = str(uuid.uuid4())
    session = request(
        "POST",
        f"/api/cloud/v1/orgs/{org_id}/session-preparations",
        body={
            "projectId": state["projectId"],
            "harness": state["harness"],
            "provider": "docker",
            "clientInstanceId": client_instance_id,
        },
        token=token,
        idempotency_key=f"cancel-preparation-{time.time_ns()}",
        expected=201,
    )["session"]
    request(
        "DELETE",
        f"/api/cloud/v1/orgs/{org_id}/sessions/{session['id']}",
        token=token,
        expected=202,
    )
    if session["id"] in visible_session_ids(org_id, state["projectId"], token):
        raise RuntimeError("cancelled preparation appeared in the session list")
elif mode == "expire-preparation":
    state = json.loads(state_file.read_text())
    token = state["token"]
    org_id = state["orgId"]
    client_instance_id = str(uuid.uuid4())
    prepared = request(
        "POST",
        f"/api/cloud/v1/orgs/{org_id}/session-preparations",
        body={
            "projectId": state["projectId"],
            "harness": state["harness"],
            "provider": "docker",
            "clientInstanceId": client_instance_id,
        },
        token=token,
        idempotency_key=f"expire-preparation-{time.time_ns()}",
        expected=201,
    )
    session = prepared["session"]
    if session["id"] in visible_session_ids(org_id, state["projectId"], token):
        raise RuntimeError("expiring preparation appeared in the session list")
    state["expiringPreparationId"] = session["id"]
    state["expiringPreparationClientInstanceId"] = client_instance_id
    state["expiringPreparationGeneration"] = prepared["preparation"]["generation"]
    state_file.write_text(json.dumps(state))
elif mode == "renew-expired-preparation":
    state = json.loads(state_file.read_text())
    error = request(
        "POST",
        f"/api/cloud/v1/orgs/{state['orgId']}/sessions/{state['expiringPreparationId']}/renew-preparation",
        body={
            "clientInstanceId": state["expiringPreparationClientInstanceId"],
            "generation": state["expiringPreparationGeneration"],
        },
        token=state["token"],
        expected=410,
    )
    if error.get("code") != "PREPARATION_EXPIRED":
        raise RuntimeError(f"unexpected expired renewal error: {error!r}")
elif mode == "start":
    state = json.loads(state_file.read_text())
    token = state["token"]
    org_id = state["orgId"]
    startup_started = time.monotonic()
    session = request(
        "POST",
        f"/api/cloud/v1/orgs/{org_id}/sessions",
        body={
            "projectId": state["projectId"],
            "kind": "orchestrator",
            "harness": "claude-code",
            "displayName": "Persistence Test",
            "prompt": "",
            "mode": "trusted",
        },
        token=token,
        idempotency_key=f"session-{time.time_ns()}",
        expected=201,
    )["session"]
    milestones = {
        "sessionAcceptedMs": round((time.monotonic() - startup_started) * 1000)
    }
    early_message_key = f"early-message-{time.time_ns()}"
    early_message_text = f"cold-start-early-message-{time.time_ns()}"
    early_message = request(
        "POST",
        f"/api/cloud/v1/orgs/{org_id}/sessions/{session['id']}/messages",
        body={"text": early_message_text, "clientSequence": 1},
        token=token,
        idempotency_key=early_message_key,
        expected=202,
    )["event"]
    repeated_message = request(
        "POST",
        f"/api/cloud/v1/orgs/{org_id}/sessions/{session['id']}/messages",
        body={"text": early_message_text, "clientSequence": 1},
        token=token,
        idempotency_key=early_message_key,
        expected=202,
    )["event"]
    if repeated_message.get("sequence") != early_message.get("sequence"):
        raise RuntimeError(
            f"idempotent early message changed sequence: {early_message!r} vs {repeated_message!r}"
        )
    milestones["earlyMessageAcceptedMs"] = round(
        (time.monotonic() - startup_started) * 1000
    )
    wait_for_startup(org_id, session["id"], token, startup_started, milestones)
    wait_for_terminal_turn(org_id, session["id"], token, 0)
    matching_messages = [
        event
        for event in events(org_id, session["id"], token)
        if event.get("type") == "chat.user_message"
        and event.get("payload", {}).get("clientSequence") == 1
        and event.get("payload", {}).get("text") == early_message_text
    ]
    if len(matching_messages) != 1:
        raise RuntimeError(
            f"early message was not durable exactly once: {matching_messages!r}"
        )
    milestones["earlyMessageDeliveredMs"] = round(
        (time.monotonic() - startup_started) * 1000
    )
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
    wait_for_agent_terminal_ticket(org_id, session["id"], token)
    state.update({"sessionId": session["id"], "timing": milestones})
    state_file.write_text(json.dumps(state))
elif mode == "verify":
    state = json.loads(state_file.read_text())
    token = state["token"]
    org_id = state["orgId"]
    session_id = state["sessionId"]
    wait_for_running(org_id, session_id, token)
    wait_for_agent_terminal_ticket(org_id, session_id, token)
elif mode == "wake":
    state = json.loads(state_file.read_text())
    token = state["token"]
    org_id = state["orgId"]
    session_id = state["sessionId"]
    resume = request(
        "POST",
        f"/api/cloud/v1/orgs/{org_id}/sessions/{session_id}/resume",
        token=token,
        expected=202,
    )["session"]
    if resume.get("desiredState") != "running":
        raise RuntimeError(f"resume did not record running intent: {resume!r}")
    wait_for_running(org_id, session_id, token)
    wait_for_agent_terminal_ticket(org_id, session_id, token)
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

state_value() {
	local key="$1"
	python3 - "$state_file" "$key" <<'PY'
import json
import pathlib
import sys

state = json.loads(pathlib.Path(sys.argv[1]).read_text())
print(state[sys.argv[2]])
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

seed_smoke_project() {
	local org project
	org="$(org_id)"
	# Project creation normally verifies a user credential against the remote
	# repository API. Seed only this disposable local database so the smoke test
	# stays credential-free and the Docker worker exercises anonymous checkout.
	project="$(
		compose exec \
			-e "PGOPTIONS=-c ao.org_id=${org}" \
			-T postgres \
			psql \
			--username ao_cloud_owner \
			--dbname ao_cloud \
			--quiet \
			--tuples-only \
			--no-align \
			--command \
			"INSERT INTO ao_projects (
				org_id, display_name, repository_url, default_branch, config
			) VALUES (
				'${org}', 'Persistence Test',
				'https://github.com/octocat/Hello-World', 'main', '{}'::jsonb
			) RETURNING id"
	)"
	python3 - "$state_file" "$project" <<'PY'
import json
import pathlib
import sys
import uuid

path = pathlib.Path(sys.argv[1])
state = json.loads(path.read_text())
project_id = sys.argv[2].strip()
uuid.UUID(project_id)
state["projectId"] = project_id
path.write_text(json.dumps(state))
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

wait_for_git_workspace() {
	local container_id="$1" attempts=30
	while ((attempts > 0)); do
		if docker exec "$container_id" git -C /workspace/repository rev-parse --is-inside-work-tree >/dev/null 2>&1; then
			return 0
		fi
		attempts=$((attempts - 1))
		sleep 1
	done
	echo "Worker Git workspace did not become ready." >&2
	return 1
}

prepare_workspace_review_fixture() {
	local container_id="$1"
	docker exec "$container_id" bash -c '
		set -euo pipefail
		cd /workspace/repository
		git config user.email smoke@ao.local
		git config user.name "AO Cloud Smoke"
		printf "unchanged\n" > review-unchanged.txt
		printf "base committed\n" > review-committed.txt
		printf "base staged\n" > review-staged.txt
		printf "base unstaged\n" > review-unstaged.txt
		git add review-unchanged.txt review-committed.txt review-staged.txt review-unstaged.txt
		git commit -m "test: establish review baseline" >/dev/null
		git update-ref refs/ao/diff-base HEAD
		printf "committed change\n" > review-committed.txt
		git add review-committed.txt
		git commit -m "test: committed review change" >/dev/null
		printf "staged change\n" > review-staged.txt
		git add review-staged.txt
		printf "unstaged change\n" > review-unstaged.txt
		printf "untracked change\n" > review-untracked.txt
	'
}

exercise_workspace_diff_api() {
	python3 - "$AO_CLOUD_PORT" "$state_file" <<'PY'
import json
import pathlib
import sys
import urllib.error
import urllib.request

port, state_path = sys.argv[1:]
state = json.loads(pathlib.Path(state_path).read_text())
base_url = f"http://127.0.0.1:{port}"
prefix = f"/api/cloud/v1/orgs/{state['orgId']}/sessions/{state['sessionId']}"
headers = {"Accept": "application/json", "Authorization": f"Bearer {state['token']}"}

def request(method, path, body=None):
    request_headers = dict(headers)
    data = None
    if body is not None:
        request_headers["Content-Type"] = "application/json"
        data = json.dumps(body).encode()
    operation = urllib.request.Request(base_url + path, data=data, headers=request_headers, method=method)
    try:
        with urllib.request.urlopen(operation, timeout=10) as response:
            return json.load(response)
    except urllib.error.HTTPError as error:
        raise RuntimeError(f"{method} {path} returned {error.code}: {error.read().decode(errors='replace')}") from error

workspace_file = request("PUT", prefix + "/workspace/file", {"path": ".ao-cloud-smoke-api", "content": "durable-worker-transport\n"})
if workspace_file.get("content") != "durable-worker-transport\n":
    raise RuntimeError(f"workspace write returned unexpected content: {workspace_file!r}")
read_back = request("GET", prefix + "/workspace/file?path=.ao-cloud-smoke-api")
if read_back != workspace_file:
    raise RuntimeError(f"workspace read did not match the durable write: {read_back!r}")
listing = request("GET", prefix + "/workspace/files?limit=100")
if ".ao-cloud-smoke-api" not in {item.get("path") for item in listing["items"]}:
    raise RuntimeError(f"workspace listing omitted the written file: {listing!r}")
diff = request("GET", prefix + "/workspace/diff")
summary = next((item for item in diff.get("files", []) if item.get("path") == ".ao-cloud-smoke-api"), None)
if summary != {"path": ".ao-cloud-smoke-api", "status": "untracked", "additions": 1, "deletions": 0, "binary": False}:
    raise RuntimeError(f"workspace diff summary returned unexpected file: {summary!r}")
detail = request("GET", prefix + "/workspace/file/diff?path=.ao-cloud-smoke-api")
if detail.get("status") != "untracked" or detail.get("content") != "durable-worker-transport\n" or "new file mode 100644" not in detail.get("diff", "") or detail.get("diffTruncated"):
    raise RuntimeError(f"workspace diff-file returned unexpected detail: {detail!r}")

review = request("GET", prefix + "/workspace/review")
expected = {
    "committed": "review-committed.txt",
    "staged": "review-staged.txt",
    "unstaged": "review-unstaged.txt",
    "untracked": "review-untracked.txt",
}
for section, path in expected.items():
    if path not in {item.get("path") for item in review["sections"][section]}:
        raise RuntimeError(f"workspace review omitted {path} from {section}: {review!r}")
if "review-unchanged.txt" not in {item.get("path") for item in review["files"]}:
    raise RuntimeError(f"workspace review omitted unchanged tracked file: {review!r}")

tree = request("GET", prefix + "/workspace/tree")
if "review-unchanged.txt" not in {item.get("path") for item in tree["entries"]}:
    raise RuntimeError(f"workspace tree omitted unchanged tracked file: {tree!r}")
search = request("GET", prefix + "/workspace/search?query=review-unstaged")
if "review-unstaged.txt" not in {item.get("path") for item in search["results"]}:
    raise RuntimeError(f"workspace search omitted matching path: {search!r}")

diffs = request("POST", prefix + "/workspace/review/diffs", {
    "scope": "staged", "paths": ["review-staged.txt"], "contextLines": 3,
    "ignoreWhitespace": False, "workspaceVersion": review["workspaceVersion"],
})
if "staged change" not in "".join(group.get("patch", "") for group in diffs["groups"]):
    raise RuntimeError(f"scoped workspace diff omitted staged content: {diffs!r}")
revision = request("GET", prefix + "/workspace/review/revision?path=review-staged.txt&scope=staged&side=after")
if revision.get("content") != "staged change\n":
    raise RuntimeError(f"workspace revision returned unexpected content: {revision!r}")

editable = request("GET", prefix + "/workspace/review/file?path=review-unstaged.txt&scope=unstaged")
written = request("PUT", prefix + "/workspace/review/file", {
    "path": "review-unstaged.txt", "content": "updated through review API\n",
    "expectedFileFingerprint": editable["fileFingerprint"],
})
if written.get("content") != "updated through review API\n" or written.get("fileFingerprint") == editable["fileFingerprint"]:
    raise RuntimeError(f"fingerprint-checked workspace write failed: {written!r}")
PY
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

record_startup_result() {
	python3 - "$state_file" "${AO_CLOUD_STARTUP_RESULT_FILE:-}" <<'PY'
import json
import pathlib
import sys

state_path, result_path = sys.argv[1:]
state = json.loads(pathlib.Path(state_path).read_text())
result = dict(state["timing"])

payload = json.dumps(result, sort_keys=True)
if result_path:
    pathlib.Path(result_path).write_text(payload + "\n")
print("COLD_START_RESULT " + payload)
PY
}

force_preparation_expiry() {
	local org="$1" session="$2"
	compose exec -e "PGOPTIONS=-c ao.org_id=${org}" -T postgres \
		psql -U ao_cloud_owner -d ao_cloud -v ON_ERROR_STOP=1 -c \
		"UPDATE ao_sessions
		 SET preparation_expires_at = now() - interval '1 second'
		 WHERE org_id = '${org}' AND id = '${session}';
		 UPDATE ao_sandboxes
		 SET preparation_expires_at = now() - interval '1 second', reconcile_after = now()
		 WHERE org_id = '${org}' AND session_id = '${session}';" >/dev/null
}

wait_for_preparation_expired() {
	local org="$1" session="$2" attempts=90 expired=""
	while ((attempts > 0)); do
		expired="$(
			compose exec -e "PGOPTIONS=-c ao.org_id=${org}" -T postgres \
				psql -U ao_cloud_owner -d ao_cloud -Atc \
				"SELECT session.is_terminated
				 FROM ao_sessions session
				 JOIN ao_sandboxes sandbox
				   ON sandbox.org_id = session.org_id AND sandbox.session_id = session.id
				 WHERE session.org_id = '${org}' AND session.id = '${session}'
				   AND sandbox.observed_state IN ('deleted', 'terminated', 'failed')"
		)"
		if [[ "$expired" == t ]]; then
			return 0
		fi
		attempts=$((attempts - 1))
		sleep 1
	done
	echo "Expired preparation ${session} was not deleted and terminated." >&2
	return 1
}

wait_for_sql_true() {
	local org="$1" query="$2" description="$3" attempts=90 result=""
	while ((attempts > 0)); do
		result="$(
			compose exec -e "PGOPTIONS=-c ao.org_id=${org}" -T postgres \
				psql -U ao_cloud_owner -d ao_cloud -Atc "$query"
		)"
		if [[ "$result" == t ]]; then
			return 0
		fi
		attempts=$((attempts - 1))
		sleep 1
	done
	echo "$description" >&2
	return 1
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
seed_smoke_project
if [[ "$measure_startup" != true ]]; then
	exercise_api prepare
	exercise_api renew-preparation
	prepared_session="$(state_value preparationId)"
	wait_for_sql_true "$(org_id)" \
		"SELECT session.preparation_expires_at = sandbox.preparation_expires_at
			AND session.preparation_expires_at > now()
		 FROM ao_sessions session
		 JOIN ao_sandboxes sandbox
		   ON sandbox.org_id = session.org_id AND sandbox.session_id = session.id
		 WHERE session.org_id = '$(org_id)' AND session.id = '${prepared_session}'" \
		"Renewed preparation expiries did not match."
	prepared_worker="$(wait_for_worker "$prepared_session")"
	exercise_api prepare-ready
	if [[ "$(docker exec "$prepared_worker" git -C /workspace/repository config --get remote.origin.promisor)" != true ]]; then
		echo "Prepared checkout is not configured as a partial clone." >&2
		exit 1
	fi
	if [[ "$(docker exec "$prepared_worker" git -C /workspace/repository config --get remote.origin.partialclonefilter)" != blob:none ]]; then
		echo "Prepared checkout does not use the blobless filter." >&2
		exit 1
	fi
	exercise_api commit-preparation
	exercise_api renew-committed-preparation
	wait_for_sql_true "$(org_id)" \
		"SELECT EXISTS (
			SELECT 1 FROM ao_worker_requests
			WHERE session_id = '${prepared_session}'
			  AND kind = 'terminal.input' AND status = 'succeeded'
		) OR EXISTS (
			SELECT 1 FROM ao_turns
			WHERE session_id = '${prepared_session}' AND state = 'completed'
		)" \
		"Prepared prompt was not delivered to the running harness."
	exercise_api delete-preparation
	wait_for_worker_stopped "$prepared_worker"
	exercise_api cancel-preparation
	exercise_api expire-preparation
	expiring_session="$(state_value expiringPreparationId)"
	force_preparation_expiry "$(org_id)" "$expiring_session"
	exercise_api renew-expired-preparation
	wait_for_preparation_expired "$(org_id)" "$expiring_session"
fi
exercise_api start
session="$(session_id)"
org="$(org_id)"
first_worker="$(wait_for_worker "$session")"
if [[ "$measure_startup" == true ]]; then
	record_startup_result
	exit 0
fi
wait_for_git_workspace "$first_worker"
prepare_workspace_review_fixture "$first_worker"
exercise_workspace_diff_api
docker exec "$first_worker" ao list >/dev/null
# ao-worker boot must materialize the cloud using-ao skill where the standing
# prompts point the agent.
docker exec "$first_worker" test -f /workspace/.ao/worker/skills/using-ao/SKILL.md
docker exec "$first_worker" test -f /workspace/.ao/worker/skills/using-ao/commands/orchestration.md
# The harness process appears shortly after the worker container does; retry
# the argv probe instead of racing the PTY launch.
wait_for_process_marker() {
	local container="$1" marker="$2" attempts=30
	while ((attempts > 0)); do
		if docker exec "$container" sh -c '
			for cmdline in /proc/[0-9]*/cmdline; do
				[ -r "$cmdline" ] || continue
				if tr "\000" "\n" < "$cmdline" | grep -Fq -- "$1"; then
					exit 0
				fi
			done
			exit 1
		' sh "$marker"; then
			return 0
		fi
		attempts=$((attempts - 1))
		sleep 1
	done
	echo "Process marker '$marker' never appeared in $container." >&2
	exit 1
}
# The orchestrator harness launches with the coordination prompt in its argv.
wait_for_process_marker "$first_worker" "AO Orchestrator Role"
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
# Send BEFORE the child is up: the message must queue durably and be typed into
# the agent PTY once the terminal has painted (readiness gate), not be lost.
docker exec "$first_worker" ao send "$child_session" "Report smoke status" >/dev/null
child_worker="$(wait_for_worker "$child_session")"
# The child harness carries the worker prompt, including report guidance (it
# has an orchestrator parent).
wait_for_process_marker "$child_worker" "AO Worker Role"
wait_for_child_message() {
	local target="$1" description="$2" attempts=45 forwarded=""
	while ((attempts > 0)); do
		forwarded="$(
			compose exec -e "PGOPTIONS=-c ao.org_id=${org}" -T postgres \
				psql -U ao_cloud_owner -d ao_cloud -Atc "$target"
		)"
		if [[ "$forwarded" == "t" ]]; then
			return 0
		fi
		attempts=$((attempts - 1))
		sleep 1
	done
	echo "$description" >&2
	exit 1
}
wait_for_child_message \
	"SELECT EXISTS (SELECT 1 FROM ao_turns WHERE session_id = '${child_session}' AND state = 'completed') OR EXISTS (SELECT 1 FROM ao_worker_requests WHERE session_id = '${child_session}' AND kind = 'terminal.input' AND status = 'succeeded')" \
	"Orchestrator message was not forwarded into the child agent PTY."
# Child -> orchestrator report lands in the parent's conversation with the
# worker-provenance prefix.
docker exec "$child_worker" ao report "smoke child reporting done" >/dev/null
wait_for_child_message \
	"SELECT EXISTS (SELECT 1 FROM ao_events WHERE session_id = '${session}' AND type = 'chat.user_message' AND payload::text LIKE '%from worker%smoke child reporting done%')" \
	"Child report did not land in the orchestrator conversation."
# A parentless orchestrator cannot report (scope never issued).
if docker exec "$first_worker" ao report "should be rejected" >/dev/null 2>&1; then
	echo "ao report from a parentless session must fail with SCOPE_REQUIRED." >&2
	exit 1
fi
# List enrichment: branch and prs ride every child item.
docker exec "$first_worker" ao list --json | python3 -c '
import json, sys
items = json.load(sys.stdin)
assert items, "orchestrator sees no children"
child = items[0]
assert child["branch"], child
assert isinstance(child["prs"], list), child
'
docker exec "$first_worker" ao kill "$child_session" >/dev/null
# Terminated children leave the default listing; --all keeps the history.
attempts=45
while ((attempts > 0)); do
	list_state="$(docker exec "$first_worker" sh -c "ao list --json && echo --- && ao list --all --json" | python3 -c '
import json, sys
raw = sys.stdin.read().split("---")
live = {item["id"] for item in (json.loads(raw[0]) or [])}
everything = {item["id"]: item for item in (json.loads(raw[1]) or [])}
child = sys.argv[1]
if child not in live and child in everything and everything[child]["isTerminated"]:
    print("settled")
else:
    print("pending")
' "$child_session")"
	if [[ "$list_state" == "settled" ]]; then
		break
	fi
	attempts=$((attempts - 1))
	sleep 1
done
if [[ "$list_state" != "settled" ]]; then
	echo "Killed child did not settle out of the default ao list (or out of --all)." >&2
	exit 1
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
