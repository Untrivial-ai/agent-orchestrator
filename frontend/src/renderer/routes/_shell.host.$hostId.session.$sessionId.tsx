import { createFileRoute } from "@tanstack/react-router";
import { SessionView } from "../components/SessionView";
import { refKey } from "../lib/hosts";

export const Route = createFileRoute("/_shell/host/$hostId/session/$sessionId")({
	component: HostSessionRoute,
});

function HostSessionRoute() {
	const { hostId, sessionId } = Route.useParams();
	return <SessionView key={refKey({ host: hostId, id: sessionId })} hostId={hostId} sessionId={sessionId} />;
}
