import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { EditorHandoffState, OpenSessionTargetInput } from "../../shared/editor-handoff";
import { TopbarOpenEditorButton } from "./TopbarOpenEditorButton";
import { TooltipProvider } from "./ui/tooltip";

vi.mock("../lib/telemetry", () => ({ captureRendererEvent: vi.fn() }));

const openMock = vi.fn(async ({ targetId }: OpenSessionTargetInput) => {
	if (targetId === "file-manager") return { id: "file-manager" as const, name: "Finder", kind: "file_manager" as const };
	if (targetId === "terminal") return { id: "terminal" as const, name: "Terminal", kind: "terminal" as const };
	if (targetId === "vscode") return { id: "vscode" as const, name: "VS Code", kind: "editor" as const };
	return { id: "cursor" as const, name: "Cursor", kind: "editor" as const };
});

const availableState: EditorHandoffState = {
	targets: [
		{ id: "cursor", name: "Cursor", kind: "editor" },
		{ id: "vscode", name: "VS Code", kind: "editor" },
		{ id: "file-manager", name: "Finder", kind: "file_manager" },
		{ id: "terminal", name: "Terminal", kind: "terminal" },
	],
	preferredEditorId: "cursor",
	workspaceAvailable: true,
};

function setState(state: EditorHandoffState) {
	window.ao!.editorHandoff.getState = vi.fn().mockResolvedValue(state);
	window.ao!.editorHandoff.open = openMock;
}

function renderButton(
	{ sessionCreatedAt, sessionTerminated }: { sessionCreatedAt?: string; sessionTerminated?: boolean } = {},
) {
	const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
	return render(
		<QueryClientProvider client={client}>
			<TooltipProvider>
				<TopbarOpenEditorButton
					sessionId="sess-1"
					projectId="proj-1"
					sessionCreatedAt={sessionCreatedAt}
					sessionTerminated={sessionTerminated}
				/>
			</TooltipProvider>
		</QueryClientProvider>,
	);
}

