import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { describe, expect, it, vi } from "vitest";
import type { ReactNode } from "react";
import { RunEntry } from "./RunEntry";

vi.mock("@tanstack/react-router", () => ({
	useNavigate: () => vi.fn(),
}));

function wrapper({ children }: { children: ReactNode }) {
	const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
}

const runningRunWithSession = {
	id: "run-2",
	taskId: "task-1",
	status: "running" as const,
	attempt: 2,
	createdAt: "2026-01-01",
	sessionId: "sess-abc-123",
	startedAt: "2026-01-01T01:00:00Z",
};

const runningRunWithoutSession = {
	id: "run-3",
	taskId: "task-1",
	status: "running" as const,
	attempt: 3,
	createdAt: "2026-01-01",
	startedAt: "2026-01-01T01:00:00Z",
};

describe("RunEntry — session navigation evidence", () => {
	it("renders View Session button when sessionId exists on latest running run", () => {
		render(
			<RunEntry run={runningRunWithSession} isLatest onViewSession={vi.fn()} />,
			{ wrapper },
		);
		expect(screen.getByTestId("view-session-run-2")).toBeInTheDocument();
	});

	it("does NOT render View Session button when sessionId is absent", () => {
		render(
			<RunEntry run={runningRunWithoutSession} isLatest onViewSession={vi.fn()} />,
			{ wrapper },
		);
		expect(screen.queryByTestId("view-session-run-3")).not.toBeInTheDocument();
	});

	it("calls onViewSession with correct sessionId on click", async () => {
		const user = userEvent.setup();
		const onViewSession = vi.fn();
		render(
			<RunEntry run={runningRunWithSession} isLatest onViewSession={onViewSession} />,
			{ wrapper },
		);

		await user.click(screen.getByTestId("view-session-run-2"));
		expect(onViewSession).toHaveBeenCalledOnce();
		expect(onViewSession).toHaveBeenCalledWith("sess-abc-123");
	});

	it("does NOT call onViewSession when run is not latest", async () => {
		const onViewSession = vi.fn();
		render(
			<RunEntry run={runningRunWithSession} isLatest={false} onViewSession={onViewSession} />,
			{ wrapper },
		);
		// Button should not exist for non-latest
		expect(screen.queryByTestId("view-session-run-2")).not.toBeInTheDocument();
	});
});
