#!/usr/bin/env bash
# Launch Agent Orchestrator against throwaway state, so first-run onboarding can
# be walked by hand without touching your real ~/.ao or another running app.
#
# Isolation
#   AO_DEV_ELECTRON_DIR  own Chromium profile (also avoids the single-instance lock)
#   AO_RUN_FILE          own running.json + supervise socket
#   AO_DATA_DIR          own database, projects, and worktrees
#   AO_PORT              own daemon port
#
# What it changes for the app
#   GH_CONFIG_DIR       empty, so gh reads as signed out and the GitHub step
#                       offers a real sign-in. Credentials land in the dummy
#                       profile, so your own gh login is untouched.
#   NPM_CONFIG_PREFIX   global npm installs land in the dummy prefix, which is
#                       on the daemon's PATH, so an agent the app installs is
#                       one the app can then see.
#
# What it cannot do
#   Hide installed tools. The app probes your login shell for PATH and then
#   appends a floor of standard bin directories (/opt/homebrew/bin,
#   /usr/local/bin, and so on), so anything installed on this machine stays
#   visible to the daemon. gh, tmux, git, and harnesses cannot be made to look
#   "not installed" this way. For a genuine bare-machine run use a fresh macOS
#   user account or a VM, which is also the closest match to a new Mac.
#
#   Sign out of AO Cloud. Cloud auth resolves to ~/.ao/dev for any dev build
#   (cloudDataDir() in src/main.ts), ignoring AO_DATA_DIR, so the cloud project
#   flow opens already signed in and its sign-in panel is unreachable here.
#   Move ~/.ao/dev/cloud-auth.bin aside first to walk that path.
#
# Usage
#   scripts/dummy-onboarding-env.sh [--skip-build] [--dry-run]
#                                   [-- <extra electron-forge args>]
#
# State lives under $AO_DUMMY_ROOT (default /tmp/ao-dummy); delete that
# directory for a fresh run.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
FRONTEND_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
STATE_ROOT="${AO_DUMMY_ROOT:-/tmp/ao-dummy}"

skip_build=0
dry_run=0
extra_args=()

while [ $# -gt 0 ]; do
	case "$1" in
		--skip-build) skip_build=1 ;;
		--dry-run) dry_run=1 ;;
		--) shift; extra_args=("$@"); break ;;
		*) extra_args+=("$1") ;;
	esac
	shift
done

mkdir -p "$STATE_ROOT/electron" "$STATE_ROOT/data" "$STATE_ROOT/gh" "$STATE_ROOT/npm-global/bin" "$STATE_ROOT/npm-global/lib"

# A ready-to-pick project folder for the "Open local folder" step.
PROJECT_DIR="$STATE_ROOT/project"
if [ ! -d "$PROJECT_DIR/.git" ]; then
	mkdir -p "$PROJECT_DIR"
	printf '# Dummy project\n\nCreated by scripts/dummy-onboarding-env.sh for onboarding testing.\n' > "$PROJECT_DIR/README.md"
	git -C "$PROJECT_DIR" init -q
	git -C "$PROJECT_DIR" config user.email "dummy@example.com"
	git -C "$PROJECT_DIR" config user.name "AO Dummy"
	git -C "$PROJECT_DIR" add README.md
	git -C "$PROJECT_DIR" commit -q -m "chore: initialize dummy project"
fi

echo "dummy onboarding environment"
echo "  state root      $STATE_ROOT"
echo "  daemon port     ${AO_PORT:-4751}"
echo "  pick this repo  $PROJECT_DIR"
echo "  gh config       $STATE_ROOT/gh (empty: the GitHub step starts signed out)"
echo "  npm prefix      $STATE_ROOT/npm-global"
echo
echo "  installed tools stay visible: this isolates state, it does not hide"
echo "  gh, tmux, git, or harnesses."
echo

cd "$FRONTEND_ROOT"
export AO_DUMMY_ROOT="$STATE_ROOT"
export AO_DEV_ELECTRON_DIR="$STATE_ROOT/electron"
export AO_RUN_FILE="$STATE_ROOT/running.json"
export AO_DATA_DIR="$STATE_ROOT/data"
export AO_PORT="${AO_PORT:-4751}"
export GH_CONFIG_DIR="$STATE_ROOT/gh"
export NPM_CONFIG_PREFIX="$STATE_ROOT/npm-global"

if [ "$dry_run" = "1" ]; then
	echo "dry run: environment prepared, app not launched."
	echo "  AO_RUN_FILE=$AO_RUN_FILE"
	echo "  AO_DATA_DIR=$AO_DATA_DIR"
	echo "  AO_DEV_ELECTRON_DIR=$AO_DEV_ELECTRON_DIR"
	echo "  GH_CONFIG_DIR=$GH_CONFIG_DIR"
	echo "  NPM_CONFIG_PREFIX=$NPM_CONFIG_PREFIX"
	exit 0
fi

if [ "$skip_build" != "1" ]; then
	# predev builds the daemon binary and the runtime assets forge expects.
	npm run predev
fi

# electron-forge needs its own "--" before args meant for the Electron app.
if [ "${#extra_args[@]}" -gt 0 ]; then
	exec ./node_modules/.bin/electron-forge start -- "${extra_args[@]}"
fi
exec ./node_modules/.bin/electron-forge start
