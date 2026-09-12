import { createFileRoute } from "@tanstack/react-router";
import { PlanDetailView } from "../components/workflow/PlanDetailView";
import { ProjectWorkspaceNav } from "../components/ProjectWorkspaceNav";

export const Route = createFileRoute("/_shell/projects/$projectId_/workflow/$planId")({
	component: PlanDetailRoute,
});

function PlanDetailRoute() {
	const { projectId, planId } = Route.useParams();
	return (
		<div className="flex h-full flex-col">
			<ProjectWorkspaceNav active="workflow" projectId={projectId} />
			<div className="flex-1 overflow-auto">
				<PlanDetailView projectId={projectId} planId={planId} />
			</div>
		</div>
	);
}
