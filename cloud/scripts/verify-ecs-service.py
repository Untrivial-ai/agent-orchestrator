#!/usr/bin/env python3

import argparse
import json
import os
import subprocess
import sys

from lib.deployment import validate_service, validate_task_artifacts


def aws(region: str, *arguments: str):
    command = ["aws", "--region", region]
    profile = os.environ.get("AWS_PROFILE", "").strip()
    if profile:
        command.extend(["--profile", profile])
    command.extend(arguments)
    result = subprocess.run(command, check=True, capture_output=True, text=True)
    return json.loads(result.stdout)


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--region", required=True)
    parser.add_argument("--cluster", required=True)
    parser.add_argument("--service", required=True)
    parser.add_argument("--alarm", required=True)
    parser.add_argument("--expected-task-definition")
    parser.add_argument("--expected-control-image")
    parser.add_argument("--expected-worker-image")
    args = parser.parse_args()
    if bool(args.expected_control_image) != bool(args.expected_worker_image):
        parser.error(
            "--expected-control-image and --expected-worker-image must be used together"
        )

    services = aws(
        args.region,
        "ecs",
        "describe-services",
        "--cluster",
        args.cluster,
        "--services",
        args.service,
    )
    if services.get("failures") or len(services.get("services", [])) != 1:
        raise SystemExit("ECS service was not found")
    service = services["services"][0]
    task_arns = aws(
        args.region,
        "ecs",
        "list-tasks",
        "--cluster",
        args.cluster,
        "--service-name",
        args.service,
        "--desired-status",
        "RUNNING",
    ).get("taskArns", [])
    tasks = []
    if task_arns:
        tasks = aws(
            args.region,
            "ecs",
            "describe-tasks",
            "--cluster",
            args.cluster,
            "--tasks",
            *task_arns,
        ).get("tasks", [])
    load_balancers = service.get("loadBalancers", [])
    if len(load_balancers) != 1:
        raise SystemExit("ECS service must have exactly one target group")
    targets = aws(
        args.region,
        "elbv2",
        "describe-target-health",
        "--target-group-arn",
        load_balancers[0]["targetGroupArn"],
    ).get("TargetHealthDescriptions", [])
    alarms = aws(
        args.region,
        "cloudwatch",
        "describe-alarms",
        "--alarm-names",
        args.alarm,
    ).get("MetricAlarms", [])
    if len(alarms) != 1:
        raise SystemExit("deployment alarm was not found")

    try:
        validate_service(
            service=service,
            tasks=tasks,
            targets=targets,
            alarm_state=alarms[0].get("StateValue", ""),
            expected_task_definition=args.expected_task_definition,
        )
        if args.expected_control_image:
            task_definition = aws(
                args.region,
                "ecs",
                "describe-task-definition",
                "--task-definition",
                args.expected_task_definition or service["taskDefinition"],
                "--include",
                "TAGS",
            )
            validate_task_artifacts(
                task_definition,
                container_name="control-plane",
                control_image=args.expected_control_image,
                worker_image=args.expected_worker_image,
            )
    except ValueError as error:
        raise SystemExit(str(error)) from error
    print(
        json.dumps(
            {
                "cluster": args.cluster,
                "service": args.service,
                "taskDefinition": service["taskDefinition"],
                "runningCount": service["runningCount"],
                "targets": len(targets),
                "alarm": args.alarm,
                "controlPlaneImage": args.expected_control_image,
                "workerImage": args.expected_worker_image,
            }
        )
    )


if __name__ == "__main__":
    main()
