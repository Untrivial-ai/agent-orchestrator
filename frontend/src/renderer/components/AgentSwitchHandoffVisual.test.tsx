import { act, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { AgentSwitchHandoffVisual } from "./AgentSwitchHandoffVisual";

describe("AgentSwitchHandoffVisual", () => {
	it("renders both agent marks joined by an animated dotted curve", () => {
		const { container } = render(<AgentSwitchHandoffVisual fromHarness="codex" targetHarness="claude-code" stage="preparing" />);

		expect(screen.getByTestId("agent-switch-handoff-visual")).toHaveAttribute("data-active-step", "preparing");
		const curve = screen.getByTestId("agent-switch-handoff-curve");
		expect(curve.querySelector(".agent-switch-handoff-track")).not.toBeNull();
		expect(curve.querySelector(".agent-switch-handoff-flow")).not.toBeNull();
		expect(curve.querySelector(".agent-switch-handoff-orb")).not.toBeNull();
		// Non-uniform scaling stretched the curve and turned the orb into an ellipse.
		expect(curve).not.toHaveAttribute("preserveAspectRatio", "none");
		expect(screen.getByText("Codex")).toBeInTheDocument();
		expect(screen.getByText("Claude Code")).toBeInTheDocument();
		// Both agent logos render as real images (not fallback initials).
		expect(container.querySelectorAll("img")).toHaveLength(2);
	});

	it("keeps both agent marks at the ends of the curve in the compact variant", () => {
		const { container } = render(<AgentSwitchHandoffVisual fromHarness="codex" targetHarness="claude-code" stage="preparing" variant="compact" />);

		// Compact hides the text labels but still renders both logos.
		expect(container.querySelectorAll("img")).toHaveLength(2);
		expect(screen.queryByText("Codex")).not.toBeInTheDocument();
		expect(screen.queryByText("Claude Code")).not.toBeInTheDocument();
		expect(screen.getByTestId("agent-switch-handoff-curve")).toBeInTheDocument();
	});

	it("shows a single toast with a breathing orb for the active stage", () => {
		render(<AgentSwitchHandoffVisual fromHarness="codex" targetHarness="claude-code" stage="preparing" />);

		const toast = screen.getByTestId("agent-switch-handoff-toast");
		expect(toast).toHaveAttribute("data-phase", "working");
		expect(toast).toHaveAttribute("data-step", "preparing");
		expect(toast.querySelector(".agent-switch-handoff-orb-core")).not.toBeNull();
		expect(screen.getByTestId("agent-switch-handoff-toast-label")).toHaveTextContent("Preparing handoff");
	});

	it("announces the active stage from a screen-reader-only live region", () => {
		render(<AgentSwitchHandoffVisual fromHarness="codex" targetHarness="claude-code" stage="stopping_source" />);

		const label = screen.getByTestId("agent-switch-handoff-active-label");
		expect(label).toHaveClass("sr-only");
		expect(label).toHaveAttribute("aria-live", "polite");
		expect(label).toHaveTextContent("Stopping source agent");
	});

	it("resolves the orb to a green tick then advances to the next stage label", () => {
		vi.useFakeTimers();
		const { rerender } = render(
			<AgentSwitchHandoffVisual fromHarness="codex" targetHarness="claude-code" stage="preparing" />,
		);

		const toast = () => screen.getByTestId("agent-switch-handoff-toast");
		expect(toast()).toHaveAttribute("data-step", "preparing");
		expect(toast()).toHaveAttribute("data-phase", "working");
		expect(screen.getByTestId("agent-switch-handoff-active-label")).toHaveTextContent("Preparing handoff");

		// Daemon advances to the next stage.
		rerender(<AgentSwitchHandoffVisual fromHarness="codex" targetHarness="claude-code" stage="stopping_source" />);

		// The completing stage flashes a green tick while its label stays put.
		expect(toast()).toHaveAttribute("data-phase", "resolved");
		expect(toast()).toHaveAttribute("data-step", "preparing");
		expect(screen.getByTestId("agent-switch-handoff-toast-label")).toHaveTextContent("Preparing handoff");

		// After the 480 ms handoff delay the toast moves on to the next stage.
		act(() => {
			vi.advanceTimersByTime(500);
		});
		expect(toast()).toHaveAttribute("data-step", "stopping_source");
		expect(toast()).toHaveAttribute("data-phase", "working");
		expect(screen.getByTestId("agent-switch-handoff-toast-label")).toHaveTextContent("Stopping source agent");

		vi.useRealTimers();
	});
});
