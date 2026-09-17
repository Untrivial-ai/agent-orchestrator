import { createFileRoute } from "@tanstack/react-router";
import { HomePage } from "../components/HomePage";
import { MigrationPopup } from "../components/MigrationPopup";

export const Route = createFileRoute("/_shell/")({
	component: ShellIndex,
});

function ShellIndex() {
	return (
		<>
			<MigrationPopup />
			<HomePage />
		</>
	);
}
