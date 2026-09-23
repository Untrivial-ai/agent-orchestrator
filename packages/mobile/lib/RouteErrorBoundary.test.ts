import { readdirSync, readFileSync } from "node:fs";
import { sep } from "node:path";
import { describe, expect, it } from "vitest";

// Route files are React Native modules, so vitest reads their source instead of
// importing them. What expo-router acts on is the `ErrorBoundary` export, plus a
// layout's `unstable_settings.screenErrorBoundary` or a navigator's
// `unstable_screenErrorBoundary` prop — all visible in the text.
const appDir = new URL("../app/", import.meta.url);
const source = (route: string) => readFileSync(new URL(route, appDir), "utf8");

// An exported fallback here could not render: see RouteErrorBoundary.
const rootLayout = "_layout.tsx";

const screenRoutes = [
	"(tabs)/index.tsx",
	"(tabs)/projects.tsx",
	"(tabs)/prs.tsx",
	"notifications.tsx",
	"onboarding.tsx",
	"pair.tsx",
	"preview/[id].tsx",
	"project/[id].tsx",
	"review/[sessionId].tsx",
	"reviewer/[reviewId].tsx",
	"session/[id].tsx",
	"settings.tsx",
	"shell/[handleId].tsx",
];

const sheetRoutes = [
	"spawn.tsx",
	"sheets/agent.tsx",
	"sheets/chat-settings.tsx",
	"sheets/composer-picker.tsx",
	"sheets/connect.tsx",
	"sheets/conversation-actions.tsx",
	"sheets/conversation-map.tsx",
	"sheets/conversation-rename.tsx",
	"sheets/model.tsx",
	"sheets/project.tsx",
	"sheets/review-actions.tsx",
	"sheets/store-update.tsx",
];

// The tab navigator alone: it renders no screen of its own, and a fallback there
// would swallow the failure of whichever tab is mounted inside it.
const deferredRoutes = ["(tabs)/_layout.tsx"];

describe("route error boundaries", () => {
	it("places every route file on exactly one list", () => {
		// expo-router takes any .js/.jsx/.ts/.tsx under app/ as a route, bar the
		// `+api`, `+html`, `+middleware` and `+native-intent` files, of which this app
		// has none. The recursive listing joins with the platform separator.
		const routes = readdirSync(appDir, { recursive: true, encoding: "utf8" })
			.filter((file) => /\.[jt]sx?$/.test(file))
			.map((file) => file.split(sep).join("/"))
			.sort();
		const placed = [rootLayout, ...screenRoutes, ...sheetRoutes, ...deferredRoutes];
		expect(new Set(placed).size).toBe(placed.length);
		expect(routes).toEqual([...placed].sort());
	});

	it("keeps any boundary out of the root layout", () => {
		// Wider than the export on purpose: a `screenErrorBoundary` setting there
		// would give every child route a fallback (the deferred ones included) without
		// appearing on these lists, and a hand-written class boundary would too.
		expect(source(rootLayout)).not.toMatch(/Boundary|componentDidCatch|getDerivedStateFromError/);
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
