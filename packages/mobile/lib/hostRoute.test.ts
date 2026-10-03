import { describe, expect, it } from "vitest";
import type { ServerConfig } from "./config";
import { hostRouteMatches, openingHostId, previewForConfig, spawnHostMatches } from "./hostRoute";

describe("host-qualified mobile routes", () => {
	it("does not treat B's same project ID as A's project", () => {
		expect(hostRouteMatches("host-a", "host-b")).toBe(false);
		expect(hostRouteMatches(undefined, "host-b")).toBe(false);
		expect(hostRouteMatches("host-a", "host-a")).toBe(true);
	});

	it("binds a generic composer to its opening machine", () => {
		const openedOn = openingHostId(undefined, "host-a");
		expect(openedOn).toBe("host-a");
		expect(hostRouteMatches(openedOn, "host-b")).toBe(false);
	});

	it("keeps an explicit A link tied to A, even if B is selected", () => {
		expect(openingHostId("host-a", "host-b")).toBe("host-a");
		expect(openingHostId(undefined, undefined)).toBeUndefined();
	});

	it("does not adopt an old bare project ID deep link on B", () => {
		expect(spawnHostMatches({ openedHostId: "host-b", currentHostId: "host-b", routeProjectId: "same-id" }))
			.toBe(false);
		expect(spawnHostMatches({ openedHostId: "host-b", currentHostId: "host-b", routeProjectId: "same-id", routeHostId: "host-b" }))
			.toBe(true);
	});

	it("never pairs A's preview URL with B's credential or a changed endpoint", () => {
		const a = { hostId: "host-a", host: "alice", password: "a" } as ServerConfig;
		const b = { hostId: "host-b", host: "bob", password: "b" } as ServerConfig;
		const loaded = { config: a, value: { url: "http://alice/preview", authenticated: true } };
		expect(previewForConfig(loaded, a, "host-a")).toEqual(loaded.value);
		expect(previewForConfig(loaded, a, undefined)).toBeNull();
		expect(previewForConfig(loaded, b, "host-a")).toBeNull();
		expect(previewForConfig(loaded, b, "host-b")).toBeNull();
		expect(previewForConfig(loaded, { ...a, host: "new-address" }, "host-a")).toBeNull();
	});
});
