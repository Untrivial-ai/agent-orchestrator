import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

const source = readFileSync(fileURLToPath(new URL("./spawn-composer-controls.ios.tsx", import.meta.url)), "utf8");

describe("iOS spawn menu layout", () => {
	it("keeps the project trigger compact and the other selectors steady", () => {
		expect(source).toContain('frame({ width: HARNESS_MENU_WIDTH })');
		expect(source).toContain('const PROJECT_MENU_WIDTH = 224');
		expect(source).toContain('{projectLabel}</Text>\n\t\t\t\t\t\t\t<Image systemName="chevron.up.chevron.down"');
		expect(source).toContain('padding({ horizontal: 4 }), frame({ width: PROJECT_MENU_WIDTH, alignment: "leading" })');
		expect(source).toContain('layoutPriority(1)');
		expect(source).toContain('accessibilityIdentifier("spawn-project")');
		expect(source).toContain('accessibilityIdentifier("spawn-model")');
	});
});
