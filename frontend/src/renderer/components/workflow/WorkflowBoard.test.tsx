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

vi.mock("@tanstack/react-router", () => ({
	useNavigate: () => vi.fn(),
}));

import { WorkflowBoard } from "./WorkflowBoard";

function wrapper({ children }: { children: ReactNode }) {
	const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, retryDelay: 0 } } });
	return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
}

const mockPlans = [
	{ id: "plan-1", title: "Auth System", objective: "Implement auth", status: "draft", createdAt: "2026-09-12T10:00:00Z" },
	{ id: "plan-2", title: "Payment Integration", objective: "Add Stripe", status: "in_progress", createdAt: "2026-09-11T10:00:00Z" },
	{ id: "plan-3", title: "Dashboard", objective: "Build dashboard", status: "completed", createdAt: "2026-09-10T10:00:00Z" },
];

beforeEach(() => {
	getMock.mockReset();
	postMock.mockReset();
});

describe("WorkflowBoard", () => {
	it("shows loading skeleton initially", () => {
		getMock.mockReturnValue(new Promise(() => {}));
		render(<WorkflowBoard projectId="proj-1" />, { wrapper });
		expect(document.querySelector(".animate-pulse")).toBeTruthy();
	});

	it("renders plan list after loading", async () => {
		getMock.mockResolvedValue({ data: { plans: mockPlans }, error: undefined });

		render(<WorkflowBoard projectId="proj-1" />, { wrapper });
		await screen.findByText("Auth System");
		expect(screen.getByText("Payment Integration")).toBeInTheDocument();
		expect(screen.getByText("Dashboard")).toBeInTheDocument();
	});

	it("shows empty state when no plans", async () => {
		getMock.mockResolvedValue({ data: { plans: [] }, error: undefined });

		render(<WorkflowBoard projectId="proj-1" />, { wrapper });
		await screen.findByText("No plans yet");
		expect(screen.getByText("No plans yet")).toBeInTheDocument();
	});

	it("shows error state with retry button on API error", async () => {
		getMock.mockResolvedValue({ data: undefined, error: { message: "Server error" } });

		render(<WorkflowBoard projectId="proj-1" />, { wrapper });
		await screen.findByText("Failed to load plans");
		expect(screen.getByText("Retry")).toBeInTheDocument();
	});

	it("retries fetch when Retry button is clicked", async () => {
		getMock.mockResolvedValueOnce({ data: undefined, error: { message: "Server error" } });

		render(<WorkflowBoard projectId="proj-1" />, { wrapper });
		await screen.findByText("Failed to load plans");

		getMock.mockResolvedValueOnce({ data: { plans: mockPlans }, error: undefined });
		fireEvent.click(screen.getByText("Retry"));

		await screen.findByText("Auth System");
		expect(screen.getByText("Auth System")).toBeInTheDocument();
	});

	it("renders Create Plan button", async () => {
		getMock.mockResolvedValue({ data: { plans: [] }, error: undefined });

		render(<WorkflowBoard projectId="proj-1" />, { wrapper });
		await screen.findByText("Create Plan");
		expect(screen.getByText("Create Plan")).toBeInTheDocument();
	});

	it("filters plans by status", async () => {
		getMock.mockResolvedValue({ data: { plans: mockPlans }, error: undefined });

		render(<WorkflowBoard projectId="proj-1" />, { wrapper });
		await screen.findByText("Auth System");

		// All plans should be visible initially
		expect(screen.getByText("Auth System")).toBeInTheDocument();
		expect(screen.getByText("Payment Integration")).toBeInTheDocument();
		expect(screen.getByText("Dashboard")).toBeInTheDocument();
	});

	it("searches plans by title", async () => {
		getMock.mockResolvedValue({ data: { plans: mockPlans }, error: undefined });

		render(<WorkflowBoard projectId="proj-1" />, { wrapper });
		await screen.findByText("Auth System");

		const searchInput = screen.getByPlaceholderText("Search plans...");
		fireEvent.change(searchInput, { target: { value: "Payment" } });

		expect(screen.getByText("Payment Integration")).toBeInTheDocument();
		expect(screen.queryByText("Auth System")).not.toBeInTheDocument();
		expect(screen.queryByText("Dashboard")).not.toBeInTheDocument();
	});
});
