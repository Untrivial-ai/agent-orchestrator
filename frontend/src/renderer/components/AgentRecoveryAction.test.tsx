import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { AgentRecoveryAction } from "./AgentRecoveryAction";

describe("AgentRecoveryAction", () => {
	it("renders nothing when recovery is unnecessary", () => {
		const { container } = render(
			<AgentRecoveryAction action={null} agentLabel="Claude Code" onAction={vi.fn()} />,
		);

		expect(container).toBeEmptyDOMElement();
	});

	it.each([
		["install", "Install agent"],
		["login", "Log in"],
		["review", "Review configuration"],
		["configure", "Configure agent"],
	] as const)("invokes the %s recovery action from a standalone button", (action, label) => {
		const onAction = vi.fn();
		render(
			<div role="toolbar" aria-label="Agent actions">
				<AgentRecoveryAction
					action={action}
					agentLabel="Claude Code"
					onAction={onAction}
					variant="compact"
				/>
			</div>,
		);

		fireEvent.click(screen.getByRole("button", { name: label }));

		expect(onAction).toHaveBeenCalledWith(action);
		expect(screen.queryByRole("menuitem")).not.toBeInTheDocument();
	});

	it("explains the recovery before presenting its action", () => {
		render(
			<AgentRecoveryAction action="login" agentLabel="Claude Code" onAction={vi.fn()} variant="explanatory" />,
		);

		expect(screen.getByText("Claude Code needs authentication before it can start." )).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Log in" })).toBeInTheDocument();
	});

	it("keeps the compact toolbar variant free of explanatory copy", () => {
		render(
			<AgentRecoveryAction action="configure" agentLabel="Codex" onAction={vi.fn()} variant="compact" />,
		);

		expect(screen.queryByText("AO could not confirm Codex readiness." )).not.toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Configure agent" })).toBeInTheDocument();
	});
});
