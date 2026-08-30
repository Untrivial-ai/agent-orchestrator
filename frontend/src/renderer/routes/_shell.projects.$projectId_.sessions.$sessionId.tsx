import { createFileRoute } from "@tanstack/react-router";
import { SessionView } from "../components/SessionView";
import { useCloudOrg } from "../hooks/useCloudOrg";

export const Route = createFileRoute("/_shell/projects/$projectId_/sessions/$sessionId")({
	component: ProjectSessionRoute,
});

function ProjectSessionRoute() {
	const { sessionId } = Route.useParams();
	const { org } = useCloudOrg();
	return <SessionView sessionId={sessionId} cloudOrgId={org?.id} />;
}
