import { createFileRoute } from "@tanstack/react-router";
import { StandaloneArchiveView } from "../components/StandaloneArchiveView";

export const Route = createFileRoute("/_shell/sessions/")({
	component: StandaloneArchiveRoute,
});

function StandaloneArchiveRoute() {
	return <StandaloneArchiveView />;
}
