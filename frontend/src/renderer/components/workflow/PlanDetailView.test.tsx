import { render, screen, fireEvent } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ReactNode } from "react";

const { getMock, postMock, hasTrustedApiBaseUrlMock } = vi.hoisted(() => ({
	getMock: vi.fn(),
	postMock: vi.fn(),
	hasTrustedApiBaseUrlMock: vi.fn(() => true),
}));

vi.mock("../../lib/api-client", () => ({
	apiClient: { GET: getMock, POST: postMock },
	hasTrustedApiBaseUrl: hasTrustedApiBaseUrlMock,
	apiErrorMessage: (e: unknown) => (e instanceof Error ? e.message : "error"),
}));

const navigateMock = vi.fn();
vi.mock("@tanstack/react-router", () => ({
	useNavigate: () => navigateMock,
}));

import { PlanDetailView } from "./PlanDetailView";

function wrapper({ children }: { children: ReactNode }) {
	const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, retryDelay: 0 } } });
	return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
}

function mockPlan(status: string) {
	return {
		id: "plan-1",
		projectId: "proj-1",
		title: "Build Auth System",
		objective: "Implement user authentication",
		requirements: "Must support OAuth2",
		implementationSummary: "Using passport.js",
		status,
	};
}

beforeEach(() => {
	getMock.mockReset();
	postMock.mockReset();
	navigateMock.mockClear();
});

