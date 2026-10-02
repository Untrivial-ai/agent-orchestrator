import fs from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";
import {
	backdropFor, chipColorFor, HARNESS_CHIP, harnessInitial, hasLogo, LOGO_KEYS,
	NEEDS_DARK_BACKDROP, NEEDS_LIGHT_BACKDROP,
} from "./harnessLogo";
import { darkTheme, lightTheme } from "./theme";

const PNG = (require("pngjs") as {
	PNG: { sync: { read: (data: Buffer) => { width: number; height: number; data: Buffer } } };
}).PNG;

const NEUTRAL = [
	"agy", "aider", "amp", "auggie", "autohand", "claude-code", "codex", "copilot",
	"crush", "kimchi", "kiro", "muse", "qwen", "vibe",
];

function luminance([red, green, blue]: readonly number[]): number {
	const linear = (value: number) => {
		const channel = value / 255;
		return channel <= 0.04045 ? channel / 12.92 : ((channel + 0.055) / 1.055) ** 2.4;
	};
	return 0.2126 * linear(red) + 0.7152 * linear(green) + 0.0722 * linear(blue);
}

function hexRgb(hex: string): [number, number, number] {
	return [1, 3, 5].map((index) => Number.parseInt(hex.slice(index, index + 2), 16)) as [number, number, number];
}

function lowContrastShare(data: Buffer, background: readonly [number, number, number]): number {
	const backgroundLuminance = luminance(background);
	let visible = 0;
	let lowContrast = 0;
	for (let i = 0; i < data.length; i += 4) {
		const alpha = data[i + 3] / 255;
		if (alpha === 0) continue;
		visible++;
		const composited = [0, 1, 2].map((channel) =>
			Math.round(data[i + channel] * alpha + background[channel] * (1 - alpha)),
		);
		const foregroundLuminance = luminance(composited);
		const contrast = (Math.max(foregroundLuminance, backgroundLuminance) + 0.05) /
			(Math.min(foregroundLuminance, backgroundLuminance) + 0.05);
		if (contrast < 1.5) lowContrast++;
	}
	return lowContrast / visible;
}

// The harnesses the daemon accepts — backend/internal/domain/harness.go.
const ALL_HARNESSES = [
	"claude-code", "codex", "aider", "opencode", "grok", "droid", "amp", "agy",
	"crush", "cursor", "qwen", "copilot", "goose", "auggie", "continue", "devin",
	"cline", "kimi", "muse", "kiro", "kilocode", "vibe", "pi", "autohand",
	"kimchi", "prime-agent", "fx",
];

// The fake harness exists only for tests and intentionally has no brand asset.
const NO_ASSET = ["fake"];

describe("logo registry", () => {
	it("has a mark for every harness that ships one", () => {
		for (const h of ALL_HARNESSES.filter((h) => !NO_ASSET.includes(h))) {
			expect(hasLogo(h), h).toBe(true);
		}
	});

	it("has no mark for the harnesses that ship none, rather than a stand-in", () => {
		for (const h of NO_ASSET) expect(hasLogo(h), h).toBe(false);
	});

	// Desktop's toAgentProvider() funnels anything unrecognised into "codex", so
	// an unknown agent renders as the Codex logo — confidently wrong.
	it("does not claim a mark for an unknown harness", () => {
		expect(hasLogo("some-new-agent")).toBe(false);
		expect(hasLogo(undefined)).toBe(false);
		expect(hasLogo(null)).toBe(false);
		expect(hasLogo("  ")).toBe(false);
	});

	it("is case- and whitespace-insensitive", () => {
		expect(hasLogo("Claude-Code")).toBe(true);
		expect(hasLogo(" codex ")).toBe(true);
	});

	// The registry is a hand-written list beside a directory of files and a
	// second hand-written list of `require`s. Checking it against the real
	// directory is what stops a rename from silently dropping a mark — a missing
	// asset is a bundle-time failure, which no type check would catch.
	it("matches the asset directory exactly", () => {
		const dir = path.join(__dirname, "..", "assets", "agents");
		const onDisk = fs
			.readdirSync(dir)
			.filter((f) => f.endsWith(".png"))
			.map((f) => f.replace(/\.png$/, ""))
			.sort();
		expect(onDisk).toEqual([...LOGO_KEYS].sort());
		for (const key of onDisk) {
			const png = fs.readFileSync(path.join(dir, `${key}.png`));
			expect([...png.subarray(0, 8)], key).toEqual([137, 80, 78, 71, 13, 10, 26, 10]);
			expect(() => PNG.sync.read(png), key).not.toThrow();
		}
	});

	it("registers fx for Metro", () => {
		const registry = fs.readFileSync(path.join(__dirname, "harnessLogoAssets.ts"), "utf8");
		expect(registry).toMatch(/fx:\s*require\("\.\.\/assets\/agents\/fx\.png"\)/);
	});

	it("keeps the Copilot mark visible on both card backgrounds", () => {
		const png = PNG.sync.read(fs.readFileSync(path.join(__dirname, "..", "assets", "agents", "copilot.png")));
		expect(png.width).toBe(128);
		expect(png.height).toBe(128);
		expect(lowContrastShare(png.data, hexRgb(lightTheme.bgElevated))).toBeLessThan(0.5);
		expect(lowContrastShare(png.data, hexRgb(darkTheme.bgElevated))).toBeLessThan(0.5);
	});
});

