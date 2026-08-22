import { describe, expect, it } from "vitest";
import { isCloudSignInConfigured } from "./cloud-session";

describe("isCloudSignInConfigured", () => {
	it("hides Cloud sign-in when the Google client ID is absent", () => {
		expect(isCloudSignInConfigured(undefined, "https://cloud.example")).toBe(false);
		expect(isCloudSignInConfigured("   ", "https://cloud.example")).toBe(false);
	});

	it("hides Cloud sign-in when the API URL is absent", () => {
		expect(isCloudSignInConfigured("google-client.apps.googleusercontent.com", undefined)).toBe(false);
		expect(isCloudSignInConfigured("google-client.apps.googleusercontent.com", "   ")).toBe(false);
	});

	it("shows Cloud sign-in when Google auth and the API are configured", () => {
		expect(
			isCloudSignInConfigured("google-client.apps.googleusercontent.com", "https://cloud.example"),
		).toBe(true);
	});
});
