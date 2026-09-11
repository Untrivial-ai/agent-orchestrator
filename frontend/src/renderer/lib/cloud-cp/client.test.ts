import { describe, expect, it, vi } from "vitest";
import { createCloudCpClient } from "./client";

describe("personal Coder provider client", () => {
	it("lists account-scoped provider connections", async () => {
		const fetchImpl = vi.fn<typeof fetch>().mockResolvedValue(
			new Response(JSON.stringify({ providerConnections: [] }), {
				status: 200,
				headers: { "Content-Type": "application/json" },
			}),
		);
		const client = createCloudCpClient({
			baseUrl: "https://cloud.example.com",
			getToken: async () => "ao-token",
			fetchImpl,
		});

		await expect(client.listUserProviderConnections()).resolves.toEqual({
			providerConnections: [],
		});
		expect(fetchImpl).toHaveBeenCalledWith(
			"https://cloud.example.com/api/cloud/v1/me/providers",
			expect.objectContaining({ method: "GET" }),
		);
	});

	it("sends Coder setup details only to the personal provider route", async () => {
		const fetchImpl = vi.fn<typeof fetch>().mockResolvedValue(
			new Response(
				JSON.stringify({
					providerConnection: {
						id: "connection-1",
						provider: "coder",
						label: "default",
						config: { baseUrl: "https://coder.example.com" },
						validationState: "valid",
						createdAt: "2026-01-01T00:00:00Z",
						updatedAt: "2026-01-01T00:00:00Z",
					},
				}),
				{ status: 200, headers: { "Content-Type": "application/json" } },
			),
		);
		const client = createCloudCpClient({
			baseUrl: "https://cloud.example.com",
			getToken: async () => "ao-token",
			fetchImpl,
		});
		const body = {
			baseUrl: "https://coder.example.com",
			apiToken: "coder-secret",
			templateId: "2a2e262c-b31c-4202-946d-a19ad45d1fd2",
			durableRoot: "/workspace",
		};

		await client.putUserCoderConnection(body);
		const [url, init] = fetchImpl.mock.calls[0] ?? [];
		expect(url).toBe("https://cloud.example.com/api/cloud/v1/me/providers/coder");
		expect(init?.method).toBe("PUT");
		expect(JSON.parse(String(init?.body))).toEqual(body);
		expect(new Headers(init?.headers).get("Authorization")).toBe("Bearer ao-token");
	});
});
