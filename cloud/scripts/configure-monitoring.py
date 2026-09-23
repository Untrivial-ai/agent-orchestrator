#!/usr/bin/env python3

import argparse
import json
import os
import subprocess

from lib.monitoring import alarm_definitions, dashboard_body


def aws(region: str, *arguments: str, output=True):
    command = ["aws", "--region", region]
    profile = os.environ.get("AWS_PROFILE", "").strip()
    if profile:
        command.extend(["--profile", profile])
    command.extend(arguments)
    result = subprocess.run(command, check=True, capture_output=True, text=True)
    return json.loads(result.stdout) if output and result.stdout.strip() else None


def dimensions(region: str, environment: str) -> tuple[str, str]:
    service = aws(
        region,
        "ecs",
        "describe-services",
        "--cluster",
        f"ao-cloud-{environment}",
        "--services",
        f"ao-cloud-{environment}-api",
    )["services"][0]
    target_group_arn = service["loadBalancers"][0]["targetGroupArn"]
    target_group = aws(
        region,
        "elbv2",
        "describe-target-groups",
        "--target-group-arns",
        target_group_arn,
    )["TargetGroups"][0]
    load_balancer_arn = target_group["LoadBalancerArns"][0]
    return (
        load_balancer_arn.split("loadbalancer/", 1)[1],
        target_group_arn.split(":", 5)[5],
    )


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--region", default=os.environ.get("AWS_REGION", "eu-north-1"))
    parser.add_argument("--dry-run", action="store_true")
    args = parser.parse_args()
    topic = os.environ.get("AO_CLOUD_ALERT_TOPIC_ARN", "").strip()
    environment_dimensions = {
        environment: dimensions(args.region, environment)
        for environment in ("staging", "production")
    }
    operations = {
        "logRetention": {
            "/ao-cloud/staging/control-plane": 30,
            "/ao-cloud/production/control-plane": 90,
        },
        "alarms": [
            alarm
            for environment, (load_balancer, target_group) in environment_dimensions.items()
            for alarm in alarm_definitions(
                environment,
                load_balancer_dimension=load_balancer,
                target_group_dimension=target_group,
                alert_topic_arn=topic,
            )
        ],
        "dashboard": dashboard_body(args.region, environment_dimensions),
    }
    if args.dry_run:
        print(json.dumps(operations, indent=2, sort_keys=True))
        return

    for log_group, retention in operations["logRetention"].items():
        aws(
            args.region,
            "logs",
            "put-retention-policy",
            "--log-group-name",
            log_group,
            "--retention-in-days",
            str(retention),
            output=False,
        )
    for alarm in operations["alarms"]:
        aws(
            args.region,
            "cloudwatch",
            "put-metric-alarm",
            "--cli-input-json",
            json.dumps(alarm, separators=(",", ":")),
            output=False,
        )
    aws(
        args.region,
        "cloudwatch",
        "put-dashboard",
        "--dashboard-name",
        "ao-cloud",
        "--dashboard-body",
        json.dumps(operations["dashboard"], separators=(",", ":")),
        output=False,
    )
    print(
        f"Configured {len(operations['alarms'])} alarms and the ao-cloud dashboard."
    )
    if not topic:
        print(
            "AO_CLOUD_ALERT_TOPIC_ARN is unset; alarms are visible but have no notification actions."
        )


if __name__ == "__main__":
    main()
