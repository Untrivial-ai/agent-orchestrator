import { createFileRoute } from "@tanstack/react-router";
import { SessionsBoard } from "../components/SessionsBoard";
import { ProjectWorkspaceNav } from "../components/ProjectWorkspaceNav";

export const Route = createFileRoute("/_shell/projects/$projectId")({
	component: ProjectBoardRoute,
});

function ProjectBoardRoute() {
	const { projectId } = Route.useParams();
	return (
		<div className="flex h-full flex-col">
			<ProjectWorkspaceNav active="sessions" projectId={projectId} />
			<div className="flex-1 overflow-hidden">
				<SessionsBoard projectId={projectId} />
			</div>
		</div>
	);
}
