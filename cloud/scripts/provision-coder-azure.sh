#!/usr/bin/env bash
set -euo pipefail

# Stand up a self-contained, TLS-terminated Coder deployment on Azure so cloud
# sessions with provider=coder run on Azure while the AO control plane and its
# database stay on AWS. The AO code is unchanged: the coder provider already
# talks to a Coder API endpoint (AO_CLOUD_CODER_URL), so "coder on Azure" is
# this infra plus repointing that env var at the HTTPS URL this script prints.
#
# Shape (single VM, matches cloud/coder/main.tf which is a Docker template):
#   one Azure VM runs Docker + Caddy (auto-TLS) + Coder + Postgres, all as
#   containers with --restart unless-stopped (survive VM reboots). The Coder
#   provisioner uses the VM's own Docker socket, so each workspace is a container
#   on that VM; Caddy fronts Coder with a Let's Encrypt cert so the CP<->Coder
#   token never crosses the internet in cleartext. Coder's 3000 is NOT published
#   (only reachable via Caddy/443).
#
# Requires: az (logged in). Region defaults to swedencentral to sit next to the
# AWS control plane in eu-north-1 (keeps the workspace<->CP terminal relay fast).
#
# After this prints CODER_URL (https), build the workspace image and publish the
# template with provision-coder-azure-template.sh, then point a (non-shared)
# control plane at the printed values.

LOCATION="${AO_AZURE_LOCATION:-swedencentral}"
RG="${AO_AZURE_RG:-ao-coder-azure}"
VM="${AO_AZURE_VM:-ao-coder-azure-vm}"
VM_SIZE="${AO_AZURE_VM_SIZE:-Standard_D4s_v5}"
VM_IMAGE="${AO_AZURE_VM_IMAGE:-Ubuntu2204}"
ADMIN_USER="${AO_AZURE_ADMIN_USER:-azureuser}"
DNS_LABEL_OVERRIDE="${AO_AZURE_DNS_LABEL:-}"   # empty => reuse existing label, else generate once
# Restrict who can reach SSH (22). SSH is only needed for the one-time template
# build (provision-coder-azure-template.sh). Default to a CIDR you control; leave
# unset only if you will close 22 immediately after provisioning (the running
# deployment needs only 443 + `az vm run-command` for admin).
SSH_SOURCE="${AO_AZURE_SSH_SOURCE:-}"
CODER_IMAGE="${AO_CODER_IMAGE:-ghcr.io/coder/coder:latest}"
CODER_ADMIN_EMAIL="${AO_CODER_ADMIN_EMAIL:-admin@ao-coder.dev}"
CODER_ADMIN_USERNAME="${AO_CODER_ADMIN_USERNAME:-aoadmin}"

say() { printf '\n=== %s ===\n' "$*"; }

say "resource group ${RG} (${LOCATION})"
az group create -n "$RG" -l "$LOCATION" -o none

say "VM ${VM} (${VM_SIZE})"
if ! az vm show -g "$RG" -n "$VM" -o none 2>/dev/null; then
  # Retry: az vm create occasionally races on the auto-created NIC
  # (ResourceNotFound ...VMNic) even though the VM ends up created; a retry is
  # idempotent because the second attempt finds the resources and completes.
  created=""
  for attempt in 1 2 3; do
    if az vm create -g "$RG" -n "$VM" \
        --image "$VM_IMAGE" --size "$VM_SIZE" \
        --admin-username "$ADMIN_USER" --generate-ssh-keys \
        --public-ip-sku Standard --nsg-rule SSH \
        --tags purpose=ao-coder-azure managed-by=provision-coder-azure -o none; then
      created=1; break
    fi
    echo "vm create attempt ${attempt} failed (transient NIC race); retrying in 10s..."
    sleep 10
  done
  [ -n "$created" ] || { echo "VM creation failed after 3 attempts"; exit 1; }
fi

