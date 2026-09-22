#!/usr/bin/env bash
set -euo pipefail

# Bake the native Azure Coder workspace image used by the ao-azure-vm template
# (cloud/coder-azure-vm/main.tf). The image is Ubuntu + node + ALL AO harnesses
# (claude-code, codex, cursor-agent) + gh + the release-matched ao-worker/ao
# binaries + a `coder` user with NOPASSWD sudo. Nothing runs in a container at
# runtime; the coder agent + AO worker run NATIVELY (the template's cloud-init
# starts the coder agent as a systemd service and the control plane bootstraps
# the baked ao-worker over it), mirroring eleven_x's AWS EC2 dev-kit.
#
# It bakes FROM the current image (which already carries node + claude + the
# coder user + the ao-worker/ao binaries) and only ADDS the missing harnesses,
# so this is also the way to refresh harness versions without a full rebuild.
# For a first-ever bake (no prior image), point AO_AZURE_SOURCE_IMAGE at a plain
# Ubuntu 22.04 Gen2 URN and extend the install block to add node + claude + the
# coder user + extract the binaries from the CP image.
#
# Output: a NEW Gen2 managed image (default name suffixed so the live template's
# image never disappears mid-provision); re-point the template at it with
# provision-coder-azure-template.sh-style `coder templates push` afterwards.
#
# Requires: az (logged in to the subscription that owns the RG).

RG="${AO_AZURE_RG:-ao-coder-azure}"
LOCATION="${AO_AZURE_LOCATION:-swedencentral}"
SOURCE_IMAGE_NAME="${AO_AZURE_SOURCE_IMAGE:-ao-coder-workspace-image}"
NEW_IMAGE_NAME="${AO_AZURE_NEW_IMAGE:-ao-coder-workspace-image-allharness}"
BAKE_VM="${AO_AZURE_BAKE_VM:-ao-bake-tmp}"
ADMIN_USER="${AO_AZURE_ADMIN_USER:-azureadmin}"
VM_SIZE="${AO_AZURE_BAKE_VM_SIZE:-Standard_D2s_v5}"

CLAUDE_CODE_VERSION="${CLAUDE_CODE_VERSION:-2.1.228}"
CODEX_VERSION="${CODEX_VERSION:-0.147.0}"
CURSOR_AGENT_VERSION="${CURSOR_AGENT_VERSION:-2026.08.11-e8db854}"
GH_VERSION="${GH_VERSION:-2.97.0}"

echo "=== resolve source image ${SOURCE_IMAGE_NAME} ==="
SRC_ID="$(az image show -g "$RG" -n "$SOURCE_IMAGE_NAME" --query id -o tsv)"
[ -n "$SRC_ID" ] || { echo "source image not found"; exit 1; }
echo "source image: $SRC_ID"

echo "=== clean up any prior bake VM ${BAKE_VM} ==="
az vm delete -g "$RG" -n "$BAKE_VM" --yes 2>/dev/null || true

echo "=== create temp bake VM from the source image ==="
az vm create -g "$RG" -n "$BAKE_VM" \
  --image "$SRC_ID" \
  --size "$VM_SIZE" \
  --admin-username "$ADMIN_USER" \
  --generate-ssh-keys \
  --public-ip-address "" \
  --nic-delete-option Delete --os-disk-delete-option Delete \
  --output none
echo "bake VM created"

echo "=== install missing harnesses (codex + cursor-agent) + verify all three ==="
az vm run-command invoke -g "$RG" -n "$BAKE_VM" --command-id RunShellScript --scripts "
set -e
export DEBIAN_FRONTEND=noninteractive
# codex (node already present from the base image)
sudo npm install --global '@openai/codex@${CODEX_VERSION}'
# cursor-agent (native tarball; amd64 -> x64)
sudo mkdir -p '/opt/cursor-agent/${CURSOR_AGENT_VERSION}'
curl --fail --location --silent --show-error \
  'https://downloads.cursor.com/lab/${CURSOR_AGENT_VERSION}/linux/x64/agent-cli-package.tar.gz' \
  | sudo tar --strip-components=1 -xzf - -C '/opt/cursor-agent/${CURSOR_AGENT_VERSION}'
sudo ln -sf '/opt/cursor-agent/${CURSOR_AGENT_VERSION}/cursor-agent' /usr/local/bin/cursor-agent
# gh (idempotent; base image may already have it)
if ! command -v gh >/dev/null 2>&1; then
  curl --fail --location --silent --show-error \
    'https://github.com/cli/cli/releases/download/v${GH_VERSION}/gh_${GH_VERSION}_linux_amd64.tar.gz' \
    | sudo tar --strip-components=2 -xzf - -C /usr/local/bin 'gh_${GH_VERSION}_linux_amd64/bin/gh'
fi
echo '--- versions ---'
# HOME must be set: the cursor-agent launcher reads \$HOME under 'set -u', and
# az run-command runs with no HOME. At worker runtime HOME is always exported.
export HOME=\"\${HOME:-/root}\"
claude --version || echo 'claude MISSING'
codex --version || echo 'codex MISSING'
cursor-agent --version || echo 'cursor-agent MISSING'
gh --version | head -1 || echo 'gh MISSING'
# make the freshly installed tools available to the coder user by default
ls -l /usr/local/bin/cursor-agent
" --query 'value[0].message' -o tsv

echo "=== deallocate + generalize the bake VM ==="
# Do NOT run 'waagent -deprovision' via 'az vm run-command': deprovision tears
# down the guest agent that run-command reports back through, so the invoke call
# blocks until the extension timeout (~90 min). generalize does not require it,
# and each workspace VM gets fresh cloud-init + a fresh coder identity at boot,
# so a deprovision buys us nothing here.
az vm deallocate -g "$RG" -n "$BAKE_VM" --output none
az vm generalize -g "$RG" -n "$BAKE_VM" --output none

echo "=== create the new Gen2 managed image ${NEW_IMAGE_NAME} ==="
az image delete -g "$RG" -n "$NEW_IMAGE_NAME" 2>/dev/null || true
az image create -g "$RG" -n "$NEW_IMAGE_NAME" \
  --source "$BAKE_VM" \
  --hyper-v-generation V2 \
  --output none
NEW_ID="$(az image show -g "$RG" -n "$NEW_IMAGE_NAME" --query id -o tsv)"
echo "NEW IMAGE: $NEW_ID"

echo "=== tear down the bake VM ==="
az vm delete -g "$RG" -n "$BAKE_VM" --yes 2>/dev/null || true

echo
echo "DONE. Re-point the ao-azure-vm template at the new image, e.g.:"
echo "  coder templates push ao-azure-vm -d cloud/coder-azure-vm --variable image_id=$NEW_ID --variable subnet_id=<subnet> -y"
echo "NEW_IMAGE_ID=$NEW_ID"
