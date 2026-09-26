import { createFileRoute } from "@tanstack/react-router";
import { AutomationsView } from "../components/AutomationsView";

export const Route = createFileRoute("/_shell/automations")({ component: AutomationsView });