say "DNS label -> stable FQDN (for the TLS cert)"
# Resolve the Coder HOST VM's OWN public IP explicitly (via its NIC), never the
# "first public IP in the resource group": once the VM-per-workspace template is
# in use, the group also holds per-workspace public IPs, and picking [0] could
# stamp the Coder DNS label onto a workspace IP - breaking the stable FQDN, its
# Let's Encrypt cert, health checks, and the wired CP endpoint.
PIPID="$(az vm list-ip-addresses -g "$RG" -n "$VM" --query '[0].virtualMachine.network.publicIpAddresses[0].id' -o tsv)"
[ -n "$PIPID" ] || { echo "could not resolve the Coder VM (${VM}) public IP"; exit 1; }
# Sticky label: reuse the label already on the IP so re-runs keep the same FQDN
# (regenerating it would break the wired CP and force a fresh Let's Encrypt cert).
EXISTING_LABEL="$(az network public-ip show --ids "$PIPID" --query 'dnsSettings.domainNameLabel' -o tsv 2>/dev/null || true)"
DNS_LABEL="${DNS_LABEL_OVERRIDE:-${EXISTING_LABEL:-ao-coder-$RANDOM}}"
if [ "$DNS_LABEL" != "$EXISTING_LABEL" ]; then
  az network public-ip update --ids "$PIPID" --dns-name "$DNS_LABEL" -o none
fi
FQDN="$(az network public-ip show --ids "$PIPID" --query 'dnsSettings.fqdn' -o tsv)"
IP="$(az network public-ip show --ids "$PIPID" --query 'ipAddress' -o tsv)"
say "FQDN ${FQDN} (${IP})"

# Lock SSH down if a source CIDR was provided (default az rule is world-open).
if [ -n "$SSH_SOURCE" ]; then
  NSG0="$(az network nsg list -g "$RG" --query '[0].name' -o tsv)"
  az network nsg rule update -g "$RG" --nsg-name "$NSG0" -n default-allow-ssh \
    --source-address-prefixes "$SSH_SOURCE" -o none 2>/dev/null || true
  say "SSH restricted to ${SSH_SOURCE}"
else
  say "WARNING: SSH (22) is OPEN to the internet (key-only). Set AO_AZURE_SSH_SOURCE, or after provisioning close it: az network nsg rule delete -g ${RG} --nsg-name <nsg> -n default-allow-ssh (admin then via 'az vm run-command')."
fi

say "open NSG ports 80/443 (Coder is HTTPS-only via Caddy; 3000 stays unpublished)"
NSG="$(az network nsg list -g "$RG" --query '[0].name' -o tsv)"
az network nsg rule show -g "$RG" --nsg-name "$NSG" -n coder -o none 2>/dev/null \
  && az network nsg rule update -g "$RG" --nsg-name "$NSG" -n coder --destination-port-ranges 80 443 -o none \
  || az network nsg rule create -g "$RG" --nsg-name "$NSG" -n coder --priority 1010 \
       --destination-port-ranges 80 443 --access Allow --protocol Tcp --direction Inbound -o none

say "install Docker"
az vm run-command invoke -g "$RG" -n "$VM" --command-id RunShellScript --scripts \
  'command -v docker >/dev/null 2>&1 || curl -fsSL https://get.docker.com | sh; systemctl enable --now docker; docker --version' \
  --query 'value[0].message' -o tsv | tail -1

say "bring up Caddy (auto-TLS) + Coder + Postgres (idempotent; secrets persisted on the VM)"
az vm run-command invoke -g "$RG" -n "$VM" --command-id RunShellScript --scripts '
set -e
umask 077   # so /etc/ao-coder.env is never briefly world-readable between create and chmod
ENVF=/etc/ao-coder.env
grep -q "^PGPW=" "$ENVF" 2>/dev/null || { echo "PGPW=$(openssl rand -hex 16)" >> "$ENVF"; chmod 600 "$ENVF"; }
. "$ENVF"
FQDN="'"$FQDN"'"
docker network create coder 2>/dev/null || true
# Postgres
if ! docker ps -a --format "{{.Names}}" | grep -q "^coder-db$"; then
  docker run -d --name coder-db --network coder --restart unless-stopped \
    -e POSTGRES_USER=coder -e POSTGRES_PASSWORD="$PGPW" -e POSTGRES_DB=coder \
    -v coder-db-data:/var/lib/postgresql/data postgres:16
  sleep 12
fi
# Caddy (auto Let'"'"'s Encrypt for $FQDN -> coder:3000)
mkdir -p /etc/caddy
printf "%s {\n    reverse_proxy coder:3000\n}\n" "$FQDN" > /etc/caddy/Caddyfile
docker rm -f caddy >/dev/null 2>&1 || true
docker run -d --name caddy --network coder --restart unless-stopped \
  -p 80:80 -p 443:443 -v caddy_data:/data -v /etc/caddy/Caddyfile:/etc/caddy/Caddyfile:ro caddy:2
