import { createFileRoute } from "@tanstack/react-router";
import { SessionView } from "../components/SessionView";
import { refKey } from "../lib/hosts";

export const Route = createFileRoute("/_shell/host/$hostId/project/$projectId_/session/$sessionId")({
	component: HostProjectSessionRoute,
});

function HostProjectSessionRoute() {
	const { hostId, projectId, sessionId } = Route.useParams();
	return <SessionView key={refKey({ host: hostId, id: sessionId })} hostId={hostId} projectId={projectId} sessionId={sessionId} />;
}
