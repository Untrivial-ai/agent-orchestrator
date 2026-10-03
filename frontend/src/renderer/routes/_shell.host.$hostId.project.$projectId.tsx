import { createFileRoute } from "@tanstack/react-router";
import { SessionsBoard } from "../components/SessionsBoard";
import { refKey } from "../lib/hosts";

export const Route = createFileRoute("/_shell/host/$hostId/project/$projectId")({
	component: HostProjectBoardRoute,
});

function HostProjectBoardRoute() {
	const { hostId, projectId } = Route.useParams();
	return <SessionsBoard key={refKey({ host: hostId, id: projectId })} hostId={hostId} projectId={projectId} />;
}
