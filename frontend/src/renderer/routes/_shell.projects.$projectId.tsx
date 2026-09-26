import { createFileRoute } from "@tanstack/react-router";
import { SessionsBoard } from "../components/SessionsBoard";
import { preloadSessionUsageSummaries } from "../hooks/useSessionUsageSummaries";

export const Route = createFileRoute("/_shell/projects/$projectId")({
	loader: ({ context, params }) =>
		preloadSessionUsageSummaries(context.queryClient, params.projectId),
	component: ProjectBoardRoute,
});

function ProjectBoardRoute() {
	const { projectId } = Route.useParams();
	return <SessionsBoard projectId={projectId} />;
}
