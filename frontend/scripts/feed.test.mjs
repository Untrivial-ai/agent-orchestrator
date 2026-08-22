// @vitest-environment node
import { describe, it, expect, vi, beforeEach } from "vitest";
import { mkdtempSync, writeFileSync, readFileSync, existsSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { createHash } from "node:crypto";

vi.mock("./blockmap.mjs", () => ({
	writeBlockmap: vi.fn(async (filePath) => {
		writeFileSync(`${filePath}.blockmap`, "fake-blockmap");
		return { sha512: "MOCKED_BLOCKMAP_SHA512", size: 999 };
	}),
}));

import { selectInstallers, feedFilename, buildYml, hashFile, generateFeeds } from "./feed.mjs";
import { writeBlockmap } from "./blockmap.mjs";
const V = "0.10.4";
const NAMES = [
	"Agent.Orchestrator.Setup.0.10.4.exe", // win versioned
	"Agent.Orchestrator-0.10.4.AppImage", // linux versioned
	"Agent.Orchestrator-darwin-arm64-0.10.4.zip", // mac arm64 versioned
	"Agent.Orchestrator-darwin-x64-0.10.4.zip", // mac x64 versioned
	"agent-orchestrator-darwin-arm64.zip", // ao-start alias (no version) -> excluded
	"agent-orchestrator-win32-x64.exe", // alias (no version) -> excluded
	"agent-orchestrator_0.10.4_amd64.deb", // deb -> excluded by extension
	"agent-orchestrator-0.10.4.x86_64.rpm", // rpm -> excluded by extension
];

describe("selectInstallers", () => {
	it("keeps only versioned exe/AppImage/darwin-zip, split by arch", () => {
		const s = selectInstallers(NAMES, V);
		expect(s.win).toEqual(["Agent.Orchestrator.Setup.0.10.4.exe"]);
		expect(s.linux).toEqual(["Agent.Orchestrator-0.10.4.AppImage"]);
		expect(s.macArm64).toEqual(["Agent.Orchestrator-darwin-arm64-0.10.4.zip"]);
		expect(s.macX64).toEqual(["Agent.Orchestrator-darwin-x64-0.10.4.zip"]);
	});
});

describe("feedFilename", () => {
	it("maps channel + platform to electron-updater names", () => {
		expect(feedFilename("latest", "win")).toBe("latest.yml");
		expect(feedFilename("latest", "mac")).toBe("latest-mac.yml");
		expect(feedFilename("latest", "linux")).toBe("latest-linux.yml");
		expect(feedFilename("nightly", "win")).toBe("nightly.yml");
		expect(feedFilename("nightly", "mac")).toBe("nightly-mac.yml");
		expect(feedFilename("nightly", "linux")).toBe("nightly-linux.yml");
	});

	// Channel-isolation invariant: a pr<N> channel MUST produce its own namespaced
	// feed filenames and MUST NOT produce filenames that start with "latest" or
	// "nightly". This guards against the #2270 poisoning class where a pr-channel
	// feed accidentally overwrites the shared latest-mac.yml / nightly-mac.yml.
	describe("pr<N> channel isolation (guards against #2270 latest-mac.yml poisoning)", () => {
		it("pr2270 + mac => pr2270-mac.yml", () => {
			expect(feedFilename("pr2270", "mac")).toBe("pr2270-mac.yml");
		});

		it("pr2270 + linux => pr2270-linux.yml", () => {
			expect(feedFilename("pr2270", "linux")).toBe("pr2270-linux.yml");
		});

		it("pr2270 + win => pr2270.yml", () => {
			expect(feedFilename("pr2270", "win")).toBe("pr2270.yml");
		});

		it.each(["mac", "linux", "win"])(
			"pr<N> channel never yields a filename starting with 'latest' (platform: %s)",
			(platform) => {
				expect(feedFilename("pr2270", platform)).not.toMatch(/^latest/);
			},
		);

		it.each(["mac", "linux", "win"])(
			"pr<N> channel never yields a filename starting with 'nightly' (platform: %s)",
			(platform) => {
				expect(feedFilename("pr2270", platform)).not.toMatch(/^nightly/);
			},
		);
	});
});

describe("buildYml", () => {
	it("serializes one file with deprecated top-level fields and no blockMapSize", () => {
		const yml = buildYml(
			"0.10.4",
			[{ url: "Agent.Orchestrator.Setup.0.10.4.exe", sha512: "AA/BB+cc==", size: 123 }],
			"2026-06-27T12:00:00.000Z",
		);
		expect(yml).toBe(
			"version: 0.10.4\n" +
				"files:\n" +
				"  - url: Agent.Orchestrator.Setup.0.10.4.exe\n" +
				"    sha512: AA/BB+cc==\n" +
				"    size: 123\n" +
				"path: Agent.Orchestrator.Setup.0.10.4.exe\n" +
				"sha512: AA/BB+cc==\n" +
				"releaseDate: '2026-06-27T12:00:00.000Z'\n",
		);
		expect(yml).not.toContain("blockMapSize");
	});

	it("lists both mac arches with arm64 first and points top-level at arm64", () => {
		const yml = buildYml(
			"0.10.4",
			[
				{ url: "Agent.Orchestrator-darwin-arm64-0.10.4.zip", sha512: "ARM==", size: 10 },
				{ url: "Agent.Orchestrator-darwin-x64-0.10.4.zip", sha512: "X64==", size: 20 },
			],
			"2026-06-27T12:00:00.000Z",
		);
		const lines = yml.split("\n");
		expect(lines[2]).toBe("  - url: Agent.Orchestrator-darwin-arm64-0.10.4.zip");
		expect(lines[5]).toBe("  - url: Agent.Orchestrator-darwin-x64-0.10.4.zip");
		expect(yml).toContain("path: Agent.Orchestrator-darwin-arm64-0.10.4.zip");
	});

	it("omits important key when flag is false (byte-identical to old output)", () => {
		const yml = buildYml(
			"0.10.4",
			[{ url: "Agent.Orchestrator.Setup.0.10.4.exe", sha512: "AA/BB+cc==", size: 123 }],
			"2026-06-27T12:00:00.000Z",
			false,
		);
		expect(yml).not.toContain("important");
	});

	it("emits important: true as top-level key when flag is true", () => {
		const yml = buildYml(
			"0.10.4",
			[{ url: "Agent.Orchestrator.Setup.0.10.4.exe", sha512: "AA/BB+cc==", size: 123 }],
			"2026-06-27T12:00:00.000Z",
			true,
		);
		expect(yml).toContain("important: true\n");
		// must still have all existing fields
		expect(yml).toContain("version: 0.10.4");
		expect(yml).toContain("releaseDate:");
	});
});

describe("hashFile", () => {
	it("computes sha512 (base64) and byte size of a real file, matching node:crypto directly", () => {
		const dir = mkdtempSync(join(tmpdir(), "feed-test-"));
		const filePath = join(dir, "sample.zip");
		const content = "fake zip contents for hashing";
		writeFileSync(filePath, content);

		const { sha512, size } = hashFile(filePath);

		const want = createHash("sha512").update(Buffer.from(content)).digest("base64");
		expect(sha512).toBe(want);
		expect(size).toBe(Buffer.byteLength(content));

		rmSync(dir, { recursive: true, force: true });
	});

	it("does not write any sidecar file, unlike writeBlockmap", () => {
		const dir = mkdtempSync(join(tmpdir(), "feed-test-"));
		const filePath = join(dir, "sample.zip");
		writeFileSync(filePath, "content");

		hashFile(filePath);

		expect(existsSync(`${filePath}.blockmap`)).toBe(false);

		rmSync(dir, { recursive: true, force: true });
	});
});

// Windows is the only platform whose updater fetches a .blockmap sidecar.
//   mac   is regression coverage for #3034: Squirrel.Mac's ShipIt `ditto`
//         install step fails against a corrupt update cache, and a
//         sidecar-driven differential update is the corruption path (#3151,
//         #3267 decision 4).
//   linux never had a reader: AppImageUpdater takes its blockmap from the
//         AppImage tail and needs a blockMapSize feed.mjs does not emit, so a
//         sidecar fed nothing. Generation retired in #3288 workstream 3.
describe("generateFeeds blockmap sidecars are windows-only", () => {
	beforeEach(() => {
		vi.clearAllMocks();
	});

	it("does not call writeBlockmap or write a .blockmap sidecar for mac zips (#3034)", async () => {
		const dir = mkdtempSync(join(tmpdir(), "feed-test-"));
		const macZip = "Agent.Orchestrator-darwin-arm64-0.10.4.zip";
		writeFileSync(join(dir, macZip), "fake mac zip");

		await generateFeeds(dir, "0.10.4", "nightly", "2026-06-27T12:00:00.000Z");

		expect(writeBlockmap).not.toHaveBeenCalled();
		expect(existsSync(join(dir, `${macZip}.blockmap`))).toBe(false);

		const yml = readFileSync(join(dir, "nightly-mac.yml"), "utf8");
		expect(yml).not.toContain("blockMapSize");
		expect(yml).toContain(`url: ${macZip}`);

		rmSync(dir, { recursive: true, force: true });
	});

	it("does not call writeBlockmap or write a .blockmap sidecar for linux AppImages (#3288)", async () => {
		const dir = mkdtempSync(join(tmpdir(), "feed-test-"));
		const linuxAppImage = "Agent.Orchestrator-0.10.4.AppImage";
		writeFileSync(join(dir, linuxAppImage), "fake linux installer");

		await generateFeeds(dir, "0.10.4", "nightly", "2026-06-27T12:00:00.000Z");

		expect(writeBlockmap).not.toHaveBeenCalled();
		expect(existsSync(join(dir, `${linuxAppImage}.blockmap`))).toBe(false);

		// The feed still lists the AppImage with a real hash; only the dead
		// sidecar is gone, and blockMapSize stays absent so AppImageUpdater
		// keeps falling back to a full download rather than erroring.
		const yml = readFileSync(join(dir, "nightly-linux.yml"), "utf8");
		expect(yml).not.toContain("blockMapSize");
		expect(yml).toContain(`url: ${linuxAppImage}`);
		expect(yml).toContain(
			`sha512: ${createHash("sha512").update(Buffer.from("fake linux installer")).digest("base64")}`,
		);

		rmSync(dir, { recursive: true, force: true });
	});

	it("still calls writeBlockmap for the win installer", async () => {
		const dir = mkdtempSync(join(tmpdir(), "feed-test-"));
		const winExe = "Agent.Orchestrator.Setup.0.10.4.exe";
		const linuxAppImage = "Agent.Orchestrator-0.10.4.AppImage";
		writeFileSync(join(dir, winExe), "fake win installer");
		writeFileSync(join(dir, linuxAppImage), "fake linux installer");

		await generateFeeds(dir, "0.10.4", "nightly", "2026-06-27T12:00:00.000Z");

		expect(writeBlockmap).toHaveBeenCalledTimes(1);
		expect(writeBlockmap).toHaveBeenCalledWith(join(dir, winExe));

		rmSync(dir, { recursive: true, force: true });
	});
});
