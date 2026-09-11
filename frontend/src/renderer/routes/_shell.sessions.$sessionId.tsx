import { createFileRoute } from "@tanstack/react-router";
import { SessionView } from "../components/SessionView";
import { useCloudOrg } from "../hooks/useCloudOrg";

export const Route = createFileRoute("/_shell/sessions/$sessionId")({
	component: SessionRoute,
});

function SessionRoute() {
	const { sessionId } = Route.useParams();
	const { org } = useCloudOrg();
	return <SessionView sessionId={sessionId} cloudOrgId={org?.id} />;
}
