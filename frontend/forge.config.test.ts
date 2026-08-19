import { afterEach, describe, expect, it, vi } from "vitest";

// postMake's dmg/zip branches only need to prove they call the right
// maker-dmg functions with the right gates; the functions' own behavior
// (sealDmg's credential matrix, verifyMacArtifact's script invocation) is
// covered by makers/maker-dmg.test.ts. Mocking the whole module keeps this
// suite from spawning codesign/xcrun/bash indirectly through the real chain.
// vi.mock's factory is hoisted above the rest of the file (including plain
// const declarations), so the mock fns themselves must go through vi.hoisted.
const { sealDmg, verifyDmg, verifyMacArtifact, isSigningConfigured } = vi.hoisted(() => ({
	sealDmg: vi.fn<(path: string) => Promise<boolean>>(),
	verifyDmg: vi.fn<(path: string) => Promise<void>>(async () => undefined),
	verifyMacArtifact: vi.fn<(path: string) => Promise<void>>(async () => undefined),
	isSigningConfigured: vi.fn<() => boolean>(),
}));
vi.mock("./makers/maker-dmg", async (importOriginal) => {
	const actual = await importOriginal<typeof import("./makers/maker-dmg")>();
	return { ...actual, sealDmg, verifyDmg, verifyMacArtifact, isSigningConfigured };
});

import config, { macSignOptionsForFile } from "./forge.config";

type MacSignOptions = {
	identity?: string;
	optionsForFile?: typeof macSignOptionsForFile;
};

async function loadMacSignOptions(env: {
	APPLE_SIGNING_IDENTITY?: string;
	CSC_LINK?: string;
}): Promise<MacSignOptions | undefined> {
	vi.stubEnv("APPLE_SIGNING_IDENTITY", env.APPLE_SIGNING_IDENTITY ?? "");
	vi.stubEnv("CSC_LINK", env.CSC_LINK ?? "");
	vi.resetModules();
	const { default: envConfig } = await import("./forge.config");
	return envConfig.packagerConfig?.osxSign as MacSignOptions | undefined;
}

afterEach(() => {
	vi.unstubAllEnvs();
	vi.resetModules();
	sealDmg.mockReset();
	verifyDmg.mockReset().mockImplementation(async () => undefined);
	verifyMacArtifact.mockReset().mockImplementation(async () => undefined);
	isSigningConfigured.mockReset();
});

describe("macOS signing", () => {
	it("allows the bundled Node runtime to execute V8 JIT code on Intel Macs", () => {
		expect(
			macSignOptionsForFile(
				"/tmp/Agent Orchestrator.app/Contents/Resources/acp-runtime/node/bin/node",
				"x64",
			),
		).toEqual({
			entitlements: [
				"com.apple.security.cs.allow-jit",
				"com.apple.security.cs.allow-unsigned-executable-memory",
			],
		});
	});

	it("keeps electron-osx-sign defaults for every other bundle file", () => {
		expect(
			macSignOptionsForFile(
				"/tmp/Agent Orchestrator.app/Contents/MacOS/agent-orchestrator",
			),
		).toEqual({});
	});

	it("keeps the narrower default JIT entitlement on Apple silicon", () => {
		expect(
			macSignOptionsForFile("/tmp/Agent Orchestrator.app/Contents/Resources/acp-runtime/node/bin/node", "arm64"),
		).toEqual({});
	});

	it.each([
		{
			name: "an explicit signing identity",
			env: { APPLE_SIGNING_IDENTITY: "Developer ID Application: AO (TEAMID)" },
			identity: "Developer ID Application: AO (TEAMID)",
		},
		{
			name: "a CSC_LINK certificate",
			env: { CSC_LINK: "base64-certificate" },
			identity: undefined,
		},
	])("wires the per-file override when signing with $name", async ({ env, identity }) => {
		const signOptions = await loadMacSignOptions(env);

		expect(signOptions?.identity).toBe(identity);
		expect(signOptions?.optionsForFile).toEqual(expect.any(Function));
	});

	it("leaves unsigned local packages unsigned", async () => {
		await expect(loadMacSignOptions({})).resolves.toBeUndefined();
	});
});

