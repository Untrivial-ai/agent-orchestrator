#!/usr/bin/env bash
set -euo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
smoke_script="${AO_CLOUD_VARIATION_SMOKE_SCRIPT:-$repository_root/scripts/test-cloud-local.sh}"

usage() {
	cat <<'EOF'
Usage: cloud/scripts/test-cloud-cold-start-variations.sh --self-test
       cloud/scripts/test-cloud-cold-start-variations.sh --local

Runs the cold-start smoke path with stream plus relay, durable stream, and
polling transports. The local matrix also runs the replacement lifecycle once.
EOF
}

validate_result() {
	local result_file="$1" case_name="$2" stream="$3" relay="$4"
	python3 - "$result_file" "$case_name" "$stream" "$relay" <<'PY'
import json
import pathlib
import sys

path, name, stream, relay = sys.argv[1:]
result = json.loads(pathlib.Path(path).read_text())
required = {
    "sessionAcceptedMs",
    "earlyMessageAcceptedMs",
    "earlyMessageDeliveredMs",
    "sandboxProvisioningMs",
    "workerConnectedMs",
    "workerReadyMs",
    "checkoutStartedMs",
    "checkoutCompletedMs",
    "restoreStartedMs",
    "restoreCompletedMs",
    "workspaceReadyMs",
    "agentLaunchStartedMs",
    "agentReadyMs",
    "runtimeRunningMs",
}
missing = sorted(required - result.keys())
if missing:
    raise SystemExit(f"{name} omitted fields: {', '.join(missing)}")
invalid = sorted(
    field
    for field in required
    if not isinstance(result[field], int) or isinstance(result[field], bool) or result[field] < 0
)
if invalid:
    raise SystemExit(f"{name} has invalid fields: {', '.join(invalid)}")
ordered = [
    "workerConnectedMs",
    "checkoutStartedMs",
    "checkoutCompletedMs",
    "restoreCompletedMs",
    "workspaceReadyMs",
    "agentLaunchStartedMs",
    "agentReadyMs",
    "earlyMessageDeliveredMs",
]
for before, after in zip(ordered, ordered[1:]):
    if result[before] > result[after]:
        raise SystemExit(
            f"{name} has impossible order: {before}={result[before]} after {after}={result[after]}"
        )
summary = {
    "case": name,
    "stream": stream == "1",
    "relay": relay == "1",
    "milliseconds": result,
}
print("COLD_START_VARIATION " + json.dumps(summary, sort_keys=True))
PY
}

run_measure_case() {
	local case_name="$1" stream="$2" relay="$3"
	local result_file="$result_directory/${case_name}.json"
	printf 'Cold-start variation: %s\n' "$case_name"
	env \
		AO_CLOUD_TERMINAL_STREAM="$stream" \
		AO_CLOUD_TERMINAL_RELAY="$relay" \
		AO_CLOUD_STARTUP_RESULT_FILE="$result_file" \
		bash "$smoke_script" --measure-startup
	validate_result "$result_file" "$case_name" "$stream" "$relay"
}

run_local_matrix() {
	run_measure_case stream-relay 1 1
	run_measure_case stream-durable 1 0
	run_measure_case polling 0 0
	printf 'Cold-start variation: replacement-lifecycle\n'
	env AO_CLOUD_TERMINAL_STREAM=1 AO_CLOUD_TERMINAL_RELAY=1 bash "$smoke_script"
}

mode="${1:-}"
if [[ $# != 1 || ("$mode" != "--self-test" && "$mode" != "--local") ]]; then
	usage >&2
	exit 2
fi

result_directory="$(mktemp -d "${TMPDIR:-/tmp}/ao-cloud-variations.XXXXXX")"
cleanup() {
	rm -rf "$result_directory"
}
trap cleanup EXIT

if [[ "$mode" == "--self-test" ]]; then
	bash -n "$0"
	bash -n "$repository_root/scripts/test-cloud-local.sh"
	fake_root="$(mktemp -d "${TMPDIR:-/tmp}/ao-cloud-variation-self-test.XXXXXX")"
	fake_smoke="$fake_root/fake-smoke.sh"
	fake_log="$fake_root/calls.log"
	cat > "$fake_smoke" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s:%s:%s\n' "${AO_CLOUD_TERMINAL_STREAM:-}" "${AO_CLOUD_TERMINAL_RELAY:-}" "${1:-lifecycle}" >> "$AO_CLOUD_VARIATION_FAKE_LOG"
if [[ "${1:-}" == "--measure-startup" ]]; then
    printf '%s\n' '{"agentLaunchStartedMs":80,"agentReadyMs":90,"checkoutCompletedMs":50,"checkoutStartedMs":40,"earlyMessageAcceptedMs":3,"earlyMessageDeliveredMs":95,"restoreCompletedMs":70,"restoreStartedMs":50,"runtimeRunningMs":30,"sandboxProvisioningMs":10,"sessionAcceptedMs":2,"workerConnectedMs":30,"workerReadyMs":35,"workspaceReadyMs":70}' > "$AO_CLOUD_STARTUP_RESULT_FILE"
fi
EOF
	chmod 0700 "$fake_smoke"
	AO_CLOUD_VARIATION_SMOKE_SCRIPT="$fake_smoke" \
		AO_CLOUD_VARIATION_FAKE_LOG="$fake_log" \
		bash "$0" --local >/dev/null
	mapfile -t calls < "$fake_log"
	expected=(
		"1:1:--measure-startup"
		"1:0:--measure-startup"
		"0:0:--measure-startup"
		"1:1:lifecycle"
	)
	if [[ "${calls[*]}" != "${expected[*]}" ]]; then
		echo "Variation matrix mismatch: ${calls[*]}" >&2
		exit 1
	fi
	invalid="$fake_root/invalid.json"
	printf '%s\n' '{"sessionAcceptedMs":1}' > "$invalid"
	if validate_result "$invalid" invalid 0 0 >/dev/null 2>&1; then
		echo "Variation result validation accepted an incomplete sample." >&2
		exit 1
	fi
	rm -rf "$fake_root"
	printf 'cold-start variation harness self-test passed\n'
	exit 0
fi

run_local_matrix