describe("PlanDetailView", () => {
	it("shows loading skeleton initially", () => {
		getMock.mockReturnValue(new Promise(() => {}));
		const { container } = render(<PlanDetailView projectId="proj-1" planId="plan-1" />, { wrapper });
		expect(container.querySelector(".animate-pulse")).toBeTruthy();
		expect(screen.queryByText("Build Auth System")).not.toBeInTheDocument();
	});

	it("renders plan title and objective after loading", async () => {
		getMock.mockImplementation(async (url: string) => {
			if (url === "/api/v1/workflow/plans/{id}") return { data: { plan: mockPlan("draft") }, error: undefined };
			if (url === "/api/v1/workflow/plans/{id}/stages") return { data: { stages: [] }, error: undefined };
			return { data: undefined, error: undefined };
		});

		render(<PlanDetailView projectId="proj-1" planId="plan-1" />, { wrapper });
		await screen.findByText("Build Auth System");
		expect(screen.getByText("Implement user authentication")).toBeInTheDocument();
	});

	it("renders requirements and implementation summary", async () => {
		getMock.mockImplementation(async (url: string) => {
			if (url === "/api/v1/workflow/plans/{id}") return { data: { plan: mockPlan("draft") }, error: undefined };
			if (url === "/api/v1/workflow/plans/{id}/stages") return { data: { stages: [] }, error: undefined };
			return { data: undefined, error: undefined };
		});

		render(<PlanDetailView projectId="proj-1" planId="plan-1" />, { wrapper });
		await screen.findByText("Build Auth System");
		expect(screen.getByText("Must support OAuth2")).toBeInTheDocument();
		expect(screen.getByText("Using passport.js")).toBeInTheDocument();
	});

	it("shows Confirm and Cancel buttons for draft plan", async () => {
		getMock.mockImplementation(async (url: string) => {
			if (url === "/api/v1/workflow/plans/{id}") return { data: { plan: mockPlan("draft") }, error: undefined };
			if (url === "/api/v1/workflow/plans/{id}/stages") return { data: { stages: [] }, error: undefined };
			return { data: undefined, error: undefined };
		});

		render(<PlanDetailView projectId="proj-1" planId="plan-1" />, { wrapper });
		await screen.findByText("Build Auth System");
		expect(screen.getByTestId("plan-confirm")).toBeInTheDocument();
		expect(screen.getByTestId("plan-cancel")).toBeInTheDocument();
	});

	it("shows Start and Cancel buttons for confirmed plan", async () => {
		getMock.mockImplementation(async (url: string) => {
			if (url === "/api/v1/workflow/plans/{id}") return { data: { plan: mockPlan("confirmed") }, error: undefined };
			if (url === "/api/v1/workflow/plans/{id}/stages") return { data: { stages: [] }, error: undefined };
			return { data: undefined, error: undefined };
		});

		render(<PlanDetailView projectId="proj-1" planId="plan-1" />, { wrapper });
		await screen.findByText("Build Auth System");
		expect(screen.getByTestId("plan-start")).toBeInTheDocument();
		expect(screen.getByTestId("plan-cancel")).toBeInTheDocument();
	});

	it("shows Complete and Cancel buttons for in_progress plan", async () => {
		getMock.mockImplementation(async (url: string) => {
			if (url === "/api/v1/workflow/plans/{id}") return { data: { plan: mockPlan("in_progress") }, error: undefined };
			if (url === "/api/v1/workflow/plans/{id}/stages") return { data: { stages: [] }, error: undefined };
			return { data: undefined, error: undefined };
		});

		render(<PlanDetailView projectId="proj-1" planId="plan-1" />, { wrapper });
		await screen.findByText("Build Auth System");
		expect(screen.getByTestId("plan-complete")).toBeInTheDocument();
		expect(screen.getByTestId("plan-cancel")).toBeInTheDocument();
	});

	it("shows no action buttons for completed plan", async () => {
		getMock.mockImplementation(async (url: string) => {
			if (url === "/api/v1/workflow/plans/{id}") return { data: { plan: mockPlan("completed") }, error: undefined };
			if (url === "/api/v1/workflow/plans/{id}/stages") return { data: { stages: [] }, error: undefined };
			return { data: undefined, error: undefined };
		});

		render(<PlanDetailView projectId="proj-1" planId="plan-1" />, { wrapper });
		await screen.findByText("Build Auth System");
		expect(screen.queryByTestId("plan-confirm")).not.toBeInTheDocument();
		expect(screen.queryByTestId("plan-start")).not.toBeInTheDocument();
		expect(screen.queryByTestId("plan-complete")).not.toBeInTheDocument();
		expect(screen.queryByTestId("plan-cancel")).not.toBeInTheDocument();
	});

	it("shows no action buttons for cancelled plan", async () => {
		getMock.mockImplementation(async (url: string) => {
			if (url === "/api/v1/workflow/plans/{id}") return { data: { plan: mockPlan("cancelled") }, error: undefined };
			if (url === "/api/v1/workflow/plans/{id}/stages") return { data: { stages: [] }, error: undefined };
			return { data: undefined, error: undefined };
		});

		render(<PlanDetailView projectId="proj-1" planId="plan-1" />, { wrapper });
		await screen.findByText("Build Auth System");
		expect(screen.queryByTestId("plan-cancel")).not.toBeInTheDocument();
	});

	it("shows error state with back button when plan not found", async () => {
		getMock.mockResolvedValue({ data: undefined, error: { message: "not found" } });

		render(<PlanDetailView projectId="proj-1" planId="bad-id" />, { wrapper });
		await screen.findByText("Plan not found");
		expect(screen.getByText("Back to Plans")).toBeInTheDocument();
	});

	it("navigates back when Back to Plans is clicked", async () => {
		getMock.mockImplementation(async (url: string) => {
			if (url === "/api/v1/workflow/plans/{id}") return { data: { plan: mockPlan("draft") }, error: undefined };
			if (url === "/api/v1/workflow/plans/{id}/stages") return { data: { stages: [] }, error: undefined };
			return { data: undefined, error: undefined };
		});

		render(<PlanDetailView projectId="proj-1" planId="plan-1" />, { wrapper });
		await screen.findByText("Build Auth System");
		fireEvent.click(screen.getByText("Back to Plans"));
		expect(navigateMock).toHaveBeenCalledWith({
			to: "/projects/$projectId/workflow",
			params: { projectId: "proj-1" },
		});
	});
});
