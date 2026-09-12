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

const pendingRun = { id: "run-1", taskId: "task-1", status: "pending" as const, attempt: 1, createdAt: "2026-01-01" };
const runningRun = { id: "run-2", taskId: "task-1", status: "running" as const, attempt: 2, createdAt: "2026-01-01", sessionId: "sess-1", startedAt: "2026-01-01T01:00:00Z" };
const succeededRun = { id: "run-3", taskId: "task-1", status: "succeeded" as const, attempt: 3, createdAt: "2026-01-01" };

describe("RunEntry — action buttons for latest run", () => {
	it("shows Start and Cancel for latest pending run", () => {
		const onStart = vi.fn();
		const onCancel = vi.fn();
		render(
			<RunEntry run={pendingRun} isLatest onStart={onStart} onCancel={onCancel} />,
			{ wrapper },
		);
		expect(screen.getByTestId("start-run-run-1")).toBeInTheDocument();
		expect(screen.getByTestId("cancel-run-run-1")).toBeInTheDocument();
	});

	it("does NOT show actions when isLatest is false", () => {
		render(
			<RunEntry run={pendingRun} isLatest={false} onStart={vi.fn()} onCancel={vi.fn()} />,
			{ wrapper },
		);
		expect(screen.queryByTestId("start-run-run-1")).not.toBeInTheDocument();
		expect(screen.queryByTestId("cancel-run-run-1")).not.toBeInTheDocument();
	});

	it("shows View Session and Cancel for latest running run", () => {
		const onViewSession = vi.fn();
		render(
			<RunEntry run={runningRun} isLatest onViewSession={onViewSession} onCancel={vi.fn()} />,
			{ wrapper },
		);
		expect(screen.getByTestId("view-session-run-2")).toBeInTheDocument();
		expect(screen.getByTestId("cancel-run-run-2")).toBeInTheDocument();
	});

	it("does NOT show actions for succeeded run", () => {
		render(
			<RunEntry run={succeededRun} isLatest onStart={vi.fn()} onCancel={vi.fn()} />,
			{ wrapper },
		);
		expect(screen.queryByTestId("start-run-run-3")).not.toBeInTheDocument();
		expect(screen.queryByTestId("cancel-run-run-3")).not.toBeInTheDocument();
	});

	it("calls onStart when Start button is clicked", async () => {
		const user = userEvent.setup();
		const onStart = vi.fn();
		render(<RunEntry run={pendingRun} isLatest onStart={onStart} />, { wrapper });

		await user.click(screen.getByTestId("start-run-run-1"));
		expect(onStart).toHaveBeenCalledTimes(1);
	});

	it("calls onCancel when Cancel button is clicked", async () => {
		const user = userEvent.setup();
		const onCancel = vi.fn();
		render(<RunEntry run={pendingRun} isLatest onCancel={onCancel} />, { wrapper });

		await user.click(screen.getByTestId("cancel-run-run-1"));
		expect(onCancel).toHaveBeenCalledTimes(1);
	});

	it("calls onViewSession with sessionId when View Session is clicked", async () => {
		const user = userEvent.setup();
		const onViewSession = vi.fn();
		render(<RunEntry run={runningRun} isLatest onViewSession={onViewSession} />, { wrapper });

		await user.click(screen.getByTestId("view-session-run-2"));
		expect(onViewSession).toHaveBeenCalledWith("sess-1");
	});

	it("disables Start button when startPending is true", () => {
		render(<RunEntry run={pendingRun} isLatest onStart={vi.fn()} startPending />, { wrapper });
		expect(screen.getByTestId("start-run-run-1")).toBeDisabled();
	});

	it("disables Cancel button when cancelPending is true", () => {
		render(<RunEntry run={pendingRun} isLatest onCancel={vi.fn()} cancelPending />, { wrapper });
		expect(screen.getByTestId("cancel-run-run-1")).toBeDisabled();
	});
});
