#!/usr/bin/env python3
"""Pure helpers for producing and validating AO Cloud ECS deployments."""

from __future__ import annotations

import copy
import re
from typing import Any
from urllib.parse import urlparse


TASK_KEYS = {
    "networkMode",
    "containerDefinitions",
    "volumes",
    "placementConstraints",
    "requiresCompatibilities",
    "cpu",
    "memory",
    "pidMode",
    "ipcMode",
    "proxyConfiguration",
    "inferenceAccelerators",
    "ephemeralStorage",
    "runtimePlatform",
}

NODEOPS_SECRET_ENV = {
    "AO_CLOUD_NODEOPS_BASE_URL": "base_url",
    "AO_CLOUD_NODEOPS_API_KEY": "api_key",
    "AO_CLOUD_NODEOPS_DEFAULT_SHAPE": "default_shape",
    "AO_CLOUD_NODEOPS_DEFAULT_ROOTFS": "default_rootfs",
    "AO_CLOUD_NODEOPS_INGRESS": "ingress",
    "AO_CLOUD_NODEOPS_SSH_KEY_PATH": "ssh_key_path",
    "AO_CLOUD_NODEOPS_REGION": "region",
    "AO_CLOUD_NODEOPS_WORKER_TOKEN_TTL": "worker_token_ttl",
}
WORKER_SECRET_ENV = {
    "AO_CLOUD_WORKER_SIGNING_KEY": "signing_key",
    "AO_CLOUD_MAX_ACTIVE_SANDBOXES_PER_ORG": "max_active_sandboxes_per_org",
    "AO_CLOUD_SANDBOX_RECONCILE_INTERVAL": "sandbox_reconcile_interval",
    "AO_CLOUD_SANDBOX_STARTUP_TIMEOUT": "sandbox_startup_timeout",
    "AO_CLOUD_WORKER_HEARTBEAT_TIMEOUT": "worker_heartbeat_timeout",
}
HOSTED_SECRET_ENV = NODEOPS_SECRET_ENV | WORKER_SECRET_ENV
PROVIDER_AUTO_PAUSE_ENV = "AO_CLOUD_NODEOPS_AUTO_PAUSE_MINUTES"
WORKER_BINARY_PATH = "/ao-worker"
WORKER_HELPER_BINARY_PATH = "/ao"
_DIGEST_IMAGE = re.compile(r"^.+@sha256:[0-9a-f]{64}$")
_DURATION_PART = re.compile(r"(\d+)(ms|s|m|h)")


def secret_environment(secret_arn: str, fields: dict[str, str]) -> dict[str, str]:
    if not secret_arn.strip():
        raise ValueError("secret ARN must not be empty")
    return {
        environment_name: f"{secret_arn}:{json_key}::"
        for environment_name, json_key in fields.items()
    }


def validate_hosted_settings(
    nodeops: dict[str, Any], worker: dict[str, Any]
) -> None:
    _require_secret_strings("NodeOps", nodeops, NODEOPS_SECRET_ENV.values())
    _require_secret_strings("worker", worker, WORKER_SECRET_ENV.values())
    if "auto_pause_minutes" in nodeops:
        raise ValueError("NodeOps settings must not configure provider auto-pause")

    base_url = urlparse(nodeops["base_url"])
    if base_url.scheme != "https" or not base_url.netloc:
        raise ValueError("NodeOps base_url must be an absolute HTTPS URL")
    for key in ("api_key", "default_shape", "default_rootfs"):
        if not nodeops[key].strip():
            raise ValueError(f"NodeOps {key} must not be empty")
    if nodeops["ingress"].strip().lower() not in ("", "enabled", "disabled"):
        raise ValueError("NodeOps ingress must be enabled, disabled, or empty")
    if _duration_seconds(nodeops["worker_token_ttl"]) <= 0:
        raise ValueError("NodeOps worker_token_ttl must be positive")

    if len(worker["signing_key"].strip()) < 32:
        raise ValueError("worker signing_key must contain at least 32 characters")
    try:
        quota = int(worker["max_active_sandboxes_per_org"])
    except ValueError as error:
        raise ValueError(
            "worker max_active_sandboxes_per_org must be an integer"
        ) from error
    if quota < 1:
        raise ValueError("worker max_active_sandboxes_per_org must be positive")
    if _duration_seconds(worker["sandbox_reconcile_interval"]) <= 0:
        raise ValueError("worker sandbox_reconcile_interval must be positive")
    if _duration_seconds(worker["sandbox_startup_timeout"]) < 30:
        raise ValueError("worker sandbox_startup_timeout must be at least 30s")
    if _duration_seconds(worker["worker_heartbeat_timeout"]) < 30:
        raise ValueError("worker worker_heartbeat_timeout must be at least 30s")


