import { createContext, useContext } from "react";
import type { SidebarDestinationId } from "./sidebar-navigation";

/**
 * The sidebar's own context, kept in a module of its own.
 *
 * Both shells used to declare it, and `ui.tsx` imported the hook from one of
 * them — so `ui` depended on the shell that depends on `ui`, which Metro reports
 * as a require cycle. The context belongs to neither shell, and living here is
 * what lets both share one provider.
 */

export type SidebarScrollRequest = {
	destination: SidebarDestinationId;
	sequence: number;
};

export type SidebarNavigationContextValue = {
	openSidebar: () => void;
	scrollRequest: SidebarScrollRequest | null;
};

export const SidebarNavigationContext = createContext<SidebarNavigationContextValue | null>(null);

export function useSidebarNavigation(): SidebarNavigationContextValue {
	const context = useContext(SidebarNavigationContext);
	if (!context) throw new Error("useSidebarNavigation must be used within <SidebarNavigationShell>");
	return context;
}

export function useOptionalSidebarNavigation(): SidebarNavigationContextValue | null {
	return useContext(SidebarNavigationContext);
}
