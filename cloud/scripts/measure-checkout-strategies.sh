#!/usr/bin/env bash
set -euo pipefail

usage() {
	cat <<'EOF'
Usage: cloud/scripts/measure-checkout-strategies.sh [--runs N]

Builds local synthetic repositories and compares the current full checkout,
blob-filtered checkout, and blob-filtered protocol v2 checkout. No production
checkout setting is changed.
EOF
}

runs=1
while (($# > 0)); do
	case "$1" in
		--runs)
			[[ $# -ge 2 ]] || { usage >&2; exit 2; }
			runs="$2"
			shift 2
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
if ! [[ "$runs" =~ ^[1-9][0-9]*$ ]] || ((runs > 10)); then
	echo "--runs must be an integer from 1 through 10" >&2
	exit 2
fi

for tool in git python3 du; do
	command -v "$tool" >/dev/null || {
		echo "$tool is required" >&2
		exit 1
	}
done

work_root="$(mktemp -d "${TMPDIR:-/tmp}/ao-checkout-matrix.XXXXXX")"
cleanup() {
	rm -rf "$work_root"
}
trap cleanup EXIT

export GIT_CONFIG_NOSYSTEM=1
export GIT_TERMINAL_PROMPT=0
export GIT_LFS_SKIP_SMUDGE=1
export GIT_AUTHOR_NAME="AO Checkout Test"
export GIT_AUTHOR_EMAIL="checkout-test@example.invalid"
export GIT_COMMITTER_NAME="$GIT_AUTHOR_NAME"
export GIT_COMMITTER_EMAIL="$GIT_AUTHOR_EMAIL"

create_submodule() {
	local source="$work_root/submodule-source"
	local remote="$work_root/submodule.git"
	git init --quiet --initial-branch=main "$source"
	printf 'submodule fixture\n' > "$source/fixture.txt"
	git -C "$source" add fixture.txt
	git -C "$source" commit --quiet -m "fixture"
	git clone --quiet --bare "$source" "$remote"
	git -C "$remote" symbolic-ref HEAD refs/heads/main
}

create_repository() {
	local name="$1" commits="$2" files="$3" generated_kib="$4"
	local source="$work_root/${name}-source"
	local remote="$work_root/${name}.git"
	git init --quiet --initial-branch=main "$source"
	mkdir -p "$source/apps/web/src" "$source/apps/api/src" "$source/packages/shared/src" "$source/generated"
	printf '# %s checkout fixture\n' "$name" > "$source/README.md"
	printf 'history 0\n' > "$source/history.txt"
	printf 'version https://git-lfs.github.com/spec/v1\noid sha256:%064d\nsize 1048576\n' 0 > "$source/asset.lfs"
	python3 - "$source" "$files" "$generated_kib" <<'PY'
import pathlib
import sys

root = pathlib.Path(sys.argv[1])
file_count = int(sys.argv[2])
generated_kib = int(sys.argv[3])
roots = [root / "apps/web/src", root / "apps/api/src", root / "packages/shared/src"]
for index in range(file_count):
    target = roots[index % len(roots)] / f"module-{index:04d}.ts"
    target.write_text(f"export const value{index} = {index};\n")
chunk = bytes((index % 251 for index in range(1024)))
with (root / "generated/bundle.dat").open("wb") as output:
    for _ in range(generated_kib):
        output.write(chunk)
PY
	git -C "$source" -c protocol.file.allow=always submodule add --quiet "file://$work_root/submodule.git" vendor/fixture
	git -C "$source" add .
	git -C "$source" commit --quiet -m "initial fixture"
	local commit
	for ((commit = 1; commit <= commits; commit++)); do
		printf 'history %d\n' "$commit" >> "$source/history.txt"
		printf 'export const revision = %d;\n' "$commit" > "$source/apps/web/src/revision.ts"
		git -C "$source" add history.txt apps/web/src/revision.ts
		git -C "$source" commit --quiet -m "history ${commit}"
	done
	git clone --quiet --bare "$source" "$remote"
	git -C "$remote" symbolic-ref HEAD refs/heads/main
	git -C "$remote" config uploadpack.allowFilter true
	git -C "$remote" config uploadpack.allowAnySHA1InWant true
}

clone_candidate() {
	local candidate="$1" remote="$2" destination="$3"
	case "$candidate" in
		full)
			git clone --quiet --no-tags "file://$remote" "$destination"
			;;
		partial)
			git clone --quiet --no-tags --filter=blob:none "file://$remote" "$destination"
			;;
		protocol-tuned)
			git -c protocol.version=2 -c fetch.parallel=4 clone --quiet --no-tags --filter=blob:none "file://$remote" "$destination"
			;;
		*)
			echo "unknown checkout candidate: $candidate" >&2
			return 1
			;;
	esac
}

