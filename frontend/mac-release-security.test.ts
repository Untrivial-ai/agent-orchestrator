import { describe, expect, it } from "vitest";
import {
	assertMacReleaseCredentials,
	isForgePublishCommand,
	macPackagerSecurity,
} from "./mac-release-security";

describe("macOS release credentials", () => {
	it("allows the intentionally unsigned public make path", () => {
		expect(() =>
			assertMacReleaseCredentials({}, "darwin", false),
		).not.toThrow();
	});

	it("fails closed when a direct macOS publish has no credentials", () => {
		expect(() => assertMacReleaseCredentials({}, "darwin", true)).toThrow(
			/macOS publish requires both signing and notarization credentials/,
		);
	});

	it("rejects incomplete App Store Connect credentials", () => {
		expect(() =>
			assertMacReleaseCredentials(
				{ APPLE_API_KEY: "/tmp/AuthKey.p8" },
				"darwin",
				false,
			),
		).toThrow(
			/APPLE_API_KEY, APPLE_API_KEY_ID, and APPLE_API_ISSUER must be set together/,
		);
	});

	it("rejects signing without notarization for any macOS artifact", () => {
		expect(() =>
			assertMacReleaseCredentials(
				{ APPLE_SIGNING_IDENTITY: "Developer ID Application" },
				"darwin",
				false,
			),
		).toThrow(
			/macOS packaging requires signing and notarization credentials together/,
		);
	});

	it("rejects notarization without signing for any macOS artifact", () => {
		expect(() =>
			assertMacReleaseCredentials(
				{ AO_NOTARY_PROFILE: "ao-notary" },
				"darwin",
				false,
			),
		).toThrow(
			/macOS packaging requires signing and notarization credentials together/,
		);
	});

	it("accepts a complete identity and App Store Connect credential set", () => {
		const env = {
			APPLE_SIGNING_IDENTITY: "Developer ID Application",
			APPLE_API_KEY: "/tmp/AuthKey.p8",
			APPLE_API_KEY_ID: "KEY123",
			APPLE_API_ISSUER: "issuer-id",
		};
		expect(() =>
			assertMacReleaseCredentials(env, "darwin", true),
		).not.toThrow();
		expect(macPackagerSecurity(env).osxSign).toMatchObject({
			identity: "Developer ID Application",
			hardenedRuntime: true,
			entitlements: "assets/entitlements.mac.plist",
			entitlementsInherit: "assets/entitlements.mac.inherit.plist",
		});
	});

	it("accepts a signing certificate link with a keychain notary profile", () => {
		const env = {
			CSC_LINK: "file:///certificate.p12",
			AO_NOTARY_PROFILE: "ao-notary",
		};
		expect(() =>
			assertMacReleaseCredentials(env, "darwin", true),
		).not.toThrow();
		expect(macPackagerSecurity(env).osxSign).toMatchObject({
			hardenedRuntime: true,
		});
	});

	it("does not apply the macOS guard to other platforms", () => {
		expect(() =>
			assertMacReleaseCredentials(
				{ APPLE_API_KEY: "/tmp/AuthKey.p8" },
				"linux",
				true,
			),
		).not.toThrow();
	});

	it("recognizes publish without confusing make for a publication", () => {
		expect(isForgePublishCommand(["node", "electron-forge", "publish"])).toBe(
			true,
		);
		expect(isForgePublishCommand(["node", "electron-forge", "make"])).toBe(
			false,
		);
	});
});
