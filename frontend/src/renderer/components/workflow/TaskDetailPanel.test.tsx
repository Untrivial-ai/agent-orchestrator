import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ReactNode } from "react";

const { getMock, hasTrustedApiBaseUrlMock } = vi.hoisted(() => ({
	getMock: vi.fn(),
	hasTrustedApiBaseUrlMock: vi.fn(() => true),
}));

vi.mock("../../lib/api-client", () => ({
	apiClient: { GET: getMock },
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

const mockTask = {
	id: "task-1",
	title: "Implement Login",
	description: "Build the login form",
	acceptanceCriteria: "User can log in",
	status: "running",
	taskType: "coding",
	agentRoleId: "role-dev",
	providerId: "openai",
	providerModelId: "gpt-4o",
	sequence: 1,
};

const mockRuns = [
	{
		id: "run-1",
		taskId: "task-1",
		attempt: 2,
		status: "running",
		sessionId: "sess-2",
		agentRoleId: "role-dev",
		providerId: "openai",
		providerDisplayName: "OpenAI",
		providerModelId: "gpt-4o",
		providerModelName: "GPT-4o",
		executorType: "claude-code",
		createdAt: "2026-09-12T10:00:00Z",
		startedAt: "2026-09-12T10:00:05Z",
		finishedAt: null,
		resultSummary: "",
		errorMessage: "",
		previousRunId: "run-0",
		retryMode: "resume",
	},
	{
		id: "run-0",
		taskId: "task-1",
		attempt: 1,
		status: "failed",
		sessionId: "sess-1",
		agentRoleId: "role-dev",
		providerId: "openai",
		providerDisplayName: "OpenAI",
		providerModelId: "gpt-4o",
		providerModelName: "GPT-4o",
		executorType: "claude-code",
		createdAt: "2026-09-12T09:00:00Z",
		startedAt: "2026-09-12T09:00:05Z",
		finishedAt: "2026-09-12T09:05:00Z",
		resultSummary: "",
		errorMessage: "Timeout",
		previousRunId: "",
		retryMode: "",
	},
];

beforeEach(() => {
	getMock.mockReset();
});

describe("TaskDetailPanel", () => {
	it("shows loading skeleton initially", () => {
		getMock.mockReturnValue(new Promise(() => {})); // never resolves
		render(<TaskDetailPanel taskId="task-1" onClose={vi.fn()} />, { wrapper });
		expect(screen.getByTestId("task-detail-panel")).toBeInTheDocument();
	});

	it("renders task info after loading", async () => {
		getMock.mockImplementation(async (url: string) => {
			if (url === "/api/v1/workflow/tasks/{id}") return { data: { task: mockTask }, error: undefined };
			if (url === "/api/v1/workflow/tasks/{id}/runs") return { data: { runs: mockRuns }, error: undefined };
			return { data: undefined, error: undefined };
		});

		render(<TaskDetailPanel taskId="task-1" onClose={vi.fn()} />, { wrapper });

		await screen.findByText("Implement Login");
		expect(screen.getByText("Implement Login")).toBeInTheDocument();
		expect(screen.getByText("Build the login form")).toBeInTheDocument();
		expect(screen.getByText("User can log in")).toBeInTheDocument();
	});

	it("does not render any write operation buttons", async () => {
		getMock.mockImplementation(async (url: string) => {
			if (url === "/api/v1/workflow/tasks/{id}") return { data: { task: mockTask }, error: undefined };
			if (url === "/api/v1/workflow/tasks/{id}/runs") return { data: { runs: mockRuns }, error: undefined };
			return { data: undefined, error: undefined };
		});

		render(<TaskDetailPanel taskId="task-1" onClose={vi.fn()} />, { wrapper });

		await screen.findByText("Implement Login");
		// No Start, Cancel, Retry, or Review buttons
		expect(screen.queryByRole("button", { name: /start/i })).not.toBeInTheDocument();
		expect(screen.queryByRole("button", { name: /cancel/i })).not.toBeInTheDocument();
		expect(screen.queryByRole("button", { name: /retry/i })).not.toBeInTheDocument();
		expect(screen.queryByRole("button", { name: /review/i })).not.toBeInTheDocument();
	});

	it("calls onClose when close button is clicked", async () => {
		const onClose = vi.fn();
		getMock.mockImplementation(async (url: string) => {
			if (url === "/api/v1/workflow/tasks/{id}") return { data: { task: mockTask }, error: undefined };
			if (url === "/api/v1/workflow/tasks/{id}/runs") return { data: { runs: [] }, error: undefined };
			return { data: undefined, error: undefined };
		});

		render(<TaskDetailPanel taskId="task-1" onClose={onClose} />, { wrapper });

		await screen.findByText("Implement Login");
		fireEvent.click(screen.getByTestId("close-task-detail"));
		expect(onClose).toHaveBeenCalled();
	});

	it("renders run history tab with runs sorted by attempt descending", async () => {
		const user = userEvent.setup();
		getMock.mockImplementation(async (url: string) => {
			if (url === "/api/v1/workflow/tasks/{id}") return { data: { task: mockTask }, error: undefined };
			if (url === "/api/v1/workflow/tasks/{id}/runs") return { data: { runs: mockRuns }, error: undefined };
			return { data: undefined, error: undefined };
		});

		render(<TaskDetailPanel taskId="task-1" onClose={vi.fn()} />, { wrapper });

		await screen.findByText("Implement Login");
		await user.click(screen.getByText("Run History"));
		await waitFor(() => {
			expect(screen.getByTestId("run-entry-run-1")).toBeInTheDocument();
		});
		expect(screen.getByTestId("run-entry-run-0")).toBeInTheDocument();
	});

	it("shows empty state when no runs exist", async () => {
		const user = userEvent.setup();
		getMock.mockImplementation(async (url: string) => {
			if (url === "/api/v1/workflow/tasks/{id}") return { data: { task: mockTask }, error: undefined };
			if (url === "/api/v1/workflow/tasks/{id}/runs") return { data: { runs: [] }, error: undefined };
			return { data: undefined, error: undefined };
		});

		render(<TaskDetailPanel taskId="task-1" onClose={vi.fn()} />, { wrapper });

		await screen.findByText("Implement Login");
		await user.click(screen.getByText("Run History"));
		await waitFor(() => {
			expect(screen.getByText("No execution records")).toBeInTheDocument();
		});
	});
});
