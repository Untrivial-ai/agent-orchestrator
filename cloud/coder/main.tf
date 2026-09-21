terraform {
  required_providers {
    coder = {
      source = "coder/coder"
    }
    docker = {
      source = "kreuzwerker/docker"
    }
  }
}

variable "docker_socket" {
  default     = ""
  description = "Optional Docker socket URI"
  type        = string
}

variable "workspace_image" {
  default     = "ao-coder-workspace:local"
  description = "Approved image containing the release-matched AO worker and coding harness"
  type        = string
}

variable "workspace_memory_mb" {
  default     = 0
  description = "Hard per-workspace memory limit in MB (0 = unlimited). Set this on shared single-host deployments (e.g. the Azure Coder VM) so one workspace cannot OOM the host and take down its siblings."
  type        = number
}

variable "workspace_cpu_shares" {
  default     = 0
  description = "Relative CPU weight per workspace (0 = default/unset). On a shared host this keeps CPU fair across concurrent workspaces without a hard cap."
  type        = number
}

provider "docker" {
  host = var.docker_socket != "" ? var.docker_socket : null
}

data "coder_provisioner" "me" {}
data "coder_workspace" "me" {}
data "coder_workspace_owner" "me" {}

resource "coder_agent" "main" {
  arch = data.coder_provisioner.me.arch
  os   = "linux"

  startup_script = <<-EOT
    set -e
    if [ ! -f ~/.init_done ]; then
      cp -rT /etc/skel ~
      touch ~/.init_done
    fi
    claude --version
  EOT

  env = {
    GIT_AUTHOR_NAME     = coalesce(data.coder_workspace_owner.me.full_name, data.coder_workspace_owner.me.name)
    GIT_AUTHOR_EMAIL    = data.coder_workspace_owner.me.email
    GIT_COMMITTER_NAME  = coalesce(data.coder_workspace_owner.me.full_name, data.coder_workspace_owner.me.name)
    GIT_COMMITTER_EMAIL = data.coder_workspace_owner.me.email
  }
}

resource "docker_volume" "home_volume" {
  name = "coder-${data.coder_workspace.me.id}-home"

  lifecycle {
    ignore_changes = all
  }

  labels {
    label = "coder.owner"
    value = data.coder_workspace_owner.me.name
  }
  labels {
    label = "coder.owner_id"
    value = data.coder_workspace_owner.me.id
  }
  labels {
    label = "coder.workspace_id"
    value = data.coder_workspace.me.id
  }
  labels {
    label = "coder.workspace_name_at_creation"
    value = data.coder_workspace.me.name
  }
}

resource "docker_container" "workspace" {
  count = data.coder_workspace.me.start_count
  image = var.workspace_image

  name     = "coder-${data.coder_workspace_owner.me.name}-${lower(data.coder_workspace.me.name)}"
  hostname = data.coder_workspace.me.name
  entrypoint = [
    "sh",
    "-c",
    replace(coder_agent.main.init_script, "/localhost|127\\.0\\.0\\.1/", "host.docker.internal"),
  ]
  env = ["CODER_AGENT_TOKEN=${coder_agent.main.token}"]

  # Optional per-workspace limits (no-op when the variables are 0, so the
  # existing single-tenant deployments are unchanged). Used on shared single-host
  # deployments so one workspace cannot exhaust the host.
  memory     = var.workspace_memory_mb > 0 ? var.workspace_memory_mb : null
  cpu_shares = var.workspace_cpu_shares > 0 ? var.workspace_cpu_shares : null

  host {
    host = "host.docker.internal"
    ip   = "host-gateway"
  }

  volumes {
    container_path = "/home/coder"
    volume_name    = docker_volume.home_volume.name
    read_only      = false
  }

  labels {
    label = "coder.owner"
    value = data.coder_workspace_owner.me.name
  }
  labels {
    label = "coder.owner_id"
    value = data.coder_workspace_owner.me.id
  }
  labels {
    label = "coder.workspace_id"
    value = data.coder_workspace.me.id
  }
  labels {
    label = "coder.workspace_name"
    value = data.coder_workspace.me.name
  }
}
