import { act, fireEvent, render, screen } from "@testing-library/react";
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
	return render(
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
	expect(screen.queryByText(/Leave AO closed until it reopens/)).toBeNull();
});

it("renders the nightly build date from the UTC instant", async () => {
	// The stamp 202609070300 encodes 03:00 UTC. Near the UTC day boundary the
	// date-only dialog label must show the device-local calendar day of the
	// correct instant, not the stamp digits re-read as local wall time
	// (issue #5059). The expected label is derived from the absolute instant,
	// so this holds in every timezone.
	useUiStore.setState({ updateInstallPromptOpen: true });
	renderDialog({ state: "downloaded", version: "0.12.11-nightly.202609070300" });
	const expected = new Intl.DateTimeFormat("en", { month: "short", day: "numeric" }).format(
		new Date(Date.UTC(2026, 8, 7, 3, 0)),
	);
	expect(await screen.findByText(`Nightly 0.12.11 · ${expected}`)).toBeVisible();
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
	await userEvent.click(screen.getByRole("button", { name: "Restart & install" }));
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

function deferredInstall() {
	let resolve!: () => void;
	let reject!: (error: Error) => void;
	const promise = new Promise<void>((res, rej) => { resolve = res; reject = rej; });
	updInstall.mockReturnValue(promise);
	return { resolve, reject };
}

it("keeps notes and session risks visible, blocks duplicate submits and dismissal, shows a minimal preparing state, and closes on success", async () => {
	const install = deferredInstall();
	workspaceData.current = [{ sessions: [session()] }];
	useUiStore.setState({ updateInstallPromptOpen: true });
	renderDialog({ state: "downloaded", version: "1.2.3", releaseNotes: "Safer updates" });
	await screen.findByText("Safer updates");
	const confirm = screen.getByRole("button", { name: "Restart & install" });
	act(() => { fireEvent.click(confirm); fireEvent.click(confirm); });
	expect(updInstall).toHaveBeenCalledTimes(1);
	// Minimal working state: the button relabels and disables; no progress bar.
	expect(confirm).toBeDisabled();
	expect(confirm).toHaveTextContent("Preparing update…");
	expect(screen.getByRole("button", { name: "Cancel" })).toBeDisabled();
	expect(screen.queryByRole("button", { name: "Close" })).toBeNull();
	expect(screen.queryByRole("progressbar")).toBeNull();
	fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
	await userEvent.keyboard("{Escape}");
	const overlay = document.querySelector('[data-slot="dialog-overlay"]')!;
	fireEvent.pointerDown(overlay);
	fireEvent.click(overlay);
	expect(useUiStore.getState().updateInstallPromptOpen).toBe(true);
	expect(screen.getByTestId("restart-sessions-warning")).toBeVisible();
	// Notes and version stay visible through a status change, still no progress bar.
	act(() => updOnStatus.mock.calls[0][0]({ state: "downloading", percent: 42.5 }));
	expect(screen.queryByRole("progressbar")).toBeNull();
	expect(screen.getByText("Safer updates")).toBeVisible();
	expect(screen.getByText("v1.2.3")).toBeVisible();
	await act(async () => install.resolve());
	expect(screen.queryByTestId("restart-to-update-dialog")).toBeNull();
});

it("shows an inline failure and allows retry", async () => {
	const install = deferredInstall();
	useUiStore.setState({ updateInstallPromptOpen: true });
	renderDialog({ state: "downloaded", version: "1.2.3", releaseNotes: "Safer updates" });
	await screen.findByText("Safer updates");
	await userEvent.click(screen.getByRole("button", { name: "Restart & install" }));
	await act(async () => install.reject(new Error("Error invoking remote method 'updates:install': Error: macOS preparation timed out. Close AO and reopen it before trying again.")));
	expect(screen.getByRole("alert")).toHaveTextContent("AO could not prepare the update. Please try again.");
	expect(screen.getByRole("alert")).toHaveTextContent("macOS preparation timed out. Close AO and reopen it before trying again.");
	expect(screen.getByRole("alert")).not.toHaveTextContent("Error invoking remote method");
	expect(screen.getByText("Safer updates")).toBeVisible();
	expect(screen.getByRole("button", { name: "Cancel" })).toBeEnabled();
	expect(screen.getByRole("button", { name: "Close" })).toBeEnabled();
	expect(screen.queryByRole("progressbar")).toBeNull();
	const retry = deferredInstall();
	await userEvent.click(screen.getByRole("button", { name: "Restart & install" }));
	expect(updInstall).toHaveBeenCalledTimes(2);
	expect(screen.queryByRole("alert")).toBeNull();
	expect(screen.queryByText(/Close AO and reopen it/)).toBeNull();
	await act(async () => retry.resolve());
	expect(useUiStore.getState().updateInstallPromptOpen).toBe(false);
});

it("allows cancelling after preparation fails", async () => {
	updInstall.mockRejectedValue(new Error("Preparation failed"));
	useUiStore.setState({ updateInstallPromptOpen: true });
	renderDialog({ state: "downloaded", version: "1.2.3" });
	await screen.findByText("v1.2.3");
	await userEvent.click(screen.getByRole("button", { name: "Restart & install" }));
	await screen.findByRole("alert");
	await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
	expect(useUiStore.getState().updateInstallPromptOpen).toBe(false);
});

it.each(["resolve", "reject"] as const)("ignores an install %s after unmount", async (result) => {
	const install = deferredInstall();
	useUiStore.setState({ updateInstallPromptOpen: true });
	const view = renderDialog({ state: "downloaded", version: "1.2.3" });
	await screen.findByText("v1.2.3");
	await userEvent.click(screen.getByRole("button", { name: "Restart & install" }));
	view.unmount();
	// A subsequent dialog must not be closed by the previous mount's promise.
	renderDialog({ state: "downloaded", version: "1.2.3" });
	await act(async () => {
		if (result === "resolve") install.resolve();
		else install.reject(new Error("Preparation failed"));
	});
	expect(useUiStore.getState().updateInstallPromptOpen).toBe(true);
	expect(screen.queryByRole("alert")).toBeNull();
	expect(screen.getByRole("button", { name: "Restart & install" })).toBeEnabled();
});

it("renders bounded recovery details as plain text", async () => {
	const message = "<strong>Fix folder permissions</strong> " + "x".repeat(1500);
	updInstall.mockRejectedValue(new Error(message));
	useUiStore.setState({ updateInstallPromptOpen: true });
	renderDialog({ state: "downloaded", version: "1.2.3" });
	await screen.findByText("v1.2.3");
	await userEvent.click(screen.getByRole("button", { name: "Restart & install" }));
	const alert = await screen.findByRole("alert");
	expect(alert).toHaveTextContent(message.slice(0, 1000));
	expect(alert.querySelector("strong")).toBeNull();
	expect(alert.lastElementChild?.textContent).toHaveLength(1000);
});

it("shows the replacement build and requires another explicit confirmation", async () => {
	useUiStore.setState({ updateInstallPromptOpen: true });
	updInstall.mockResolvedValueOnce({ state: "confirmation-required", version: "2.2.0", releaseNotes: "New release B" });
	renderDialog({ state: "downloaded", version: "2.1.0", releaseNotes: "Old release A" });
	await screen.findByText("Old release A");
	await userEvent.click(screen.getByRole("button", { name: "Restart & install" }));
	expect(updInstall).toHaveBeenCalledWith("2.1.0");
	expect(await screen.findByText("New release B")).toBeVisible();
	expect(screen.getByText("v2.2.0")).toBeVisible();
	expect(screen.queryByText("Old release A")).toBeNull();
	expect(useUiStore.getState().updateInstallPromptOpen).toBe(true);
	expect(updInstall).toHaveBeenCalledTimes(1);
	await userEvent.click(screen.getByRole("button", { name: "Restart & install" }));
	expect(updInstall).toHaveBeenLastCalledWith("2.2.0");
	expect(useUiStore.getState().updateInstallPromptOpen).toBe(false);
});

it("does not reuse old release notes when a replacement has none and allows cancelling", async () => {
	useUiStore.setState({ updateInstallPromptOpen: true });
	updInstall.mockResolvedValueOnce({ state: "confirmation-required", version: "2.2.0" });
	renderDialog({ state: "downloaded", version: "2.1.0", releaseNotes: "Old release A" });
	await screen.findByText("Old release A");
	await userEvent.click(screen.getByRole("button", { name: "Restart & install" }));
	expect(await screen.findByText("v2.2.0")).toBeVisible();
	expect(screen.queryByText("Old release A")).toBeNull();
	expect(screen.getByRole("status")).toHaveTextContent("confirm again");
	await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
	expect(updInstall).toHaveBeenCalledTimes(1);
	expect(useUiStore.getState().updateInstallPromptOpen).toBe(false);
});
