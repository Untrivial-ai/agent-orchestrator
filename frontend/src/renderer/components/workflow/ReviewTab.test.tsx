import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";

const { postMock, getMock, hasTrustedApiBaseUrlMock } = vi.hoisted(() => ({
	postMock: vi.fn(),
	getMock: vi.fn(),
	hasTrustedApiBaseUrlMock: vi.fn(() => true),
}));

vi.mock("../../../lib/api-client", () => ({
	apiClient: { GET: getMock, POST: postMock },
	hasTrustedApiBaseUrl: hasTrustedApiBaseUrlMock,
	apiErrorMessage: (e: unknown) => (e instanceof Error ? e.message : "error"),
}));

vi.mock("react-i18next", () => ({
	useTranslation: () => ({
		t: (key: string, opts?: Record<string, unknown>) => opts?.defaultValue ?? key,
	}),
}));

vi.mock("lucide-react", () => ({
	XIcon: () => null,
}));

import { ReviewTab } from "./ReviewTab";
import type { components } from "../../../api/schema";
import { workflowQueryKeys } from "../../hooks/useWorkflowPlans";

type TaskView = components["schemas"]["TaskView"];
type RunView = components["schemas"]["ControllersRunView"];
type RunReviewView = components["schemas"]["RunReviewView"];

function createWrapper() {
	const queryClient = new QueryClient({
		defaultOptions: { queries: { retry: false, retryDelay: 0 } },
	});
	return {
		queryClient,
		wrapper: ({ children }: { children: ReactNode }) => (
			<QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
		),
	};
}

const baseTask: TaskView = {
	id: "task-1",
	stageId: "stage-1",
	title: "Test Task",
	status: "review",
	taskType: "code_generation",
	createdAt: "2026-01-01T00:00:00Z",
	acceptanceCriteria: "",
	agentRoleId: "",
	description: "",
	providerId: "",
	providerModelId: "",
	sequence: 1,
};

const succeededRun: RunView = {
	id: "run-1",
	taskId: "task-1",
	status: "succeeded",
	attempt: 1,
	createdAt: "2026-01-01T00:00:00Z",
};

const failedRun: RunView = {
	id: "run-1",
	taskId: "task-1",
	status: "failed",
	attempt: 1,
	createdAt: "2026-01-01T00:00:00Z",
};

const cancelledRun: RunView = {
	id: "run-1",
	taskId: "task-1",
	status: "cancelled",
	attempt: 1,
	createdAt: "2026-01-01T00:00:00Z",
};

const runningRun: RunView = {
	id: "run-1",
	taskId: "task-1",
	status: "running",
	attempt: 1,
	createdAt: "2026-01-01T00:00:00Z",
};

const pendingRun: RunView = {
	id: "run-1",
	taskId: "task-1",
	status: "pending",
	attempt: 1,
	createdAt: "2026-01-01T00:00:00Z",
};

const pendingReview: RunReviewView = {
	id: "review-1",
	runId: "run-1",
	source: "human",
	status: "pending",
	issues: "",
	summary: "",
	createdAt: "2026-01-01T00:00:00Z",
};

const passedReview: RunReviewView = {
	id: "review-1",
	runId: "run-1",
	source: "human",
	status: "passed",
	issues: "",
	summary: "Looks good",
	createdAt: "2026-01-01T00:00:00Z",
	completedAt: "2026-01-01T01:00:00Z",
};

const rejectedReviewWithIssues: RunReviewView = {
	id: "review-1",
	runId: "run-1",
	source: "human",
	status: "rejected",
	issues: "Needs fix",
	summary: "",
	createdAt: "2026-01-01T00:00:00Z",
	completedAt: "2026-01-01T01:00:00Z",
};

const rejectedReviewNoIssues: RunReviewView = {
	id: "review-1",
	runId: "run-1",
	source: "human",
	status: "rejected",
	issues: "",
	summary: "",
	createdAt: "2026-01-01T00:00:00Z",
	completedAt: "2026-01-01T01:00:00Z",
};

beforeEach(() => {
	postMock.mockReset();
	getMock.mockReset();
});

function seedReviews(queryClient: QueryClient, runId: string, reviews: RunReviewView[]) {
	queryClient.setQueryData(workflowQueryKeys.reviews(runId), reviews);
}