measure_candidate() {
	local size="$1" candidate="$2" run="$3"
	local remote="$work_root/${size}.git"
	local destination="$work_root/clone-${size}-${candidate}-${run}"
	local started finished clone_ms missing_before objects_before objects_after first_blob_ms
	started="$(date +%s%N)"
	clone_candidate "$candidate" "$remote" "$destination"
	finished="$(date +%s%N)"
	clone_ms=$(((finished - started) / 1000000))

	missing_before="$(git -C "$destination" rev-list --objects --all --missing=print | awk '/^\?/ { count++ } END { print count + 0 }')"
	objects_before="$(du -sb "$destination/.git/objects" | awk '{print $1}')"
	local root_commit old_blob old_blob_hash
	root_commit="$(git -C "$destination" rev-list --max-parents=0 HEAD)"
	old_blob="$(git -C "$destination" rev-parse "${root_commit}:history.txt")"
	started="$(date +%s%N)"
	old_blob_hash="$(git -C "$destination" show "${root_commit}:history.txt" | git hash-object --stdin)"
	finished="$(date +%s%N)"
	first_blob_ms=$(((finished - started) / 1000000))
	if [[ "$old_blob_hash" != "$old_blob" ]]; then
		echo "$size $candidate fetched the wrong historical blob" >&2
		return 1
	fi
	objects_after="$(du -sb "$destination/.git/objects" | awk '{print $1}')"

	git -C "$destination" status --porcelain=v1 > "$work_root/status.txt"
	if [[ -s "$work_root/status.txt" ]]; then
		echo "$size $candidate checkout started dirty" >&2
		return 1
	fi
	printf 'validation diff\n' >> "$destination/README.md"
	git -C "$destination" diff --check
	git -C "$destination" diff -- README.md >/dev/null
	git -C "$destination" restore README.md
	git -C "$destination" blame --line-porcelain HEAD -- history.txt >/dev/null
	git -C "$destination" log --oneline --all >/dev/null
	git -C "$destination" branch validation-branch
	git -C "$destination" fetch --quiet --no-tags origin
	git -C "$destination" -c protocol.file.allow=always submodule update --quiet --init
	test "$(cat "$destination/vendor/fixture/fixture.txt")" = "submodule fixture"
	grep -q '^version https://git-lfs.github.com/spec/v1$' "$destination/asset.lfs"
	git -C "$destination" switch --quiet -c rebase-check HEAD~1
	printf 'rebase validation\n' > "$destination/rebase-validation.txt"
	git -C "$destination" add rebase-validation.txt
	git -C "$destination" commit --quiet -m "rebase validation"
	git -C "$destination" rebase --quiet main
	git -C "$destination" switch --quiet main
	git -C "$destination" branch -D rebase-check >/dev/null
	git -C "$destination" status --porcelain=v1 > "$work_root/status.txt"
	if [[ -s "$work_root/status.txt" ]]; then
		echo "$size $candidate operations left the checkout dirty" >&2
		return 1
	fi

	local tree_hash commit_count git_bytes worktree_bytes promisor
	tree_hash="$(git -C "$destination" rev-parse 'HEAD^{tree}')"
	commit_count="$(git -C "$destination" rev-list --count HEAD)"
	git_bytes="$(du -sb "$destination/.git" | awk '{print $1}')"
	worktree_bytes="$(du -sb "$destination" | awk '{print $1}')"
	promisor="$(git -C "$destination" config --bool --get remote.origin.promisor || printf 'false')"
	python3 - \
		"$results_file" "$size" "$candidate" "$run" "$clone_ms" "$first_blob_ms" \
		"$missing_before" "$objects_before" "$objects_after" "$git_bytes" "$worktree_bytes" \
		"$tree_hash" "$old_blob_hash" "$commit_count" "$promisor" <<'PY'
import json
import pathlib
import sys

(
    output,
    size,
    candidate,
    run,
    clone_ms,
    first_blob_ms,
    missing_before,
    objects_before,
    objects_after,
    git_bytes,
    worktree_bytes,
    tree_hash,
    old_blob_hash,
    commit_count,
    promisor,
) = sys.argv[1:]
record = {
    "size": size,
    "candidate": candidate,
    "run": int(run),
    "cloneMs": int(clone_ms),
    "firstOmittedBlobMs": int(first_blob_ms),
    "missingBefore": int(missing_before),
    "objectsBeforeBytes": int(objects_before),
    "objectsAfterBytes": int(objects_after),
    "gitBytes": int(git_bytes),
    "worktreeBytes": int(worktree_bytes),
    "treeHash": tree_hash,
    "oldBlobHash": old_blob_hash,
    "commitCount": int(commit_count),
    "promisor": promisor == "true",
    "operations": {
        "status": True,
        "diff": True,
        "blame": True,
        "log": True,
        "branch": True,
        "rebase": True,
        "fetch": True,
        "submodule": True,
        "largeFilePointer": True,
    },
}
with pathlib.Path(output).open("a") as stream:
    stream.write(json.dumps(record, sort_keys=True) + "\n")
print("CHECKOUT_RESULT " + json.dumps(record, sort_keys=True))
PY
}

