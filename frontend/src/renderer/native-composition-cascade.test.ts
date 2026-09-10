import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";

/**
 * The live browser page renders as a native WebContentsView *behind* the
 * transparent shell. Opening any overlay (the tabs-rail flyout, a menu) raises
 * the shell above that native view (see window-composition.ts's setOverlayOpen),
 * so every shell layer stacked over the page must be transparent for the
 * duration — otherwise the page blanks out for as long as the overlay is open.
 *
 * That contract lives entirely in CSS and cannot be exercised in jsdom, which
 * applies no stylesheet. It has silently regressed repeatedly, each time via an
 * ordinary-looking edit that added or moved an opaque ancestor. These tests pin
 * the contract at the source level: every element that wraps the browser panel
 * and paints its own background must be cleared by the native-composition
 * cascade.
 */

const css = readFileSync(resolve(process.cwd(), "src/renderer/styles.css"), "utf8");

const NATIVE_COMPOSITION = 'html[data-native-browser-composition="true"]';
const LIVE_PAGE = '.browser-panel[data-browser-native-page="live"]';

type Rule = { selector: string; body: string };

function rules(): Rule[] {
	// Flat top-level parse is enough: the native-composition cascade is written
	// as plain top-level rules, not nested inside media/layer blocks.
	return css
		.split("}")
		.map((chunk) => {
			const open = chunk.indexOf("{");
			if (open === -1) return null;
			return { selector: chunk.slice(0, open).trim(), body: chunk.slice(open + 1).trim() };
		})
		.filter((rule): rule is Rule => rule !== null);
}

function clearsBackgroundFor(match: (selector: string) => boolean): boolean {
	return rules().some(
		(rule) =>
			rule.selector.includes(NATIVE_COMPOSITION) &&
			/background:\s*transparent/.test(rule.body) &&
			rule.selector
				.split(",")
				.map((part) => part.trim())
				.some(match),
	);
}

describe("native-composition transparency cascade", () => {
	it("clears the docked inspector container that wraps the browser panel", () => {
		// SessionView.tsx's inspector container carries an opaque `bg-background`
		// utility and is an ancestor of the browser panel, so it paints over the
		// live page whenever the shell is raised for the tabs flyout.
		expect(
			clearsBackgroundFor(
				(selector) => selector.includes('[data-slot="inspector-container"]') && selector.includes(LIVE_PAGE),
			),
		).toBe(true);
	});

	it("clears the app shell root on every platform, not just Windows/Linux", () => {
		// _shell.tsx's shell wrapper carries an opaque `bg-sidebar`. It was only
		// ever reachable through the `.platform-windows` / `.platform-linux`
		// classes, which macOS never applies — so the shell blanked the live page
		// on macOS alone. Match it by its platform-independent class instead.
		expect(
			clearsBackgroundFor((selector) => selector.includes(".app-shell-root") && selector.includes(LIVE_PAGE)),
		).toBe(true);
	});

	it("clears the browser panel's own viewport", () => {
		expect(clearsBackgroundFor((selector) => selector.includes(".browser-panel__viewport"))).toBe(true);
	});

	it("clears the app shell root while the browser panel is popped out", () => {
		// The maximized panel is portaled straight to <body> (SessionView.tsx), so
		// it is NOT a descendant of `.app-shell-root` and the docked `:has(LIVE_PAGE)`
		// rule above can never match it. The popped-out block clears `#root`,
		// `.platform-windows`, `.platform-linux`, the center/session surfaces … but
		// omitted `.app-shell-root` — the same platform-independent hook the docked
		// rule needed, since macOS applies no `.platform-*` class. So on macOS the
		// shell wrapper kept painting its opaque `bg-sidebar` over the full window
		// and blanked the live page for as long as any overlay (e.g. the
		// device-preset dropdown) held the shell raised.
		expect(
			clearsBackgroundFor(
				(selector) => selector.includes(".app-shell-root") && selector.includes(".browser-popout-overlay"),
			),
		).toBe(true);
	});

	it("clears the popped-out overlay surface", () => {
		expect(clearsBackgroundFor((selector) => selector.endsWith(".browser-popout-overlay"))).toBe(true);
	});

	it("clears the popped-out frame that directly wraps the maximized browser panel", () => {
		// SessionView.tsx portals the maximized browser into a `.browser-popout-frame`
		// wrapper that paints an opaque `background: var(--bg)` plate. The popped-out
		// shell clears above target #root ancestors via `:has(.browser-popout-overlay)`
		// and never match this element, so it must be cleared explicitly — otherwise
		// raising the transparent shell for a tooltip/menu paints the frame's opaque
		// background over the native page and blanks it to black (Windows, maximized).
		expect(clearsBackgroundFor((selector) => selector.includes(".browser-popout-frame"))).toBe(true);
	});
});
