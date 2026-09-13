import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, test, vi } from "vitest";
import type { OrchestratorChildView } from "../hooks/useOrchestratorChildren";
import type { WorkspaceSession } from "../types/workspace";

const { captureRendererEvent, navigate, childrenQuery } = vi.hoisted(() => ({
	captureRendererEvent: vi.fn(),
	navigate: vi.fn(),
	childrenQuery: {
		data: undefined as OrchestratorChildView[] | undefined,
		isLoading: false,
		isError: false,
	},
}));

vi.mock("../lib/telemetry", () => ({ captureRendererEvent }));
vi.mock("@tanstack/react-router", () => ({ useNavigate: () => navigate }));
vi.mock("../hooks/useOrchestratorChildren", async (importOriginal) => ({
	...(await importOriginal<object>()),
	useOrchestratorChildren: () => childrenQuery,
}));

import { OrchestratorChildrenSection } from "./OrchestratorChildrenSection";

const session = {
	id: "orch-1",
	workspaceId: "project-1",
	kind: "orchestrator",
	cloud: { orgId: "org-1" },
} as unknown as WorkspaceSession;

const childView = (overrides: Partial<OrchestratorChildView>): OrchestratorChildView => ({
	id: "child-1",
	title: "Fix CI",
	provider: "claude-code",
	status: "working",
	activity: { state: "active", lastActivityAt: "2026-08-30T12:00:00Z" },
	isTerminated: false,
	updatedAt: "2026-08-30T12:00:00Z",
	prs: [],
	...overrides,
});

describe("OrchestratorChildrenSection", () => {
	beforeEach(() => {
		captureRendererEvent.mockClear();
		navigate.mockClear();
		childrenQuery.data = undefined;
		childrenQuery.isLoading = false;
		childrenQuery.isError = false;
	});

	test("renders empty, loading, and error states", () => {
		childrenQuery.isLoading = true;
		const { rerender, unmount } = render(<OrchestratorChildrenSection session={session} />);
		expect(screen.getByText("Loading workers…")).toBeInTheDocument();

		childrenQuery.isLoading = false;
		childrenQuery.data = [];
		rerender(<OrchestratorChildrenSection session={session} />);
		expect(screen.getByText("No workers spawned yet.")).toBeInTheDocument();

		childrenQuery.data = undefined;
		childrenQuery.isError = true;
		rerender(<OrchestratorChildrenSection session={session} />);
		expect(screen.getByRole("status")).toHaveTextContent("Workers are unavailable right now.");
		unmount();
	});

	test("renders worker rows with status, PR chips, and terminated de-emphasis", () => {
		childrenQuery.data = [
			childView({
				prs: [
					{
						url: "https://github.com/o/r/pull/42",
						number: 42,
						state: "open",
						ci: "failing",
						review: "none",
						mergeability: "unstable",
						reviewComments: false,
						updatedAt: "2026-08-30T12:00:00Z",
					},
				],
			}),
			childView({ id: "child-2", title: "Old worker", status: "terminated", isTerminated: true }),
		];
		render(<OrchestratorChildrenSection session={session} />);

		expect(screen.getByText("Workers (2)")).toBeInTheDocument();
		expect(screen.getByText("Fix CI")).toBeInTheDocument();
		const chip = screen.getByRole("link", { name: "PR #42" });
		expect(chip).toHaveAttribute("href", "https://github.com/o/r/pull/42");
		expect(chip).toHaveTextContent("CI failing");

		const rows = screen.getAllByTestId("orchestrator-child-row");
		expect(rows[1].className).toContain("opacity-60");
	});

	test("row click navigates to the worker and reports telemetry", async () => {
		childrenQuery.data = [childView({})];
		render(<OrchestratorChildrenSection session={session} />);
		await userEvent.click(screen.getByText("Fix CI"));
		expect(navigate).toHaveBeenCalledWith({
			to: "/projects/$projectId/sessions/$sessionId",
			params: { projectId: "project-1", sessionId: "child-1" },
		});
		expect(captureRendererEvent).toHaveBeenCalledWith("ao.renderer.cloud_worker_opened", { has_pr: false });
	});

	test("reports the workers-viewed event once with the count", () => {
		childrenQuery.data = [childView({}), childView({ id: "child-2" })];
		const { rerender } = render(<OrchestratorChildrenSection session={session} />);
		rerender(<OrchestratorChildrenSection session={session} />);
		const viewed = captureRendererEvent.mock.calls.filter((c) => c[0] === "ao.renderer.cloud_workers_viewed");
		expect(viewed).toEqual([["ao.renderer.cloud_workers_viewed", { worker_count: 2 }]]);
	});
});
