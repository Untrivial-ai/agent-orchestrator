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

describe("generateFeeds macOS sidecar suppression", () => {
	beforeEach(() => {
		vi.clearAllMocks();
	});

	it("suppresses Nightly mac sidecars for both architectures", async () => {
		const dir = mkdtempSync(join(tmpdir(), "feed-test-"));
		const macZips = [
			"Agent.Orchestrator-darwin-arm64-0.10.4.zip",
			"Agent.Orchestrator-darwin-x64-0.10.4.zip",
		];
		for (const macZip of macZips) writeFileSync(join(dir, macZip), "fake mac zip");

		await generateFeeds(dir, "0.10.4", "nightly", "2026-06-27T12:00:00.000Z");

		for (const macZip of macZips) {
			expect(writeBlockmap).not.toHaveBeenCalled();
			expect(existsSync(join(dir, `${macZip}.blockmap`))).toBe(false);
		}

		const yml = readFileSync(join(dir, "nightly-mac.yml"), "utf8");
		expect(yml).not.toContain("blockMapSize");
		for (const macZip of macZips) {
			expect(yml).toContain(`url: ${macZip}`);
			const bytes = readFileSync(join(dir, macZip));
			expect(yml).toContain(`sha512: ${createHash("sha512").update(bytes).digest("base64")}`);
			expect(yml).toContain(`size: ${bytes.length}`);
		}

		rmSync(dir, { recursive: true, force: true });
	});

	it.each(["latest", "nightly", "pr3288", "unknown"].flatMap(channel => ["arm64", "x64"].map(arch => [channel, arch])))("keeps %s mac %s feeds full-download-only", async (channel, arch) => {
		const dir = mkdtempSync(join(tmpdir(), "feed-test-"));
		const macZip = `Agent.Orchestrator-darwin-${arch}-0.10.4.zip`;
		writeFileSync(join(dir, macZip), "fake mac zip");

		await generateFeeds(dir, "0.10.4", channel, "2026-06-27T12:00:00.000Z");

		expect(writeBlockmap).not.toHaveBeenCalled();
		expect(existsSync(join(dir, `${macZip}.blockmap`))).toBe(false);
		rmSync(dir, { recursive: true, force: true });
	});

	it("still calls writeBlockmap for win and linux installers", async () => {
		const dir = mkdtempSync(join(tmpdir(), "feed-test-"));
		const winExe = "Agent.Orchestrator.Setup.0.10.4.exe";
		const linuxAppImage = "Agent.Orchestrator-0.10.4.AppImage";
		writeFileSync(join(dir, winExe), "fake win installer");
		writeFileSync(join(dir, linuxAppImage), "fake linux installer");

		await generateFeeds(dir, "0.10.4", "nightly", "2026-06-27T12:00:00.000Z");

		expect(writeBlockmap).toHaveBeenCalledTimes(2);
		expect(writeBlockmap).toHaveBeenCalledWith(join(dir, winExe));
		expect(writeBlockmap).toHaveBeenCalledWith(join(dir, linuxAppImage));

		rmSync(dir, { recursive: true, force: true });
	});
});
