#!/usr/bin/env bash
set -euo pipefail

repository_root="$(git rev-parse --show-toplevel)"
temporary_directory="$(mktemp -d)"
trap 'rm -rf "$temporary_directory"' EXIT

fixture="$temporary_directory/superproject"
clone="$temporary_directory/clone"
mkdir -p "$fixture/private" "$temporary_directory/home"
git -C "$fixture" init --quiet
cp "$repository_root/.gitmodules" "$fixture/.gitmodules"
git -C "$fixture" add .gitmodules

submodule_commit="$(
	git -C "$repository_root" ls-tree HEAD private/ao-cloud |
		awk '{print $3}'
)"
git -C "$fixture" update-index \
	--add \
	--cacheinfo "160000,$submodule_commit,private/ao-cloud"
git -C "$fixture" \
	-c user.name="AO CI" \
	-c user.email="ci@aoagents.dev" \
	commit --quiet --message="test optional private submodule"

output="$(
	HOME="$temporary_directory/home" \
		GIT_CONFIG_NOSYSTEM=1 \
		GIT_TERMINAL_PROMPT=0 \
		git -c credential.helper= clone \
		--recurse-submodules \
		"file://$fixture" \
		"$clone" 2>&1
)"
printf '%s\n' "$output"

if [[ "$output" != *"Skipping submodule 'private/ao-cloud'"* ]]; then
	echo "Recursive clone did not report skipping the private submodule." >&2
	exit 1
fi
if [[ -e "$clone/private/ao-cloud/.git" ]]; then
	echo "Recursive clone initialized the private submodule." >&2
	exit 1
fi
