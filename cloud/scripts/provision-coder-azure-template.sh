#!/usr/bin/env bash
set -euo pipefail

# Build the AO Coder workspace image on the Azure Coder VM and publish the Coder
# template that references it, with per-workspace resource limits. Run after
# provision-coder-azure.sh.
#
# The workspace image (cloud/coder/Sandbox.Dockerfile) bakes the release-matched
# /ao-worker and /ao binaries straight out of the control-plane image, so pass
# the SAME control-plane image the (dogfood) CP runs (AO_CLOUD_CP_IMAGE). The
# image is amd64 (matches the D-series VM) - no cross-arch worker gymnastics.
#
# Single-VM shape: the image is built locally on the VM (tag ao-coder-workspace:
# local) and the template references that tag, so the Coder provisioner - which
# shares the VM's Docker daemon - finds it with no registry round trip. The
# template also pins per-workspace memory/cpu so one workspace cannot exhaust the
# shared VM.
#
# Requires: az (logged in), aws (for the ECR pull of the CP image), the ssh key
# from provision-coder-azure.sh (--generate-ssh-keys -> ~/.ssh/id_rsa).

RG="${AO_AZURE_RG:-ao-coder-azure}"
VM="${AO_AZURE_VM:-ao-coder-azure-vm}"
ADMIN_USER="${AO_AZURE_ADMIN_USER:-azureuser}"
SSH_KEY="${AO_AZURE_SSH_KEY:-$HOME/.ssh/id_rsa}"
AWS_REGION="${AWS_REGION:-eu-north-1}"
CP_IMAGE="${AO_CLOUD_CP_IMAGE:?set AO_CLOUD_CP_IMAGE to the control-plane image whose worker to bake, e.g. <acct>.dkr.ecr.eu-north-1.amazonaws.com/ao-cloud-control-plane:<tag>-linux-amd64}"
TEMPLATE_NAME="${AO_CLOUD_CODER_TEMPLATE_NAME:-ao-linux-docker}"
WORKSPACE_IMAGE="${AO_CLOUD_CODER_WORKSPACE_IMAGE:-ao-coder-workspace:local}"
WORKSPACE_MEMORY_MB="${AO_CLOUD_CODER_WORKSPACE_MEMORY_MB:-4096}"
WORKSPACE_CPU_SHARES="${AO_CLOUD_CODER_WORKSPACE_CPU_SHARES:-1024}"
CODER_ADMIN_EMAIL="${AO_CODER_ADMIN_EMAIL:-admin@ao-coder.dev}"
TEMPLATE_DIR="${AO_CLOUD_CODER_TEMPLATE_DIR:-coder}"
ECR_REGISTRY="${CP_IMAGE%%/*}"

# Resolve the Coder HOST VM's OWN public IP explicitly (via its NIC), never the
# "first public IP in the resource group": once the VM-per-workspace template is
# in use, the group also holds per-workspace public IPs, so [0] could resolve to
# a workspace - which would SSH to and build the image on the wrong host. az show
# is occasionally flaky for a fresh VM, so keep the explicit IP/FQDN overrides.
PIPID="$(az vm list-ip-addresses -g "$RG" -n "$VM" --query '[0].virtualMachine.network.publicIpAddresses[0].id' -o tsv 2>/dev/null || true)"
IP="${AO_AZURE_VM_IP:-$(az network public-ip show --ids "$PIPID" --query 'ipAddress' -o tsv 2>/dev/null || true)}"
FQDN="${AO_AZURE_FQDN:-$(az network public-ip show --ids "$PIPID" --query 'dnsSettings.fqdn' -o tsv 2>/dev/null || true)}"
[[ -n "$IP" && -n "$FQDN" ]] || { echo "could not resolve the Coder VM (${VM}) IP/FQDN; set AO_AZURE_VM_IP and AO_AZURE_FQDN"; exit 1; }
SSHK=(-i "$SSH_KEY" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=20)
echo "=== Coder VM: ${IP} (${FQDN}) ==="

echo "=== copy template sources ==="
scp "${SSHK[@]}" "${TEMPLATE_DIR}/main.tf" "${TEMPLATE_DIR}/Sandbox.Dockerfile" "${ADMIN_USER}@${IP}:/tmp/"

