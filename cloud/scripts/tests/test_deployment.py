import copy
import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parents[1]))

from lib.deployment import (
    NODEOPS_SECRET_ENV,
    WORKER_SECRET_ENV,
    build_task_definition,
    secret_environment,
    validate_hosted_settings,
    validate_service,
    validate_task_artifacts,
)


DIGEST = "a" * 64
CONTROL_IMAGE = f"registry/ao-cloud-control-plane@sha256:{DIGEST}"
WORKER_IMAGE = f"registry/ao-cloud-worker@sha256:{'b' * 64}"


def hosted_secret_overrides(environment="production"):
    return secret_environment(
        f"arn:secret:ao-cloud/{environment}/nodeops", NODEOPS_SECRET_ENV
    ) | secret_environment(
        f"arn:secret:ao-cloud/{environment}/worker", WORKER_SECRET_ENV
    )


def hosted_settings():
    return (
        {
            "base_url": "https://api.sb.createos.sh",
            "api_key": "nodeops-secret",
            "default_shape": "s-4vcpu-8gb",
            "default_rootfs": "devbox:1",
            "ingress": "disabled",
            "ssh_key_path": "",
            "region": "eu-north-1",
            "worker_token_ttl": "15m",
        },
        {
            "signing_key": "w" * 64,
            "max_active_sandboxes_per_org": "10",
            "sandbox_reconcile_interval": "2s",
            "sandbox_startup_timeout": "3m",
            "worker_heartbeat_timeout": "1m",
        },
    )


def task_source(environment="production"):
    return {
        "taskDefinition": {
            "taskRoleArn": f"arn:task:{environment}",
            "executionRoleArn": f"arn:execution:{environment}",
            "networkMode": "awsvpc",
            "requiresCompatibilities": ["FARGATE"],
            "cpu": "256",
            "memory": "512",
            "containerDefinitions": [
                {
                    "name": "control-plane",
                    "image": "old",
                    "environment": [
                        {"name": "AO_CLOUD_ENV", "value": environment},
                        {"name": "AO_CLOUD_PUBLIC_URL", "value": "https://api.aoagents.dev"},
                    ],
                    "secrets": [
                        {
                            "name": "AO_CLOUD_DATABASE_URL",
                            "valueFrom": f"arn:secret:ao-cloud/{environment}/database-url",
                        }
                    ],
                    "logConfiguration": {
                        "options": {
                            "awslogs-group": f"/ao-cloud/{environment}/control-plane"
                        }
                    },
                }
            ],
        }
    }


def healthy_service():
    task = "arn:task-definition:production:7"
    return (
        {
            "desiredCount": 2,
            "runningCount": 2,
            "pendingCount": 0,
            "taskDefinition": task,
            "deployments": [
                {
                    "status": "PRIMARY",
                    "rolloutState": "COMPLETED",
                    "taskDefinition": task,
                }
            ],
        },
        [{"taskDefinitionArn": task}, {"taskDefinitionArn": task}],
        [
            {"TargetHealth": {"State": "healthy"}},
            {"TargetHealth": {"State": "healthy"}},
        ],
    )


