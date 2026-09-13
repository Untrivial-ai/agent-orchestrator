import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import type { ReactNode } from "react";
import { CuesDialog } from "./CuesDialog";
import { CueComposerMenu } from "./chat/CueComposerMenu";
import { TooltipProvider } from "./ui/tooltip";
import * as cues from "../lib/cues";

const { toast, navigate } = vi.hoisted(() => ({ toast: vi.fn(), navigate: vi.fn() }));
vi.mock("../stores/ui-store", () => ({ useUiStore: (select: (s: unknown) => unknown) => select({ showGlobalToast: toast }) }));
vi.mock("../lib/navigate-to-session", () => ({ useNavigateToSession: () => navigate }));
vi.mock("../lib/cues", async (original) => ({
	...await original<typeof import("../lib/cues")>(),
	fetchProjectCues: vi.fn(), createCue: vi.fn(), updateCue: vi.fn(), deleteCue: vi.fn(), invokeCue: vi.fn(),
}));

const cue: cues.CueDTO = { id: "cue-1", projectId: "project", name: "Tests", type: "command", command: "npm test", description: "", createdAt: "2026-09-13T00:00:00Z", updatedAt: "2026-09-13T00:00:00Z" };
function deferred<T>() {
	let resolve!: (value: T) => void;
	let reject!: (error: Error) => void;
	const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no; });
	return { promise, resolve, reject };
}
function setup(node: ReactNode) {
	const client = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: 60_000 }, mutations: { retry: false } } });
	const wrap = (child: ReactNode) => <QueryClientProvider client={client}><TooltipProvider>{child}</TooltipProvider></QueryClientProvider>;
	const view = render(wrap(node));
	return { ...view, rerender: (child: ReactNode) => view.rerender(wrap(child)) };
}
beforeEach(() => {
	vi.resetAllMocks();
	vi.mocked(cues.fetchProjectCues).mockResolvedValue([cue]);
	vi.mocked(cues.createCue).mockResolvedValue(cue);
	vi.mocked(cues.updateCue).mockResolvedValue(cue);
	vi.mocked(cues.deleteCue).mockResolvedValue(undefined);
	vi.mocked(cues.invokeCue).mockResolvedValue("worker");
});
afterEach(cleanup);

test("dialog refreshes even fresh cached cues on every opening, including external edits and deletion", async () => {
	const props = { projectId: "project", onOpenChange: vi.fn() };
	const view = setup(<CuesDialog {...props} open />);
	await screen.findByText("Tests");
	view.rerender(<CuesDialog {...props} open={false} />);
	const refresh = deferred<cues.CueDTO[]>();
	vi.mocked(cues.fetchProjectCues).mockReturnValueOnce(refresh.promise);
	view.rerender(<CuesDialog {...props} open />);
	expect(screen.queryByRole("button", { name: "Run in new session" })).toBeNull();
	await act(async () => refresh.resolve([{ ...cue, name: "Updated" }]));
	await screen.findByText("Updated");
	view.rerender(<CuesDialog {...props} open={false} />);
	vi.mocked(cues.fetchProjectCues).mockResolvedValue([]);
	view.rerender(<CuesDialog {...props} open />);
	await screen.findByText(/No cues yet/);
	expect(cues.fetchProjectCues).toHaveBeenCalledTimes(3);
});

test("dialog blocks stale invocation after refresh failure and supports retry", async () => {
	vi.mocked(cues.fetchProjectCues).mockRejectedValueOnce(new Error("offline"));
	setup(<CuesDialog open projectId="project" onOpenChange={vi.fn()} />);
	await screen.findByRole("alert");
	expect(screen.queryByRole("button", { name: "Run in new session" })).toBeNull();
	fireEvent.click(screen.getByRole("button", { name: "Try again" }));
	await screen.findByText("Tests");
});

