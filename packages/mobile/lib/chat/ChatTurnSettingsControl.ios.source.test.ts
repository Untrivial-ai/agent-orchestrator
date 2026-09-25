import { existsSync, mkdtempSync, readFileSync, rmSync } from "node:fs";
import { createRequire } from "node:module";
import { tmpdir } from "node:os";
import { join } from "node:path";
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

describe("iOS model setting", () => {
	it("shows a layers symbol on the selector, but only checkmarks in its choices", () => {
		const symbol = source.slice(source.indexOf("function settingSymbol"));
		expect(symbol).toContain('if (row.providerKind === "model" || id.includes("model")) return "square.stack.3d.up"');
		expect(source).toContain('systemImage={choice.selected ? "checkmark" : undefined}');
	});

	it("does not give the agent selector a model icon", () => {
		const symbol = source.slice(source.indexOf("function settingSymbol"));
		expect(symbol).toContain('if (id.includes("agent")) return undefined');
	});
});

describe("iOS approvals setting", () => {
	it("shows the complete dashed-check artwork as one image in the menu row", () => {
		expect(source).toContain('isApprovalRow(row) ? <ApprovalRowLabel row={row} />');
		expect(source).toContain('<Image assetName="CircleDashedCheck" />');
	});
});

describe("native approvals icon asset", () => {
	it("provides sharp 1x, 2x, and 3x variants for both appearances", () => {
		const root = new URL("../../assets/icons/CircleDashedCheck.imageset/", import.meta.url);
		expect(existsSync(new URL("Contents.json", root))).toBe(true);
		const catalog = JSON.parse(readFileSync(new URL("Contents.json", root), "utf8")) as {
			images: Array<{ filename: string; scale: string; appearances?: Array<{ appearance: string; value: string }> }>;
		};
		for (const appearance of ["light", "dark"]) {
			for (const scale of [1, 2, 3]) {
				const entry = catalog.images.find((image) => image.scale === `${scale}x` && (appearance === "light"
					? !image.appearances?.length
					: image.appearances?.some((item) => item.appearance === "luminosity" && item.value === "dark")));
				expect(entry).toBeDefined();
				const png = readFileSync(new URL(entry!.filename, root));
				expect(png.readUInt32BE(16)).toBe(18 * scale);
				expect(png.readUInt32BE(20)).toBe(18 * scale);
			}
		}
	});

	it("copies the asset into a clean generated iOS project", () => {
		const pluginUrl = new URL("../../plugins/withCircleDashedCheck.js", import.meta.url);
		expect(existsSync(pluginUrl)).toBe(true);
		const require = createRequire(import.meta.url);
		const { copyCircleDashedCheckAssets } = require(fileURLToPath(pluginUrl)) as { copyCircleDashedCheckAssets(root: string, projectName: string): void };
		const projectRoot = mkdtempSync(join(tmpdir(), "ao-icon-plugin-"));
		try {
			copyCircleDashedCheckAssets(projectRoot, "AO");
			const target = join(projectRoot, "AO", "Images.xcassets", "CircleDashedCheck.imageset");
			expect(readFileSync(join(target, "Contents.json"))).toEqual(readFileSync(new URL("../../assets/icons/CircleDashedCheck.imageset/Contents.json", import.meta.url)));
			expect(readFileSync(join(target, "circle-dashed-check-dark@3x.png"))).toEqual(readFileSync(new URL("../../assets/icons/CircleDashedCheck.imageset/circle-dashed-check-dark@3x.png", import.meta.url)));
		} finally {
			rmSync(projectRoot, { recursive: true, force: true });
		}
	});
});
