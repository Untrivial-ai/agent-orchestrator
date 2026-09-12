import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ReactNode } from "react";

const { postMock, getMock, hasTrustedApiBaseUrlMock } = vi.hoisted(() => ({
	postMock: vi.fn(),
	getMock: vi.fn(),
	hasTrustedApiBaseUrlMock: vi.fn(() => true),
}));

vi.mock("../../lib/api-client", () => ({
	apiClient: { GET: getMock, POST: postMock },
	hasTrustedApiBaseUrl: hasTrustedApiBaseUrlMock,
	apiErrorMessage: (e: unknown) => (e instanceof Error ? e.message : "error"),
}));

vi.mock("@tanstack/react-router", () => ({
	useNavigate: () => vi.fn(),
}));

import { TaskDetailPanel } from "./TaskDetailPanel";

function wrapper({ children }: { children: ReactNode }) {
	const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, retryDelay: 0 } } });
	return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
}

const readyTask = { id: "task-1", title: "Build API", status: "ready", description: "Build REST API", stageId: "s1" };
const runningTask = { id: "task-1", title: "Build API", status: "running", description: "Build REST API", stageId: "s1" };
const reviewTask = { id: "task-1", title: "Build API", status: "review", description: "Build REST API", stageId: "s1" };

const pendingRun = { id: "run-1", taskId: "task-1", status: "pending", attempt: 1, createdAt: "2026-01-01" };
const runningRun = { id: "run-2", taskId: "task-1", status: "running", attempt: 2, createdAt: "2026-01-01", sessionId: "sess-1", startedAt: "2026-01-01T01:00:00Z" };
const failedRun = { id: "run-3", taskId: "task-1", status: "failed", attempt: 3, createdAt: "2026-01-01", errorMessage: "crash" };
const succeededRun = { id: "run-4", taskId: "task-1", status: "succeeded", attempt: 4, createdAt: "2026-01-01", resultSummary: "ok" };

beforeEach(() => {
	postMock.mockReset();
	getMock.mockReset();
});

function mockTask(task: typeof readyTask) {
	getMock.mockImplementation((url: string) => {
		if (url.includes("/tasks/") && url.includes("/runs")) {
			return Promise.resolve({ data: { runs: [] }, error: undefined });
		}
		return Promise.resolve({ data: { task }, error: undefined });
	});
}

function mockTaskAndRuns(task: typeof readyTask, runs: unknown[]) {
	getMock.mockImplementation((url: string) => {
		if (url.includes("/runs")) {
			return Promise.resolve({ data: { runs }, error: undefined });
		}
		return Promise.resolve({ data: { task }, error: undefined });
	});
}

