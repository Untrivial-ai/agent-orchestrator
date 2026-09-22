import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { CommandCueCards } from "./CommandCueCards";
import { resetCommandCueStore, useCommandCueStore } from "../../stores/command-cue-store";
import { getCommandCueTerminalStatus, stopCommandCueTerminal } from "../../hooks/useShellTerminals";

vi.mock("../../hooks/useShellTerminals", () => ({
	getCommandCueTerminalStatus: vi.fn(),
	stopCommandCueTerminal: vi.fn(),
}));

function register(handleId = "shellterm-cue") {
	useCommandCueStore.getState().register({
		projectId: "project",
		sessionId: "session",
		handleId,
		name: "Test command",
		command: "npm test -- --run",
		state: "starting",
	});
}

beforeEach(() => {
	resetCommandCueStore();
	vi.resetAllMocks();
	vi.mocked(getCommandCueTerminalStatus).mockResolvedValue({ handleId: "shellterm-cue", state: "running" });
	vi.mocked(stopCommandCueTerminal).mockResolvedValue({ handleId: "shellterm-cue", state: "stopped" });
});

afterEach(() => {
	cleanup();
	vi.useRealTimers();
});

test("shows only this session's command cards and tracks terminal exit", async () => {
	register();
	useCommandCueStore.getState().register({
		projectId: "project",
		sessionId: "other",
		handleId: "other-handle",
		name: "Other command",
		command: "echo other",
		state: "running",
	});
	vi.mocked(getCommandCueTerminalStatus).mockResolvedValue({ handleId: "shellterm-cue", state: "exited" });

	useCommandCueStore.getState().register({
		projectId: "other-project",
		sessionId: "session",
		handleId: "other-project-handle",
		name: "Other project command",
		command: "echo wrong project",
		state: "running",
	});
	render(<CommandCueCards projectId="project" sessionId="session" onViewTerminal={vi.fn()} />);
	expect(screen.getByText("npm test -- --run")).toBeInTheDocument();
	expect(screen.queryByText("Other command")).toBeNull();
	expect(screen.queryByText("Other project command")).toBeNull();
	await screen.findByText("Exited");
	expect(getCommandCueTerminalStatus).toHaveBeenCalledTimes(1);
});

test("opens the terminal and explicitly unlocks input", async () => {
	register();
	const onView = vi.fn();
	render(<CommandCueCards projectId="project" sessionId="session" onViewTerminal={onView} />);

	fireEvent.click(screen.getByRole("button", { name: "View terminal" }));
	expect(onView).toHaveBeenCalledWith("shellterm-cue");
	fireEvent.click(screen.getByRole("button", { name: "Enable input" }));
	expect(useCommandCueStore.getState().cards["shellterm-cue"].inputEnabled).toBe(true);
});

test("keeps failed stop confirmation open for retry and blocks duplicate stops", async () => {
	register();
	let reject!: (error: Error) => void;
	const firstStop = new Promise<never>((_, no) => { reject = no; });
	vi.mocked(stopCommandCueTerminal).mockReturnValueOnce(firstStop);
	render(<CommandCueCards projectId="project" sessionId="session" onViewTerminal={vi.fn()} />);
	await screen.findByText("Running");

	fireEvent.click(screen.getByRole("button", { name: "Stop" }));
	const dialog = screen.getByRole("dialog", { name: "Stop this command?" });
	const confirm = within(dialog).getByRole("button", { name: "Stop command" });
	fireEvent.click(confirm);
	fireEvent.click(confirm);
	expect(stopCommandCueTerminal).toHaveBeenCalledTimes(1);
	await act(async () => reject(new Error("stop failed")));
	expect(await within(dialog).findByRole("alert")).toHaveTextContent("stop failed");

	fireEvent.click(confirm);
	await waitFor(() => expect(screen.queryByRole("dialog", { name: "Stop this command?" })).toBeNull());
	expect(stopCommandCueTerminal).toHaveBeenCalledTimes(2);
	expect(useCommandCueStore.getState().cards["shellterm-cue"].state).toBe("stopped");
});

test("marks a card failed when status polling fails and stops polling", async () => {
	register();
	vi.mocked(getCommandCueTerminalStatus).mockRejectedValue(new Error("daemon offline"));
	render(<CommandCueCards projectId="project" sessionId="session" onViewTerminal={vi.fn()} />);
	await screen.findByText("Failed");
	expect(screen.getByRole("alert")).toHaveTextContent("daemon offline");
	expect(getCommandCueTerminalStatus).toHaveBeenCalledTimes(1);
});
