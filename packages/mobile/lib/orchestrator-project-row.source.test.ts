import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

const source = readFileSync(new URL("./orchestrator-project-row.tsx", import.meta.url), "utf8");

describe("project row identity", () => {
	it("uses the AO mark rather than a generic folder for a project", () => {
		expect(source).toContain('import MASCOT from "../assets/mascot.png"');
		expect(source).toContain("source={MASCOT}");
		expect(source).not.toContain('<Feather name="folder"');
	});
});
