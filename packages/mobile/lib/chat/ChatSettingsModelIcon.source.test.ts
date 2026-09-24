import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

const android = readFileSync(new URL("./ChatSettingsModal.android.tsx", import.meta.url), "utf8");
const fallback = readFileSync(new URL("./ChatSettingsModal.tsx", import.meta.url), "utf8");
const icons = readFileSync(new URL("../icons.tsx", import.meta.url), "utf8");

describe("model setting icon", () => {
	it("uses layers for the Android selector, not its choices", () => {
		expect(android).toContain('<SettingRow icon="layers" label="Model"');
		expect(android).toContain('selected ? <Feather name="check"');
	});

	it("uses layers for the fallback section, not its choices", () => {
		expect(fallback).toContain('<SettingsSection icon="layers" title="Model">');
		expect(fallback).toContain('if (option.category === "model" || option.id === "model") return "layers"');
		expect(fallback).toContain('selected ? <Feather name="check"');
	});
});

describe("approvals setting icon", () => {
	it("uses the dashed check circle for the Android permission row", () => {
		expect(android).toContain('<SettingRow icon="circle-dashed-check" label="Permission mode"');
	});

	it("uses the same icon for fallback and provider-owned approvals", () => {
		expect(fallback).toContain('<SettingsSection icon="circle-dashed-check" title="Approvals">');
		expect(fallback).toContain('if (option.category === "mode" || option.id === "mode" || option.id.includes("permission") || option.id.includes("approval")) return "circle-dashed-check"');
	});

	it("maps the icon to the Lucide glyph", () => {
		expect(icons).toContain('import CircleDashedCheck from "lucide-react-native/icons/circle-dashed-check"');
		expect(icons).toContain('"circle-dashed-check": CircleDashedCheck');
	});
});
