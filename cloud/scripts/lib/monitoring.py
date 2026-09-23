#!/usr/bin/env python3

from __future__ import annotations

from typing import Any


def alarm_definitions(
    environment: str,
    *,
    load_balancer_dimension: str,
    target_group_dimension: str,
    alert_topic_arn: str = "",
) -> list[dict[str, Any]]:
    prefix = f"ao-cloud-{environment}"
    actions = [alert_topic_arn] if alert_topic_arn else []
    common = {
        "ActionsEnabled": bool(actions),
        "AlarmActions": actions,
        "OKActions": actions,
        "TreatMissingData": "notBreaching",
    }
    definitions = [
        {
            "AlarmName": f"{prefix}-target-5xx",
            "AlarmDescription": "Sustained AO Cloud target 5xx responses.",
            "Namespace": "AWS/ApplicationELB",
            "MetricName": "HTTPCode_Target_5XX_Count",
            "Dimensions": [
                {"Name": "LoadBalancer", "Value": load_balancer_dimension},
                {"Name": "TargetGroup", "Value": target_group_dimension},
            ],
            "Statistic": "Sum",
            "Period": 60,
            "EvaluationPeriods": 3,
            "DatapointsToAlarm": 2,
            "Threshold": 5,
            "ComparisonOperator": "GreaterThanOrEqualToThreshold",
        },
        {
            "AlarmName": f"{prefix}-unhealthy-targets",
            "AlarmDescription": "One or more AO Cloud targets are unhealthy.",
            "Namespace": "AWS/ApplicationELB",
            "MetricName": "UnHealthyHostCount",
            "Dimensions": [
                {"Name": "LoadBalancer", "Value": load_balancer_dimension},
                {"Name": "TargetGroup", "Value": target_group_dimension},
            ],
            "Statistic": "Maximum",
            "Period": 60,
            "EvaluationPeriods": 2,
            "DatapointsToAlarm": 2,
            "Threshold": 1,
            "ComparisonOperator": "GreaterThanOrEqualToThreshold",
        },
        {
            "AlarmName": f"{prefix}-latency-p95",
            "AlarmDescription": "AO Cloud p95 target latency exceeds two seconds.",
            "Namespace": "AWS/ApplicationELB",
            "MetricName": "TargetResponseTime",
            "Dimensions": [
                {"Name": "LoadBalancer", "Value": load_balancer_dimension},
                {"Name": "TargetGroup", "Value": target_group_dimension},
            ],
            "ExtendedStatistic": "p95",
            "Period": 60,
            "EvaluationPeriods": 5,
            "DatapointsToAlarm": 3,
            "Threshold": 2,
            "ComparisonOperator": "GreaterThanThreshold",
        },
    ]
    for metric in ("CPUUtilization", "MemoryUtilization"):
        definitions.append(
            {
                "AlarmName": f"{prefix}-ecs-{metric.removesuffix('Utilization').lower()}",
                "AlarmDescription": f"AO Cloud ECS {metric} is sustained above 80%.",
                "Namespace": "AWS/ECS",
                "MetricName": metric,
                "Dimensions": [
                    {"Name": "ClusterName", "Value": prefix},
                    {"Name": "ServiceName", "Value": f"{prefix}-api"},
                ],
                "Statistic": "Average",
                "Period": 60,
                "EvaluationPeriods": 5,
                "DatapointsToAlarm": 3,
                "Threshold": 80,
                "ComparisonOperator": "GreaterThanThreshold",
            }
        )
    definitions.extend(
        [
            {
                "AlarmName": f"{prefix}-rds-cpu",
                "AlarmDescription": "AO Cloud PostgreSQL CPU is sustained above 80%.",
                "Namespace": "AWS/RDS",
                "MetricName": "CPUUtilization",
                "Dimensions": [
                    {"Name": "DBInstanceIdentifier", "Value": f"{prefix}-storage"}
                ],
                "Statistic": "Average",
                "Period": 60,
                "EvaluationPeriods": 5,
                "DatapointsToAlarm": 3,
                "Threshold": 80,
                "ComparisonOperator": "GreaterThanThreshold",
            },
            {
                "AlarmName": f"{prefix}-rds-free-storage",
                "AlarmDescription": "AO Cloud PostgreSQL has less than 5 GiB free.",
                "Namespace": "AWS/RDS",
                "MetricName": "FreeStorageSpace",
                "Dimensions": [
                    {"Name": "DBInstanceIdentifier", "Value": f"{prefix}-storage"}
                ],
                "Statistic": "Minimum",
                "Period": 300,
                "EvaluationPeriods": 2,
                "DatapointsToAlarm": 2,
                "Threshold": 5 * 1024 * 1024 * 1024,
                "ComparisonOperator": "LessThanThreshold",
            },
            {
                "AlarmName": f"{prefix}-rds-free-memory",
                "AlarmDescription": "AO Cloud PostgreSQL has less than 256 MiB free memory.",
                "Namespace": "AWS/RDS",
                "MetricName": "FreeableMemory",
                "Dimensions": [
                    {"Name": "DBInstanceIdentifier", "Value": f"{prefix}-storage"}
                ],
                "Statistic": "Minimum",
                "Period": 300,
                "EvaluationPeriods": 2,
                "DatapointsToAlarm": 2,
                "Threshold": 256 * 1024 * 1024,
                "ComparisonOperator": "LessThanThreshold",
            },
        ]
    )
    return [{**definition, **common} for definition in definitions]


def dashboard_body(
    region: str,
    environments: dict[str, tuple[str, str]],
) -> dict[str, Any]:
    widgets = []
    for row, (environment, dimensions) in enumerate(environments.items()):
        load_balancer, target_group = dimensions
        prefix = f"ao-cloud-{environment}"
        widgets.extend(
            [
                metric_widget(
                    f"{environment.title()} ALB",
                    region,
                    0,
                    row * 6,
                    [
                        [
                            "AWS/ApplicationELB",
                            "RequestCount",
                            "LoadBalancer",
                            load_balancer,
                        ],
                        [
                            ".",
                            "HTTPCode_Target_5XX_Count",
                            ".",
                            ".",
                            "TargetGroup",
                            target_group,
                        ],
                        [".", "TargetResponseTime", ".", ".", ".", "."],
                    ],
                ),
                metric_widget(
                    f"{environment.title()} ECS",
                    region,
                    8,
                    row * 6,
                    [
                        [
                            "AWS/ECS",
                            "CPUUtilization",
                            "ClusterName",
                            prefix,
                            "ServiceName",
                            f"{prefix}-api",
                        ],
                        [".", "MemoryUtilization", ".", ".", ".", "."],
                    ],
                ),
                metric_widget(
                    f"{environment.title()} PostgreSQL",
                    region,
                    16,
                    row * 6,
                    [
                        [
                            "AWS/RDS",
                            "CPUUtilization",
                            "DBInstanceIdentifier",
                            f"{prefix}-storage",
                        ],
                        [".", "DatabaseConnections", ".", "."],
                        [".", "FreeStorageSpace", ".", "."],
                        [".", "FreeableMemory", ".", "."],
                    ],
                ),
            ]
        )
    return {"widgets": widgets}


def metric_widget(
    title: str,
    region: str,
    x: int,
    y: int,
    metrics: list[list[str]],
) -> dict[str, Any]:
    return {
        "type": "metric",
        "x": x,
        "y": y,
        "width": 8,
        "height": 6,
        "properties": {
            "title": title,
            "region": region,
            "view": "timeSeries",
            "stacked": False,
            "metrics": metrics,
            "period": 300,
        },
    }
