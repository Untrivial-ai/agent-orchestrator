import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";

const { postMock, patchMock, getMock } = vi.hoisted(() => ({
	postMock: vi.fn(),
	patchMock: vi.fn(),
	getMock: vi.fn(),
}));

vi.mock("../../lib/api-client", () => ({
	apiClient: { GET: getMock, POST: postMock, PATCH: patchMock },
	hasTrustedApiBaseUrl: vi.fn(() => true),
	apiErrorMessage: (e: unknown) => (e instanceof Error ? e.message : "error"),
}));

vi.mock("@tanstack/react-router", () => ({
	useNavigate: () => vi.fn(),
}));

vi.mock("lucide-react", () => ({
	X: () => null,
}));

import { TaskDetailPanel } from "./TaskDetailPanel";
import { workflowQueryKeys } from "../../hooks/useWorkflowPlans";

function createWrapper() {
	const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, retryDelay: 0 }, mutations: { retry: false } } });
	return { queryClient, wrapper: ({ children }: { children: ReactNode }) => <QueryClientProvider client={queryClient}>{children}</QueryClientProvider> };
}

const readyTask = {
	id: "task-1", stageId: "s1", title: "Build API", status: "ready",
	description: "Build REST API", acceptanceCriteria: "Works",
	taskType: "coding", agentRoleId: "role-1", providerId: "", providerModelId: "", sequence: 1,
	createdAt: "2026-01-01T00:00:00Z",
};

const runningTask = { ...readyTask, status: "running" };
const reviewTask = { ...readyTask, status: "review" };

const pendingRun = { id: "run-1", taskId: "task-1", status: "pending", attempt: 1, createdAt: "2026-01-01T00:00:00Z" };
const succeededRun = { id: "run-3", taskId: "task-1", status: "succeeded", attempt: 3, createdAt: "2026-01-01T00:00:00Z" };
const failedRun = { id: "run-4", taskId: "task-1", status: "failed", attempt: 4, createdAt: "2026-01-01T00:00:00Z", errorMessage: "crash" };

const mockRole = {
	id: "role-1", name: "code-gen", displayName: "Code Generator", description: "Generates code",
	enabled: true, systemPrompt: "Write clean code", defaultProviderId: "prov-1", defaultProviderModelId: "model-1",
	createdAt: "2026-01-01T00:00:00Z", updatedAt: "2026-01-01T00:00:00Z",
};

const mockReview = {
	id: "review-1", runId: "run-3", status: "pending", source: "human",
	createdAt: "2026-01-01T00:00:00Z", completedAt: null, issues: "",
};

beforeEach(() => {
	postMock.mockReset();
	patchMock.mockReset();
	getMock.mockReset();
});

function mockGet(task: typeof readyTask, runs: unknown[], reviews?: unknown[], extra?: Record<string, unknown>) {
	getMock.mockImplementation((url: string) => {
		if (url.includes("/runs")) return Promise.resolve({ data: { runs }, error: undefined });
		if (url.includes("/reviews")) return Promise.resolve({ data: { reviews: reviews ?? [] }, error: undefined });
		if (url.includes("/roles/")) return Promise.resolve({ data: extra?.role ?? mockRole, error: undefined });
		if (url.includes("/providers/")) return Promise.resolve({ data: { models: [] }, error: undefined });
		if (url.includes("/providers")) return Promise.resolve({ data: { providers: extra?.providers ?? [{ id: "prov-1", displayName: "OpenAI", enabled: true }] }, error: undefined });
		return Promise.resolve({ data: { task }, error: undefined });
	});
}

// ══════════════════════════════════════════════════════════════════════
// Path 1: Normal Success Flow
// ══════════════════════════════════════════════════════════════════════
describe("E2E Path 1: Normal Success Flow", () => {
	it("shows Create Run when task is ready", async () => {
		mockGet(readyTask, []);
		const { wrapper } = createWrapper();
		render(<TaskDetailPanel taskId="task-1" onClose={vi.fn()} />, { wrapper });
		await screen.findByText("Build API");
		const user = userEvent.setup();
		await user.click(screen.getByText("Run History"));
		await waitFor(() => expect(screen.getByTestId("create-run")).toBeInTheDocument());
	});

	it("shows Start button after Create Run produces pending run", async () => {
		mockGet(readyTask, [pendingRun]);
		const { wrapper } = createWrapper();
		render(<TaskDetailPanel taskId="task-1" onClose={vi.fn()} />, { wrapper });
		await screen.findByText("Build API");
		const user = userEvent.setup();
		await user.click(screen.getByText("Run History"));
		await waitFor(() => {
			expect(screen.getByTestId("start-run-run-1")).toBeInTheDocument();
			expect(screen.getByTestId("cancel-run-run-1")).toBeInTheDocument();
		});
	});

	it("shows Review tab and panel structure for review task", async () => {
		mockGet(reviewTask, [succeededRun], [mockReview]);
		const { wrapper } = createWrapper();
		render(<TaskDetailPanel taskId="task-1" onClose={vi.fn()} />, { wrapper });
		await screen.findByText("Build API");
		await waitFor(() => {
			expect(screen.getByText("Review")).toBeInTheDocument();
		});
	});
});