describe("postMake artifact verification", () => {
	// #3879: a valid outer seal did not prove the bundled Intel ACP Node could
	// actually run JavaScript. The dmg branch already ran verify-mac-artifact.sh
	// through sealDmg/verifyDmg; these cases prove the zip branch — the artifact
	// electron-updater installs auto-updates from, and per-arch what CI ships
	// for x64 — gets the same nested-Node check instead of shipping unverified.
	function darwinResult(artifacts: string[]) {
		return [{ artifacts, packageJSON: {}, platform: "darwin" as const, arch: "x64" as const }];
	}

	it("verifies a signed zip with the canonical script, gated on isSigningConfigured", async () => {
		isSigningConfigured.mockReturnValue(true);
		const makeResults = darwinResult(["/out/make/zip/agent-orchestrator-darwin-x64.zip"]);

		await config.hooks?.postMake?.({} as never, makeResults);

		expect(verifyMacArtifact).toHaveBeenCalledWith("/out/make/zip/agent-orchestrator-darwin-x64.zip");
	});

	it("skips zip verification for an unsigned local build", async () => {
		isSigningConfigured.mockReturnValue(false);
		const makeResults = darwinResult(["/out/make/zip/agent-orchestrator-darwin-x64.zip"]);

		await config.hooks?.postMake?.({} as never, makeResults);

		expect(verifyMacArtifact).not.toHaveBeenCalled();
	});

	it("still seals and verifies the dmg through the existing sealDmg/verifyDmg path", async () => {
		isSigningConfigured.mockReturnValue(true);
		sealDmg.mockResolvedValue(true);
		const makeResults = darwinResult(["/out/make/app.dmg"]);

		await config.hooks?.postMake?.({} as never, makeResults);

		expect(sealDmg).toHaveBeenCalledWith("/out/make/app.dmg");
		expect(verifyDmg).toHaveBeenCalledWith("/out/make/app.dmg");
		expect(verifyMacArtifact).not.toHaveBeenCalled();
	});

	it("verifies both the dmg and the zip when a make run produces both", async () => {
		isSigningConfigured.mockReturnValue(true);
		sealDmg.mockResolvedValue(true);
		const makeResults = darwinResult([
			"/out/make/zip/agent-orchestrator-darwin-x64.zip",
			"/out/make/app.dmg",
		]);

		await config.hooks?.postMake?.({} as never, makeResults);

		expect(verifyMacArtifact).toHaveBeenCalledWith("/out/make/zip/agent-orchestrator-darwin-x64.zip");
		expect(verifyDmg).toHaveBeenCalledWith("/out/make/app.dmg");
	});

	it("never verifies non-darwin artifacts", async () => {
		const makeResults = [
			{
				artifacts: ["/out/make/agent-orchestrator.exe"],
				packageJSON: {},
				platform: "win32" as const,
				arch: "x64" as const,
			},
		];

		await config.hooks?.postMake?.({} as never, makeResults);

		expect(sealDmg).not.toHaveBeenCalled();
		expect(verifyDmg).not.toHaveBeenCalled();
		expect(verifyMacArtifact).not.toHaveBeenCalled();
	});
});

describe("packaged authentication callback registration", () => {
	it("declares ao-app in the macOS bundle and Linux package metadata", () => {
		expect(config.packagerConfig?.protocols).toEqual([
			{
				name: "Agent Orchestrator authentication callback",
				schemes: ["ao-app"],
			},
		]);

		const makers = config.makers as Array<{
			name?: string;
			config?: { options?: { mimeType?: string[] } };
		}>;
		for (const name of [
			"@electron-forge/maker-deb",
			"@electron-forge/maker-rpm",
		]) {
			const maker = makers.find((candidate) => candidate.name === name);
			expect(maker?.config?.options?.mimeType).toEqual([
				"x-scheme-handler/ao-app",
			]);
		}
	});
});
