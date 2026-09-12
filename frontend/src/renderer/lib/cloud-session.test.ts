import { describe, expect, it } from "vitest";
import { isCloudSignInConfigured } from "./cloud-session";

describe("isCloudSignInConfigured", () => {
  it("is configured by default via the baked WorkOS client id", () => {
    // The client id ships as a baked default, so with no explicit argument
    // sign-in is always configured; the cloud offering toggle is the real gate.
    expect(isCloudSignInConfigured(undefined)).toBe(true);
  });

  it("treats an explicitly blank client ID as not configured", () => {
    expect(isCloudSignInConfigured("   ")).toBe(false);
  });

  it("shows Cloud sign-in when the WorkOS client ID is configured", () => {
    expect(isCloudSignInConfigured("client_123")).toBe(true);
  });
});
