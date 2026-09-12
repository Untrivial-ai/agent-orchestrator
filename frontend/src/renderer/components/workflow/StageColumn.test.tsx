import { render, screen, waitFor } from "@testing-library/react";
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

import { StageColumn } from "./StageColumn";

function wrapper({ children }: { children: ReactNode }) {
	const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, retryDelay: 0 } } });
	return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
}

type StageStatus = "pending" | "in_progress" | "ready_for_approval" | "passed" | "blocked" | "cancelled";

function makeStage(overrides: Partial<{
	id: string;
	planId: string;
	title: string;
	description: string;
	acceptanceCriteria: string;
	status: StageStatus;
	sequence: number;
	createdAt: string;
}> = {}) {
	return {
		id: "s1",
		planId: "plan-1",
		title: "Backend Setup",
		description: "Set up the backend",
		acceptanceCriteria: "Tests pass",
		status: "pending" as StageStatus,
		sequence: 1,
		createdAt: "2026-01-01T00:00:00Z",
		...overrides,
	};
}

beforeEach(() => {
	postMock.mockReset();
	getMock.mockReset();
	getMock.mockResolvedValue({ data: { tasks: [] }, error: undefined });
});

describe("StageColumn", () => {
	it("renders stage title and status badge", () => {
		render(<StageColumn stage={makeStage()} planId="plan-1" planStatus="in_progress" />, { wrapper });
		expect(screen.getByText("Backend Setup")).toBeInTheDocument();
		expect(screen.getByText("Pending")).toBeInTheDocument();
	});

	it("renders stage description", () => {
		render(<StageColumn stage={makeStage()} planId="plan-1" planStatus="in_progress" />, { wrapper });
		expect(screen.getByText("Set up the backend")).toBeInTheDocument();
	});

	it("shows Start button for pending stage when plan is in_progress", () => {
		render(<StageColumn stage={makeStage({ status: "pending" })} planId="plan-1" planStatus="in_progress" />, { wrapper });
		expect(screen.getByText("Start")).toBeInTheDocument();
	});

	it("shows Ready for Approval and Block buttons for in_progress stage", () => {
		render(<StageColumn stage={makeStage({ status: "in_progress" })} planId="plan-1" planStatus="in_progress" />, { wrapper });
		expect(screen.getByText("Ready for Approval")).toBeInTheDocument();
		expect(screen.getByText("Block")).toBeInTheDocument();
	});

	it("shows Pass button for ready_for_approval stage", () => {
		render(<StageColumn stage={makeStage({ status: "ready_for_approval" })} planId="plan-1" planStatus="in_progress" />, { wrapper });
		expect(screen.getByText("Pass")).toBeInTheDocument();
	});

	it("shows Unblock button for blocked stage", () => {
		render(<StageColumn stage={makeStage({ status: "blocked" })} planId="plan-1" planStatus="in_progress" />, { wrapper });
		expect(screen.getByText("Unblock")).toBeInTheDocument();
	});

	it("shows Cancel button for pending, in_progress, and blocked stages", () => {
		const { unmount } = render(<StageColumn stage={makeStage({ status: "pending" })} planId="plan-1" planStatus="in_progress" />, { wrapper });
		expect(screen.getByText("Cancel")).toBeInTheDocument();
		unmount();

		render(<StageColumn stage={makeStage({ status: "in_progress" })} planId="plan-1" planStatus="in_progress" />, { wrapper });
		expect(screen.getByText("Cancel")).toBeInTheDocument();
	});

	it("hides all action buttons when plan is NOT in_progress", () => {
		render(<StageColumn stage={makeStage({ status: "pending" })} planId="plan-1" planStatus="draft" />, { wrapper });
		expect(screen.queryByText("Start")).not.toBeInTheDocument();
		expect(screen.queryByText("Cancel")).not.toBeInTheDocument();
	});

	it("hides all action buttons for passed stage", () => {
		render(<StageColumn stage={makeStage({ status: "passed" })} planId="plan-1" planStatus="in_progress" />, { wrapper });
		expect(screen.queryByText("Pass")).not.toBeInTheDocument();
		expect(screen.queryByText("Cancel")).not.toBeInTheDocument();
	});

	it("shows empty task message when no tasks", async () => {
		render(<StageColumn stage={makeStage()} planId="plan-1" planStatus="in_progress" />, { wrapper });
		await waitFor(() => {
			expect(screen.getByText("No tasks")).toBeInTheDocument();
		});
	});
});