describe("TopbarOpenEditorButton", () => {
	// The bug this PR chases: the worktree goes away after getState was cached,
	// so the control is still enabled, the click fails, and nothing refreshes the
	// state, leaving it enabled to fail again. Failing the open must re-read
	// availability and disable the control.
	it("refreshes availability after a failed open so the control stops inviting the click", async () => {
		const getState = vi
			.fn()
			.mockResolvedValueOnce(availableState)
			.mockResolvedValue({ ...availableState, workspaceAvailable: false, unavailableReason: "Session workspace is not available" });
		window.ao!.editorHandoff.getState = getState;
		window.ao!.editorHandoff.open = vi.fn().mockRejectedValue(
			new Error("Error invoking remote method 'editorHandoff:open': Error: Session workspace is not available"),
		);
		renderButton();

		const main = await screen.findByRole("button", { name: "Open in Cursor" });
		expect(main).toBeEnabled();

		await userEvent.click(main);

		// the failure must trigger a refetch, not just show a message
		await waitFor(() => expect(getState).toHaveBeenCalledTimes(2));
		await waitFor(() => expect(screen.getByRole("button", { name: "Open in Cursor" })).toBeDisabled());
	});

	// Regression: an ipcMain rejection arrives wrapped as "Error invoking remote
	// method 'editorHandoff:open': Error: <reason>", and the topbar used to paint
	// that whole string into the actions row.
	it("shows the reason, not Electron's remote-method wrapper, when the open fails", async () => {
		setState(availableState);
		window.ao!.editorHandoff.open = vi.fn().mockRejectedValue(
			new Error("Error invoking remote method 'editorHandoff:open': Error: Session workspace is not available"),
		);
		renderButton();

		await userEvent.click(await screen.findByRole("button", { name: "Open in Cursor" }));

		const alert = await screen.findByRole("alert");
		expect(alert).toHaveTextContent("Session workspace is not available");
		expect(alert.textContent).not.toContain("Error invoking remote method");
	});


	beforeEach(() => {
		openMock.mockClear();
		openMock.mockImplementation(async ({ targetId }: OpenSessionTargetInput) => {
			if (targetId === "file-manager") return { id: "file-manager", name: "Finder", kind: "file_manager" };
			if (targetId === "terminal") return { id: "terminal", name: "Terminal", kind: "terminal" };
			if (targetId === "vscode") return { id: "vscode", name: "VS Code", kind: "editor" };
			return { id: "cursor", name: "Cursor", kind: "editor" };
		});
		setState(availableState);
	});

	it("uses persisted Cursor as the primary target and sends no filesystem path", async () => {
		renderButton();
		const button = await screen.findByRole("button", { name: "Open in Cursor" });
		expect(button).not.toHaveAttribute("data-priority");
		expect(button.querySelector("[data-compact-label]")).not.toBeInTheDocument();
		expect(button.querySelector("svg")).toBeInTheDocument();
		expect(button).toHaveClass("topbar-control--icon");
		expect(button).not.toHaveClass("border", "bg-raised");
		const options = screen.getByRole("button", { name: "Open workspace options" });
		expect(options).toHaveClass("topbar-control--icon", "hover:bg-transparent");
		expect(button).toHaveClass("hover:bg-transparent");
		// The button's tooltip trigger wraps it in a span (disabled buttons don't
		// fire pointer events, so the hoverable element has to be the wrapper) —
		// the actual topbar group is one level up.
		const group = button.parentElement?.parentElement;
		expect(group).toHaveClass("gap-0", "rounded-md", "hover:bg-interactive-hover", "data-[state=open]:bg-interactive-hover");
		expect(group).toHaveAttribute("data-state", "closed");
		await userEvent.click(button);
		await waitFor(() => expect(openMock).toHaveBeenCalledWith({ sessionId: "sess-1" }));
	});

	it("keeps the shared editor control highlighted while options are open", async () => {
		renderButton();
		const options = await screen.findByRole("button", { name: "Open workspace options" });
		const group = options.parentElement?.parentElement;

		await userEvent.click(options);

		expect(group).toHaveAttribute("data-state", "open");
		expect(group).toHaveClass("data-[state=open]:bg-interactive-hover");
	});

	it("keeps the no-editor state visible and offers Finder and Terminal", async () => {
		setState({
			targets: [
				{ id: "file-manager", name: "Finder", kind: "file_manager" },
				{ id: "terminal", name: "Terminal", kind: "terminal" },
			],
			preferredEditorId: "cursor",
			workspaceAvailable: true,
		});
		renderButton();
		expect(await screen.findByRole("alert")).toHaveTextContent("No supported editor found");
		expect(screen.getByRole("button", { name: "Choose editor" })).toBeDisabled();
		await userEvent.click(screen.getByRole("button", { name: "Open workspace options" }));
		expect((await screen.findAllByRole("menuitem")).map((item) => item.textContent)).toEqual([
			"Open in Finder",
			"Open in Terminal",
		]);
	});

	it("shows a missing workspace and disables every launch action", async () => {
		setState({
			...availableState,
			workspaceAvailable: false,
			unavailableReason: "Session workspace is not available.",
		});
		renderButton();
		expect(await screen.findByRole("alert")).toHaveTextContent("Session workspace is not available.");
		expect(screen.getByRole("button", { name: "Open in Cursor" })).toBeDisabled();
		expect(screen.getByRole("button", { name: "Open workspace options" })).toBeDisabled();
	});

	it("never renders a transient unavailable error while a fresh session becomes ready", async () => {
		vi.useFakeTimers();
		const renderedAlerts: string[] = [];
		const alertObserver = new MutationObserver(() => {
			for (const alert of document.querySelectorAll('[role="alert"]')) {
				renderedAlerts.push(alert.textContent ?? "");
			}
		});
		alertObserver.observe(document.body, { childList: true, subtree: true, characterData: true });
		try {
			const getState = vi
				.fn()
				.mockResolvedValueOnce({
					...availableState,
					workspaceAvailable: false,
					unavailableReason: "Session workspace is not available.",
				})
				.mockResolvedValue(availableState);
			window.ao!.editorHandoff.getState = getState;
			renderButton({ sessionCreatedAt: new Date().toISOString() });

			await act(async () => {});
			expect(getState).toHaveBeenCalledTimes(1);
			expect(screen.queryByRole("alert")).not.toBeInTheDocument();
			expect(screen.getByRole("button", { name: "Preparing workspace…" })).toBeDisabled();

			await act(async () => {
				await vi.advanceTimersByTimeAsync(499);
			});
			expect(getState).toHaveBeenCalledTimes(1);
			expect(screen.queryByRole("alert")).not.toBeInTheDocument();
			expect(screen.getByRole("button", { name: "Preparing workspace…" })).toBeDisabled();

			await act(async () => {
				await vi.advanceTimersByTimeAsync(1);
			});
			await act(async () => {
				await vi.runOnlyPendingTimersAsync();
			});
			expect(getState).toHaveBeenCalledTimes(2);
			expect(screen.getByRole("button", { name: "Open in Cursor" })).toBeEnabled();
			expect(screen.queryByRole("alert")).not.toBeInTheDocument();
			expect(renderedAlerts).toEqual([]);
		} finally {
			alertObserver.disconnect();
			vi.useRealTimers();
		}
	});

	it("shows the unavailable error only after the fresh-session readiness timeout", async () => {
		vi.useFakeTimers();
		try {
			const getState = vi.fn().mockResolvedValue({
				...availableState,
				workspaceAvailable: false,
				unavailableReason: "Session workspace is not available.",
			});
			window.ao!.editorHandoff.getState = getState;
			renderButton({ sessionCreatedAt: new Date().toISOString() });

			await act(async () => {});
			expect(screen.queryByRole("alert")).not.toBeInTheDocument();
			expect(screen.getByRole("button", { name: "Preparing workspace…" })).toBeDisabled();

			await act(async () => {
				await vi.advanceTimersByTimeAsync(5_000);
			});
			await act(async () => {
				await vi.runOnlyPendingTimersAsync();
			});
			expect(getState).toHaveBeenCalledTimes(11);
			expect(screen.getByRole("alert")).toHaveTextContent("Session workspace is not available.");
			expect(screen.getByRole("button", { name: "Open in Cursor" })).toBeDisabled();
		} finally {
			vi.useRealTimers();
		}
	});

	it("does not poll a terminated session whose workspace is gone", async () => {
		const getState = vi.fn().mockResolvedValue({
			...availableState,
			workspaceAvailable: false,
			unavailableReason: "Session workspace is not available.",
		});
		window.ao!.editorHandoff.getState = getState;
		renderButton({ sessionCreatedAt: new Date().toISOString(), sessionTerminated: true });

		expect(await screen.findByRole("alert")).toHaveTextContent("Session workspace is not available.");
		expect(getState).toHaveBeenCalledTimes(1);
	});

	it("opens safe native fallbacks from the menu", async () => {
		renderButton();
		await userEvent.click(await screen.findByRole("button", { name: "Open workspace options" }));
		await userEvent.click(await screen.findByRole("menuitem", { name: "Open in Finder" }));
		await waitFor(() => expect(openMock).toHaveBeenCalledWith({ sessionId: "sess-1", targetId: "file-manager" }));
	});

	it("updates the primary target after a chosen editor succeeds", async () => {
		renderButton();
		await userEvent.click(await screen.findByRole("button", { name: "Open workspace options" }));
		await userEvent.click(await screen.findByRole("menuitem", { name: "VS Code" }));
		expect(await screen.findByRole("button", { name: "Open in VS Code" })).toBeEnabled();
	});

	it("gives each editor its own mark and keeps real brand color behavior", async () => {
		renderButton();
		await userEvent.click(await screen.findByRole("button", { name: "Open workspace options" }));
		const vscode = await screen.findByRole("menuitem", { name: "VS Code" });
		const cursor = await screen.findByRole("menuitem", { name: "Cursor" });
		expect(vscode.querySelector("svg path")?.getAttribute("d")).not.toEqual(
			cursor.querySelector("svg path")?.getAttribute("d"),
		);
		expect(vscode.querySelector("svg")?.style.color).toBe("rgb(31, 156, 240)");
		expect(cursor.querySelector("svg")?.style.color).toBe("");
	});

	it("surfaces an Electron launch failure", async () => {
		openMock.mockRejectedValueOnce(new Error("Could not open Cursor. Check that it is installed and try again."));
		renderButton();
		await userEvent.click(await screen.findByRole("button", { name: "Open in Cursor" }));
		expect(await screen.findByRole("alert")).toHaveTextContent("Could not open Cursor");
	});
});
