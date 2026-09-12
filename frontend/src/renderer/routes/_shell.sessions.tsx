import { createFileRoute, Outlet } from "@tanstack/react-router";

export const Route = createFileRoute("/_shell/sessions")({
	component: SessionsRoute,
});

function SessionsRoute() {
	return <Outlet />;
}
