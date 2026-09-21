import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import type { ReactNode } from "react";
import { CuesSettings } from "./CuesDialog";
import { CueRunMenu } from "./chat/CueRunMenu";
import { TooltipProvider } from "./ui/tooltip";
import * as cues from "../lib/cues";

const { toast, navigate, openProjectSettings } = vi.hoisted(() => ({
	toast: vi.fn(),
	navigate: vi.fn(),
	openProjectSettings: vi.fn(),
}));
vi.mock("../stores/ui-store", () => ({
	useUiStore: (select: (s: unknown) => unknown) => select({ showGlobalToast: toast, openProjectSettings }),
}));
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
	const wrap = (child: ReactNode) => (
		<QueryClientProvider client={client}>
			{/* No delay so a hovertip assertion does not wait on the provider's timing. */}
			<TooltipProvider delayDuration={0}>{child}</TooltipProvider>
		</QueryClientProvider>
	);
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

test("settings refresh even fresh cached cues on every opening, including external edits and deletion", async () => {
	const view = setup(<CuesSettings projectId="project" />);
	await screen.findByText("Tests");
	view.rerender(null);
	const refresh = deferred<cues.CueDTO[]>();
	vi.mocked(cues.fetchProjectCues).mockReturnValueOnce(refresh.promise);
	view.rerender(<CuesSettings projectId="project" />);
	expect(screen.queryByRole("button", { name: "Run in new session" })).toBeNull();
	await act(async () => refresh.resolve([{ ...cue, name: "Updated" }]));
	await screen.findByText("Updated");
	view.rerender(null);
	vi.mocked(cues.fetchProjectCues).mockResolvedValue([]);
	view.rerender(<CuesSettings projectId="project" />);
	await screen.findByText(/No cues yet/);
	expect(cues.fetchProjectCues).toHaveBeenCalledTimes(3);
});

test("settings block stale content after refresh failure and support retry", async () => {
	vi.mocked(cues.fetchProjectCues).mockRejectedValueOnce(new Error("offline"));
	setup(<CuesSettings projectId="project" />);
	await screen.findByRole("alert");
	expect(screen.queryByRole("button", { name: "Run in new session" })).toBeNull();
	fireEvent.click(screen.getByRole("button", { name: "Try again" }));
	await screen.findByText("Tests");
});

test("validates bytes and required command, preserves content and recovers from save failure", async () => {
	setup(<CuesSettings projectId="project" />);
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
	const onBusyChange = vi.fn();
	const view = setup(<CuesSettings projectId="project" onBusyChange={onBusyChange} />);
	fireEvent.click(await screen.findByRole("button", { name: "Edit" }));
	const button = screen.getByRole("button", { name: "Save" });
	fireEvent.click(button); fireEvent.click(button);
	await waitFor(() => expect(cues.updateCue).toHaveBeenCalledTimes(1));
	expect(onBusyChange).toHaveBeenCalledWith(true);
	view.rerender(<CuesSettings projectId="other" onBusyChange={onBusyChange} />);
	await act(async () => save.resolve(cue));
	expect(toast).not.toHaveBeenCalled();
});