test("validates bytes and required command, preserves content and recovers from save failure", async () => {
	setup(<CuesDialog open projectId="project" onOpenChange={vi.fn()} />);
	fireEvent.click(screen.getByRole("button", { name: "New cue" }));
	fireEvent.change(screen.getByLabelText("Name"), { target: { value: "é".repeat(33) } });
	fireEvent.change(screen.getByLabelText("Command"), { target: { value: "  " } });
	fireEvent.click(screen.getByRole("button", { name: "Create" }));
	await screen.findByText("A command is required.");
	fireEvent.change(screen.getByLabelText("Command"), { target: { value: "  npm test\n" } });
	fireEvent.click(screen.getByRole("button", { name: "Create" }));
	await screen.findByText(/64 UTF-8 bytes/);
	expect(cues.createCue).not.toHaveBeenCalled();
	fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Tests" } });
	vi.mocked(cues.createCue).mockRejectedValueOnce(new Error("save failed"));
	fireEvent.click(screen.getByRole("button", { name: "Create" }));
	await screen.findByText("save failed");
	expect(screen.getByLabelText("Command")).toHaveValue("  npm test\n");
	fireEvent.click(screen.getByRole("button", { name: "Create" }));
	await waitFor(() => expect(cues.createCue).toHaveBeenCalledTimes(2));
	expect(cues.createCue).toHaveBeenLastCalledWith("project", expect.objectContaining({ command: "  npm test\n" }));
});

test("save blocks duplicate submissions and dismissal; completion from another project is ignored", async () => {
	const save = deferred<cues.CueDTO>();
	vi.mocked(cues.updateCue).mockReturnValue(save.promise);
	const onOpenChange = vi.fn();
	const view = setup(<CuesDialog open projectId="project" onOpenChange={onOpenChange} />);
	fireEvent.click(await screen.findByRole("button", { name: "Edit" }));
	const button = screen.getByRole("button", { name: "Save" });
	fireEvent.click(button); fireEvent.click(button);
	fireEvent.keyDown(screen.getByRole("dialog"), { key: "Escape" });
	expect(onOpenChange).not.toHaveBeenCalled();
	await waitFor(() => expect(cues.updateCue).toHaveBeenCalledTimes(1));
	view.rerender(<CuesDialog open projectId="other" onOpenChange={onOpenChange} />);
	await act(async () => save.resolve(cue));
	expect(toast).not.toHaveBeenCalled();
});

test("failed deletion stays open for retry and pending deletion cannot be dismissed", async () => {
	const deletion = deferred<void>();
	vi.mocked(cues.deleteCue).mockReturnValueOnce(deletion.promise);
	setup(<CuesDialog open projectId="project" onOpenChange={vi.fn()} />);
	fireEvent.click(await screen.findByRole("button", { name: "Delete" }));
	const dialog = screen.getByRole("dialog", { name: "Delete this cue?" });
	const confirm = within(dialog).getByRole("button", { name: "Delete" });
	fireEvent.click(confirm); fireEvent.click(confirm);
	fireEvent.keyDown(dialog, { key: "Escape" });
	expect(dialog).toBeInTheDocument();
	await act(async () => deletion.reject(new Error("delete failed")));
	await within(dialog).findByRole("alert");
	expect(cues.deleteCue).toHaveBeenCalledTimes(1);
	fireEvent.click(confirm);
	await waitFor(() => expect(screen.queryByRole("dialog", { name: "Delete this cue?" })).toBeNull());
	expect(cues.deleteCue).toHaveBeenCalledTimes(2);
});

test("project invocation creates a worker once and navigates after dispatch", async () => {
	const invocation = deferred<string>();
	vi.mocked(cues.invokeCue).mockReturnValue(invocation.promise);
	setup(<CuesDialog open projectId="project" onOpenChange={vi.fn()} />);
	const run = await screen.findByRole("button", { name: "Run in new session" });
	fireEvent.click(run); fireEvent.click(run);
	await waitFor(() => expect(cues.invokeCue).toHaveBeenCalledExactlyOnceWith("cue-1", undefined));
	await act(async () => invocation.resolve("worker"));
	expect(navigate).toHaveBeenCalledWith("project", "worker");
	expect(toast).toHaveBeenCalledWith("Sent to session", "Tests sent to session");
});

