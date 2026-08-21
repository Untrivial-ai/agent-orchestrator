import { createFileRoute } from "@tanstack/react-router";
import { PlanUsagePage } from "../components/usage/PlanUsagePage";

export const Route = createFileRoute("/_shell/usage")({ component: PlanUsagePage });
