import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parents[1]))

from lib.monitoring import alarm_definitions, dashboard_body


class MonitoringTests(unittest.TestCase):
    def test_builds_actionable_environment_alarms(self):
        alarms = alarm_definitions(
            "production",
            load_balancer_dimension="app/production/id",
            target_group_dimension="targetgroup/production/id",
            alert_topic_arn="arn:aws:sns:eu-north-1:123:alerts",
        )
        names = {alarm["AlarmName"] for alarm in alarms}
        self.assertEqual(len(alarms), 8)
        self.assertIn("ao-cloud-production-target-5xx", names)
        self.assertIn("ao-cloud-production-latency-p95", names)
        self.assertIn("ao-cloud-production-rds-free-storage", names)
        self.assertTrue(all(alarm["ActionsEnabled"] for alarm in alarms))
        self.assertTrue(
            all(
                alarm["AlarmActions"] == ["arn:aws:sns:eu-north-1:123:alerts"]
                for alarm in alarms
            )
        )

    def test_dashboard_contains_alb_ecs_and_rds_for_both_environments(self):
        dashboard = dashboard_body(
            "eu-north-1",
            {
                "staging": ("app/staging/id", "targetgroup/staging/id"),
                "production": ("app/production/id", "targetgroup/production/id"),
            },
        )
        self.assertEqual(len(dashboard["widgets"]), 6)
        titles = {
            widget["properties"]["title"] for widget in dashboard["widgets"]
        }
        self.assertEqual(
            titles,
            {
                "Staging ALB",
                "Staging ECS",
                "Staging PostgreSQL",
                "Production ALB",
                "Production ECS",
                "Production PostgreSQL",
            },
        )


if __name__ == "__main__":
    unittest.main()
