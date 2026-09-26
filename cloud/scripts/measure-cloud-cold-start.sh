#!/usr/bin/env bash
set -euo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
smoke_script="$repository_root/scripts/test-cloud-local.sh"

usage() {
	cat <<'EOF'
Usage: cloud/scripts/measure-cloud-cold-start.sh [--runs N]
       cloud/scripts/measure-cloud-cold-start.sh --self-test

Measures a fresh local Docker worker from session creation through agent.ready,
without starting a browser. Stack build and
control-plane startup are excluded from the reported session timings.
EOF
}

summarize() {
	python3 - "$@" <<'PY'
import json
import math
import pathlib
import sys

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
runs = [json.loads(pathlib.Path(path).read_text()) for path in sys.argv[1:]]
if not runs:
    raise SystemExit("no cold-start results were supplied")
for index, run in enumerate(runs, 1):
    missing = sorted(required - run.keys())
    if missing:
        raise SystemExit(f"run {index} omitted fields: {', '.join(missing)}")


def percentile(values, quantile):
    ordered = sorted(values)
    return ordered[max(0, math.ceil(len(ordered) * quantile) - 1)]


summary = {"runs": len(runs), "milliseconds": {}}
for field in sorted(required):
    values = [run[field] for run in runs]
    summary["milliseconds"][field] = {
        "p50": percentile(values, 0.50),
        "p95": percentile(values, 0.95),
        "max": max(values),
    }
print("COLD_START_SUMMARY " + json.dumps(summary, sort_keys=True))
PY
}

runs=1
self_test=false
while (($# > 0)); do
	case "$1" in
		--runs)
			[[ $# -ge 2 ]] || { usage >&2; exit 2; }
			runs="$2"
			shift 2
			;;
		--self-test)
			self_test=true
			shift
			;;
		--help|-h)
			usage
			exit 0
			;;
		*)
			usage >&2
			exit 2
			;;
	esac
done

if ! [[ "$runs" =~ ^[1-9][0-9]*$ ]] || ((runs > 100)); then
	echo "--runs must be an integer from 1 through 100" >&2
	exit 2
fi
if [[ "$self_test" == true && "$runs" != 1 ]]; then
	echo "--self-test cannot be combined with --runs" >&2
	exit 2
fi

result_directory="$(mktemp -d "${TMPDIR:-/tmp}/ao-cloud-cold-start.XXXXXX")"
cleanup() {
	rm -rf "$result_directory"
}
trap cleanup EXIT

if [[ "$self_test" == true ]]; then
	bash -n "$smoke_script"
	bash -n "$0"
	if bash "$0" --runs 0 >/dev/null 2>&1; then
		echo "invalid run count was accepted" >&2
		exit 1
	fi
	printf '%s\n' '{"agentLaunchStartedMs":9000,"agentReadyMs":10000,"checkoutCompletedMs":7000,"checkoutStartedMs":6100,"earlyMessageAcceptedMs":80,"earlyMessageDeliveredMs":10100,"restoreCompletedMs":8000,"restoreStartedMs":7000,"runtimeRunningMs":9000,"sandboxProvisioningMs":100,"sessionAcceptedMs":50,"workerConnectedMs":5000,"workerReadyMs":6000,"workspaceReadyMs":8000}' > "$result_directory/1.json"
	printf '%s\n' '{"agentLaunchStartedMs":19000,"agentReadyMs":20000,"checkoutCompletedMs":17000,"checkoutStartedMs":16100,"earlyMessageAcceptedMs":150,"earlyMessageDeliveredMs":20100,"restoreCompletedMs":18000,"restoreStartedMs":17000,"runtimeRunningMs":19000,"sandboxProvisioningMs":200,"sessionAcceptedMs":100,"workerConnectedMs":15000,"workerReadyMs":16000,"workspaceReadyMs":18000}' > "$result_directory/2.json"
	summary="$(summarize "$result_directory/1.json" "$result_directory/2.json")"
	[[ "$summary" == *'"agentReadyMs": {"max": 20000, "p50": 10000, "p95": 20000}'* ]]
	printf 'cold-start measurement self-test passed\n'
	exit 0
fi

results=()
for ((run = 1; run <= runs; run++)); do
	result="$result_directory/${run}.json"
	printf 'Cold-start run %d/%d\n' "$run" "$runs"
	AO_CLOUD_STARTUP_RESULT_FILE="$result" bash "$smoke_script" --measure-startup
	results+=("$result")
done

summarize "${results[@]}"