echo "=== ECR login on the VM (token via stdin) ==="
aws ecr get-login-password --region "$AWS_REGION" \
  | ssh "${SSHK[@]}" "${ADMIN_USER}@${IP}" "sudo docker login --username AWS --password-stdin ${ECR_REGISTRY}" >/dev/null

echo "=== build workspace image ${WORKSPACE_IMAGE} (bakes worker from ${CP_IMAGE}) ==="
ssh "${SSHK[@]}" "${ADMIN_USER}@${IP}" "
  set -e
  # Always scrub the ECR credential, even if the build fails partway.
  trap 'sudo rm -f /root/.docker/config.json ~/.docker/config.json' EXIT
  mkdir -p ~/ao-coder-template && cp /tmp/main.tf /tmp/Sandbox.Dockerfile ~/ao-coder-template/
  sudo DOCKER_BUILDKIT=1 docker build --build-arg AO_CONTROL_PLANE_IMAGE='${CP_IMAGE}' \
    -t '${WORKSPACE_IMAGE}' -f ~/ao-coder-template/Sandbox.Dockerfile ~/ao-coder-template
  sudo docker images '${WORKSPACE_IMAGE}' --format 'built {{.Repository}}:{{.Tag}} {{.Size}}'
"

echo "=== publish the ${TEMPLATE_NAME} template (mem=${WORKSPACE_MEMORY_MB}MB, cpu_shares=${WORKSPACE_CPU_SHARES}) ==="
ssh "${SSHK[@]}" "${ADMIN_USER}@${IP}" "sudo FQDN='${FQDN}' TEMPLATE_NAME='${TEMPLATE_NAME}' WORKSPACE_IMAGE='${WORKSPACE_IMAGE}' MEM='${WORKSPACE_MEMORY_MB}' CPU='${WORKSPACE_CPU_SHARES}' ADMIN_EMAIL='${CODER_ADMIN_EMAIL}' ADMINHOME='/home/${ADMIN_USER}' bash -s" <<'REMOTE'
set -e
. /etc/ao-coder.env
SESSION=$(curl -s -X POST https://$FQDN/api/v2/users/login -H "Content-Type: application/json" \
  -d "{\"email\":\"${ADMIN_EMAIL}\",\"password\":\"${CODER_ADMIN_PW}\"}" \
  | python3 -c 'import sys,json;print(json.load(sys.stdin).get("session_token",""))')
[ -z "$SESSION" ] && { echo "LOGIN_FAILED"; exit 1; }
mkdir -p "$ADMINHOME/ao-coder-template" && cp /tmp/main.tf "$ADMINHOME/ao-coder-template/main.tf"
docker exec coder mkdir -p /tmp/ao-tmpl
docker cp "$ADMINHOME/ao-coder-template/main.tf" coder:/tmp/ao-tmpl/main.tf
docker exec -e CODER_URL=https://$FQDN -e CODER_SESSION_TOKEN="$SESSION" coder \
  coder templates push "$TEMPLATE_NAME" -d /tmp/ao-tmpl \
  --variable workspace_image="$WORKSPACE_IMAGE" \
  --variable workspace_memory_mb="$MEM" \
  --variable workspace_cpu_shares="$CPU" -y 2>&1 | tail -3
ORG=$(curl -s -H "Coder-Session-Token: $SESSION" https://$FQDN/api/v2/users/me | python3 -c 'import sys,json;print(json.load(sys.stdin)["organization_ids"][0])')
TID=$(curl -s -H "Coder-Session-Token: $SESSION" "https://$FQDN/api/v2/organizations/$ORG/templates" \
  | python3 -c "import sys,json;[print(t['id']) for t in json.load(sys.stdin) if t['name']=='$TEMPLATE_NAME']")
if [ -n "$TID" ]; then
  grep -q "^CODER_TEMPLATE_ID=" /etc/ao-coder.env && sed -i "/^CODER_TEMPLATE_ID=/d" /etc/ao-coder.env
  echo "CODER_TEMPLATE_ID=$TID" >> /etc/ao-coder.env
  echo "CODER_TEMPLATE_ID=$TID"
else echo "TEMPLATE_ID_NOT_FOUND"; fi
REMOTE

echo
echo "Template published (with per-workspace limits). AO_CLOUD_CODER_TEMPLATE_ID is on the VM at /etc/ao-coder.env."
echo "Point a NON-SHARED control plane at https://${FQDN} with that template id + the CODER_TOKEN from the VM."
