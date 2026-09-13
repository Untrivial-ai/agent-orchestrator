import { readdirSync, readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

// Route files are React Native modules, so vitest reads their source instead of
// importing them. What expo-router acts on is the `ErrorBoundary` export, plus a
// layout's `unstable_settings.screenErrorBoundary` or a navigator's
// `unstable_screenErrorBoundary` prop — all visible in the text.
const appDir = new URL("../app/", import.meta.url);
const source = (route: string) => readFileSync(new URL(route, appDir), "utf8");

// Routes that can render the first content of a launch. See RouteErrorBoundary
// for why a fallback there would stop a broken update rolling back.
const launchRoutes = ["_layout.tsx", "(tabs)/_layout.tsx", "(tabs)/index.tsx", "pair.tsx"];

const screenRoutes = ["onboarding.tsx", "session/[id].tsx", "shell/[handleId].tsx", "preview/[id].tsx"];

const sheetRoutes = [
	"sheets/agent.tsx",
	"sheets/chat-settings.tsx",
	"sheets/composer-picker.tsx",
	"sheets/connect.tsx",
	"sheets/conversation-map.tsx",
	"sheets/model.tsx",
	"sheets/project.tsx",
	"sheets/store-update.tsx",
	"sheets/theme.tsx",
];

// Not launch routes, so they want a boundary too. Held back only because the open
// mobile revamp (#5349) deletes or rewrites these files through their last lines;
// whichever lands second places them, and places the routes that PR adds.
const deferredRoutes = ["(tabs)/prs.tsx", "(tabs)/orchestrator.tsx", "(tabs)/settings.tsx", "notifications.tsx", "spawn.tsx"];

describe("route error boundaries", () => {
	it("places every route file on exactly one list", () => {
		const routes = readdirSync(appDir, { recursive: true, encoding: "utf8" })
			.filter((file) => file.endsWith(".tsx"))
			.sort();
		const placed = [...launchRoutes, ...screenRoutes, ...sheetRoutes, ...deferredRoutes];
		expect(new Set(placed).size).toBe(placed.length);
		expect(routes).toEqual([...placed].sort());
	});

	it.each(launchRoutes)("never puts one on %s, which a launch can render first", (route) => {
		// Wider than the export on purpose: a hand-written class boundary around the
		// layout would render first content just the same.
		expect(source(route)).not.toMatch(/Boundary|componentDidCatch|getDerivedStateFromError/);
	});

	it.each(screenRoutes)("installs the screen fallback on %s", (route) => {
		expect(source(route)).toMatch(/^export \{ RouteErrorBoundary as ErrorBoundary \} from "(\.\.\/)+lib\/RouteErrorBoundary";$/m);
	});

	// A sheet's opener parks a callback the route releases on unmount, so its
	// fallback closes instead of retrying in place.
	it.each(sheetRoutes)("installs the close-only fallback on %s", (route) => {
		expect(source(route)).toMatch(/^export \{ SheetErrorBoundary as ErrorBoundary \} from "(\.\.\/)+lib\/RouteErrorBoundary";$/m);
	});
});
