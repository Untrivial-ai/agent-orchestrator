#!/usr/bin/env bash
set -euo pipefail

script="$(cd "$(dirname "$0")" && pwd)/setup-self-hosted.sh"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/bin" "$tmp/pkg/resources/daemon" "$tmp/pkg/resources/acp-runtime/node/bin" \
	"$tmp/pkg/resources/acp-runtime/node_modules/@agentclientprotocol/claude-agent-acp/dist" \
	"$tmp/pkg/resources/tmux/bin"

# Keep systemctl absent even when the test runner has it installed.
for tool in chmod cp date dirname git gzip ln mkdir mktemp python3 readlink rm rmdir sleep tar; do
	ln -s "$(command -v "$tool")" "$tmp/bin/$tool"
done
printf '%s\n' '#!/bin/sh' 'case "$1" in' \
	'  -u) echo 1000 ;;' '  -un) echo ao ;;' 'esac' > "$tmp/bin/id"
printf '%s\n' '#!/bin/sh' 'case "$1" in' \
	'  -s) echo Linux ;;' '  -m) echo x86_64 ;;' 'esac' > "$tmp/bin/uname"
printf '%s\n' '#!/bin/sh' 'case "$1" in' \
	'  status)' \
	'    if [ -n "${TEST_AO_BLOCK_FILE:-}" ]; then' \
	'      : > "${TEST_AO_BLOCK_FILE}.ready"' \
	'      while [ ! -e "${TEST_AO_BLOCK_FILE}.go" ]; do /bin/sleep 0.02; done' \
	'    fi' \
	'    if [ "${TEST_AO_READY_AFTER_INSTALL:-}" = 1 ] && [ -L "$TEST_AO_HOST_ROOT/current" ]; then' \
	'      TEST_AO_STATE=ready TEST_AO_EXE="$TEST_AO_HOST_ROOT/current/resources/daemon/ao"' \
	'    fi' \
	'    if [ -n "${TEST_AO_READY_FOR_TARGET:-}" ] && [ "$(readlink "$TEST_AO_HOST_ROOT/current")" = "$TEST_AO_READY_FOR_TARGET" ]; then' \
	'      TEST_AO_STATE=ready TEST_AO_EXE="$TEST_AO_HOST_ROOT/current/resources/daemon/ao"' \
	'    fi' \
	'    printf '\''{"state":"%s","executablePath":"%s"}\n'\'' "${TEST_AO_STATE:-stopped}" "${TEST_AO_EXE:-}" ;;' \
	'  remote-host)' \
	'    if [ -n "${TEST_AO_TUNNEL_PAIRING:-}" ]; then' \
	'      case "$2" in' \
	'        enable) if [ "$TEST_AO_TUNNEL_PAIRING" = unavailable ]; then' \
	'                  printf "Remote host enabled\nHost ID: h_test\nTunnel unavailable: cloudflared missing\nPassword: test-secret\n"' \
	'                else' \
	'                  printf "Remote host enabled\nHost ID: h_test\nTunnel: starting; run status for the HTTPS address\nPassword: test-secret\n"' \
	'                fi ;;' \
	'        status) printf "Remote host enabled\nHost ID: h_test\nAddress: https://example.trycloudflare.com:443\nPassword: test-secret\n" ;;' \
	'      esac' \
	'    fi ;;' \
	'  daemon) printf "%s\n" "$PATH" > "$TEST_SERVICE_PATH_CAPTURE" ;;' \
	'  version) exit 0 ;;' 'esac' > "$tmp/pkg/resources/daemon/ao"
printf '%s\n' '#!/bin/sh' 'echo v22.0.0' > "$tmp/pkg/resources/acp-runtime/node/bin/node"
printf '%s\n' '#!/bin/sh' 'echo tmux' > "$tmp/pkg/resources/tmux/bin/tmux"
: > "$tmp/pkg/resources/acp-runtime/node_modules/@agentclientprotocol/claude-agent-acp/dist/index.js"
chmod +x "$tmp/bin/id" "$tmp/bin/uname" "$tmp/pkg/resources/daemon/ao" \
	"$tmp/pkg/resources/acp-runtime/node/bin/node" "$tmp/pkg/resources/tmux/bin/tmux"

