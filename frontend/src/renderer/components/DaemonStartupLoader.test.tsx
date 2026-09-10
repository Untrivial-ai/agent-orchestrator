import { act, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { DaemonStartupLoader } from "./DaemonStartupLoader";

vi.mock("../hooks/useSystemRequirementsGate", () => ({
	useSystemRequirementsGate: () => ({
		query: { isSuccess: true, refetch: vi.fn() },
		requirements: [
			{ id: "git", label: "git", satisfied: true, required: true, detail: "/usr/bin/git" },
			{ id: "tmux", label: "tmux", satisfied: true, required: true, detail: "/usr/bin/tmux" },
			{ id: "gh", label: "gh", satisfied: true, required: false, detail: "/usr/bin/gh" },
		],
		blocked: false,
		requirementsBlocked: false,
	}),
}));

describe("DaemonStartupLoader", () => {
	afterEach(() => vi.useRealTimers());

	it("shows startup progress while the lightweight requirements preflight runs", () => {
		vi.useFakeTimers();
		render(<DaemonStartupLoader />);

		expect(screen.getByRole("status", { name: "Agent Orchestrator is starting" })).toBeInTheDocument();
		expect(screen.getByText("Starting local services")).not.toHaveClass("ao-startup-status");
		act(() => vi.advanceTimersByTime(2_200));
		expect(screen.getByText("Connecting to the daemon")).toHaveClass("ao-startup-status");
	});
});