create_submodule
create_repository small 8 8 64
create_repository medium 20 40 1024
create_repository large 35 100 4096

results_file="$work_root/results.jsonl"
for run in $(seq 1 "$runs"); do
	for size in small medium large; do
		for candidate in full partial protocol-tuned; do
			measure_candidate "$size" "$candidate" "$run"
		done
	done
done

python3 - "$results_file" <<'PY'
import json
import pathlib
import statistics
import sys

records = [json.loads(line) for line in pathlib.Path(sys.argv[1]).read_text().splitlines()]
for record in records:
    if record["candidate"] == "full":
        if record["promisor"] or record["missingBefore"] != 0:
            raise SystemExit(f"full checkout has missing objects: {record}")
    elif not record["promisor"] or record["missingBefore"] <= 0:
        raise SystemExit(f"filtered checkout did not defer historical blobs: {record}")

for size in {record["size"] for record in records}:
    for run in {record["run"] for record in records}:
        group = [record for record in records if record["size"] == size and record["run"] == run]
        if len(group) != 3:
            raise SystemExit(f"incomplete matrix for {size} run {run}: {group}")
        if len({record["treeHash"] for record in group}) != 1:
            raise SystemExit(f"tree mismatch for {size} run {run}")
        if len({record["oldBlobHash"] for record in group}) != 1:
            raise SystemExit(f"historical blob mismatch for {size} run {run}")
        if len({record["commitCount"] for record in group}) != 1:
            raise SystemExit(f"history mismatch for {size} run {run}")

summary = {"runs": max(record["run"] for record in records), "cells": []}
for size in ("small", "medium", "large"):
    for candidate in ("full", "partial", "protocol-tuned"):
        group = [record for record in records if record["size"] == size and record["candidate"] == candidate]
        summary["cells"].append({
            "size": size,
            "candidate": candidate,
            "cloneMsMedian": round(statistics.median(record["cloneMs"] for record in group)),
            "firstOmittedBlobMsMedian": round(statistics.median(record["firstOmittedBlobMs"] for record in group)),
            "gitBytesMedian": round(statistics.median(record["gitBytes"] for record in group)),
            "missingBeforeMin": min(record["missingBefore"] for record in group),
        })
print("CHECKOUT_SUMMARY " + json.dumps(summary, sort_keys=True))
PY
