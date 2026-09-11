import { describe, it, expect } from "vitest";
import { providerConnectionsQueryKey } from "./useProviderConnections";

describe("useProviderConnections", () => {
	it("should have correct query key format", () => {
		const orgId = "org-456";
		const key = providerConnectionsQueryKey(orgId);

		expect(Array.isArray(key)).toBe(true);
		expect(key.length).toBe(3);
		expect(key[0]).toBe("cloud");
		expect(key[1]).toBe("provider-connections");
		expect(key[2]).toBe(orgId);
	});

	it("should generate different keys for different org IDs", () => {
		const key1 = providerConnectionsQueryKey("org-1");
		const key2 = providerConnectionsQueryKey("org-2");

		expect(key1).not.toEqual(key2);
		expect(key1[2]).toBe("org-1");
		expect(key2[2]).toBe("org-2");
	});

	it("should maintain consistent key structure", () => {
		const orgIds = ["org-123", "org-456", "org-789"];

		orgIds.forEach((orgId) => {
			const key = providerConnectionsQueryKey(orgId);
			expect(key).toEqual(["cloud", "provider-connections", orgId]);
		});
	});
});
