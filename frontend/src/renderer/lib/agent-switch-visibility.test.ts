import { describe, expect, it, vi } from "vitest";
import { RendererAgentSwitchVisibility } from "./agent-switch-visibility";

describe("RendererAgentSwitchVisibility", () => {
	it("ref-counts two route mounts and deactivates only after the last unmount", () => {
		const send = vi.fn(); const visibility = new RendererAgentSwitchVisibility(send);
		const first = visibility.registerRoute("session-a", "active");
		const second = visibility.registerRoute("session-a", "active");
		visibility.setTransportHealthy("active", false);
		first();
		expect(send).not.toHaveBeenCalledWith(expect.objectContaining({ kind: "transport", operation: "active", active: false }));
		second();
		expect(send).toHaveBeenCalledWith({ kind: "query", operation: "active", healthy: true, active: false });
	});

	it("exposes transport precedence to typed query reporters", () => {
		const send = vi.fn(); const visibility = new RendererAgentSwitchVisibility(send);
		visibility.registerRoute("session-a", "history");
		visibility.setTransportHealthy("history", false);
		expect(visibility.transportHealthy("history")).toBe(false);
		visibility.setQueryHealthy("history", false);
		expect(send).toHaveBeenLastCalledWith({ kind: "query", operation: "history", healthy: false, active: true });
	});

	it("forwards no sender identity with route-local presentation tokens", () => {
		const send = vi.fn(); const visibility = new RendererAgentSwitchVisibility(send);
		visibility.expectPresentation({ token: "token", switchId: "switch", updatedAt: "revision", localRouteKey: "route", presentationKind: "terminal_failure", durableState: "failed" });
		expect(send.mock.calls[0][0]).toEqual({ kind: "expected_presentation", token: "token", switchId: "switch", updatedAt: "revision", localRouteKey: "route", presentationKind: "terminal_failure", durableState: "failed" });
		expect(send.mock.calls[0][0]).not.toHaveProperty("senderId");
	});
});
