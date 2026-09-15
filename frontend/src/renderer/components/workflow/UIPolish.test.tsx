import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";

const { getMock, postMock } = vi.hoisted(() => ({
	getMock: vi.fn(),
	postMock: vi.fn(),
}));

vi.mock("../../lib/api-client", () => ({
	apiClient: { GET: getMock, POST: postMock },
	hasTrustedApiBaseUrl: vi.fn(() => true),
	apiErrorMessage: (e: unknown) => (e instanceof Error ? e.message : "error"),
}));

vi.mock("@tanstack/react-router", () => ({
	useNavigate: () => vi.fn(),
}));

vi.mock("lucide-react", () => ({
	ArrowLeft: () => null,
	X: () => null,
}));

vi.mock("../ProjectWorkspaceNav", () => ({
	ProjectWorkspaceNav: () => null,
}));

import { StageColumn } from "./StageColumn";
import { TaskDetailPanel } from "./TaskDetailPanel";
import { PlanDetailView } from "./PlanDetailView";

const mockStage = {
	id: "stage-1",
	planId: "plan-1",
	title: "Setup",
	description: "Initial setup",
	acceptanceCriteria: "All checks pass",
	status: "pending" as const,
	sequence: 1,
	createdAt: "2026-01-01T00:00:00Z",
};

function createWrapper() {
	const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, retryDelay: 0 }, mutations: { retry: false } } });
	return { queryClient, wrapper: ({ children }: { children: ReactNode }) => <QueryClientProvider client={queryClient}>{children}</QueryClientProvider> };
}

beforeEach(() => {
	getMock.mockReset();
	postMock.mockReset();
});

// ══════════════════════════════════════════════════════════════════════
// Loading States
// ══════════════════════════════════════════════════════════════════════
describe("Loading States", () => {
	it("StageColumn shows skeleton while tasks loading", () => {
		getMock.mockReturnValue(new Promise(() => {}));
		const { wrapper } = createWrapper();
		render(<StageColumn stage={mockStage} planId="plan-1" planStatus="in_progress" />, { wrapper });
		expect(document.querySelector(".animate-pulse")).toBeTruthy();
	});

	it("TaskDetailPanel shows skeleton while task loading", () => {
		getMock.mockReturnValue(new Promise(() => {}));
		const { wrapper } = createWrapper();
		render(<TaskDetailPanel taskId="task-1" onClose={vi.fn()} />, { wrapper });
		expect(document.querySelector(".animate-pulse")).toBeTruthy();
	});

	it("PlanDetailView shows skeleton while plan loading", () => {
		getMock.mockReturnValue(new Promise(() => {}));
		const { wrapper } = createWrapper();
		render(<PlanDetailView projectId="proj-1" planId="plan-1" />, { wrapper });
		expect(document.querySelector(".animate-pulse")).toBeTruthy();
	});
});

// ══════════════════════════════════════════════════════════════════════
// Empty States
// ══════════════════════════════════════════════════════════════════════
describe("Empty States", () => {
	it("StageColumn shows empty message when no tasks", async () => {
		getMock.mockImplementation((url: string) => {
			if (url.includes("/tasks")) return Promise.resolve({ data: { tasks: [] }, error: undefined });
			return Promise.resolve({ data: undefined, error: undefined });
		});
		const { wrapper } = createWrapper();
		render(<StageColumn stage={mockStage} planId="plan-1" planStatus="in_progress" />, { wrapper });
		await waitFor(() => expect(screen.getByText("No tasks")).toBeInTheDocument());
	});

	it("TaskDetailPanel shows Create Run button when ready task has no runs", async () => {
		getMock.mockImplementation((url: string) => {
			if (url.includes("/runs")) return Promise.resolve({ data: { runs: [] }, error: undefined });
			if (url.includes("/reviews")) return Promise.resolve({ data: { reviews: [] }, error: undefined });
			return Promise.resolve({ data: { task: { id: "t1", stageId: "s1", title: "T", status: "ready", taskType: "coding", sequence: 1, createdAt: "2026-01-01T00:00:00Z" } }, error: undefined });
		});
		const { wrapper } = createWrapper();
		render(<TaskDetailPanel taskId="t1" onClose={vi.fn()} />, { wrapper });
		await screen.findByText("T");
		const user = userEvent.setup();
		await user.click(screen.getByText("Run History"));
		await waitFor(() => expect(screen.getByTestId("create-run")).toBeInTheDocument());
	});

	it("TaskDetailPanel shows Review tab for review-status task", async () => {
		getMock.mockImplementation((url: string) => {
			if (url.includes("/runs")) return Promise.resolve({ data: { runs: [{ id: "r1", taskId: "t1", status: "succeeded", attempt: 1, createdAt: "2026-01-01T00:00:00Z" }] }, error: undefined });
			if (url.includes("/reviews")) return Promise.resolve({ data: { reviews: [] }, error: undefined });
			return Promise.resolve({ data: { task: { id: "t1", stageId: "s1", title: "T", status: "review", taskType: "coding", sequence: 1, createdAt: "2026-01-01T00:00:00Z" } }, error: undefined });
		});
		const { wrapper } = createWrapper();
		render(<TaskDetailPanel taskId="t1" onClose={vi.fn()} />, { wrapper });
		await screen.findByText("T");
		await waitFor(() => expect(screen.getByText("Review")).toBeInTheDocument());
	});
});