// ──────────────────────────────────────────────────────────────────────
// Scenario A: Run not yet finished
// ──────────────────────────────────────────────────────────────────────
describe("ReviewTab — Scenario A: Run not finished", () => {
	it("shows waiting message when latestRun is running", async () => {
		const { wrapper } = createWrapper();
		render(<ReviewTab task={baseTask} latestRun={runningRun} taskId="task-1" />, { wrapper });
		await waitFor(() => expect(screen.getByText(/waitingExecution/i)).toBeInTheDocument());
	});

	it("shows waiting message when latestRun is pending", async () => {
		const { wrapper } = createWrapper();
		render(<ReviewTab task={baseTask} latestRun={pendingRun} taskId="task-1" />, { wrapper });
		await waitFor(() => expect(screen.getByText(/waitingExecution/i)).toBeInTheDocument());
	});

	it("shows waiting message when latestRun is null", async () => {
		const { wrapper } = createWrapper();
		render(<ReviewTab task={baseTask} latestRun={null} taskId="task-1" />, { wrapper });
		await waitFor(() => expect(screen.getByText(/waitingExecution/i)).toBeInTheDocument());
	});
});

// ──────────────────────────────────────────────────────────────────────
// Scenario B: Run failed
// ──────────────────────────────────────────────────────────────────────
describe("ReviewTab — Scenario B: Run failed", () => {
	it("shows no review message", async () => {
		const { wrapper } = createWrapper();
		render(<ReviewTab task={baseTask} latestRun={failedRun} taskId="task-1" />, { wrapper });
		await waitFor(() => expect(screen.getByText(/failedNoReview/i)).toBeInTheDocument());
	});
});

// ──────────────────────────────────────────────────────────────────────
// Scenario B2: Run cancelled
// ──────────────────────────────────────────────────────────────────────
describe("ReviewTab — Scenario B2: Run cancelled", () => {
	it("shows no review message", async () => {
		const { wrapper } = createWrapper();
		render(<ReviewTab task={baseTask} latestRun={cancelledRun} taskId="task-1" />, { wrapper });
		await waitFor(() => expect(screen.getByText(/cancelledNoReview/i)).toBeInTheDocument());
	});
});

// ──────────────────────────────────────────────────────────────────────
// Scenario C: Succeeded + Task REVIEW + no Review → Create button
// ──────────────────────────────────────────────────────────────────────
describe("ReviewTab — Scenario C: Create Review", () => {
	it("shows create button when run succeeded + task review + no review", async () => {
		const { wrapper } = createWrapper();
		render(<ReviewTab task={baseTask} latestRun={succeededRun} taskId="task-1" />, { wrapper });
		await waitFor(() => expect(screen.getByTestId("create-review")).toBeInTheDocument());
	});

	it("does NOT show create button when task status is not review", async () => {
		const { wrapper } = createWrapper();
		const taskNotReview = { ...baseTask, status: "ready" } as TaskView;
		render(<ReviewTab task={taskNotReview} latestRun={succeededRun} taskId="task-1" />, { wrapper });
		await waitFor(() => expect(screen.queryByTestId("create-review")).not.toBeInTheDocument());
	});

	it("does NOT show create button when review already exists", async () => {
		const { queryClient, wrapper } = createWrapper();
		seedReviews(queryClient, "run-1", [pendingReview]);
		render(<ReviewTab task={baseTask} latestRun={succeededRun} taskId="task-1" />, { wrapper });
		await waitFor(() => expect(screen.queryByTestId("create-review")).not.toBeInTheDocument());
	});
});

// ──────────────────────────────────────────────────────────────────────
// Scenario D: Review pending → Pass/Reject buttons
// ──────────────────────────────────────────────────────────────────────
describe("ReviewTab — Scenario D: Review pending", () => {
	it("shows pass and reject buttons", async () => {
		const { queryClient, wrapper } = createWrapper();
		seedReviews(queryClient, "run-1", [pendingReview]);
		render(<ReviewTab task={baseTask} latestRun={succeededRun} taskId="task-1" />, { wrapper });
		await waitFor(() => {
			expect(screen.getByTestId("pass-review")).toBeInTheDocument();
			expect(screen.getByTestId("reject-review")).toBeInTheDocument();
		});
	});

	it("shows review status badge", async () => {
		const { queryClient, wrapper } = createWrapper();
		seedReviews(queryClient, "run-1", [pendingReview]);
		render(<ReviewTab task={baseTask} latestRun={succeededRun} taskId="task-1" />, { wrapper });
		await waitFor(() => expect(screen.getByText(/reviewStatus/i)).toBeInTheDocument());
	});

	it("shows review source as human", async () => {
		const { queryClient, wrapper } = createWrapper();
		seedReviews(queryClient, "run-1", [pendingReview]);
		render(<ReviewTab task={baseTask} latestRun={succeededRun} taskId="task-1" />, { wrapper });
		await waitFor(() => expect(screen.getByText(/sourceHuman/i)).toBeInTheDocument());
	});
});

