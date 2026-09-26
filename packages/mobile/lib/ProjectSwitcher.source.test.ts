import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

const source = readFileSync(fileURLToPath(new URL("./ProjectSwitcher.tsx", import.meta.url)), "utf8");

describe("project picker trigger", () => {
	it("does not change width when the chosen project name changes", () => {
		expect(source).toContain('Platform.OS === "ios" ? { width: "60%"');
		expect(source).toContain('maxWidth: "70%"');
	});
});
