import { describe, expect, it } from "vitest";
import config, { macSignOptionsForFile } from "./forge.config";

describe("macOS signing", () => {
	it("allows the bundled Node runtime to execute V8 JIT code on Intel Macs", () => {
		expect(
			macSignOptionsForFile(
				"/tmp/Agent Orchestrator.app/Contents/Resources/acp-runtime/node/bin/node",
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
