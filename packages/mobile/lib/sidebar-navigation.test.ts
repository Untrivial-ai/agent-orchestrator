import { describe, expect, it, vi } from "vitest";

import type { DashboardSession } from "./api";
import {
	activeSidebarDestination,
	RECENT_WORKERS_LABEL,
	scrollSidebarRefToTop,
	selectedPrimarySidebarDestination,
	sidebarNavigationSettled,
	sidebarDestinations,
	sidebarSessions,
} from "./sidebar-navigation";

vi.mock("@expo/ui/swift-ui/modifiers", () => ({
	contentShape: (shape: { shape: string }) => ({ $type: "contentShape", ...shape, kind: undefined }),
	shapes: { rectangle: () => ({ shape: "rectangle" }) },
}));

function session(overrides: Partial<DashboardSession> & Pick<DashboardSession, "id">): DashboardSession {
	return {
		projectId: "project-a",
		status: "working",
		mode: "chat",
		branch: null,
		issueId: null,
		issueTitle: null,
		userPrompt: null,
		displayName: null,
		summary: null,
		createdAt: "2026-08-01T00:00:00Z",
		lastActivityAt: "2026-08-01T00:00:00Z",
		...overrides,
		id: overrides.id,
	};
}

describe("sidebar navigation", () => {
	it("makes the complete native destination row interactive on iOS", async () => {
		(globalThis as typeof globalThis & { __DEV__?: boolean }).__DEV__ = false;
		const { sidebarDestinationHitModifiers } = await import("./sidebar-destination-hit-modifiers.ios");
		expect(sidebarDestinationHitModifiers).toContainEqual({
			$type: "contentShape",
			shape: "rectangle",
			kind: undefined,
		});
	});

	it("exposes one projects destination backed by the project inbox", () => {
		expect(sidebarDestinations).toEqual([
			{ id: "projects", label: "Projects", icon: "folder", href: "/projects" },
			{ id: "agents", label: "Workers", icon: "activity", href: "/" },
			{ id: "prs", label: "Pull Requests", icon: "git-pull-request", href: "/prs" },
			{ id: "settings", label: "Settings", icon: "settings", href: "/settings" },
		]);
	});

	it("names the sidebar session history Recent Workers", () => {
		expect(RECENT_WORKERS_LABEL).toBe("Recent Workers");
	});

	it("lists live sessions across projects with pinned sessions first, then newest activity", () => {
		const sessions = [
			session({ id: "older", projectId: "project-a", lastActivityAt: "2026-08-02T00:00:00Z" }),
			session({ id: "newest", projectId: "project-b", lastActivityAt: "2026-08-05T00:00:00Z" }),
			session({ id: "pinned-newer", isPinned: true, lastActivityAt: "2026-08-04T00:00:00Z" }),
			session({ id: "pinned-older", isPinned: true, lastActivityAt: "2026-08-03T00:00:00Z" }),
			session({ id: "terminated-flag", isPinned: true, isTerminated: true, lastActivityAt: "2026-08-06T00:00:00Z" }),
			session({ id: "terminated-status", status: "terminated", lastActivityAt: "2026-08-07T00:00:00Z" }),
		];

		expect(sidebarSessions(sessions).map(({ id }) => id)).toEqual([
			"pinned-newer",
			"pinned-older",
			"newest",
			"older",
		]);
	});

	it.each([
		["/", "agents"],
		["/projects", "projects"],
		["/(tabs)/prs", "prs"],
		["/settings", "settings"],
	])("selects the matching destination for %s", (pathname, expected) => {
		expect(activeSidebarDestination(pathname)).toBe(expected);
	});

	it("keeps the previous primary destination selected while settings is presented", () => {
		expect(selectedPrimarySidebarDestination("/settings", "projects")).toBe("projects");
		expect(selectedPrimarySidebarDestination("/prs", "projects")).toBe("prs");
	});

	it("only closes the Android drawer after the requested route is committed", () => {
		expect(sidebarNavigationSettled("/projects", "/")).toBe(false);
		expect(sidebarNavigationSettled("/projects", "/(tabs)/projects")).toBe(true);
		expect(sidebarNavigationSettled("/session/worker-7", "/session/worker-7")).toBe(true);
		expect(sidebarNavigationSettled(null, "/projects")).toBe(false);
	});

	it("scrolls a standard scroll view to the top when its active item is reselected", () => {
		const calls: unknown[] = [];
		scrollSidebarRefToTop({ scrollTo: (options: unknown) => calls.push(options) });
		expect(calls).toEqual([{ y: 0, animated: true }]);
	});

	it("scrolls a section list to its first item when its active item is reselected", () => {
		const calls: unknown[] = [];
		scrollSidebarRefToTop({ scrollToLocation: (options: unknown) => calls.push(options) });
		expect(calls).toEqual([{ sectionIndex: 0, itemIndex: 0, viewOffset: 0, animated: true }]);
	});
});
