import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, expect, it, vi } from "vitest";
import { RestartToUpdateDialog } from "./RestartToUpdateDialog";
import { useUiStore } from "../stores/ui-store";
import { TooltipProvider } from "./ui/tooltip";
import type { UpdateStatus } from "../../main/update-settings";

const { updInstall, updGetStatus, updOnStatus, workspaceData } = vi.hoisted(() => ({
	updInstall: vi.fn(),
	updGetStatus: vi.fn(),
	updOnStatus: vi.fn(),
	workspaceData: { current: [] as unknown[] },
}));

vi.mock("../lib/bridge", () => ({
	aoBridge: { updates: { getStatus: updGetStatus, install: updInstall, onStatus: updOnStatus } },
}));
vi.mock("../hooks/useWorkspaceQuery", () => ({
	useWorkspaceQuery: () => ({ data: workspaceData.current }),
}));

function session(overrides: Record<string, unknown> = {}) {
	return {
		id: "s1",
		title: "Fix the updater",
		workspaceName: "agent-orchestrator",
		provider: "claude-code",
		mode: "chat",
		status: "working",
		...overrides,
	};
}

function renderDialog(status: UpdateStatus) {
	updGetStatus.mockResolvedValue(status);
	render(
		<TooltipProvider>
			<RestartToUpdateDialog />
		</TooltipProvider>,
	);
}

beforeEach(() => {
	for (const m of [updInstall, updGetStatus, updOnStatus]) m.mockReset();
	updOnStatus.mockReturnValue(() => undefined);
	workspaceData.current = [];
	useUiStore.setState({ updateInstallPromptOpen: false });
});

it("renders nothing at all while closed", () => {
	renderDialog({ state: "downloaded" });
	expect(screen.queryByTestId("restart-to-update-dialog")).toBeNull();
	// Gated before the hooks run, so the status channel is never subscribed.
	expect(updGetStatus).not.toHaveBeenCalled();
	expect(updOnStatus).not.toHaveBeenCalled();
});

it("shows what the build changes", async () => {
	useUiStore.setState({ updateInstallPromptOpen: true });
	renderDialog({
		state: "downloaded",
		version: "0.12.11-nightly.202609021713",
		releaseNotes: "Fixed the re-stage loop\nRebuilt the Updates page",
	});
	expect(await screen.findByText(/Fixed the re-stage loop/)).toBeVisible();
	expect(screen.getByText("Nightly 0.12.11 · Sep 2")).toBeVisible();
});

it("names the sessions that would lose a turn and waits for confirmation", async () => {
	workspaceData.current = [
		{ sessions: [session(), session({ id: "s2", mode: "tui", title: "Terminal one" })] },
	];
	useUiStore.setState({ updateInstallPromptOpen: true });
	renderDialog({ state: "downloaded", version: "1.2.3" });

	const warning = await screen.findByTestId("restart-sessions-warning");
	expect(warning).toHaveTextContent("1 chat session will lose its current turn");
	expect(warning).toHaveTextContent("agent-orchestrator · Fix the updater");
	// The TUI session survives a quit, so naming it would be crying wolf.
	expect(warning).not.toHaveTextContent("Terminal one");

	expect(updInstall).not.toHaveBeenCalled();
	await userEvent.click(screen.getByRole("button", { name: "Install and restart" }));
	expect(updInstall).toHaveBeenCalledTimes(1);
});

it("stays quiet when nothing is at risk", async () => {
	workspaceData.current = [
		{
			sessions: [
				session({ mode: "tui" }),
				session({ id: "s2", provider: "codex", chatProviderPreserved: true }),
				session({ id: "s3", provider: "claude-code", chatProviderPreserved: true }),
				session({ id: "s4", provider: "cursor", chatProviderPreserved: true }),
			],
		},
	];
	useUiStore.setState({ updateInstallPromptOpen: true });
	renderDialog({ state: "downloaded", version: "1.2.3" });
	await screen.findByTestId("restart-to-update-dialog");
	expect(screen.queryByTestId("restart-sessions-warning")).toBeNull();
});

it.each([false, undefined])(
	"warns about Codex when persistent ownership is %s",
	async (chatProviderPreserved) => {
		workspaceData.current = [{ sessions: [session({ provider: "codex", chatProviderPreserved })] }];
		useUiStore.setState({ updateInstallPromptOpen: true });
		renderDialog({ state: "downloaded", version: "1.2.3" });

		const warning = await screen.findByTestId("restart-sessions-warning");
		expect(warning).toHaveTextContent("1 chat session will lose its current turn");
		expect(warning).toHaveTextContent("agent-orchestrator · Fix the updater");
		expect(updInstall).not.toHaveBeenCalled();
	},
);

it("cancelling never installs", async () => {
	useUiStore.setState({ updateInstallPromptOpen: true });
	renderDialog({ state: "downloaded", version: "1.2.3" });
	await screen.findByTestId("restart-to-update-dialog");
	await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
	expect(updInstall).not.toHaveBeenCalled();
	expect(useUiStore.getState().updateInstallPromptOpen).toBe(false);
});