def _require_secret_strings(
    label: str, values: dict[str, Any], required: Any
) -> None:
    missing = sorted(set(required) - values.keys())
    if missing:
        raise ValueError(f"{label} settings are missing: {', '.join(missing)}")
    invalid = sorted(key for key in required if not isinstance(values[key], str))
    if invalid:
        raise ValueError(f"{label} settings must be strings: {', '.join(invalid)}")


def _duration_seconds(value: str) -> float:
    position = 0
    seconds = 0.0
    units = {"ms": 0.001, "s": 1, "m": 60, "h": 3600}
    for match in _DURATION_PART.finditer(value.strip()):
        if match.start() != position:
            raise ValueError(f"invalid duration setting: {value!r}")
        seconds += int(match.group(1)) * units[match.group(2)]
        position = match.end()
    if position == 0 or position != len(value.strip()):
        raise ValueError(f"invalid duration setting: {value!r}")
    return seconds


def validate_digest_image(image: str, label: str) -> None:
    if not _DIGEST_IMAGE.fullmatch(image):
        raise ValueError(f"{label} image must be pinned to a sha256 digest")


def build_task_definition(
    source: dict[str, Any],
    *,
    family: str,
    container_name: str,
    image: str,
    release: str,
    environment: str,
    log_group: str,
    region: str,
    runtime_database_user: str = "",
    worker_image: str = "",
    environment_overrides: dict[str, str] | None = None,
    secret_overrides: dict[str, str] | None = None,
) -> dict[str, Any]:
    task = source["taskDefinition"]
    payload = {
        key: copy.deepcopy(value)
        for key, value in task.items()
        if key in TASK_KEYS
    }
    payload.update(
        {
            "family": family,
            "taskRoleArn": task["taskRoleArn"],
            "executionRoleArn": task["executionRoleArn"],
        }
    )
    container = next(
        item
        for item in payload["containerDefinitions"]
        if item["name"] == container_name
    )
    validate_digest_image(image, container_name)
    container["image"] = image

    environment_overrides = environment_overrides or {}
    secret_overrides = secret_overrides or {}
    if (
        PROVIDER_AUTO_PAUSE_ENV in environment_overrides
        or PROVIDER_AUTO_PAUSE_ENV in secret_overrides
    ):
        raise ValueError("provider auto-pause must not be configured by deployment")
    values = {
        item["name"]: item["value"]
        for item in container.get("environment", [])
        if item["name"] != PROVIDER_AUTO_PAUSE_ENV
    }
    values["AO_CLOUD_RELEASE"] = release
    if container_name == "control-plane":
        validate_digest_image(worker_image, "worker")
        values.update(
            {
                "AO_CLOUD_ENV": environment,
                "AO_CLOUD_HTTP_ADDRESS": ":8080",
                "AO_CLOUD_LOCAL_AUTH": "false",
                "AO_CLOUD_MIGRATE_ON_STARTUP": "false",
                "AO_CLOUD_SANDBOX_PROVIDER": "nodeops",
                "AO_CLOUD_WORKER_BINARY_PATH": WORKER_BINARY_PATH,
                "AO_CLOUD_WORKER_HELPER_BINARY_PATH": WORKER_HELPER_BINARY_PATH,
            }
        )
    elif container_name == "migration":
        values["AO_CLOUD_RUNTIME_DATABASE_USER"] = runtime_database_user
    values.update(environment_overrides)
    container["environment"] = [
        {"name": name, "value": value}
        for name, value in sorted(values.items())
    ]
    secrets = {
        item["name"]: item["valueFrom"]
        for item in container.get("secrets", [])
        if item["name"] != PROVIDER_AUTO_PAUSE_ENV
    }
    secrets.update(secret_overrides)
    if container_name == "control-plane":
        missing = sorted(set(HOSTED_SECRET_ENV) - secrets.keys())
        if missing:
            raise ValueError(
                "control-plane task is missing hosted secrets: "
                + ", ".join(missing)
            )
        plaintext = sorted(set(HOSTED_SECRET_ENV) & values.keys())
        if plaintext:
            raise ValueError(
                "hosted settings must not be plaintext environment values: "
                + ", ".join(plaintext)
            )
    container["secrets"] = [
        {"name": name, "valueFrom": value}
        for name, value in sorted(secrets.items())
    ]
    log_options = container["logConfiguration"]["options"]
    log_options.update(
        {
            "awslogs-group": log_group,
            "awslogs-region": region,
            "awslogs-stream-prefix": (
                "api" if container_name == "control-plane" else "migration"
            ),
        }
    )
    payload["tags"] = [
        {"key": "Project", "value": "ao-cloud"},
        {"key": "Environment", "value": environment},
        {"key": "Release", "value": release},
    ]
    if container_name == "control-plane":
        payload["tags"].append({"key": "WorkerImage", "value": worker_image})
    reject_cross_environment_references(payload, environment)
    return payload


