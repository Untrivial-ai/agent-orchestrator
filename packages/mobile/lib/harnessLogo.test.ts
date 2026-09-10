import fs from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";
import { backdropFor, harnessInitial, hasLogo, LOGO_KEYS } from "./harnessLogo";

// The 24 harnesses the daemon accepts — backend/internal/domain/harness.go.
const ALL_HARNESSES = [
	"claude-code", "codex", "aider", "opencode", "grok", "droid", "amp", "agy",
	"crush", "cursor", "qwen", "copilot", "goose", "auggie", "continue", "devin",
	"cline", "kimi", "muse", "kiro", "kilocode", "vibe", "pi", "autohand",
	"kimchi", "prime-agent",
];

// The fake harness exists only for tests and intentionally has no brand asset.
// prime-agent ships a mark on desktop but it has not been ported to mobile yet.
const NO_ASSET = ["fake", "prime-agent"];

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
	});
});

describe("backdropFor", () => {
	// Measured, not guessed: >50% of these marks fall below 1.5:1 contrast
	// against white. opencode and cursor are pure #ffffff.
	it("puts a dark chip behind marks that vanish on a light card", () => {
		for (const h of ["opencode", "cursor", "cline", "continue", "grok", "copilot"]) {
			expect(backdropFor(h), h).toBe("needs-dark");
		}
	});

	// The symmetric case, which is the one that is easy to miss: goose and
	// kilocode are pure black and vanish on the dark card.
	it("puts a light chip behind marks that vanish on a dark card", () => {
		for (const h of ["kilocode", "goose", "devin", "droid", "pi", "kimi"]) {
			expect(backdropFor(h), h).toBe("needs-light");
		}
	});

	// A chip behind every mark would turn a row of logos into a row of boxes.
	it("leaves colourful marks bare", () => {
	for (const h of ["claude-code", "codex", "amp", "qwen", "vibe", "aider", "crush", "muse", "kiro"]) {
			expect(backdropFor(h), h).toBe("neutral");
		}
	});

	it("never assigns two polarities to one mark", () => {
		const dark = ["opencode", "cursor", "cline", "continue", "grok", "copilot"];
		const light = ["kilocode", "goose", "devin", "droid", "pi", "kimi"];
		expect(dark.filter((h) => light.includes(h))).toEqual([]);
	});

	it("treats an unknown or missing harness as neutral", () => {
		expect(backdropFor("some-new-agent")).toBe("neutral");
		expect(backdropFor(null)).toBe("neutral");
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