class TaskDefinitionTests(unittest.TestCase):
    def test_preserves_production_secrets_and_updates_release(self):
        payload = build_task_definition(
            task_source(),
            family="ao-cloud-production-api",
            container_name="control-plane",
            image=CONTROL_IMAGE,
            worker_image=WORKER_IMAGE,
            release="abc123",
            environment="production",
            log_group="/ao-cloud/production/control-plane",
            region="eu-north-1",
            environment_overrides={
                "AO_CLOUD_PUBLIC_URL": "https://api.aoagents.dev"
            },
            secret_overrides={
                "AO_CLOUD_GITHUB_APP_ID": (
                    "arn:secret:ao-cloud/production/github:app_id::"
                )
            }
            | hosted_secret_overrides(),
        )
        container = payload["containerDefinitions"][0]
        self.assertEqual(container["image"], CONTROL_IMAGE)
        self.assertEqual(
            container["secrets"][0]["valueFrom"],
            "arn:secret:ao-cloud/production/database-url",
        )
        environment = {
            item["name"]: item["value"] for item in container["environment"]
        }
        self.assertEqual(environment["AO_CLOUD_RELEASE"], "abc123")
        self.assertEqual(environment["AO_CLOUD_ENV"], "production")
        self.assertEqual(environment["AO_CLOUD_WORKER_BINARY_PATH"], "/ao-worker")
        self.assertEqual(environment["AO_CLOUD_WORKER_HELPER_BINARY_PATH"], "/ao")
        secrets = {
            item["name"]: item["valueFrom"] for item in container["secrets"]
        }
        self.assertEqual(
            secrets["AO_CLOUD_GITHUB_APP_ID"],
            "arn:secret:ao-cloud/production/github:app_id::",
        )
        self.assertNotIn("AO_CLOUD_NODEOPS_AUTO_PAUSE_MINUTES", environment)
        self.assertNotIn("AO_CLOUD_NODEOPS_AUTO_PAUSE_MINUTES", secrets)
        tags = {item["key"]: item["value"] for item in payload["tags"]}
        self.assertEqual(tags["WorkerImage"], WORKER_IMAGE)

    def test_rejects_staging_reference_in_production_template(self):
        source = task_source()
        source["taskDefinition"]["containerDefinitions"][0]["secrets"][0][
            "valueFrom"
        ] = "arn:secret:ao-cloud/staging/database-url"
        with self.assertRaisesRegex(ValueError, "staging reference"):
            build_task_definition(
                source,
                family="ao-cloud-production-api",
                container_name="control-plane",
                image=CONTROL_IMAGE,
                worker_image=WORKER_IMAGE,
                release="abc123",
                environment="production",
                log_group="/ao-cloud/production/control-plane",
                region="eu-north-1",
                secret_overrides=hosted_secret_overrides(),
            )

    def test_rejects_production_reference_in_staging_template(self):
        source = task_source("staging")
        source["taskDefinition"]["containerDefinitions"][0]["secrets"][0][
            "valueFrom"
        ] = "arn:secret:ao-cloud/production/database-url"
        with self.assertRaisesRegex(ValueError, "production reference"):
            build_task_definition(
                source,
                family="ao-cloud-staging-api",
                container_name="control-plane",
                image=CONTROL_IMAGE,
                worker_image=WORKER_IMAGE,
                release="abc123",
                environment="staging",
                log_group="/ao-cloud/staging/control-plane",
                region="eu-north-1",
                secret_overrides=hosted_secret_overrides("staging"),
            )

    def test_rejects_provider_auto_pause_override(self):
        with self.assertRaisesRegex(ValueError, "auto-pause"):
            build_task_definition(
                task_source(),
                family="ao-cloud-production-api",
                container_name="control-plane",
                image=CONTROL_IMAGE,
                worker_image=WORKER_IMAGE,
                release="abc123",
                environment="production",
                log_group="/ao-cloud/production/control-plane",
                region="eu-north-1",
                secret_overrides=hosted_secret_overrides()
                | {"AO_CLOUD_NODEOPS_AUTO_PAUSE_MINUTES": "arn:secret:field::"},
            )

    def test_validates_rendered_artifact_contract(self):
        payload = build_task_definition(
            task_source(),
            family="ao-cloud-production-api",
            container_name="control-plane",
            image=CONTROL_IMAGE,
            worker_image=WORKER_IMAGE,
            release="abc123",
            environment="production",
            log_group="/ao-cloud/production/control-plane",
            region="eu-north-1",
            secret_overrides=hosted_secret_overrides(),
        )
        validate_task_artifacts(
            {"taskDefinition": payload, "tags": payload["tags"]},
            container_name="control-plane",
            control_image=CONTROL_IMAGE,
            worker_image=WORKER_IMAGE,
        )


class HostedSettingsTests(unittest.TestCase):
    def test_accepts_complete_environment_scoped_settings(self):
        nodeops, worker = hosted_settings()
        validate_hosted_settings(nodeops, worker)

    def test_rejects_missing_worker_setting(self):
        nodeops, worker = hosted_settings()
        del worker["worker_heartbeat_timeout"]
        with self.assertRaisesRegex(ValueError, "worker_heartbeat_timeout"):
            validate_hosted_settings(nodeops, worker)

    def test_rejects_provider_auto_pause_setting(self):
        nodeops, worker = hosted_settings()
        nodeops["auto_pause_minutes"] = "30"
        with self.assertRaisesRegex(ValueError, "auto-pause"):
            validate_hosted_settings(nodeops, worker)

    def test_rejects_invalid_startup_timeout(self):
        nodeops, worker = hosted_settings()
        worker["sandbox_startup_timeout"] = "10s"
        with self.assertRaisesRegex(ValueError, "at least 30s"):
            validate_hosted_settings(nodeops, worker)


class ServiceValidationTests(unittest.TestCase):
    def test_accepts_stable_healthy_service(self):
        service, tasks, targets = healthy_service()
        validate_service(
            service=service,
            tasks=tasks,
            targets=targets,
            alarm_state="OK",
        )

    def test_rejects_empty_target_group(self):
        service, tasks, _ = healthy_service()
        with self.assertRaisesRegex(ValueError, "target count"):
            validate_service(
                service=service,
                tasks=tasks,
                targets=[],
                alarm_state="OK",
            )

    def test_rejects_mixed_task_revisions(self):
        service, tasks, targets = healthy_service()
        changed = copy.deepcopy(tasks)
        changed[1]["taskDefinitionArn"] = "arn:task-definition:production:6"
        with self.assertRaisesRegex(ValueError, "mixed"):
            validate_service(
                service=service,
                tasks=changed,
                targets=targets,
                alarm_state="OK",
            )

    def test_rejects_alarm_or_incomplete_rollout(self):
        service, tasks, targets = healthy_service()
        with self.assertRaisesRegex(ValueError, "alarm state"):
            validate_service(
                service=service,
                tasks=tasks,
                targets=targets,
                alarm_state="ALARM",
            )
        service["deployments"][0]["rolloutState"] = "IN_PROGRESS"
        with self.assertRaisesRegex(ValueError, "not complete"):
            validate_service(
                service=service,
                tasks=tasks,
                targets=targets,
                alarm_state="OK",
            )


if __name__ == "__main__":
    unittest.main()
