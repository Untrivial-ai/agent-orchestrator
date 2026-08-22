variable "aws_region" {
  description = "AWS region for the staging control plane."
  type        = string
  default     = "us-west-2"
}

variable "environment" {
  description = "Hosted AO environment name."
  type        = string
  default     = "staging"

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{1,20}$", var.environment))
    error_message = "environment must be a short lowercase slug"
  }
}

variable "vpc_cidr" {
  description = "CIDR reserved for the AO Cloud staging VPC."
  type        = string
  default     = "10.42.0.0/16"
}

variable "database_instance_class" {
  description = "Single-AZ RDS instance class used by staging."
  type        = string
  default     = "db.t4g.micro"
}

variable "database_name" {
  type    = string
  default = "ao_cloud"
}

variable "database_owner_user" {
  type    = string
  default = "ao_cloud_owner"
}

variable "database_runtime_user" {
  type    = string
  default = "ao_cloud_runtime"
}

variable "control_plane_image" {
  description = "Digest-pinned control-plane image. A placeholder is safe while desired_count is zero."
  type        = string
  default     = "public.ecr.aws/docker/library/busybox:1.36"
}

variable "deployment_enabled" {
  description = "Run one API task after secrets and migrations are ready."
  type        = bool
  default     = false
}