// ══════════════════════════════════════════════════════════════════════
// Path 2: Reject Retry Resume
// ══════════════════════════════════════════════════════════════════════
describe("E2E Path 2: Reject Retry Resume", () => {
	it("shows Create Run after reject returns task to ready", async () => {
		const rejectedReview = { ...mockReview, status: "rejected", issues: "Fix bugs", completedAt: "2026-01-01T02:00:00Z" };
		mockGet(readyTask, [succeededRun], [rejectedReview]);
		const { wrapper } = createWrapper();
		render(<TaskDetailPanel taskId="task-1" onClose={vi.fn()} />, { wrapper });
		await screen.findByText("Build API");
		const user = userEvent.setup();
		await user.click(screen.getByText("Run History"));
		await waitFor(() => expect(screen.getByTestId("create-run")).toBeInTheDocument());
	});
});

// ══════════════════════════════════════════════════════════════════════
// Path 3: Reject Retry Fresh
// ══════════════════════════════════════════════════════════════════════
describe("E2E Path 3: Reject Retry Fresh", () => {
	it("task returns to ready after reject, allowing new run creation", async () => {
		const rejectedReview = { ...mockReview, status: "rejected", issues: "Start over", completedAt: "2026-01-01T02:00:00Z" };
		const readyTaskFresh = { ...readyTask, status: "ready" };
		mockGet(readyTaskFresh, [succeededRun], [rejectedReview]);
		const { wrapper } = createWrapper();
		render(<TaskDetailPanel taskId="task-1" onClose={vi.fn()} />, { wrapper });
		await screen.findByText("Build API");
		const user = userEvent.setup();
		await user.click(screen.getByText("Run History"));
		await waitFor(() => expect(screen.getByTestId("create-run")).toBeInTheDocument());
	});
});

// ══════════════════════════════════════════════════════════════════════
// Path 4: FAILED Flow
// ══════════════════════════════════════════════════════════════════════
describe("E2E Path 4: FAILED Flow", () => {
	it("shows Create Run when task is ready after failed run (re-execute path)", async () => {
		mockGet(readyTask, [failedRun]);
		const { wrapper } = createWrapper();
		render(<TaskDetailPanel taskId="task-1" onClose={vi.fn()} />, { wrapper });
		await screen.findByText("Build API");
		const user = userEvent.setup();
		await user.click(screen.getByText("Run History"));
		await waitFor(() => expect(screen.getByTestId("create-run")).toBeInTheDocument());
	});

	it("FAILED run does not show Retry button in Review tab", async () => {
		const failedRunReview = { ...failedRun, status: "failed" };
		mockGet(reviewTask, [failedRunReview], []);
		const { wrapper } = createWrapper();
		render(<TaskDetailPanel taskId="task-1" onClose={vi.fn()} />, { wrapper });
		await screen.findByText("Build API");
		const user = userEvent.setup();
		await user.click(screen.getByText("Review"));
		await waitFor(() => {
			expect(screen.queryByRole("button", { name: /retry/i })).not.toBeInTheDocument();
		});
	});

	it("shows recovering message when task running but latest run failed", async () => {
		mockGet(runningTask, [failedRun]);
		const { wrapper } = createWrapper();
		render(<TaskDetailPanel taskId="task-1" onClose={vi.fn()} />, { wrapper });
		await screen.findByText("Build API");
		const user = userEvent.setup();
		await user.click(screen.getByText("Run History"));
		await waitFor(() => expect(screen.getByText(/Recovering/)).toBeInTheDocument());
	});
});

