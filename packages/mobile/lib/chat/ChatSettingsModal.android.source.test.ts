import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

// Pins the Android sheet's wiring from its source, as ChatSettingsModelIcon.source.test.ts
// does; the decisions it delegates are tested in turnSettingsModel.test.ts.
const android = readFileSync(new URL("./ChatSettingsModal.android.tsx", import.meta.url), "utf8");

describe("Android turn settings sheet", () => {
	it("places the effort slider without clamping an effort that is none of its levels", () => {
		expect(android).toContain("const selectedIndex = effortSliderIndex(choices, selected);");
		expect(android).toContain("const next = effortSliderWrite(choices, selected, index);");
		expect(android).not.toMatch(/Math\.max\(0,\s*(?:choices\.findIndex|effortSliderIndex)/);
	});

	it("names a native model with the helper the iOS row uses", () => {
		expect(android).toContain("nativeModelLabel(selected, snapshot.settings.model)");
	});
});