describe("TaskDetailPanel — Phase 2.6-B", () => {
	it("shows Create Run button when task is ready and no active runs", async () => {
		mockTask(readyTask);
		render(<TaskDetailPanel taskId="task-1" onClose={vi.fn()} />, { wrapper });

		// Switch to runs tab
		const user = userEvent.setup();
		await waitFor(() => expect(screen.getByText("Run History")).toBeInTheDocument());
		await user.click(screen.getByText("Run History"));

		await waitFor(() => {
			expect(screen.getByTestId("create-run")).toBeInTheDocument();
		});
	});

	it("does NOT show Create Run button when task is not ready", async () => {
		mockTask(runningTask);
		render(<TaskDetailPanel taskId="task-1" onClose={vi.fn()} />, { wrapper });

		const user = userEvent.setup();
		await waitFor(() => expect(screen.getByText("Run History")).toBeInTheDocument());
		await user.click(screen.getByText("Run History"));

		await waitFor(() => {
			expect(screen.queryByTestId("create-run")).not.toBeInTheDocument();
		});
	});

	it("does NOT show Create Run when there is an active (pending) run", async () => {
		mockTaskAndRuns(readyTask, [pendingRun]);
		render(<TaskDetailPanel taskId="task-1" onClose={vi.fn()} />, { wrapper });

		const user = userEvent.setup();
		await waitFor(() => expect(screen.getByText("Run History")).toBeInTheDocument());
		await user.click(screen.getByText("Run History"));

		await waitFor(() => {
			expect(screen.queryByTestId("create-run")).not.toBeInTheDocument();
		});
	});

	it("does NOT show Create Run when there is an active (running) run", async () => {
		mockTaskAndRuns(runningTask, [runningRun]);
		render(<TaskDetailPanel taskId="task-1" onClose={vi.fn()} />, { wrapper });

		const user = userEvent.setup();
		await waitFor(() => expect(screen.getByText("Run History")).toBeInTheDocument());
		await user.click(screen.getByText("Run History"));

		await waitFor(() => {
			expect(screen.queryByTestId("create-run")).not.toBeInTheDocument();
		});
	});

	it("shows Start and Cancel buttons for latest pending run", async () => {
		mockTaskAndRuns(readyTask, [pendingRun]);
		render(<TaskDetailPanel taskId="task-1" onClose={vi.fn()} />, { wrapper });

		const user = userEvent.setup();
		await waitFor(() => expect(screen.getByText("Run History")).toBeInTheDocument());
		await user.click(screen.getByText("Run History"));

		await waitFor(() => {
			expect(screen.getByTestId("start-run-run-1")).toBeInTheDocument();
			expect(screen.getByTestId("cancel-run-run-1")).toBeInTheDocument();
		});
	});

	it("shows View Session and Cancel for latest running run", async () => {
		mockTaskAndRuns(runningTask, [runningRun]);
		render(<TaskDetailPanel taskId="task-1" onClose={vi.fn()} />, { wrapper });

		const user = userEvent.setup();
		await waitFor(() => expect(screen.getByText("Run History")).toBeInTheDocument());
		await user.click(screen.getByText("Run History"));

		await waitFor(() => {
			expect(screen.getByTestId("view-session-run-2")).toBeInTheDocument();
			expect(screen.getByTestId("cancel-run-run-2")).toBeInTheDocument();
		});
	});

	it("shows recovering message when task running but latest run failed", async () => {
		mockTaskAndRuns(runningTask, [failedRun]);
		render(<TaskDetailPanel taskId="task-1" onClose={vi.fn()} />, { wrapper });

		const user = userEvent.setup();
		await waitFor(() => expect(screen.getByText("Run History")).toBeInTheDocument());
		await user.click(screen.getByText("Run History"));

		await waitFor(() => {
			expect(screen.getByText(/Recovering/)).toBeInTheDocument();
		});
	});

	it("shows Create Run when task is ready after failed run (re-execute path)", async () => {
		mockTaskAndRuns(readyTask, [failedRun]);
		render(<TaskDetailPanel taskId="task-1" onClose={vi.fn()} />, { wrapper });

		const user = userEvent.setup();
		await waitFor(() => expect(screen.getByText("Run History")).toBeInTheDocument());
		await user.click(screen.getByText("Run History"));

		await waitFor(() => {
			expect(screen.getByTestId("create-run")).toBeInTheDocument();
		});
	});

	it("shows waiting review message when task is in review status", async () => {
		mockTaskAndRuns(reviewTask, [succeededRun]);
		render(<TaskDetailPanel taskId="task-1" onClose={vi.fn()} />, { wrapper });

		const user = userEvent.setup();
		await waitFor(() => expect(screen.getByText("Run History")).toBeInTheDocument());
		await user.click(screen.getByText("Run History"));

		await waitFor(() => {
			expect(screen.getByText(/Waiting for review|等待审核/)).toBeInTheDocument();
		});
	});

	it("calls CreateRun API when Create Run button is clicked", async () => {
		mockTask(readyTask);
		postMock.mockResolvedValue({ data: { run: pendingRun }, error: undefined });

		const user = userEvent.setup();
		render(<TaskDetailPanel taskId="task-1" onClose={vi.fn()} />, { wrapper });

		await waitFor(() => expect(screen.getByText("Run History")).toBeInTheDocument());
		await user.click(screen.getByText("Run History"));

		await waitFor(() => expect(screen.getByTestId("create-run")).toBeInTheDocument());
		await user.click(screen.getByTestId("create-run"));

		expect(postMock).toHaveBeenCalledWith("/api/v1/workflow/runs", {
			body: { taskId: "task-1" },
		});
	});
});
