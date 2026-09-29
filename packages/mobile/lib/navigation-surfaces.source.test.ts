import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

function source(relativePath: string): string {
	return readFileSync(fileURLToPath(new URL(relativePath, import.meta.url)), "utf8");
}

const layout = source("../app/_layout.tsx");
const session = source("./chat/ChatSessionScreen.tsx");

describe("navigation surfaces", () => {
	// The navigator paints two backgrounds of its own, both visible only while a
	// page moves: the stack container (the seam between the outgoing and incoming
	// screens) and the content view under a screen. Its defaults are white, which
	// is what put a white edge around every transition.
	it("paints the navigator's own surfaces from the palette", () => {
		expect(layout).toContain("NavigationThemeProvider");
		expect(layout).toContain("background: t.bgBase");
		expect(layout).toContain("card: t.bgSurface");
	});

	it("pushes sub-pages with the platform slide", () => {
		expect(layout).toContain('animation: "slide_from_right"');
	});

	// The session used to render a spinner until the push animation finished, so
	// the page assembled itself after it had already landed.
	it("renders a session's content while it is still sliding in", () => {
		expect(session).not.toContain("Preparing conversation");
		expect(session).not.toContain("deferRouteContent");
	});

	// The title is native header state, so it is set on the first commit. Deferring
	// it with the trailing control made iOS animate the header in from the top.
	it("sets the session header title without waiting for the control swap", () => {
		const titleEffect = session.indexOf("headerTitle: () => (");
		const gatedControl = session.indexOf("headerRightReady\n\t\t\t\t? glassHeaderControl");
		expect(titleEffect).toBeGreaterThan(-1);
		expect(gatedControl).toBeGreaterThan(-1);
		expect(gatedControl).toBeGreaterThan(titleEffect);
	});
});