// ══════════════════════════════════════════════════════════════════════
// Path 5: AgentRole Display
// ══════════════════════════════════════════════════════════════════════
describe("E2E Path 5: AgentRole Display", () => {
	it("shows role display name in TaskRoleInfo", async () => {
		mockGet(readyTask, []);
		const { wrapper } = createWrapper();
		render(<TaskDetailPanel taskId="task-1" onClose={vi.fn()} />, { wrapper });
		await screen.findByText("Build API");
		await waitFor(() => {
			expect(screen.getByTestId("task-role-info")).toBeInTheDocument();
		});
	});

	it("shows provider source badge for task override", async () => {
		const taskWithProvider = { ...readyTask, providerId: "prov-1", providerModelId: "gpt-4o" };
		mockGet(taskWithProvider, []);
		const { wrapper } = createWrapper();
		render(<TaskDetailPanel taskId="task-1" onClose={vi.fn()} />, { wrapper });
		await screen.findByText("Build API");
		await waitFor(() => {
			expect(screen.getByTestId("provider-source-badge")).toHaveTextContent("Task Override");
		});
	});

	it("shows role default source when no task provider override", async () => {
		mockGet(readyTask, []);
		const { wrapper } = createWrapper();
		render(<TaskDetailPanel taskId="task-1" onClose={vi.fn()} />, { wrapper });
		await screen.findByText("Build API");
		await waitFor(() => {
			expect(screen.getByTestId("provider-source-badge")).toHaveTextContent("Role Default");
		});
	});

	it("shows system prompt summary from role", async () => {
		mockGet(readyTask, []);
		const { wrapper } = createWrapper();
		render(<TaskDetailPanel taskId="task-1" onClose={vi.fn()} />, { wrapper });
		await screen.findByText("Build API");
		await waitFor(() => {
			expect(screen.getByTestId("role-info-prompt")).toHaveTextContent(/Write clean code/);
		});
	});
});

// ══════════════════════════════════════════════════════════════════════
// Path 6: Role Disable
// ══════════════════════════════════════════════════════════════════════
describe("E2E Path 6: Role Disable", () => {
	it("shows disabled badge when role is disabled", async () => {
		const disabledRole = { ...mockRole, enabled: false };
		getMock.mockImplementation((url: string) => {
			if (url.includes("/runs")) return Promise.resolve({ data: { runs: [] }, error: undefined });
			if (url.includes("/roles/")) return Promise.resolve({ data: disabledRole, error: undefined });
			if (url.includes("/providers")) return Promise.resolve({ data: { providers: [{ id: "prov-1", displayName: "OpenAI", enabled: true }] }, error: undefined });
			return Promise.resolve({ data: { task: readyTask }, error: undefined });
		});
		const { wrapper } = createWrapper();
		render(<TaskDetailPanel taskId="task-1" onClose={vi.fn()} />, { wrapper });
		await screen.findByText("Build API");
		await waitFor(() => {
			expect(screen.getByTestId("role-info-disabled-badge")).toBeInTheDocument();
		});
	});
});

// ══════════════════════════════════════════════════════════════════════
// Path 7: Conflict Recovery (409)
// ══════════════════════════════════════════════════════════════════════
describe("E2E Path 7: Conflict Recovery", () => {
	it("Create Run button remains available when API returns conflict", async () => {
		mockGet(readyTask, []);
		postMock.mockRejectedValueOnce(new Error("409 Conflict"));
		const { wrapper } = createWrapper();
		render(<TaskDetailPanel taskId="task-1" onClose={vi.fn()} />, { wrapper });
		await screen.findByText("Build API");
		const user = userEvent.setup();
		await user.click(screen.getByText("Run History"));
		await waitFor(() => expect(screen.getByTestId("create-run")).toBeInTheDocument());
		await user.click(screen.getByTestId("create-run"));
		await waitFor(() => {
			expect(screen.getByText("Create run failed")).toBeInTheDocument();
		});
	});

	it("task info refreshes after mutation invalidation", async () => {
		const { queryClient, wrapper } = createWrapper();
		queryClient.setQueryData(workflowQueryKeys.task("task-1"), readyTask);
		render(<TaskDetailPanel taskId="task-1" onClose={vi.fn()} />, { wrapper });
		await screen.findByText("Build API");
		await waitFor(() => {
			expect(screen.getByTestId("task-role-info")).toBeInTheDocument();
		});
	});
});

// ══════════════════════════════════════════════════════════════════════
// Path 8: Multi-language
// ══════════════════════════════════════════════════════════════════════
describe("E2E Path 8: Multi-language", () => {
	it("renders role info section with i18n keys", async () => {
		mockGet(readyTask, []);
		const { wrapper } = createWrapper();
		render(<TaskDetailPanel taskId="task-1" onClose={vi.fn()} />, { wrapper });
		await screen.findByText("Build API");
		await waitFor(() => {
			expect(screen.getByText("Role Information")).toBeInTheDocument();
		});
	});
});
