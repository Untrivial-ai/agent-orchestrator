import { describe, expect, it } from "vitest";
import { isCloudSignInConfigured } from "./cloud-session";

describe("isCloudSignInConfigured", () => {
  it("hides Cloud sign-in when the WorkOS client ID is absent", () => {
    expect(isCloudSignInConfigured(undefined)).toBe(false);
    expect(isCloudSignInConfigured("   ")).toBe(false);
  });

  it("shows Cloud sign-in when the WorkOS client ID is configured", () => {
    expect(isCloudSignInConfigured("client_123")).toBe(true);
  });
});
