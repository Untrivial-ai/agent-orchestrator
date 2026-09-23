import { act, render } from "@testing-library/react";
import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import { RendererAgentSwitchVisibility } from "../lib/agent-switch-visibility";
import { useAgentSwitchPresentationVisibility, useAgentSwitchRouteVisibility } from "./useAgentSwitchVisibility";

describe("agent switch visibility hooks", () => {
	it("registers focus and online ownership and cancels on route unmount", () => {
		const send = vi.fn(); const coordinator = new RendererAgentSwitchVisibility(send);
		const view = render(<Route coordinator={coordinator} />);
		expect(send).toHaveBeenCalledWith({ kind: "focus", value: document.hasFocus() });
		expect(send).toHaveBeenCalledWith({ kind: "online", value: navigator.onLine });
		view.unmount();
		expect(send).toHaveBeenCalledWith({ kind: "transport", operation: "history", healthy: true, active: false });
	});

	it("expects in a passive effect, acknowledges after commit, and cancels on dismissal", () => {
		const send = vi.fn(); const coordinator = new RendererAgentSwitchVisibility(send);
		let dismiss: () => void = () => undefined;
		const tokenFactory = () => "token";
		function Surface() {
			const [visible, setVisible] = useState(true); dismiss = () => setVisible(false);
			useAgentSwitchPresentationVisibility({ coordinator, localRouteKey: "route", agentSwitch: { id: "switch", state: "failed", errorCode: "switch_failed", fromHarness: "claude-code", targetHarness: "codex", agentHandoffStatus: "not_attempted", updatedAt: "revision" }, presentationKind: "terminal_failure", visible, tokenFactory });
			return visible ? <div role="alert">failed</div> : null;
		}
		render(<Surface />);
		expect(send.mock.calls.map(([value]) => value.kind)).toEqual(["expected_presentation", "presented"]);
		act(() => dismiss());
		expect(send).toHaveBeenLastCalledWith({ kind: "cancel", token: "token" });
	});

	it("does not expect a normal historical failure that is not visible", () => {
		const send = vi.fn(); const coordinator = new RendererAgentSwitchVisibility(send);
		function Surface() {
			useAgentSwitchPresentationVisibility({ coordinator, localRouteKey: "route", agentSwitch: { id: "historical", state: "failed", errorCode: "switch_failed", fromHarness: "claude-code", targetHarness: "codex", agentHandoffStatus: "not_attempted", updatedAt: "revision" }, presentationKind: "terminal_failure", visible: false });
			return null;
		}
		render(<Surface />);
		expect(send).not.toHaveBeenCalled();
	});
});

function Route({ coordinator }: { coordinator: RendererAgentSwitchVisibility }) {
	useAgentSwitchRouteVisibility("session/one", "history", coordinator);
	return null;
}
