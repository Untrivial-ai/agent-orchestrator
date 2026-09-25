import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

const source = readFileSync(fileURLToPath(new URL("./spawn-composer-controls.ios.tsx", import.meta.url)), "utf8");

describe("iOS spawn menu layout", () => {
	it("keeps project and model triggers steady across label lengths", () => {
		expect(source).toContain('frame({ width: HARNESS_MENU_WIDTH })');
		expect(source).toContain('{projectLabel}</Text>\n\t\t\t\t\t\t\t<Spacer />');
		expect(source).toContain('padding({ horizontal: 4 }), frame({ maxWidth: 1000, alignment: "leading" })');
		expect(source).toContain('layoutPriority(1)');
		expect(source).toContain('accessibilityIdentifier("spawn-project")');
		expect(source).toContain('accessibilityIdentifier("spawn-model")');
	});
});
