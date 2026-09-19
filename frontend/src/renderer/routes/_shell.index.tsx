import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useEffect } from "react";
import { HomePage } from "../components/HomePage";
import { MigrationPopup } from "../components/MigrationPopup";
import { hasCompletedOnboarding } from "../lib/onboarding-finish";
import { useUiStore } from "../stores/ui-store";

export const Route = createFileRoute("/_shell/")({
	component: ShellIndex,
});

function ShellIndex() {
	const navigate = useNavigate();
	const settingsModal = useUiStore((state) => state.settingsModal);

	// First run goes to the flow. Nothing else about the home route belongs to
	// onboarding, so the rest of this file stays as main has it.
	useEffect(() => {
		if (!settingsModal && !hasCompletedOnboarding()) {
			void navigate({ to: "/onboarding", replace: true });
		}
	}, [navigate, settingsModal]);

	return (
		<>
			<MigrationPopup />
			<HomePage />
		</>
	);
}
