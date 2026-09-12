import { createFileRoute } from "@tanstack/react-router";
import { WorkflowBoard } from "../components/workflow/WorkflowBoard";
import { ProjectWorkspaceNav } from "../components/ProjectWorkspaceNav";

export const Route = createFileRoute("/_shell/projects/$projectId_/workflow")({
	component: WorkflowRoute,
});

function WorkflowRoute() {
	const { projectId } = Route.useParams();
	return (
		<div className="flex h-full flex-col">
			<ProjectWorkspaceNav active="workflow" projectId={projectId} />
			<div className="flex-1 overflow-auto">
				<WorkflowBoard projectId={projectId} />
			</div>
		</div>
	);
}
