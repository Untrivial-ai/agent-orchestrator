import type { DashboardSession } from "./api";

export type SidebarDestinationId = "projects" | "agents" | "prs" | "settings";
export type PrimarySidebarDestinationId = Exclude<SidebarDestinationId, "settings">;

export type SidebarDestination = {
	id: SidebarDestinationId;
	label: string;
	icon: "folder" | "activity" | "git-pull-request" | "settings";
	href: "/projects" | "/" | "/prs" | "/settings";
};

export const RECENT_WORKERS_LABEL = "Recent Workers";

export const sidebarDestinations: readonly SidebarDestination[] = [
	{ id: "projects", label: "Projects", icon: "folder", href: "/projects" },
	{ id: "agents", label: "Workers", icon: "activity", href: "/" },
	{ id: "prs", label: "Pull Requests", icon: "git-pull-request", href: "/prs" },
	{ id: "settings", label: "Settings", icon: "settings", href: "/settings" },
];

export function sidebarSessions(sessions: readonly DashboardSession[]): DashboardSession[] {
	return sessions
		.filter((session) => !session.isTerminated && session.status !== "terminated")
		.sort((a, b) => {
			const pinnedOrder = Number(Boolean(b.isPinned)) - Number(Boolean(a.isPinned));
			if (pinnedOrder !== 0) return pinnedOrder;
			return b.lastActivityAt.localeCompare(a.lastActivityAt);
		});
}

export function activeSidebarDestination(pathname: string): SidebarDestinationId {
	const withoutGroup = pathname.replace(/^\/\(tabs\)/, "") || "/";
	return sidebarDestinations.find(({ href }) => href === withoutGroup)?.id ?? "agents";
}

export function selectedPrimarySidebarDestination(
	pathname: string,
	previous: PrimarySidebarDestinationId,
): PrimarySidebarDestinationId {
	const active = activeSidebarDestination(pathname);
	return active === "settings" ? previous : active;
}

function normalizedPath(pathname: string): string {
	const path = pathname.replace(/^\/\(tabs\)/, "") || "/";
	return path.length > 1 ? path.replace(/\/$/, "") : path;
}

export function sidebarNavigationSettled(pendingPath: string | null, pathname: string): boolean {
	return pendingPath !== null && normalizedPath(pendingPath) === normalizedPath(pathname);
}

type ScrollableSidebarRef = {
	scrollTo?: (options: { y: number; animated: boolean }) => void;
	scrollToLocation?: (options: {
		sectionIndex: number;
		itemIndex: number;
		viewOffset: number;
		animated: boolean;
	}) => void;
};

export function scrollSidebarRefToTop(ref: ScrollableSidebarRef | null | undefined) {
	if (ref?.scrollToLocation) {
		ref.scrollToLocation({ sectionIndex: 0, itemIndex: 0, viewOffset: 0, animated: true });
		return;
	}
	ref?.scrollTo?.({ y: 0, animated: true });
}
