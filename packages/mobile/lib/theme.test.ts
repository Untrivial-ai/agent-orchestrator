import { describe, expect, it } from "vitest";
import {
	attentionMetaFor,
	darkTheme,
	lightTheme,
	statusVisual,
	terminalTheme,
	themeFor,
	type Theme,
} from "./theme";

describe("palette parity", () => {
	// A token present in one palette and missing from the other is an `undefined`
	// colour at runtime, which React Native silently renders as transparent —
	// invisible text rather than a crash. This is the test that catches it.
	it("defines exactly the same tokens in both themes", () => {
		expect(Object.keys(lightTheme).sort()).toEqual(Object.keys(darkTheme).sort());
	});

	it("has no empty or undefined values", () => {
		for (const [name, palette] of [
			["dark", darkTheme],
			["light", lightTheme],
		] as const) {
			for (const [token, value] of Object.entries(palette)) {
				expect(value, `${name}.${token}`).toBeTruthy();
			}
		}
	});

	// A half-finished light palette is the likely failure mode: colours copied
	// across unchanged look washed out on white, and nothing else would flag it.
	it("darkens every semantic colour for light mode rather than reusing it", () => {
		const semantic = ["accent", "orange", "amber", "red", "green"] as const;
		for (const token of semantic) {
			expect(lightTheme[token], token).not.toBe(darkTheme[token]);
		}
	});

	it("inverts the surface ramp", () => {
		expect(lightTheme.bgBase).not.toBe(darkTheme.bgBase);
		expect(lightTheme.textPrimary).not.toBe(darkTheme.textPrimary);
	});

	// The accent flips polarity between themes — near-white ink on dark, near-black
	// on light — so the ink has to flip with it.
	it("flips the accent ink between themes", () => {
		expect(darkTheme.onAccent).toBe("#0b0c0e");
		expect(lightTheme.onAccent).toBe("#ffffff");
	});

	// Every hue in the palette means a state, and the interactive accent carries
	// emphasis with contrast instead. Blue stays out — it meant nothing specific
	// here, and the pin was carrying a stray one. Purple is in, meaning exactly
	// one thing: a pull request that landed.
	it("keeps blue out of the palette and purple to a single meaning", () => {
		for (const t of [darkTheme, lightTheme]) {
			for (const gone of ["blue", "tintBlue", "attention"]) {
				expect(t, gone).not.toHaveProperty(gone);
			}
			expect(t.purple, "purple").toBeTruthy();
			// Not the accent, not the success hue: a merged PR must not read as
			// either "selected" or "mergeable".
			expect(t.purple).not.toBe(t.accent);
			expect(t.purple).not.toBe(t.green);
			expect(t.accent).not.toBe(t.orange);
			expect(t.accentBorder).toBeTruthy();
		}
	});

	it("marks a merged session with the purple, not the muted grey it used to share", () => {
		for (const t of [darkTheme, lightTheme]) {
			expect(statusVisual(t, "merged").color).toBe(t.purple);
			expect(statusVisual(t, "merged").color).not.toBe(t.textSecondary);
		}
	});
});

describe("themeFor", () => {
	it("maps a scheme to its palette, defaulting to dark", () => {
		expect(themeFor("light")).toBe(lightTheme);
		expect(themeFor("dark")).toBe(darkTheme);
	});
});

describe("terminalTheme", () => {
	it("defines the same slots for both schemes", () => {
		expect(Object.keys(terminalTheme("light")).sort()).toEqual(Object.keys(terminalTheme("dark")).sort());
	});

	// The terminal follows the theme, so a dark-tuned ANSI palette on a light
	// background would be unreadable — every slot has to differ.
	it("gives light mode its own ANSI ramp", () => {
		const dark = terminalTheme("dark");
		const light = terminalTheme("light");
		for (const slot of Object.keys(dark) as (keyof typeof dark)[]) {
			expect(light[slot], slot).not.toBe(dark[slot]);
		}
	});

	// Deliberately independent of the product palette: a red from a test runner
	// must not be redefined by our "failing" red.
	it("does not reuse the product semantic colours", () => {
		expect(terminalTheme("dark").red).not.toBe(darkTheme.red);
		expect(terminalTheme("dark").green).not.toBe(darkTheme.green);
	});

	// A TUI that fills a row with "black" must draw an invisible band, not a bar
	// across the canvas. Agent TUIs only ever use this slot as a fill — as a
	// foreground it is unreadable on their own dark background.
	it("collapses the black slot into the background so filled rows stay flat", () => {
		for (const scheme of ["light", "dark"] as const) {
			const term = terminalTheme(scheme);
			expect(term.black, scheme).toBe(term.background);
		}
	});

	// brightBlack is the dim-text slot and must stay readable — collapsing it too
	// would erase the "Cooked for 2s" and token-budget lines.
	it("keeps the dim-text slot distinct from the background", () => {
		for (const scheme of ["light", "dark"] as const) {
			const term = terminalTheme(scheme);
			expect(term.brightBlack, scheme).not.toBe(term.background);
		}
	});
});

describe("theme-aware helpers", () => {
	const both: [string, Theme][] = [
		["dark", darkTheme],
		["light", lightTheme],
	];

	it("resolves status colours from the passed theme", () => {
		for (const [, t] of both) {
			expect(statusVisual(t, "working").color).toBe(t.orange);
			expect(statusVisual(t, "stuck").color).toBe(t.red);
			expect(statusVisual(t, "mergeable").color).toBe(t.green);
		}
		// And the two themes genuinely disagree, i.e. the argument is used.
		expect(statusVisual(lightTheme, "working").color).not.toBe(statusVisual(darkTheme, "working").color);
	});

	it("keeps labels identical across themes", () => {
		expect(statusVisual(lightTheme, "needs_input").label).toBe(statusVisual(darkTheme, "needs_input").label);
	});

	it("falls back to the raw status for an unrecognised value", () => {
		expect(statusVisual(darkTheme, "something_new").label).toBe("something_new");
		expect(statusVisual(darkTheme, null).label).toBe("unknown");
	});

	// Regression: these four had no case, so the board rendered the wire value
	// verbatim — a card literally read "no_signal".
	it("labels every status the daemon actually sends", () => {
		expect(statusVisual(darkTheme, "no_signal").label).toBe("No signal");
		expect(statusVisual(darkTheme, "exited").label).toBe("Exited");
		expect(statusVisual(darkTheme, "draft").label).toBe("Draft PR");
		expect(statusVisual(darkTheme, "unknown").label).toBe("Unknown");
	});

	// A label that still contains an underscore is a wire value that leaked.
	it("never renders a raw enum for a known status", () => {
		const known = [
			"working", "idle", "needs_input", "exited", "no_signal", "ci_failed",
			"changes_requested", "review_pending", "draft", "pr_open", "approved",
			"mergeable", "merged", "terminated", "unknown", "spawning", "detecting",
			"stuck", "errored", "done", "cleanup", "killed",
		];
		for (const s of known) {
			expect(statusVisual(darkTheme, s).label, s).not.toContain("_");
		}
	});

	it("builds attention metadata from the passed theme", () => {
		for (const [, t] of both) {
			const meta = attentionMetaFor(t);
			expect(meta.merge.color).toBe(t.green);
			expect(meta.working.color).toBe(t.orange);
		}
	});

	it("keeps attention ordering stable across themes", () => {
		const order = (t: Theme) =>
			Object.entries(attentionMetaFor(t))
				.sort((a, b) => a[1].order - b[1].order)
				.map(([k]) => k);
		expect(order(lightTheme)).toEqual(order(darkTheme));
	});
});
