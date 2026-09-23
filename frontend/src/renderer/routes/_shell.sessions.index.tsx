import { createFileRoute } from "@tanstack/react-router";
import { SessionsBoard } from "../components/SessionsBoard";

export const Route = createFileRoute("/_shell/sessions/")({
	component: AllSessionsBoardRoute,
});

function AllSessionsBoardRoute() {
	return <SessionsBoard />;
}
