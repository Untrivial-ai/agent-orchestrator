import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useEffect } from "react";
import { HomePage } from "../components/HomePage";
import { MigrationPopup } from "../components/MigrationPopup";
import { useWorkspaceQuery } from "../hooks/useWorkspaceQuery";
import { hasCompletedOnboarding, markOnboardingComplete } from "../lib/onboarding-finish";
import { usesPreviewWorkspaceData } from "../lib/preview-mode";
import { useUiStore } from "../stores/ui-store";

export const Route = createFileRoute("/_shell/")({
	component: ShellIndex,
});

function ShellIndex() {
	const navigate = useNavigate();
	const workspaceQuery = useWorkspaceQuery();
	const settingsModal = useUiStore((state) => state.settingsModal);

	// First run goes to the flow. Nothing else about the home route belongs to
	// onboarding, so the rest of this file stays as main has it.
	useEffect(() => {
		// Preview mode serves mock data with no real machine to set up, and the
		// renderer smoke suite runs this way, so it goes straight to the shell.
		if (usesPreviewWorkspaceData) return;
		if (settingsModal || hasCompletedOnboarding()) return;
		// The flag did not exist before this release, so an upgraded profile has
		// projects but no flag. Wait for the list: sending someone back through
		// setup because their profile predates the flag is not a first run.
		if (!workspaceQuery.isSuccess) return;
		const workspaces = workspaceQuery.data ?? [];
		if (workspaces.some((workspace) => workspace.kind !== "scratch")) {
			markOnboardingComplete();
			return;
		}
		void navigate({ to: "/onboarding", replace: true });
	}, [navigate, settingsModal, workspaceQuery.data, workspaceQuery.isSuccess]);

	return (
		<>
			<MigrationPopup />
			<HomePage />
		</>
	);
}
