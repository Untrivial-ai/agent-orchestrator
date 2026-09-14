import { createFileRoute } from "@tanstack/react-router";
import { AgentRolePage } from "../components/workflow/AgentRolePage";

export const Route = createFileRoute("/_shell/projects/$projectId_/workflow/roles")({
	component: AgentRoleRoute,
});

function AgentRoleRoute() {
	const { projectId } = Route.useParams();
	return <AgentRolePage projectId={projectId} />;
}
