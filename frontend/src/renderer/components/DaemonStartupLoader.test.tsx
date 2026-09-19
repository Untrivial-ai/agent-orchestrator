import { act, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { aoBridge } from "../lib/bridge";
import { DaemonStartupLoader } from "./DaemonStartupLoader";

vi.mock("../hooks/useSystemRequirementsGate", () => ({
	useSystemRequirementsGate: () => ({
		query: { isSuccess: true, refetch: vi.fn() },
		requirements: [
			{ id: "git", label: "git", satisfied: true, required: true, detail: "/usr/bin/git" },
			{ id: "tmux", label: "tmux", satisfied: true, required: false, detail: "/usr/bin/tmux" },
			{ id: "gh", label: "gh", satisfied: true, required: false, detail: "/usr/bin/gh" },
		],
		blocked: false,
		requirementsBlocked: false,
	}),
}));

describe("DaemonStartupLoader", () => {
	afterEach(() => {
		vi.useRealTimers();
		vi.restoreAllMocks();
	});

	it("shows startup progress while the lightweight requirements preflight runs", () => {
		vi.useFakeTimers();
		render(<DaemonStartupLoader />);

		expect(screen.getByRole("status", { name: "Agent Orchestrator is starting" })).toBeInTheDocument();
		expect(screen.getByText("Starting local services")).not.toHaveClass("ao-startup-status");
		act(() => vi.advanceTimersByTime(2_200));
		expect(screen.getByText("Connecting to the daemon")).toHaveClass("ao-startup-status");
	});

	it("shows update-specific progress after a post-update relaunch", async () => {
		vi.useFakeTimers();
		vi.spyOn(aoBridge.updates, "isPostUpdateRelaunch").mockResolvedValue(true);
		render(<DaemonStartupLoader />);

		await act(async () => Promise.resolve());
		expect(screen.getByText("Updating AO")).toBeInTheDocument();
		act(() => vi.advanceTimersByTime(2_200));
		expect(screen.getByText("Restarting AO")).toHaveClass("ao-startup-status");
	});

	it("keeps showing normal startup progress when the relaunch check fails", async () => {
		vi.spyOn(aoBridge.updates, "isPostUpdateRelaunch").mockRejectedValue(new Error("IPC unavailable"));
		render(<DaemonStartupLoader />);

		await act(async () => Promise.resolve());
		expect(screen.getByText("Starting local services")).toBeInTheDocument();
	});
});