// ──────────────────────────────────────────────────────────────────────
// Scenario E: Review passed → readonly
// ──────────────────────────────────────────────────────────────────────
describe("ReviewTab — Scenario E: Review passed", () => {
	it("shows passed message", async () => {
		const { queryClient, wrapper } = createWrapper();
		seedReviews(queryClient, "run-1", [passedReview]);
		render(<ReviewTab task={baseTask} latestRun={succeededRun} taskId="task-1" />, { wrapper });
		await waitFor(() => expect(screen.getByText(/passedMessage/i)).toBeInTheDocument());
	});

	it("does NOT show pass/reject buttons", async () => {
		const { queryClient, wrapper } = createWrapper();
		seedReviews(queryClient, "run-1", [passedReview]);
		render(<ReviewTab task={baseTask} latestRun={succeededRun} taskId="task-1" />, { wrapper });
		await waitFor(() => expect(screen.queryByTestId("pass-review")).not.toBeInTheDocument());
		expect(screen.queryByTestId("reject-review")).not.toBeInTheDocument();
	});
});

// ──────────────────────────────────────────────────────────────────────
// Scenario F: Review rejected + Task READY + non-empty issues → Retry
// ──────────────────────────────────────────────────────────────────────
describe("ReviewTab — Scenario F: Review rejected with retry", () => {
	it("shows retry button when rejected + task ready + non-empty issues", async () => {
		const { queryClient, wrapper } = createWrapper();
		seedReviews(queryClient, "run-1", [rejectedReviewWithIssues]);
		const taskReady = { ...baseTask, status: "ready" } as TaskView;
		render(<ReviewTab task={taskReady} latestRun={succeededRun} taskId="task-1" />, { wrapper });
		await waitFor(() => expect(screen.getByTestId("retry-run")).toBeInTheDocument());
	});

	it("shows rejected message", async () => {
		const { queryClient, wrapper } = createWrapper();
		seedReviews(queryClient, "run-1", [rejectedReviewWithIssues]);
		const taskReady = { ...baseTask, status: "ready" } as TaskView;
		render(<ReviewTab task={taskReady} latestRun={succeededRun} taskId="task-1" />, { wrapper });
		await waitFor(() => expect(screen.getByText(/rejectedMessage/i)).toBeInTheDocument());
	});

	it("does NOT show retry when task status is not ready", async () => {
		const { queryClient, wrapper } = createWrapper();
		seedReviews(queryClient, "run-1", [rejectedReviewWithIssues]);
		const taskReview = { ...baseTask, status: "review" } as TaskView;
		render(<ReviewTab task={taskReview} latestRun={succeededRun} taskId="task-1" />, { wrapper });
		await waitFor(() => expect(screen.queryByTestId("retry-run")).not.toBeInTheDocument());
	});

	it("does NOT show retry when issues is empty", async () => {
		const { queryClient, wrapper } = createWrapper();
		seedReviews(queryClient, "run-1", [rejectedReviewNoIssues]);
		const taskReady = { ...baseTask, status: "ready" } as TaskView;
		render(<ReviewTab task={taskReady} latestRun={succeededRun} taskId="task-1" />, { wrapper });
		await waitFor(() => expect(screen.queryByTestId("retry-run")).not.toBeInTheDocument());
	});
});

// ──────────────────────────────────────────────────────────────────────
// StatusBadge review entity
// ──────────────────────────────────────────────────────────────────────
describe("ReviewTab — StatusBadge for review entity", () => {
	it("shows review status badge for pending review", async () => {
		const { queryClient, wrapper } = createWrapper();
		seedReviews(queryClient, "run-1", [pendingReview]);
		render(<ReviewTab task={baseTask} latestRun={succeededRun} taskId="task-1" />, { wrapper });
		await waitFor(() => expect(screen.getByText(/reviewStatus/i)).toBeInTheDocument());
	});
});
