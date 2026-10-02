import { createFileRoute } from "@tanstack/react-router";
import { SessionView } from "../components/SessionView";
import { useCloudOrg } from "../hooks/useCloudOrg";

export const Route = createFileRoute("/_shell/projects/$projectId_/sessions/$sessionId")({
	component: ProjectSessionRoute,
});

function ProjectSessionRoute() {
	const { projectId, sessionId } = Route.useParams();
	const { org } = useCloudOrg();
	return <SessionView sessionId={sessionId} projectId={projectId} cloudOrgId={org?.id} />;
}