function openMenu() {
	fireEvent.keyDown(screen.getByRole("button", { name: "Run a cue" }), { key: "ArrowDown" });
}
test("composer remains discoverable when empty and sees externally created cues on reopening", async () => {
	vi.mocked(cues.fetchProjectCues).mockResolvedValueOnce([]);
	setup(<CueComposerMenu projectId="project" sessionId="session" />);
	expect(cues.fetchProjectCues).not.toHaveBeenCalled();
	openMenu();
	await screen.findByText(/No cues yet/);
	fireEvent.keyDown(screen.getByRole("menu"), { key: "Escape" });
	openMenu();
	const item = await screen.findByRole("menuitem", { name: "Tests" });
	fireEvent.click(item);
	await waitFor(() => expect(cues.invokeCue).toHaveBeenCalledExactlyOnceWith("cue-1", "session"));
	expect(navigate).not.toHaveBeenCalled();
});

test("composer displays refresh errors and retries without closing", async () => {
	vi.mocked(cues.fetchProjectCues).mockRejectedValueOnce(new Error("offline"));
	setup(<CueComposerMenu projectId="project" sessionId="session" />);
	openMenu();
	await screen.findByRole("alert");
	fireEvent.click(screen.getByRole("menuitem", { name: "Try again" }));
	await screen.findByRole("menuitem", { name: "Tests" });
});

test("composer ignores late responses after switching sessions and never retries dispatch", async () => {
	const invocation = deferred<string>();
	vi.mocked(cues.invokeCue).mockReturnValueOnce(invocation.promise);
	const view = setup(<CueComposerMenu projectId="project" sessionId="session" />);
	openMenu();
	fireEvent.click(await screen.findByRole("menuitem", { name: "Tests" }));
	await waitFor(() => expect(cues.invokeCue).toHaveBeenCalledTimes(1));
	view.rerender(<CueComposerMenu projectId="project" sessionId="other" />);
	await act(async () => invocation.reject(new Error("unavailable")));
	expect(toast).not.toHaveBeenCalled();
	expect(cues.invokeCue).toHaveBeenCalledTimes(1);
});

test("closing and reopening the dialog isolates an earlier invocation response", async () => {
	const invocation = deferred<string>();
	vi.mocked(cues.invokeCue).mockReturnValue(invocation.promise);
	const props = { projectId: "project", onOpenChange: vi.fn() };
	const view = setup(<CuesDialog {...props} open />);
	fireEvent.click(await screen.findByRole("button", { name: "Run in new session" }));
	await waitFor(() => expect(cues.invokeCue).toHaveBeenCalledTimes(1));
	view.rerender(<CuesDialog {...props} open={false} />);
	view.rerender(<CuesDialog {...props} open />);
	await act(async () => invocation.resolve("worker"));
	expect(navigate).not.toHaveBeenCalled();
	expect(props.onOpenChange).not.toHaveBeenCalled();
	expect(toast).not.toHaveBeenCalled();
});

test("agent editing validates required content and keeps a failed update editable", async () => {
	vi.mocked(cues.fetchProjectCues).mockResolvedValue([{ ...cue, type: "agent", prompt: "explain", command: "" }]);
	setup(<CuesDialog open projectId="project" onOpenChange={vi.fn()} />);
	fireEvent.click(await screen.findByRole("button", { name: "Edit" }));
	fireEvent.change(screen.getByLabelText("Agent instruction"), { target: { value: "  " } });
	fireEvent.click(screen.getByRole("button", { name: "Save" }));
	await screen.findByText("A prompt is required.");
	expect(cues.updateCue).not.toHaveBeenCalled();
	fireEvent.change(screen.getByLabelText("Agent instruction"), { target: { value: "  explain\n" } });
	vi.mocked(cues.updateCue).mockRejectedValueOnce(new Error("update failed"));
	fireEvent.click(screen.getByRole("button", { name: "Save" }));
	await screen.findByText("update failed");
	expect(screen.getByLabelText("Agent instruction")).toHaveValue("  explain\n");
	fireEvent.click(screen.getByRole("button", { name: "Save" }));
	await waitFor(() => expect(cues.updateCue).toHaveBeenCalledTimes(2));
	expect(cues.updateCue).toHaveBeenLastCalledWith("cue-1", expect.objectContaining({ prompt: "  explain\n", type: "agent" }));
});
