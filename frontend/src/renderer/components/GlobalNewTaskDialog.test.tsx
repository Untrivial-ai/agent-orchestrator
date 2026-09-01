import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { useUiStore } from "../stores/ui-store";
import { GlobalNewTaskDialog } from "./GlobalNewTaskDialog";

const { navigateMock } = vi.hoisted(() => ({ navigateMock: vi.fn() }));

vi.mock("@tanstack/react-router", () => ({
	useNavigate: () => navigateMock,
}));

// Probe stand-in: surfaces the props the real dialog would receive, preserves a
// draft while open, and exposes the real onOpenChange boundary.
vi.mock("./NewTaskDialog", () => ({
	NewTaskDialog: ({
		open,
		projectId,
		onCreated,
		onOpenChange,
	}: {
		open: boolean;
		projectId?: string;
		onCreated: (id: string) => void;
		onOpenChange: (open: boolean) => void;
	}) => {
		const [draft, setDraft] = useState("");
		return open ? (
			<div data-testid="new-task-dialog" data-project={projectId}>
				<label>
					task
					<input aria-label="task" value={draft} onChange={(event) => setDraft(event.currentTarget.value)} />
				</label>
				<button type="button" onClick={() => onCreated("sess-9")}>
					create
				</button>
				<button type="button" onClick={() => onOpenChange(false)}>
					close
				</button>
			</div>
		) : null;
	},
}));

function renderDialog() {
	const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	render(
		<QueryClientProvider client={queryClient}>
			<GlobalNewTaskDialog />
		</QueryClientProvider>,
	);
	return queryClient;
}

beforeEach(() => {
	navigateMock.mockReset();
	useUiStore.setState({ newTaskRequest: null });
});

afterEach(() => {
	vi.restoreAllMocks();
});

describe("GlobalNewTaskDialog", () => {
	it("stays closed until a new-task request arrives", () => {
		renderDialog();
		expect(screen.queryByTestId("new-task-dialog")).not.toBeInTheDocument();
	});

	it("opens for the requested project and navigates to the created session", async () => {
		const user = userEvent.setup();
		const queryClient = renderDialog();
		const invalidate = vi.spyOn(queryClient, "invalidateQueries");

		act(() => {
			useUiStore.getState().requestNewTask("proj-7");
		});

		const dialog = await screen.findByTestId("new-task-dialog");
		expect(dialog).toHaveAttribute("data-project", "proj-7");

		await user.click(screen.getByRole("button", { name: "create" }));
		expect(invalidate).toHaveBeenCalledWith({ queryKey: ["workspaces"] });
		expect(invalidate).toHaveBeenCalledWith({ queryKey: ["editor-handoff", "sess-9"] });
		expect(navigateMock).toHaveBeenCalledWith({
			to: "/projects/$projectId/sessions/$sessionId",
			params: { projectId: "proj-7", sessionId: "sess-9" },
		});
	});

	it("actually closes and re-opens for a fresh request to the same project", async () => {
		const user = userEvent.setup();
		renderDialog();

		act(() => {
			useUiStore.getState().requestNewTask("proj-7");
		});
		await screen.findByTestId("new-task-dialog");

		await user.click(screen.getByRole("button", { name: "close" }));
		expect(screen.queryByTestId("new-task-dialog")).not.toBeInTheDocument();

		act(() => {
			useUiStore.getState().requestNewTask("proj-7");
		});
		expect(await screen.findByTestId("new-task-dialog")).toHaveAttribute("data-project", "proj-7");
	});

	it("does not retarget or replay a request received while the dialog is open", async () => {
		const user = userEvent.setup();
		renderDialog();

		act(() => {
			useUiStore.getState().requestNewTask("proj-7");
		});
		const dialog = await screen.findByTestId("new-task-dialog");
		await user.type(screen.getByRole("textbox", { name: "task" }), "keep this draft");

		act(() => {
			useUiStore.getState().requestNewTask("proj-8");
		});
		expect(dialog).toHaveAttribute("data-project", "proj-7");
		expect(screen.getByRole("textbox", { name: "task" })).toHaveValue("keep this draft");

		await user.click(screen.getByRole("button", { name: "close" }));
		expect(screen.queryByTestId("new-task-dialog")).not.toBeInTheDocument();

		act(() => {
			useUiStore.getState().requestNewTask("proj-8");
		});
		expect(await screen.findByTestId("new-task-dialog")).toHaveAttribute("data-project", "proj-8");
	});
});
