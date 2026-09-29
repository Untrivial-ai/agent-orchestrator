import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

const settingsButton = readFileSync(new URL("./sidebar-settings-button.android.tsx", import.meta.url), "utf8");
const spawnButton = readFileSync(new URL("./sidebar-spawn-button.android.tsx", import.meta.url), "utf8");

describe("Android sidebar action buttons", () => {
	it("keeps the floating controls visible against the drawer", () => {
		expect(settingsButton).toContain('backgroundColor: pressed ? t.accentTint : t.bgElevatedHover');
		expect(spawnButton).toContain('backgroundColor: pressed ? t.accentTint : t.bgElevatedHover');
		expect(settingsButton).toContain('borderColor: active ? t.accent : t.borderStrong');
		expect(spawnButton).toContain('borderColor: t.borderStrong');
	});
});
