import { createFileRoute } from "@tanstack/react-router";
import { SessionsBoard } from "../components/SessionsBoard";
import { preloadSessionUsageSummaries } from "../hooks/useSessionUsageSummaries";

export const Route = createFileRoute("/_shell/sessions/")({
	loader: ({ context }) => preloadSessionUsageSummaries(context.queryClient),
	component: AllSessionsBoardRoute,
});

function AllSessionsBoardRoute() {
	return <SessionsBoard />;
}