// ══════════════════════════════════════════════════════════════════════
// Error States
// ══════════════════════════════════════════════════════════════════════
describe("Error States", () => {
	it("StageColumn shows error when tasks fail to load", async () => {
		getMock.mockResolvedValue({ data: undefined, error: { message: "Server error" } });
		const { wrapper } = createWrapper();
		render(<StageColumn stage={mockStage} planId="plan-1" planStatus="in_progress" />, { wrapper });
		await waitFor(() => expect(screen.getByText("Failed to load tasks")).toBeInTheDocument());
	});

	it("TaskDetailPanel shows error when task not found", async () => {
		getMock.mockResolvedValue({ data: undefined, error: { message: "404" } });
		const { wrapper } = createWrapper();
		render(<TaskDetailPanel taskId="nonexistent" onClose={vi.fn()} />, { wrapper });
		await waitFor(() => expect(screen.getByText("Task not found")).toBeInTheDocument());
	});

	it("PlanDetailView shows error when plan not found", async () => {
		getMock.mockResolvedValue({ data: undefined, error: { message: "404" } });
		const { wrapper } = createWrapper();
		render(<PlanDetailView projectId="proj-1" planId="bad-plan" />, { wrapper });
		await waitFor(() => expect(screen.getByText("Plan not found")).toBeInTheDocument());
	});

	it("TaskDetailPanel shows create run error on API failure", async () => {
		const task = { id: "t1", stageId: "s1", title: "T", status: "ready", taskType: "coding", sequence: 1, createdAt: "2026-01-01T00:00:00Z" };
		getMock.mockImplementation((url: string) => {
			if (url.includes("/runs")) return Promise.resolve({ data: { runs: [] }, error: undefined });
			if (url.includes("/reviews")) return Promise.resolve({ data: { reviews: [] }, error: undefined });
			return Promise.resolve({ data: { task }, error: undefined });
		});
		postMock.mockRejectedValueOnce(new Error("409 Conflict"));
		const { wrapper } = createWrapper();
		render(<TaskDetailPanel taskId="t1" onClose={vi.fn()} />, { wrapper });
		await screen.findByText("T");
		const user = userEvent.setup();
		await user.click(screen.getByText("Run History"));
		await waitFor(() => expect(screen.getByTestId("create-run")).toBeInTheDocument());
		await user.click(screen.getByTestId("create-run"));
		await waitFor(() => expect(screen.getByText("Create run failed")).toBeInTheDocument());
	});

	it("StageColumn shows error text in destructive style", async () => {
		getMock.mockResolvedValue({ data: undefined, error: { message: "500" } });
		const { wrapper } = createWrapper();
		render(<StageColumn stage={mockStage} planId="plan-1" planStatus="in_progress" />, { wrapper });
		await waitFor(() => {
			const errorText = screen.getByText("Failed to load tasks");
			expect(errorText).toHaveClass("text-destructive");
		});
	});
});

