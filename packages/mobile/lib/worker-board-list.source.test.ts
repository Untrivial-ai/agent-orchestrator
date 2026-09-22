import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

function source(relativePath: string): string {
	return readFileSync(fileURLToPath(new URL(relativePath, import.meta.url)), "utf8");
}

const list = source("./worker-board-list.tsx");
const board = source("../app/(tabs)/index.tsx");

describe("worker board list identity", () => {
	// Swapping the project filter in place left the board showing two lists at
	// once: recycled cells of the old grouping under the new data, and the empty
	// state drawn on top of rows that were still on screen. A different set of
	// workers is a different list, so it is mounted as one.
	it("remounts the list when the set of workers changes", () => {
		expect(list).toContain("key={identityKey}");
		expect(board).toContain("identityKey={`${workerProjectId}|${query.trim()}`}");
	});
});

describe("worker dock keyboard lift", () => {
	// The library's animated `height` is the keyboard's frame origin, which is
	// negative while the keyboard is up — its own avoiding view negates it before
	// use. Feeding it to a `max(…, 0)` gave a lift of exactly zero, which parked
	// the search field under the keyboard instead of above it.
	it("drives the lift from progress, not from the animated height", () => {
		expect(board).toContain("-keyboardAnimation.progress.value * workerDockLift(keyboardHeight, insets.bottom)");
		expect(board).not.toContain("keyboardAnimation.height.value");
	});
});

describe("worker row second line", () => {
	const row = source("./worker-list-row.tsx");

	// The line led with the branch unconditionally, so every row carried a worktree
	// path under its title. Dropping the branch outright then left rows with no PR
	// showing nothing at all, on exactly the sessions whose branch was the only
	// thing naming their worktree.
	it("falls back to the branch when the row has no pull request", () => {
		expect(row).toContain("const details = prs?.text ?? row.branch ?? \"\";");
		expect(row).not.toContain("const details = prs?.text ?? \"\";");
	});
});

describe("worker row working indicator", () => {
	const row = source("./worker-list-row.tsx");
	const ui = source("./ui.tsx");

	// The shape for "working" is a circle, and a still circle reads as stuck or
	// decided rather than live. It was drawn as a bare glyph, so the one status
	// that means "still going" was the one status that never moved — and the same
	// word breathed on the project cards, which use `<Dot breathing>`.
	it("breathes the status glyph a working row is showing", () => {
		expect(row).toContain("<Breathing enabled={Boolean(visual.breathing)}>");
		expect(row).not.toContain("<Feather name={glyph} size={12} color={visual.color} />\n\t\t\t\t) : null}");
	});

	// One loop, one decision: the setting is read inside the primitive, so a new
	// consumer cannot forget it.
	it("drives both the dot and the glyph from the shared loop", () => {
		expect(ui).toContain("export function useBreathing(");
		expect(ui).toContain("shouldBreathe(reduceMotion, enabled)");
		expect(ui).toContain("const pulse = useBreathing(breathing);");
		expect(ui.match(/useBreathing\(/g)?.length ?? 0).toBeGreaterThanOrEqual(3);
	});
});
