import { describe, expect, it } from "vitest";
import { macPlatformFromRenderer, Platform } from "./useOS";

describe("macPlatformFromRenderer", () => {
	it.each([
		"Intel Iris OpenGL Engine",
		"AMD Radeon Pro 5500M OpenGL Engine",
		"ATI Radeon HD 5770 OpenGL Engine",
		"NVIDIA GeForce GT 750M OpenGL Engine",
	])("selects the Intel build for an Intel-era Mac GPU: %s", (renderer) => {
		expect(macPlatformFromRenderer(renderer)).toBe(Platform.MacIntel);
	});

	it.each([
		"Apple M3",
		"Apple GPU",
		"ANGLE (Google, Vulkan 1.3.0 (SwiftShader Device))",
		"WebKit WebGL",
		"",
		undefined,
	])("defaults to Apple Silicon for an Apple or ambiguous renderer: %s", (renderer) => {
		expect(macPlatformFromRenderer(renderer)).toBe(Platform.MacAppleSilicon);
	});
});
