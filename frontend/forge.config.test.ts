import { afterEach, describe, expect, it, vi } from "vitest";
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
