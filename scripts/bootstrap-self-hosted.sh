#!/usr/bin/env bash
# Fresh Ubuntu x64 entry point. The lower-level installer owns AO releases and service setup.
set -euo pipefail

usage() {
	printf '%s\n' 'Usage: bootstrap-self-hosted.sh [--source-ref BRANCH] [--lan]'
	printf '%s\n' 'Installs prerequisites, then installs released AO or builds a source branch.'
}

source_ref=""
tunnel=true
while (($#)); do
	case "$1" in
		--source-ref) source_ref="${2:?--source-ref needs a branch}"; shift 2 ;;
		--lan) tunnel=false; shift ;;
		-h|--help) usage; exit 0 ;;
		*) usage >&2; exit 2 ;;
	esac
done

[[ "$(id -u)" != 0 ]] || { printf '%s\n' 'Run as the host user, not root.' >&2; exit 1; }
[[ "$(uname -s):$(uname -m)" == Linux:x86_64 ]] || { printf '%s\n' 'This bootstrap supports Ubuntu x64 only.' >&2; exit 1; }
command -v apt-get >/dev/null || { printf '%s\n' 'This bootstrap requires Ubuntu apt-get.' >&2; exit 1; }
[[ -z "$source_ref" || "$source_ref" =~ ^[a-zA-Z0-9][a-zA-Z0-9._/-]*$ ]] || {
	printf '%s\n' 'Invalid source branch.' >&2; exit 2;
}

sudo apt-get update
sudo apt-get install -y ca-certificates curl gh git python3
sudo loginctl enable-linger "$(id -un)"
systemctl --user show-environment >/dev/null || {
	printf '%s\n' 'A systemd user session is required for an always-on AO host.' >&2; exit 1;
}

if "$tunnel" && ! command -v cloudflared >/dev/null; then
	sudo install -d -m 0755 /usr/share/keyrings
	curl -fsSL https://pkg.cloudflare.com/cloudflare-main.gpg | sudo tee /usr/share/keyrings/cloudflare-main.gpg >/dev/null
	printf '%s\n' 'deb [signed-by=/usr/share/keyrings/cloudflare-main.gpg] https://pkg.cloudflare.com/cloudflared noble main' |
		sudo tee /etc/apt/sources.list.d/cloudflared.list >/dev/null
	sudo apt-get update
	sudo apt-get install -y cloudflared
fi

stage="$(mktemp -d)"
trap 'rm -rf -- "$stage"' EXIT
installer="$stage/setup-self-hosted.sh"
if [[ -z "$source_ref" ]]; then
	curl -fsSL https://raw.githubusercontent.com/Untrivial-ai/agent-orchestrator/main/scripts/setup-self-hosted.sh -o "$installer"
else
	sudo apt-get install -y build-essential pkg-config xz-utils
	tools="$HOME/.local/ao-build-tools"
	mkdir -p "$tools"
	if [[ ! -x "$tools/go/bin/go" ]]; then
		curl -fsSL https://go.dev/dl/go1.27.1.linux-amd64.tar.gz -o "$stage/go.tar.gz"
		printf '%s  %s\n' 63d339f0da5ab53635a56f2490a7984dfe12dfcff22ad749f63edaf590168445 "$stage/go.tar.gz" | sha256sum -c -
		tar -xzf "$stage/go.tar.gz" -C "$tools"
	fi
	if [[ ! -x "$tools/node-v22.23.2-linux-x64/bin/node" ]]; then
		curl -fsSL https://nodejs.org/dist/v22.23.2/node-v22.23.2-linux-x64.tar.xz -o "$stage/node.tar.xz"
		curl -fsSL https://nodejs.org/dist/v22.23.2/SHASUMS256.txt -o "$stage/SHASUMS256.txt"
		expected="$(awk '$2 == "node-v22.23.2-linux-x64.tar.xz" { print $1 }' "$stage/SHASUMS256.txt")"
		[[ "$expected" =~ ^[0-9a-f]{64}$ ]] || { printf '%s\n' 'Node checksum missing.' >&2; exit 1; }
		printf '%s  %s\n' "$expected" "$stage/node.tar.xz" | sha256sum -c -
		tar -xJf "$stage/node.tar.xz" -C "$tools"
	fi
	export PATH="$tools/go/bin:$tools/node-v22.23.2-linux-x64/bin:$PATH"
	git clone --depth 1 --single-branch --branch "$source_ref" https://github.com/Untrivial-ai/agent-orchestrator.git "$stage/source"
	(
		cd "$stage/source/frontend"
		export GOMAXPROCS=2 NODE_OPTIONS=--max-old-space-size=1536
		npm ci
		npm run build:daemon
		npm run build:tmux
		npm run build:acp-runtime
		npm run build:host
	)
	installer="$stage/source/scripts/setup-self-hosted.sh"
	bundle="$stage/source/frontend/dist-host/ao-host-linux-x64.tar.gz"
fi

args=()
[[ -z "$source_ref" ]] || args+=(--bundle "$bundle")
if "$tunnel"; then
	args+=(--tunnel)
fi
bash "$installer" "${args[@]}"

printf '\nDay-two commands: %s\n' "$HOME/.ao/host/current/resources/daemon/ao remote-host status"
printf 'Service logs: journalctl --user -u ao-self-hosted.service -n 100 --no-pager\n'