describe("backdropFor", () => {
	// Measured, not guessed: >50% of these marks fall below 1.5:1 contrast
	// against white. opencode and cursor are pure #ffffff.
	it("puts a dark chip behind marks that vanish on a light card", () => {
		for (const h of ["opencode", "cursor", "cline", "continue", "grok"]) {
			expect(backdropFor(h), h).toBe("needs-dark");
		}
	});

	// The symmetric case, which is the one that is easy to miss: goose and
	// kilocode are pure black and vanish on the dark card.
	it("puts a light chip behind marks that vanish on a dark card", () => {
		for (const h of ["kilocode", "goose", "devin", "droid", "pi", "kimi", "fx", "prime-agent"]) {
			expect(backdropFor(h), h).toBe("needs-light");
		}
	});

	it("assigns each logo exactly one polarity", () => {
		const dark = [...NEEDS_DARK_BACKDROP];
		const light = [...NEEDS_LIGHT_BACKDROP];
		expect(dark.filter((h) => NEEDS_LIGHT_BACKDROP.has(h))).toEqual([]);
		expect(NEUTRAL.filter((h) => NEEDS_DARK_BACKDROP.has(h) || NEEDS_LIGHT_BACKDROP.has(h))).toEqual([]);
		expect([...new Set([...dark, ...light, ...NEUTRAL])].sort()).toEqual([...LOGO_KEYS].sort());
		for (const h of NEUTRAL) expect(backdropFor(h), h).toBe("neutral");
	});

	it("treats an unknown or missing harness as neutral", () => {
		expect(backdropFor("some-new-agent")).toBe("neutral");
		expect(backdropFor(null)).toBe("neutral");
	});
});

describe("chipColorFor", () => {
	// Every surface that draws a mark has to route through this. The spawn
	// composer's SwiftUI menu drew the raw asset instead, so opencode's pure-white
	// mark rendered on the light theme's own surface and disappeared.
	it("returns the fixed chip for each polarity", () => {
		expect(chipColorFor("opencode")).toBe(HARNESS_CHIP.dark);
		expect(chipColorFor("cursor")).toBe(HARNESS_CHIP.dark);
		expect(chipColorFor("goose")).toBe(HARNESS_CHIP.light);
		expect(chipColorFor("kilocode")).toBe(HARNESS_CHIP.light);
	});

	it("returns nothing for a mark that needs no backdrop", () => {
		expect(chipColorFor("codex")).toBeUndefined();
		expect(chipColorFor("claude-code")).toBeUndefined();
		expect(chipColorFor("copilot")).toBeUndefined();
		expect(chipColorFor(null)).toBeUndefined();
	});

	// A palette token would follow the theme and put the white mark back on a
	// light surface, which is the bug this exists to prevent.
	it("keeps both chips theme-independent", () => {
		expect(HARNESS_CHIP.dark).toBe("#24272e");
		expect(HARNESS_CHIP.light).toBe("#ffffff");
	});
});

describe("harnessInitial", () => {
	it("gives the uppercase initial", () => {
		expect(harnessInitial("unknown-agent")).toBe("U");
		expect(harnessInitial("fake")).toBe("F");
	});

	it("falls back to a question mark rather than rendering nothing", () => {
		expect(harnessInitial("")).toBe("?");
		expect(harnessInitial("   ")).toBe("?");
		expect(harnessInitial(undefined)).toBe("?");
	});
});
