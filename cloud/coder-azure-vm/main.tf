# AO Coder template: one dedicated Azure VM per workspace, running the AO worker
# + harness NATIVELY on the VM (no Docker, no container) - mirroring eleven_x's
# AWS EC2-per-workspace dev-kit with a baked AMI. The VM boots from a baked
# managed image (ao-coder-workspace-image: Ubuntu + node + claude + ao-worker/ao
# + a coder user with NOPASSWD sudo), and cloud-init starts the coder agent as a
# native systemd service. The CP then bootstraps the AO worker natively over the
# agent, exactly like the eleven_x setup. Stop deallocates the VM; start brings
# it back (the systemd agent restarts on boot).

terraform {
  required_providers {
    coder   = { source = "coder/coder" }
    azurerm = { source = "hashicorp/azurerm" }
    tls     = { source = "hashicorp/tls" }
  }
}

provider "azurerm" {
  features {}
}

variable "resource_group" {
  type    = string
  default = "ao-coder-azure"
}
variable "location" {
  type    = string
  default = "swedencentral"
}
variable "subnet_id" {
  type = string
}
variable "vm_size" {
  type    = string
  default = "Standard_D2s_v5" # ~2 vCPU / 8 GB, close to eleven_x t3.medium
}
variable "image_id" {
  type = string # the baked managed image (native worker + harness)
}
variable "admin_username" {
  type    = string
  default = "azureadmin"
}

data "coder_provisioner" "me" {}
data "coder_workspace" "me" {}
data "coder_workspace_owner" "me" {}

# Machine size, chosen per session by AO's template picker (rich parameter
# "size"). AO only sends this for a non-default template; a template rendered
# with no value falls back to the default below, which maps to the same SKU the
# template used before this parameter existed, so behavior is unchanged.
data "coder_parameter" "size" {
  name         = "size"
  display_name = "Machine size"
  description  = "vCPU and memory for the workspace VM."
  type         = "string"
  default      = "small"
  mutable      = false
  icon         = "/icon/memory.svg"
  order        = 1
  option {
    name  = "Small — 2 vCPU / 8 GB"
    value = "small"
  }
  option {
    name  = "Medium — 4 vCPU / 16 GB"
    value = "medium"
  }
  option {
    name  = "Large — 8 vCPU / 32 GB"
    value = "large"
  }
}

# Optional per-session shell snippet run after the workspace is ready (dev-env
# bring-up, e.g. a Makefile target). Empty by default.
data "coder_parameter" "startup_script" {
  name         = "startup_script"
  display_name = "Startup script"
  description  = "Optional shell commands to run once the workspace is ready."
  type         = "string"
  default      = ""
  mutable      = true
  order        = 2
}

resource "coder_agent" "main" {
  arch = "amd64"
  os   = "linux"

  startup_script = <<-EOT
    set -e
    if [ ! -f ~/.init_done ]; then
      cp -rT /etc/skel ~ 2>/dev/null || true
      touch ~/.init_done
    fi
    # Warm ALL harnesses into the page cache during the boot window so the first
    # real launch is snappy. codex launches via a Node shim that then exec's a
    # large native binary; left cold, that adds ~10s to first frame on a freshly
    # booted VM. claude is a single binary and was already warmed here - which is
    # exactly why claude sessions felt instant while codex/cursor lagged. Warm
    # all three so the terminal is snappy regardless of the selected harness.
    claude --version || true
    codex --version || true
    cursor-agent --version || true
    # Optional AO-provided per-session dev-env bring-up. Run outside `set -e` so a
    # failing user script never blocks the agent from coming up.
    set +e
    ${data.coder_parameter.startup_script.value}
    set -e
  EOT

  env = {
    GIT_AUTHOR_NAME     = coalesce(data.coder_workspace_owner.me.full_name, data.coder_workspace_owner.me.name)
    GIT_AUTHOR_EMAIL    = data.coder_workspace_owner.me.email
    GIT_COMMITTER_NAME  = coalesce(data.coder_workspace_owner.me.full_name, data.coder_workspace_owner.me.name)
    GIT_COMMITTER_EMAIL = data.coder_workspace_owner.me.email
  }
}

# Throwaway key so azurerm is satisfied; the VM has no inbound SSH (the agent
# dials out to Coder), so this key never reaches it.
resource "tls_private_key" "vm" {
  algorithm = "RSA"
  rsa_bits  = 4096
}

