import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

const source = readFileSync(
	fileURLToPath(new URL("../app/settings.tsx", import.meta.url)),
	"utf8",
);

describe("settings screen density", () => {
	it("renders from the already-resolved app config without a second storage loader", () => {
		expect(source).toMatch(/const \{ config,[^}]*reloadConfig \} = useApp\(\);/);
		expect(source).not.toMatch(/\bloadConfig\(\)/);
	});

	it("keeps the Appearance slot wide while trailing-aligning the native menu", () => {
		expect(source).toContain("<View style={styles.appearancePicker}>");
		expect(source).toContain('<Host matchContents={{ horizontal: true }} style={{ height: 38 }}');
		expect(source).toMatch(/appearancePicker:\s*\{[^}]*width:\s*124[^}]*alignItems:\s*"flex-end"/s);
	});

	it("uses the compact sizing rhythm shared by the Workers UI", () => {
		expect(source).toMatch(/header:\s*\{\s*height:\s*64/);
		expect(source).toMatch(/content:\s*\{[^}]*paddingHorizontal:\s*space\.lg[^}]*gap:\s*space\.lg/s);
		expect(source).toMatch(/card:\s*\{[^}]*borderRadius:\s*16/s);
		expect(source).toMatch(/row:\s*\{\s*minHeight:\s*52/);
		// Sizes come from the type ramp now, so the contract is the ramp step
		// (subheadline over footnote), not a bare number.
		expect(source).toMatch(/rowLabel:\s*\{[^}]*fontSize:\s*type\.subheadline\.fontSize/s);
		expect(source).toMatch(/rowValue:\s*\{[^}]*fontSize:\s*type\.footnote\.fontSize/s);
	});
});
