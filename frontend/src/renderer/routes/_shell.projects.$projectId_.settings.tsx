import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useEffect } from "react";
import { useUiStore } from "../stores/ui-store";
import { useCloudProjectsQuery, useWorkspaceQuery } from "../hooks/useWorkspaceQuery";

export const Route = createFileRoute("/_shell/projects/$projectId_/settings")({
	component: ProjectSettingsRoute,
});

// Deep-link shim: project settings is a modal. In-app openers call
// openProjectSettings() directly; this route only handles cold loads.
function ProjectSettingsRoute() {
	const { projectId } = Route.useParams();
	const navigate = useNavigate();
	const openProjectSettings = useUiStore((state) => state.openProjectSettings);
	const workspaces = useWorkspaceQuery();
	const cloudProjects = useCloudProjectsQuery();
	const workspace = workspaces.data?.find((item) => item.id === projectId);

	useEffect(() => {
		if (!workspace) return;
		openProjectSettings(projectId, { cloudOrgId: workspace.cloudOrgId });
		void navigate({ to: "/projects/$projectId", params: { projectId }, replace: true });
	}, [navigate, openProjectSettings, projectId, workspace]);

	if (workspace) return null;
	const error = cloudProjects.error ?? workspaces.error;
	return <p role={error ? "alert" : "status"}>{error instanceof Error ? error.message : workspaces.isLoading || cloudProjects.isLoading ? "Loading project..." : "Project not found."}</p>;
}