bundle="$tmp/host.tar.gz"
case "${1:-}" in
	bad-tmux)
		chmod -x "$tmp/pkg/resources/tmux/bin/tmux"
		COPYFILE_DISABLE=1 tar -czf "$bundle" -C "$tmp/pkg" resources
		if env PATH="$tmp/bin" AO_HOST_INSTALL_DIR="$tmp/host" /bin/bash "$script" --bundle "$bundle" --install-only > "$tmp/out" 2>&1; then
			printf '%s\n' 'non-executable tmux was accepted' >&2; exit 1
		fi
		grep -q 'bundled tmux is not executable' "$tmp/out" || { cat "$tmp/out" >&2; exit 1; }
		;;
	no-systemd)
		COPYFILE_DISABLE=1 tar -czf "$bundle" -C "$tmp/pkg" resources
		if env PATH="$tmp/bin" AO_HOST_INSTALL_DIR="$tmp/host" /bin/bash "$script" --bundle "$bundle" > "$tmp/out" 2>&1; then
			printf '%s\n' 'default install without systemd succeeded' >&2; exit 1
		fi
		grep -q 'systemd user services are required' "$tmp/out" || { cat "$tmp/out" >&2; exit 1; }
		[[ ! -L "$tmp/host/current" ]]
		releases=("$tmp/host/releases"/*)
		[[ ! -e "${releases[0]}" ]]
		;;
	inactive-systemd)
		printf '%s\n' '#!/bin/sh' 'exit 1' > "$tmp/bin/systemctl"
		chmod +x "$tmp/bin/systemctl"
		COPYFILE_DISABLE=1 tar -czf "$bundle" -C "$tmp/pkg" resources
		if env PATH="$tmp/bin" AO_HOST_INSTALL_DIR="$tmp/host" /bin/bash "$script" --bundle "$bundle" > "$tmp/out" 2>&1; then
			printf '%s\n' 'default install without an active user service manager succeeded' >&2; exit 1
		fi
		grep -q 'systemd user services are unavailable' "$tmp/out" || { cat "$tmp/out" >&2; exit 1; }
		[[ ! -L "$tmp/host/current" ]]
		releases=("$tmp/host/releases"/*)
		[[ ! -e "${releases[0]}" ]]
		;;
	prune)
		COPYFILE_DISABLE=1 tar -czf "$bundle" -C "$tmp/pkg" resources
		install() {
			env PATH="$tmp/bin" AO_HOST_INSTALL_DIR="$tmp/host" TEST_AO_STATE="$1" TEST_AO_EXE="${2:-}" \
				/bin/bash "$script" --bundle "$bundle" --install-only > "$tmp/out" 2>&1
		}
		install stopped
		first="$(readlink "$tmp/host/current")"
		install stopped
		second="$(readlink "$tmp/host/current")"
		install ready "$first/resources/daemon/ao"
		third="$(readlink "$tmp/host/current")"
		[[ -d "$first" && -d "$second" && -d "$third" ]]
		install stopped
		fourth="$(readlink "$tmp/host/current")"
		[[ ! -e "$first" && ! -e "$second" && -d "$third" && -d "$fourth" ]]
		releases=("$tmp/host/releases"/*)
		[[ ${#releases[@]} -eq 2 ]]
		;;
	failed-restarts)
		printf '%s\n' '#!/bin/sh' \
			'case "$2" in' \
			'  restart)' \
			'    echo restart >> "$TEST_SYSTEMCTL_LOG"' \
			'    if [ ! -e "$TEST_SYSTEMCTL_FAIL_ONCE" ]; then : > "$TEST_SYSTEMCTL_FAIL_ONCE"; exit 1; fi ;;' \
			'esac' > "$tmp/bin/systemctl"
		chmod +x "$tmp/bin/systemctl"
		COPYFILE_DISABLE=1 tar -czf "$bundle" -C "$tmp/pkg" resources
		env PATH="$tmp/bin" AO_HOST_INSTALL_DIR="$tmp/host" /bin/bash "$script" --bundle "$bundle" --install-only > "$tmp/out" 2>&1
		good="$(readlink "$tmp/host/current")"
		for attempt in 1 2; do
			log="$tmp/restarts-$attempt.log"
			if env HOME="$tmp/home" PATH="$tmp/bin" AO_HOST_INSTALL_DIR="$tmp/host" \
				TEST_SYSTEMCTL_LOG="$log" TEST_SYSTEMCTL_FAIL_ONCE="$tmp/fail-$attempt" \
				/bin/bash "$script" --bundle "$bundle" > "$tmp/out" 2>&1; then
				printf '%s\n' 'simulated service restart unexpectedly succeeded' >&2; exit 1
			fi
			[[ "$(readlink "$tmp/host/current")" == "$good" ]] || { printf '%s\n' 'failed restart left the new release active' >&2; exit 1; }
			[[ "$(grep -c '^restart$' "$log")" == 2 ]] || { printf '%s\n' 'failed restart did not restart the previous service' >&2; exit 1; }
		done
		[[ -d "$good" ]] || { printf '%s\n' 'last working release was deleted after failed restarts' >&2; exit 1; }
		grep -qx 'KillMode=process' "$tmp/home/.config/systemd/user/ao-self-hosted.service"
		;;
	failed-first-install)
		printf '%s\n' '#!/bin/sh' \
			'echo "$*" >> "$TEST_SYSTEMCTL_LOG"' \
			'case "$2" in' \
			'  enable) : > "$TEST_SYSTEMCTL_ENABLED" ;;' \
			'  restart) exit 1 ;;' \
			'  disable) rm "$TEST_SYSTEMCTL_ENABLED" ;;' \
			'esac' > "$tmp/bin/systemctl"
		chmod +x "$tmp/bin/systemctl"
		COPYFILE_DISABLE=1 tar -czf "$bundle" -C "$tmp/pkg" resources
		result=0
		env HOME="$tmp/home" PATH="$tmp/bin" AO_HOST_INSTALL_DIR="$tmp/host" \
			TEST_SYSTEMCTL_LOG="$tmp/systemctl.log" TEST_SYSTEMCTL_ENABLED="$tmp/unit-enabled" \
			/bin/bash "$script" --bundle "$bundle" > "$tmp/out" 2>&1 || result=$?
		[[ "$result" != 0 ]] || { printf '%s\n' 'failed first restart unexpectedly succeeded' >&2; exit 1; }
		[[ ! -e "$tmp/host/current" && ! -L "$tmp/host/current" ]] || { printf '%s\n' 'failed first install left current active' >&2; exit 1; }
		[[ ! -e "$tmp/unit-enabled" ]] || { printf '%s\n' 'failed first install left the unit enabled' >&2; exit 1; }
		grep -Fqx -- '--user disable --now ao-self-hosted.service' "$tmp/systemctl.log" || { printf '%s\n' 'failed first install did not stop and disable the unit' >&2; exit 1; }
		;;
	failed-readiness)
		printf '%s\n' '#!/bin/sh' 'if [ "$2" = restart ]; then echo restart >> "$TEST_SYSTEMCTL_LOG"; fi' > "$tmp/bin/systemctl"
		chmod +x "$tmp/bin/systemctl"
		rm "$tmp/bin/sleep"
		printf '%s\n' '#!/bin/sh' 'exit 0' > "$tmp/bin/sleep"
		chmod +x "$tmp/bin/sleep"
		COPYFILE_DISABLE=1 tar -czf "$bundle" -C "$tmp/pkg" resources
		env PATH="$tmp/bin" AO_HOST_INSTALL_DIR="$tmp/host" /bin/bash "$script" --bundle "$bundle" --install-only > "$tmp/out" 2>&1
		good="$(readlink "$tmp/host/current")"
		result=0
		env HOME="$tmp/home" PATH="$tmp/bin" AO_HOST_INSTALL_DIR="$tmp/host" TEST_AO_HOST_ROOT="$tmp/host" \
			TEST_AO_READY_FOR_TARGET="$good" TEST_SYSTEMCTL_LOG="$tmp/restarts.log" \
			/bin/bash "$script" --bundle "$bundle" > "$tmp/out" 2>&1 || result=$?
		[[ "$result" != 0 ]] || { printf '%s\n' 'unready release unexpectedly succeeded' >&2; exit 1; }
		[[ "$(readlink "$tmp/host/current")" == "$good" ]] || { printf '%s\n' 'unready release left the new release active' >&2; exit 1; }
		[[ "$(grep -c '^restart$' "$tmp/restarts.log")" == 2 ]] || { printf '%s\n' 'unready release did not restart the previous service' >&2; exit 1; }
		;;
	failed-mac-bootstrap|failed-mac-readiness)
		rm "$tmp/bin/uname"
		printf '%s\n' '#!/bin/sh' 'case "$1" in -s) echo Darwin ;; -m) echo arm64 ;; esac' > "$tmp/bin/uname"
		printf '%s\n' '#!/bin/sh' \
			'echo "$1" >> "$TEST_LAUNCHCTL_LOG"' \
			'if [ "$1" = bootstrap ] && [ -n "${TEST_LAUNCHCTL_FAIL_ONCE:-}" ] && [ ! -e "$TEST_LAUNCHCTL_FAIL_ONCE" ]; then' \
			'  : > "$TEST_LAUNCHCTL_FAIL_ONCE"; exit 1' \
			'fi' > "$tmp/bin/launchctl"
		chmod +x "$tmp/bin/uname" "$tmp/bin/launchctl"
		COPYFILE_DISABLE=1 tar -czf "$bundle" -C "$tmp/pkg" resources
		env HOME="$tmp/home" PATH="$tmp/bin" AO_HOST_INSTALL_DIR="$tmp/host" TEST_AO_HOST_ROOT="$tmp/host" \
			TEST_AO_READY_AFTER_INSTALL=1 TEST_LAUNCHCTL_LOG="$tmp/initial-launch.log" \
			/bin/bash "$script" --bundle "$bundle" > "$tmp/out" 2>&1 || { cat "$tmp/out" >&2; exit 1; }
		good="$(readlink "$tmp/host/current")"
		failure=()
		if [[ "$1" == failed-mac-bootstrap ]]; then
			failure=(TEST_LAUNCHCTL_FAIL_ONCE="$tmp/fail-once")
		else
			rm "$tmp/bin/sleep"
			printf '%s\n' '#!/bin/sh' 'exit 0' > "$tmp/bin/sleep"
			chmod +x "$tmp/bin/sleep"
		fi
		result=0
		env HOME="$tmp/home" PATH="$tmp/bin" AO_HOST_INSTALL_DIR="$tmp/host" TEST_AO_HOST_ROOT="$tmp/host" \
			TEST_AO_READY_FOR_TARGET="$good" TEST_LAUNCHCTL_LOG="$tmp/upgrade-launch.log" "${failure[@]}" \
			/bin/bash "$script" --bundle "$bundle" > "$tmp/out" 2>&1 || result=$?
		[[ "$result" != 0 ]] || { printf '%s\n' 'failed Mac upgrade unexpectedly succeeded' >&2; exit 1; }
		[[ "$(readlink "$tmp/host/current")" == "$good" ]] || { printf '%s\n' 'failed Mac upgrade left the new release active' >&2; exit 1; }
		[[ "$(grep -c '^bootstrap$' "$tmp/upgrade-launch.log")" == 2 ]] || { printf '%s\n' 'failed Mac upgrade did not restart the previous agent' >&2; exit 1; }
		[[ "$(grep -c '^bootout$' "$tmp/upgrade-launch.log")" == 2 ]] || { printf '%s\n' 'failed Mac upgrade did not unload the failed agent' >&2; exit 1; }
		;;
	failed-mac-first)
		rm "$tmp/bin/uname"
		printf '%s\n' '#!/bin/sh' 'case "$1" in -s) echo Darwin ;; -m) echo arm64 ;; esac' > "$tmp/bin/uname"
		printf '%s\n' '#!/bin/sh' 'echo "$1" >> "$TEST_LAUNCHCTL_LOG"' 'if [ "$1" = bootstrap ]; then exit 1; fi' > "$tmp/bin/launchctl"
		chmod +x "$tmp/bin/uname" "$tmp/bin/launchctl"
		COPYFILE_DISABLE=1 tar -czf "$bundle" -C "$tmp/pkg" resources
		result=0
		env HOME="$tmp/home" PATH="$tmp/bin" AO_HOST_INSTALL_DIR="$tmp/host" TEST_LAUNCHCTL_LOG="$tmp/launch.log" \
			/bin/bash "$script" --bundle "$bundle" > "$tmp/out" 2>&1 || result=$?
		[[ "$result" != 0 ]] || { printf '%s\n' 'failed first Mac install unexpectedly succeeded' >&2; exit 1; }
		[[ ! -e "$tmp/host/current" && ! -L "$tmp/host/current" ]] || { printf '%s\n' 'failed first Mac install left current active' >&2; exit 1; }
		[[ ! -e "$tmp/home/Library/LaunchAgents/dev.aoagents.self-hosted.plist" ]] || { printf '%s\n' 'failed first Mac install left a login agent' >&2; exit 1; }
		[[ "$(grep -c '^bootout$' "$tmp/launch.log")" == 2 ]] || { printf '%s\n' 'failed first Mac install did not unload the failed agent' >&2; exit 1; }
		;;
	relative-current)
		COPYFILE_DISABLE=1 tar -czf "$bundle" -C "$tmp/pkg" resources
		for attempt in 1 2; do
			env PATH="$tmp/bin" AO_HOST_INSTALL_DIR="$tmp/host" /bin/bash "$script" --bundle "$bundle" --install-only > "$tmp/out" 2>&1
			if [[ "$attempt" == 1 ]]; then
				good="$(readlink "$tmp/host/current")"
				ln -sfn "releases/${good##*/}" "$tmp/host/current"
			fi
		done
		[[ -d "$good" ]] || { printf '%s\n' 'relative current target was deleted' >&2; exit 1; }
		second="$(readlink "$tmp/host/current")"
		ln -sfn "./releases/${second##*/}" "$tmp/host/current"
		env PATH="$tmp/bin" AO_HOST_INSTALL_DIR="$tmp/host" /bin/bash "$script" --bundle "$bundle" --install-only > "$tmp/out" 2>&1
		[[ -d "$second" ]] || { printf '%s\n' 'unknown current target was deleted' >&2; exit 1; }
		;;
	concurrent)
		COPYFILE_DISABLE=1 tar -czf "$bundle" -C "$tmp/pkg" resources
		block="$tmp/block"
		env PATH="$tmp/bin" AO_HOST_INSTALL_DIR="$tmp/host" TEST_AO_BLOCK_FILE="$block" \
			/bin/bash "$script" --bundle "$bundle" --install-only > "$tmp/first.out" 2>&1 &
		first_pid=$!
		for attempt in {1..100}; do
			[[ -e "$block.ready" ]] && break
			/bin/sleep 0.05
		done
		if [[ ! -e "$block.ready" ]]; then
			: > "$block.go"
			wait "$first_pid" || true
			cat "$tmp/first.out" >&2
			printf '%s\n' 'first installer did not reach status' >&2; exit 1
		fi
		second_status=0
		env PATH="$tmp/bin" AO_HOST_INSTALL_DIR="$tmp/host" \
			/bin/bash "$script" --bundle "$bundle" --install-only > "$tmp/second.out" 2>&1 || second_status=$?
		lock_held=false
		[[ -f "$tmp/host/.install.lock" ]] && lock_held=true
		: > "$block.go"
		wait "$first_pid"
		"$lock_held" || { printf '%s\n' 'second installer removed the first installer lock' >&2; exit 1; }
		[[ "$second_status" -ne 0 ]] || { printf '%s\n' 'concurrent installer was accepted' >&2; exit 1; }
		grep -q 'Install lock is held' "$tmp/second.out" || { cat "$tmp/second.out" >&2; exit 1; }
		[[ -d "$(readlink "$tmp/host/current")" ]]
		releases=("$tmp/host/releases"/*)
		[[ ${#releases[@]} -eq 1 ]]
		;;
	interrupted)
		COPYFILE_DISABLE=1 tar -czf "$bundle" -C "$tmp/pkg" resources
		env PATH="$tmp/bin" AO_HOST_INSTALL_DIR="$tmp/host" /bin/bash "$script" --bundle "$bundle" --install-only > "$tmp/first.out" 2>&1
		original="$(readlink "$tmp/host/current")"
		mkdir -p "$tmp/user-data"
		printf '%s\n' 'keep me' > "$tmp/user-data/keep.txt"
		block="$tmp/block"
		env PATH="$tmp/bin" AO_HOST_INSTALL_DIR="$tmp/host" TEST_AO_BLOCK_FILE="$block" \
			/bin/bash "$script" --bundle "$bundle" --install-only > "$tmp/interrupted.out" 2>&1 &
		installer_pid=$!
		for attempt in {1..100}; do
			[[ -e "$block.ready" ]] && break
			/bin/sleep 0.05
		done
		if [[ ! -e "$block.ready" ]]; then
			: > "$block.go"
			wait "$installer_pid" || true
			cat "$tmp/interrupted.out" >&2
			printf '%s\n' 'installer did not reach locked status' >&2; exit 1
		fi
		kill -KILL "$installer_pid"
		wait "$installer_pid" 2>/dev/null || true
		blocked_status=0
		env PATH="$tmp/bin" AO_HOST_INSTALL_DIR="$tmp/host" /bin/bash "$script" --bundle "$bundle" --install-only > "$tmp/blocked-retry.out" 2>&1 || blocked_status=$?
		[[ "$blocked_status" -ne 0 ]] || { printf '%s\n' 'retry entered while the interrupted installer child was active' >&2; exit 1; }
		grep -q 'Install lock is held' "$tmp/blocked-retry.out" || { cat "$tmp/blocked-retry.out" >&2; exit 1; }
		: > "$block.go"
		recovered=false
		for attempt in {1..100}; do
			if env PATH="$tmp/bin" AO_HOST_INSTALL_DIR="$tmp/host" /bin/bash "$script" --bundle "$bundle" --install-only > "$tmp/retry.out" 2>&1; then
				recovered=true
				break
			fi
			/bin/sleep 0.05
		done
		"$recovered" || { cat "$tmp/retry.out" >&2; exit 1; }
		[[ -f "$tmp/host/.install.lock" && -d "$original" && -d "$(readlink "$tmp/host/current")" ]]
		[[ "$(cat "$tmp/user-data/keep.txt")" == 'keep me' ]]
		;;
	piped)
		COPYFILE_DISABLE=1 tar -czf "$bundle" -C "$tmp/pkg" resources
		/bin/cat "$script" | env PATH="$tmp/bin" AO_HOST_INSTALL_DIR="$tmp/host" \
			/bin/bash -s -- --bundle "$bundle" --install-only > "$tmp/out" 2>&1
		[[ -d "$(readlink "$tmp/host/current")" ]]
		;;
	tunnel-pairing|tunnel-unavailable)
		printf '%s\n' '#!/bin/sh' 'exit 0' > "$tmp/bin/systemctl"
		chmod +x "$tmp/bin/systemctl"
		COPYFILE_DISABLE=1 tar -czf "$bundle" -C "$tmp/pkg" resources
		mode=1
		[[ "$1" == tunnel-unavailable ]] && mode=unavailable
		result=0
		env HOME="$tmp/home" PATH="$tmp/bin" AO_HOST_INSTALL_DIR="$tmp/host" \
			TEST_AO_HOST_ROOT="$tmp/host" TEST_AO_READY_AFTER_INSTALL=1 TEST_AO_TUNNEL_PAIRING="$mode" \
			/bin/bash "$script" --bundle "$bundle" --tunnel > "$tmp/out" 2>&1 || result=$?
		if [[ "$1" == tunnel-pairing ]]; then
			[[ "$result" == 0 ]] || { cat "$tmp/out" >&2; exit 1; }
			grep -q '^Address: https://example.trycloudflare.com:443$' "$tmp/out" || { cat "$tmp/out" >&2; exit 1; }
			awk '/^Address: https:\/\/example.trycloudflare.com:443$/ { address = NR } /^Password: test-secret$/ { password = NR } END { exit !(address && password > address + 1) }' "$tmp/out" || { cat "$tmp/out" >&2; exit 1; }
			grep -q 'enable Developer mode, then Settings' "$tmp/out" || { cat "$tmp/out" >&2; exit 1; }
			TEST_SERVICE_PATH_CAPTURE="$tmp/service-path" "$tmp/host/run-daemon.sh"
			[[ ":$(<"$tmp/service-path"):" == *":$tmp/host/current/resources/acp-runtime/node/bin:"* ]] || {
				printf '%s\n' 'daemon service PATH omits bundled Node' >&2; exit 1;
			}
		else
			[[ "$result" != 0 ]] || { cat "$tmp/out" >&2; exit 1; }
			grep -q 'Tunnel address is not ready' "$tmp/out" || { cat "$tmp/out" >&2; exit 1; }
			! grep -q 'Pair this host' "$tmp/out" || { cat "$tmp/out" >&2; exit 1; }
		fi
		;;
	*) printf 'Usage: %s {bad-tmux|no-systemd|inactive-systemd|prune|failed-restarts|failed-first-install|failed-readiness|failed-mac-bootstrap|failed-mac-readiness|failed-mac-first|relative-current|concurrent|interrupted|piped|tunnel-pairing|tunnel-unavailable}\n' "$0" >&2; exit 2 ;;
esac
printf 'PASS %s\n' "$1"
