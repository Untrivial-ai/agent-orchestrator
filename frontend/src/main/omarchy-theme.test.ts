// @vitest-environment node
import { describe, expect, it } from "vitest";
import { mkdtemp, mkdir, rename, rm, symlink, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { MAX_PALETTE_BYTES, parseOmarchyPalette, readOmarchyPalette } from "./omarchy-theme";

const palette = `background = "#1d150f"
foreground = "#e3ddd0"
accent = "#d4b24a"
selection = "#4d3e23"
red = "#f25107"
green = "#9fae4a"
yellow = "#c9ae4b"
blue = "#d1a86a"
magenta = "#c97a5a"
cyan = "#9bb8a8"`;

describe("Omarchy palette boundary", () => {
	it("reads the installed semantic format and optional ANSI overrides", () => {
		const result = parseOmarchyPalette(palette + '\ncolor0 = "#123ABC"');
		expect(result?.background).toBe("#1d150f");
		expect(result?.ansi).toHaveLength(16);
		expect(result?.ansi[0]).toBe("#1d150f");
		expect(result?.ansi[9]).toBe("#f57439");
	});
	it("reads legacy numbered palettes", () => {
		const source = 'background="#111111"\nforeground="#eeeeee"\naccent="#abcdef"\n'
			+ Array.from({ length: 16 }, (_, i) => `color${i}="#123456"`).join("\n");
		expect(parseOmarchyPalette(source)?.ansi).toEqual(["#111111", ...Array(6).fill("#123456"), "#eeeeee", ...Array(8).fill("#123456")]);
	});
	it.each([
		palette.replace('#1d150f', 'red; background: url(https://example.com)'),
		palette + '\naccent = "#ffffff"',
		palette.replace('accent = "#d4b24a"', ''),
		palette + ' '.repeat(MAX_PALETTE_BYTES),
	])("rejects invalid, duplicate, missing and oversized color data", (source) => {
		expect(parseOmarchyPalette(source)).toBeNull();
	});
	it("ignores non-color data without evaluating it", () => {
		expect(parseOmarchyPalette(palette + '\nscript = "$(touch /tmp/no)"\n__proto__ = "bad"')).toEqual(parseOmarchyPalette(palette));
	});
	it("follows active-link replacement, invalidation, removal and recovery", async () => {
		const dir = await mkdtemp(path.join(os.tmpdir(), "ao-omarchy-"));
		try {
			for (const name of ["one", "two"]) await mkdir(path.join(dir, name));
			await writeFile(path.join(dir, "one/colors.toml"), palette);
			await writeFile(path.join(dir, "two/colors.toml"), palette.replace("#1d150f", "#eeeeee"));
			const active = path.join(dir, "active");
			const files = [path.join(active, "colors.toml")];
			await symlink(path.join(dir, "one"), active);
			expect((await readOmarchyPalette(files, "linux"))?.background).toBe("#1d150f");
			await symlink(path.join(dir, "two"), path.join(dir, "next"));
			await rename(path.join(dir, "next"), active);
			expect((await readOmarchyPalette(files, "linux"))?.background).toBe("#eeeeee");
			await writeFile(files[0], "invalid");
			expect(await readOmarchyPalette(files, "linux")).toBeNull();
			await rm(active);
			expect(await readOmarchyPalette(files, "linux")).toBeNull();
			await symlink(path.join(dir, "one"), active);
			expect(await readOmarchyPalette(files, "linux")).not.toBeNull();
			expect(await readOmarchyPalette(files, "darwin")).toBeNull();
			expect(await readOmarchyPalette([dir], "linux")).toBeNull();
		} finally { await rm(dir, { recursive: true, force: true }); }
	});
});
