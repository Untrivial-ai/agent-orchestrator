import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

const source = readFileSync(fileURLToPath(new URL("./ChatTurnSettingsControl.ios.tsx", import.meta.url)), "utf8");

describe("iOS turn settings menu anchor", () => {
	it("sizes the native host to its label so the popup stays pinned left", () => {
		expect(source).toContain("matchContents={{ horizontal: true, vertical: true }}");
		expect(source).toContain('ignoreSafeArea="all"');
		expect(source).not.toContain("<Spacer");
		expect(source).not.toContain("maxWidth: 1000");
	});
});