# Coder (HTTPS access URL; 3000 NOT published - only Caddy reaches it)
docker rm -f coder >/dev/null 2>&1 || true
DOCKGID=$(stat -c %g /var/run/docker.sock)
docker run -d --name coder --network coder --restart unless-stopped \
  --group-add "$DOCKGID" -v /var/run/docker.sock:/var/run/docker.sock \
  -e CODER_ACCESS_URL=https://$FQDN \
  -e CODER_HTTP_ADDRESS=0.0.0.0:3000 \
  -e CODER_MAX_ADMIN_TOKEN_LIFETIME=8760h \
  -e CODER_PG_CONNECTION_URL="postgres://coder:$PGPW@coder-db:5432/coder?sslmode=disable" \
  "'"$CODER_IMAGE"'"
for i in $(seq 1 40); do curl -fsS -o /dev/null https://$FQDN/healthz 2>/dev/null && break; sleep 5; done
curl -fsS -o /dev/null -w "https://$FQDN/healthz: %{http_code}\n" https://$FQDN/healthz
' --query 'value[0].message' -o tsv | tail -3

say "create first admin + long-lived API token (via the API, idempotent; stored on the VM)"
az vm run-command invoke -g "$RG" -n "$VM" --command-id RunShellScript --scripts '
set -e
umask 077
ENVF=/etc/ao-coder.env; . "$ENVF"
FQDN="'"$FQDN"'"
grep -q "^CODER_ADMIN_PW=" "$ENVF" || echo "CODER_ADMIN_PW=$(openssl rand -hex 16)" >> "$ENVF"
. "$ENVF"
curl -s -X POST https://$FQDN/api/v2/users/first -H "Content-Type: application/json" \
  -d "{\"email\":\"'"$CODER_ADMIN_EMAIL"'\",\"username\":\"'"$CODER_ADMIN_USERNAME"'\",\"password\":\"$CODER_ADMIN_PW\",\"trial\":false}" >/dev/null 2>&1 || true
if ! grep -q "^CODER_TOKEN=" "$ENVF"; then
  SESSION=$(curl -s -X POST https://$FQDN/api/v2/users/login -H "Content-Type: application/json" \
    -d "{\"email\":\"'"$CODER_ADMIN_EMAIL"'\",\"password\":\"$CODER_ADMIN_PW\"}" \
    | python3 -c "import sys,json;print(json.load(sys.stdin).get(\"session_token\",\"\"))")
  RAW=$(docker exec -e CODER_URL=https://$FQDN -e CODER_SESSION_TOKEN="$SESSION" \
    coder coder tokens create --name ao-cp-primary --lifetime 8760h 2>&1)
  TOK=$(echo "$RAW" | grep -oE "[A-Za-z0-9]+-[A-Za-z0-9]+" | head -1)
  [ -n "$TOK" ] && echo "CODER_TOKEN=$TOK" >> "$ENVF"
fi
. "$ENVF"
[ -n "${CODER_TOKEN:-}" ] \
  && echo "token ready; whoami -> $(curl -s -o /dev/null -w "%{http_code}" -H "Coder-Session-Token: $CODER_TOKEN" https://$FQDN/api/v2/users/me)" \
  || echo "TOKEN NOT CREATED"
' --query 'value[0].message' -o tsv | tail -3

cat <<EOF

================ Coder on Azure is up (TLS) ================
CODER_URL   = https://${FQDN}
Region      = ${LOCATION}  (next to AWS eu-north-1)
Admin       = ${CODER_ADMIN_EMAIL} / user ${CODER_ADMIN_USERNAME}
Secrets on the VM at /etc/ao-coder.env (PGPW, CODER_ADMIN_PW, CODER_TOKEN) - not printed here.

Next:
  1. Build the workspace image + publish the template (with per-workspace limits):
       AO_AZURE_RG=${RG} AO_AZURE_VM=${VM} AO_AZURE_FQDN=${FQDN} \\
       AO_CLOUD_CP_IMAGE=<acct>.dkr.ecr.eu-north-1.amazonaws.com/ao-cloud-control-plane:<tag>-linux-amd64 \\
       ./scripts/provision-coder-azure-template.sh
  2. Point a NON-SHARED control plane at:
       AO_CLOUD_CODER_URL=https://${FQDN}
       AO_CLOUD_CODER_TOKEN=<the CODER_TOKEN from the VM>
       AO_CLOUD_CODER_TEMPLATE_ID=<printed by step 1>
       AO_CLOUD_CODER_OWNER=${CODER_ADMIN_USERNAME}
===========================================================
EOF