test("failed deletion stays open for retry and pending deletion cannot be dismissed", async () => {
	const deletion = deferred<void>();
	vi.mocked(cues.deleteCue).mockReturnValueOnce(deletion.promise);
	setup(<CuesSettings projectId="project" />);
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

test("project topbar invocation creates a worker once and navigates after dispatch", async () => {
	const invocation = deferred<string>();
	vi.mocked(cues.invokeCue).mockReturnValue(invocation.promise);
	setup(<CueRunMenu projectId="project" />);
	expect(screen.getByRole("button", { name: "Run a cue" }).querySelector(".lucide-play")).not.toBeNull();
	openMenu();
	const run = await screen.findByRole("menuitem", { name: "Tests" });
	fireEvent.click(run); fireEvent.click(run);
	await waitFor(() => expect(cues.invokeCue).toHaveBeenCalledExactlyOnceWith("cue-1", undefined));
	await act(async () => invocation.resolve("worker"));
	expect(navigate).toHaveBeenCalledWith("project", "worker");
	expect(toast).toHaveBeenCalledWith("Sent to session", "Tests sent to session");
});

function openMenu() {
	fireEvent.keyDown(screen.getByRole("button", { name: "Run a cue" }), { key: "ArrowDown" });
}
test("hovering the runner opens the menu and leaving closes it, with no tooltip", async () => {
	setup(<CueRunMenu projectId="project" sessionId="session" />);
	const trigger = screen.getByRole("button", { name: "Run a cue" });
	expect(screen.queryByRole("menu")).toBeNull();

	fireEvent.pointerOver(trigger, { pointerType: "mouse" });
	await screen.findByRole("menuitem", { name: "Tests" });
	expect(screen.queryByRole("tooltip")).toBeNull();

	fireEvent.pointerOut(trigger, { pointerType: "mouse" });
	await waitFor(() => expect(screen.queryByRole("menu")).toBeNull());
});

test("reopening the menu keeps cached cues visible while the refresh runs", async () => {
	const refresh = deferred<cues.CueDTO[]>();
	setup(<CueRunMenu projectId="project" sessionId="session" />);
	const trigger = screen.getByRole("button", { name: "Run a cue" });
	fireEvent.pointerOver(trigger, { pointerType: "mouse" });
	await screen.findByRole("menuitem", { name: "Tests" });
	fireEvent.pointerOut(trigger, { pointerType: "mouse" });
	await waitFor(() => expect(screen.queryByRole("menu")).toBeNull());

	vi.mocked(cues.fetchProjectCues).mockReturnValueOnce(refresh.promise);
	fireEvent.pointerOver(trigger, { pointerType: "mouse" });

	await screen.findByRole("menuitem", { name: "Tests" });
	expect(screen.queryByRole("menuitem", { name: /Loading/ })).toBeNull();
	await act(async () => refresh.resolve([cue]));
});

test("menu rows stay on the cue name and reveal the description on hover", async () => {
	vi.mocked(cues.fetchProjectCues).mockResolvedValue([{ ...cue, description: "just runs the app" }]);
	setup(<CueRunMenu projectId="project" />);
	fireEvent.pointerOver(screen.getByRole("button", { name: "Run a cue" }), { pointerType: "mouse" });
	const item = await screen.findByRole("menuitem", { name: "Tests" });
	expect(screen.queryByText("just runs the app")).toBeNull();

	fireEvent.pointerMove(item, { pointerType: "mouse" });

	expect(await screen.findByRole("tooltip")).toHaveTextContent("just runs the app");
});

test("offers the create action even when the project already has cues", async () => {
	setup(<CueRunMenu projectId="project" />);
	fireEvent.pointerOver(screen.getByRole("button", { name: "Run a cue" }), { pointerType: "mouse" });
	await screen.findByRole("menuitem", { name: "Tests" });

	fireEvent.click(screen.getByRole("menuitem", { name: "New cue" }));

	await waitFor(() => expect(openProjectSettings).toHaveBeenCalledExactlyOnceWith("project", { section: "cues" }));
});

test("an empty project offers cue setup from the menu", async () => {
	vi.mocked(cues.fetchProjectCues).mockResolvedValue([]);
	setup(<CueRunMenu projectId="project" sessionId="session" />);

	fireEvent.pointerOver(screen.getByRole("button", { name: "Run a cue" }), { pointerType: "mouse" });
	fireEvent.click(await screen.findByRole("menuitem", { name: "New cue" }));

	await waitFor(() => expect(openProjectSettings).toHaveBeenCalledExactlyOnceWith("project", { section: "cues" }));
});
test("clicking the trigger runs the primary cue and leaves the hover menu up", async () => {
	setup(<CueRunMenu projectId="project" sessionId="session" />);
	const trigger = screen.getByRole("button", { name: "Run a cue" });
	fireEvent.pointerOver(trigger, { pointerType: "mouse" });
	await screen.findByRole("menuitem", { name: "Tests" });

	fireEvent.click(trigger, { detail: 1 });

	await waitFor(() => expect(cues.invokeCue).toHaveBeenCalledExactlyOnceWith("cue-1", "session"));
	expect(screen.getByRole("menuitem", { name: "Tests" })).toBeInTheDocument();
	expect(navigate).not.toHaveBeenCalled();
});
test("keyboard activation opens the menu without running a cue", async () => {
	setup(<CueRunMenu projectId="project" />);
	const trigger = screen.getByRole("button", { name: "Run a cue" });

	fireEvent.keyDown(trigger, { key: "Enter" });

	await screen.findByRole("menuitem", { name: "Tests" });
	expect(cues.invokeCue).not.toHaveBeenCalled();
});
test("clicking with no cues set up keeps the empty menu open", async () => {
	vi.mocked(cues.fetchProjectCues).mockResolvedValue([]);
	setup(<CueRunMenu projectId="project" />);

	fireEvent.click(screen.getByRole("button", { name: "Run a cue" }), { detail: 1 });

	expect(await screen.findByRole("menuitem", { name: "New cue" })).toBeInTheDocument();
	expect(cues.invokeCue).not.toHaveBeenCalled();
});
test("session-targeted runner remains discoverable when empty and sees externally created cues on reopening", async () => {
	vi.mocked(cues.fetchProjectCues).mockResolvedValueOnce([]);
	setup(<CueRunMenu projectId="project" sessionId="session" />);
	expect(cues.fetchProjectCues).not.toHaveBeenCalled();
	openMenu();
	await screen.findByText(/No cues in this project/);
	fireEvent.keyDown(screen.getByRole("menu"), { key: "Escape" });
	openMenu();
	const item = await screen.findByRole("menuitem", { name: "Tests" });
	fireEvent.click(item);
	await waitFor(() => expect(cues.invokeCue).toHaveBeenCalledExactlyOnceWith("cue-1", "session"));
	expect(navigate).not.toHaveBeenCalled();
});

test("session-targeted runner displays refresh errors and retries without closing", async () => {
	vi.mocked(cues.fetchProjectCues).mockRejectedValueOnce(new Error("offline"));
	setup(<CueRunMenu projectId="project" sessionId="session" />);
	openMenu();
	await screen.findByRole("alert");
	fireEvent.click(screen.getByRole("menuitem", { name: "Try again" }));
	await screen.findByRole("menuitem", { name: "Tests" });
});

test("session-targeted runner ignores late responses after switching sessions and never retries dispatch", async () => {
	const invocation = deferred<string>();
	vi.mocked(cues.invokeCue).mockReturnValueOnce(invocation.promise);
	const view = setup(<CueRunMenu projectId="project" sessionId="session" />);
	openMenu();
	fireEvent.click(await screen.findByRole("menuitem", { name: "Tests" }));
	await waitFor(() => expect(cues.invokeCue).toHaveBeenCalledTimes(1));
	view.rerender(<CueRunMenu projectId="project" sessionId="other" />);
	await act(async () => invocation.reject(new Error("unavailable")));
	expect(toast).not.toHaveBeenCalled();
	expect(cues.invokeCue).toHaveBeenCalledTimes(1);
});

test("unmounting the project menu isolates an earlier invocation response", async () => {
	const invocation = deferred<string>();
	vi.mocked(cues.invokeCue).mockReturnValue(invocation.promise);
	const view = setup(<CueRunMenu projectId="project" />);
	openMenu();
	fireEvent.click(await screen.findByRole("menuitem", { name: "Tests" }));
	await waitFor(() => expect(cues.invokeCue).toHaveBeenCalledTimes(1));
	view.rerender(null);
	await act(async () => invocation.resolve("worker"));
	expect(navigate).not.toHaveBeenCalled();
	expect(toast).not.toHaveBeenCalled();
});

test("agent editing validates required content and keeps a failed update editable", async () => {
	vi.mocked(cues.fetchProjectCues).mockResolvedValue([{ ...cue, type: "agent", prompt: "explain", command: "" }]);
	setup(<CuesSettings projectId="project" />);
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