locals {
  name = lower("ao-${substr(data.coder_workspace.me.id, 0, 18)}")

  # Map the picker's t-shirt size to an Azure VM SKU. "small" matches the SKU the
  # template used before the size parameter existed, so an unset value is a no-op.
  size_to_sku = {
    small  = "Standard_D2s_v5" # ~2 vCPU / 8 GB
    medium = "Standard_D4s_v5" # ~4 vCPU / 16 GB
    large  = "Standard_D8s_v5" # ~8 vCPU / 32 GB
  }
  vm_sku = lookup(local.size_to_sku, data.coder_parameter.size.value, var.vm_size)

  # Run the coder agent as the (baked) coder user via a systemd service so it
  # survives stop/start reboots. The AO worker is baked into the image and gets
  # bootstrapped natively by the control plane over the agent.
  agent_unit = <<-UNIT
    [Unit]
    Description=Coder Agent
    After=network-online.target
    Wants=network-online.target
    [Service]
    User=coder
    Environment=CODER_AGENT_TOKEN=${coder_agent.main.token}
    Environment=HOME=/home/coder
    WorkingDirectory=/home/coder
    ExecStart=/bin/bash /opt/coder-init.sh
    Restart=always
    RestartSec=5
    [Install]
    WantedBy=multi-user.target
  UNIT

  # The AO worker bootstrap requires the durable root (/home/coder) to be a real
  # MOUNTED directory (mountpoint -q), which also gives stop/start disk
  # persistence. So format+mount the attached data disk at /home/coder BEFORE
  # starting the coder agent.
  mount_script = <<-MOUNT
    #!/bin/bash
    set -e
    for i in $(seq 1 60); do [ -b /dev/disk/azure/scsi1/lun0 ] && break; sleep 2; done
    DISK=/dev/disk/azure/scsi1/lun0
    blkid "$DISK" >/dev/null 2>&1 || mkfs.ext4 -F "$DISK"
    mkdir -p /home/coder
    grep -q " /home/coder " /etc/fstab || echo "$DISK /home/coder ext4 defaults,nofail 0 2" >> /etc/fstab
    mountpoint -q /home/coder || mount "$DISK" /home/coder
    chown coder:coder /home/coder
    chmod 0755 /home/coder
  MOUNT

  custom_data = base64encode(join("\n", [
    "#cloud-config",
    "write_files:",
    "  - path: /opt/mount-durable.sh",
    "    encoding: b64",
    "    permissions: '0755'",
    "    content: ${base64encode(local.mount_script)}",
    "  - path: /opt/coder-init.sh",
    "    encoding: b64",
    "    permissions: '0755'",
    "    content: ${base64encode(coder_agent.main.init_script)}",
    "  - path: /etc/systemd/system/coder-agent.service",
    "    encoding: b64",
    "    permissions: '0644'",
    "    content: ${base64encode(local.agent_unit)}",
    "runcmd:",
    "  - [ bash, /opt/mount-durable.sh ]",
    "  - [ systemctl, daemon-reload ]",
    "  - [ systemctl, enable, --now, coder-agent ]",
  ]))
}

# Persistent per-workspace data disk mounted at /home/coder (the durable root).
# count = 1 (not start_count) so it survives stop/start; destroyed on workspace
# delete. Satisfies the bootstrap's mountpoint check + persists workspace state.
resource "azurerm_managed_disk" "durable" {
  count                = 1
  name                 = "${local.name}-durable"
  resource_group_name  = var.resource_group
  location             = var.location
  storage_account_type = "StandardSSD_LRS"
  create_option        = "Empty"
  disk_size_gb         = 30
}

resource "azurerm_virtual_machine_data_disk_attachment" "durable" {
  count              = data.coder_workspace.me.start_count
  managed_disk_id    = azurerm_managed_disk.durable[0].id
  virtual_machine_id = azurerm_linux_virtual_machine.main[0].id
  lun                = 0
  caching            = "ReadWrite"
}

resource "azurerm_public_ip" "main" {
  count               = data.coder_workspace.me.start_count
  name                = "${local.name}-ip"
  resource_group_name = var.resource_group
  location            = var.location
  allocation_method   = "Static"
  sku                 = "Standard"
}

resource "azurerm_network_interface" "main" {
  count               = data.coder_workspace.me.start_count
  name                = "${local.name}-nic"
  resource_group_name = var.resource_group
  location            = var.location

  ip_configuration {
    name                          = "internal"
    subnet_id                     = var.subnet_id
    private_ip_address_allocation = "Dynamic"
    public_ip_address_id          = azurerm_public_ip.main[0].id
  }
}

resource "azurerm_linux_virtual_machine" "main" {
  count                 = data.coder_workspace.me.start_count
  name                  = local.name
  resource_group_name   = var.resource_group
  location              = var.location
  size                  = local.vm_sku
  admin_username        = var.admin_username
  network_interface_ids = [azurerm_network_interface.main[0].id]
  custom_data           = local.custom_data
  source_image_id       = var.image_id

  admin_ssh_key {
    username   = var.admin_username
    public_key = tls_private_key.vm.public_key_openssh
  }

  os_disk {
    caching              = "ReadWrite"
    storage_account_type = "StandardSSD_LRS"
  }

  tags = {
    "coder.workspace_id"   = data.coder_workspace.me.id
    "coder.workspace_name" = data.coder_workspace.me.name
    "coder.owner"          = data.coder_workspace_owner.me.name
  }
}