// ══════════════════════════════════════════════════════════════════════
// Conflict (409) Recovery
// ══════════════════════════════════════════════════════════════════════
describe("Conflict Recovery", () => {
	it("PlanDetailView refetches plan after mutation error", async () => {
		const plan = { id: "plan-1", projectId: "proj-1", title: "My Plan", status: "draft", createdAt: "2026-01-01T00:00:00Z" };
		getMock.mockResolvedValue({ data: { plan }, error: undefined });
		postMock.mockRejectedValueOnce(new Error("409 Conflict"));
		const { wrapper } = createWrapper();
		render(<PlanDetailView projectId="proj-1" planId="plan-1" />, { wrapper });
		await screen.findByText("My Plan");
		const user = userEvent.setup();
		await user.click(screen.getByTestId("plan-confirm"));
		await waitFor(() => {
			expect(screen.getByText("My Plan")).toBeInTheDocument();
		});
	});

	it("TaskDetailPanel shows create run error on 409 and remains usable", async () => {
		const task = { id: "t1", stageId: "s1", title: "Build", status: "ready", taskType: "coding", sequence: 1, createdAt: "2026-01-01T00:00:00Z" };
		getMock.mockImplementation((url: string) => {
			if (url.includes("/runs")) return Promise.resolve({ data: { runs: [] }, error: undefined });
			if (url.includes("/reviews")) return Promise.resolve({ data: { reviews: [] }, error: undefined });
			return Promise.resolve({ data: { task }, error: undefined });
		});
		postMock.mockRejectedValue(new Error("409 Conflict"));
		const { wrapper } = createWrapper();
		render(<TaskDetailPanel taskId="t1" onClose={vi.fn()} />, { wrapper });
		await screen.findByText("Build");
		const user = userEvent.setup();
		await user.click(screen.getByText("Run History"));
		await waitFor(() => expect(screen.getByTestId("create-run")).toBeInTheDocument());
		await user.click(screen.getByTestId("create-run"));
		await waitFor(() => expect(screen.getByText("Create run failed")).toBeInTheDocument());
		expect(screen.getByTestId("create-run")).toBeInTheDocument();
	});
});

// ══════════════════════════════════════════════════════════════════════
// i18n Workflow Key Completeness
// ══════════════════════════════════════════════════════════════════════
describe("i18n Workflow Key Completeness", () => {
	const i18nDir = resolve(__dirname, "../../i18n");

	function loadLocale(locale: string): Record<string, string> {
		return JSON.parse(readFileSync(resolve(i18nDir, `${locale}.json`), "utf-8"));
	}

	it("en.json has all required workflow.error keys", () => {
		const en = loadLocale("en");
		const requiredKeys = [
			"workflow.error.loadFailed",
			"workflow.error.planNotFound",
			"workflow.error.stagesLoadFailed",
			"workflow.error.tasksLoadFailed",
			"workflow.error.taskNotFound",
			"workflow.error.rolesLoadFailed",
		];
		for (const key of requiredKeys) {
			expect(en[key], `en missing ${key}`).toBeTruthy();
		}
	});

	it("all 8 locales have workflow.error.tasksLoadFailed key", () => {
		const locales = ["en", "zh-CN", "ja", "ko", "es", "fr", "de", "pt-BR"];
		for (const locale of locales) {
			const data = loadLocale(locale);
			expect(data["workflow.error.tasksLoadFailed"], `${locale} missing workflow.error.tasksLoadFailed`).toBeTruthy();
		}
	});

	it("all 8 locales have workflow.empty.noTasks key", () => {
		const locales = ["en", "zh-CN", "ja", "ko", "es", "fr", "de", "pt-BR"];
		for (const locale of locales) {
			const data = loadLocale(locale);
			expect(data["workflow.empty.noTasks"], `${locale} missing workflow.empty.noTasks`).toBeTruthy();
		}
	});

	it("all 8 locales have workflow.run.createFailed key", () => {
		const locales = ["en", "zh-CN", "ja", "ko", "es", "fr", "de", "pt-BR"];
		for (const locale of locales) {
			const data = loadLocale(locale);
			expect(data["workflow.run.createFailed"], `${locale} missing workflow.run.createFailed`).toBeTruthy();
		}
	});

	it("all 8 locales have workflow.error.rolesLoadFailed key", () => {
		const locales = ["en", "zh-CN", "ja", "ko", "es", "fr", "de", "pt-BR"];
		for (const locale of locales) {
			const data = loadLocale(locale);
			expect(data["workflow.error.rolesLoadFailed"], `${locale} missing workflow.error.rolesLoadFailed`).toBeTruthy();
		}
	});
});
