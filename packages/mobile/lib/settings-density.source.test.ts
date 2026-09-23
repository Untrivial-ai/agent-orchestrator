import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

const source = readFileSync(
	fileURLToPath(new URL("../app/settings.tsx", import.meta.url)),
	"utf8",
);

describe("settings screen density", () => {
	it("uses the compact sizing rhythm shared by the Workers UI", () => {
		expect(source).toMatch(/header:\s*\{\s*height:\s*64/);
		expect(source).toMatch(/content:\s*\{[^}]*paddingHorizontal:\s*16[^}]*gap:\s*18/s);
		expect(source).toMatch(/card:\s*\{[^}]*borderRadius:\s*16/s);
		expect(source).toMatch(/row:\s*\{\s*minHeight:\s*52/);
		expect(source).toMatch(/rowLabel:\s*\{[^}]*fontSize:\s*15/s);
		expect(source).toMatch(/rowValue:\s*\{[^}]*fontSize:\s*13/s);
	});
});
