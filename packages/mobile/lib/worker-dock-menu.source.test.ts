import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

function source(relativePath: string): string {
	return readFileSync(fileURLToPath(new URL(relativePath, import.meta.url)), "utf8");
}

const dock = source("./worker-dock.ios.tsx");

describe("worker dock menu", () => {
	// The dock sits at the bottom of the screen, so its menus open upward — and a
	// menu that opens upward draws its items in reverse under SwiftUI's default
	// order. That is what put "All projects" last and the project list above the
	// search row. `menuOrder("fixed")` is the documented off switch.
	it("pins the item order for both menus", () => {
		expect(dock.match(/menuOrder\("fixed"\)/g) ?? []).toHaveLength(2);
	});

	// The prefix pushed the row past the width the menu reserves and wrapped it.
	it("labels the projects menu with the selection alone", () => {
		expect(dock).toContain("<Menu label={selectedProjectLabel} systemImage=\"folder\"");
		expect(dock).not.toContain("Projects ·");
	});

	it("draws no title above the options", () => {
		expect(dock).not.toContain("Worker list options");
		expect(dock).toContain("<Section>");
	});
});
