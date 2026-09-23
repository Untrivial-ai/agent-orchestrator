import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

function source(relativePath: string): string {
	return readFileSync(fileURLToPath(new URL(relativePath, import.meta.url)), "utf8");
}

/**
 * The controls that carry a role or a state, pinned at the source.
 *
 * These are the affordances a screen reader reads and a finger aims at. None of
 * them shows up in a screenshot, so nothing else in the suite would notice them
 * being dropped.
 */
describe("Interface affordances", () => {
	describe("shared controls announce what they are", () => {
		const ui = source("./ui.tsx");

		it("gives the four pressable primitives a button role", () => {
			// The app's primary controls live here, so a missing role is missing on
			// every screen at once. `IconButton` and `ListSectionHeader` already had
			// theirs; these were the gap.
			for (const name of ["Pill", "Button", "SettingsRow"]) {
				const index = ui.indexOf(`export function ${name}(`);
				expect(index, `${name} should exist`).toBeGreaterThan(-1);
			}
			expect(ui).toContain('accessibilityRole="button"');
			expect(ui.match(/accessibilityRole="button"/g)?.length ?? 0).toBeGreaterThanOrEqual(4);
		});

		it("pins the Button's name to its title so a spinner does not erase it", () => {
			expect(ui).toContain("accessibilityLabel={title}");
			expect(ui).toContain("accessibilityState={{ disabled: disabled || loading, busy: loading }}");
		});

		it("reports the selected filter pill", () => {
			expect(ui).toContain("accessibilityState={{ selected: active }}");
		});
	});

	describe("selection and disclosure carry state", () => {
		it("marks the active project on the project picker's rows", () => {
			const sheet = source("./ProjectPickerSheet.tsx");
			expect(sheet).toContain("accessibilityState={{ selected }}");
			expect(sheet).toContain('accessibilityRole="button"');
		});

		it("marks every collapsible row in the timeline as expandable", () => {
			const timeline = source("./chat/ChatTimeline.tsx");
			// A header that toggles without saying so leaves the control sounding like
			// static text. The activity rows had this from the start; the plan and
			// changed-file rows did not.
			expect(timeline).not.toMatch(/<Pressable style=\{styles\.planHeader\}/);
			expect(timeline).not.toMatch(/<Pressable disabled=\{!hasPatch\} onPress=/);
			const expanded = timeline.match(/accessibilityState=\{\{ expanded: open \}\}/g)?.length ?? 0;
			// The three plan headers, the changed-file header and the per-file rows.
			expect(expanded).toBeGreaterThanOrEqual(5);
			expect(timeline).toContain('accessibilityState={hasPatch ? { expanded: open } : undefined}');
		});
	});

	describe("the preview overlay's controls", () => {
		const screen = source("./session/TerminalSessionScreen.tsx");

		it("names both icon-only controls and gives them a real touch target", () => {
			expect(screen).toContain('accessibilityLabel="Reload preview"');
			expect(screen).toContain('accessibilityLabel="Close preview"');
			// The glyph box alone is about 23x19pt, so the padding is asymmetric:
			// 12pt outward, 4pt toward the neighbour, which is all the 8pt gap allows
			// before the two targets would overlap.
			expect(screen).toContain(
				"const PREVIEW_RELOAD_SLOP = { top: 12, bottom: 12, left: 12, right: 4 } as const;",
			);
			expect(screen).toContain(
				"const PREVIEW_CLOSE_SLOP = { top: 12, bottom: 12, left: 4, right: 12 } as const;",
			);
			expect(screen).not.toContain("hitSlop={8} onPress");
		});

		it("lets Android's back gesture close the overlay instead of the session", () => {
			expect(screen).toContain("BackHandler.addEventListener");
			expect(screen).toMatch(
				/if \(Platform\.OS !== "android" \|\| !browserOpen\) return;[\s\S]*?setBrowserOpen\(false\);\s*return true;/,
			);
		});
	});

	it("keeps a list that holds an input tappable while the keyboard is up", () => {
		// Android's default `never` spends the first tap on the keyboard, so a row
		// under a focused field takes two taps to choose.
		for (const file of ["./ModelPickerSheet.tsx", "./chat/ComposerPickerSheet.tsx"]) {
			const list = source(file);
			expect(list, `${file} holds an input inside its list`).toContain("TextInput");
			expect(list, `${file} should keep taps while the keyboard is up`).toContain(
				'keyboardShouldPersistTaps="handled"',
			);
		}
		// The board's search field sits outside its list, but the row that opens it
		// still has to survive a tap from behind the keyboard.
		expect(source("./worker-board-list.tsx")).toContain('keyboardShouldPersistTaps="handled"');
	});
});
