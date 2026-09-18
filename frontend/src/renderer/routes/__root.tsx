import { createRootRouteWithContext, Outlet } from "@tanstack/react-router";
import { useEffect } from "react";
import { TooltipProvider } from "../components/ui/tooltip";
import type { QueryClient } from "@tanstack/react-query";
import { useKeybindingsStore } from "../stores/keybindings-store";

export const Route = createRootRouteWithContext<{
	queryClient: QueryClient;
}>()({
	component: RootComponent,
});

function RootComponent() {
	const loadKeybindings = useKeybindingsStore((state) => state.load);

	useEffect(() => {
		void loadKeybindings();
	}, [loadKeybindings]);

	return (
		<TooltipProvider>
			<Outlet />
		</TooltipProvider>
	);
}