def validate_task_artifacts(
    source: dict[str, Any],
    *,
    container_name: str,
    control_image: str,
    worker_image: str,
) -> None:
    validate_digest_image(control_image, "control-plane")
    validate_digest_image(worker_image, "worker")
    task = source["taskDefinition"]
    container = next(
        item
        for item in task["containerDefinitions"]
        if item["name"] == container_name
    )
    if container.get("image") != control_image:
        raise ValueError("task definition uses an unexpected control-plane image")
    environment = {
        item["name"]: item["value"] for item in container.get("environment", [])
    }
    if environment.get("AO_CLOUD_WORKER_BINARY_PATH") != WORKER_BINARY_PATH:
        raise ValueError("task definition does not use packaged /ao-worker")
    if (
        environment.get("AO_CLOUD_WORKER_HELPER_BINARY_PATH")
        != WORKER_HELPER_BINARY_PATH
    ):
        raise ValueError("task definition does not use packaged /ao helper")
    if PROVIDER_AUTO_PAUSE_ENV in environment:
        raise ValueError("task definition configures provider auto-pause")
    secrets = {
        item["name"]: item["valueFrom"] for item in container.get("secrets", [])
    }
    missing = sorted(set(HOSTED_SECRET_ENV) - secrets.keys())
    if missing:
        raise ValueError(
            "task definition is missing hosted secrets: " + ", ".join(missing)
        )
    if PROVIDER_AUTO_PAUSE_ENV in secrets:
        raise ValueError("task definition loads provider auto-pause from a secret")
    tags = {item["key"]: item["value"] for item in source.get("tags", [])}
    if tags.get("WorkerImage") != worker_image:
        raise ValueError("task definition uses an unexpected worker image")


def reject_cross_environment_references(
    payload: dict[str, Any], environment: str
) -> None:
    forbidden_environment = (
        "staging" if environment == "production" else "production"
    )
    stack = [payload]
    while stack:
        value = stack.pop()
        if isinstance(value, dict):
            stack.extend(value.values())
        elif isinstance(value, list):
            stack.extend(value)
        elif isinstance(value, str):
            lowered = value.lower()
            if (
                f"/{forbidden_environment}/" in lowered
                or f"ao-cloud-{forbidden_environment}" in lowered
                or f"{forbidden_environment}-api." in lowered
                or f"/ao-cloud/{forbidden_environment}/" in lowered
            ):
                raise ValueError(
                    f"{environment} task contains {forbidden_environment} "
                    f"reference: {value}"
                )


def validate_service(
    *,
    service: dict[str, Any],
    tasks: list[dict[str, Any]],
    targets: list[dict[str, Any]],
    alarm_state: str,
    expected_task_definition: str | None = None,
) -> None:
    desired = service.get("desiredCount", 0)
    if desired < 2:
        raise ValueError(f"desired task count is {desired}, expected at least 2")
    if service.get("pendingCount") != 0:
        raise ValueError("service has pending tasks")
    if service.get("runningCount") != desired:
        raise ValueError("running task count does not match desired count")

    primary = [
        deployment
        for deployment in service.get("deployments", [])
        if deployment.get("status") == "PRIMARY"
    ]
    if len(primary) != 1 or primary[0].get("rolloutState") != "COMPLETED":
        raise ValueError("primary deployment is not complete")
    task_definition = expected_task_definition or service.get("taskDefinition")
    if primary[0].get("taskDefinition") != task_definition:
        raise ValueError("primary deployment uses an unexpected task definition")
    if len(tasks) != desired:
        raise ValueError("running task inventory does not match desired count")
    if any(task.get("taskDefinitionArn") != task_definition for task in tasks):
        raise ValueError("running tasks contain a mixed task-definition revision")
    if len(targets) != desired:
        raise ValueError("registered ALB target count does not match running tasks")
    if any(
        target.get("TargetHealth", {}).get("State") != "healthy"
        for target in targets
    ):
        raise ValueError("one or more ALB targets are unhealthy")
    if alarm_state != "OK":
        raise ValueError(f"deployment alarm state is {alarm_state}, expected OK")
