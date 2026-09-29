import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { toKanbanColumn } from "@aoagents/product-ui";
import {
	STANDALONE_PROJECT_KIND,
	STANDALONE_WORKSPACE_ID,
	type WorkspaceSession,
	type WorkspaceSummary,
} from "../types/workspace";

const {
	navigateMock,
	postMock,
	workspaceQueryMock,
	usageQueryMock,
	notificationShowMock,
} = vi.hoisted(() => ({
	navigateMock: vi.fn(),
	postMock: vi.fn(),
	workspaceQueryMock: vi.fn(),
	usageQueryMock: vi.fn(),
	notificationShowMock: vi.fn(),
}));

vi.mock("@tanstack/react-router", () => ({
	useNavigate: () => navigateMock,
}));

vi.mock("../hooks/useWorkspaceQuery", () => ({
	workspaceQueryKey: ["workspaces"],
	cloudSessionsQueryKey: ["cloud-sessions"],
	useWorkspaceQuery: workspaceQueryMock,
}));

vi.mock("../hooks/useSessionUsageSummaries", () => ({
	useSessionUsageSummaries: usageQueryMock,
}));

vi.mock("../lib/api-client", () => ({
	apiClient: {
		GET: vi.fn().mockResolvedValue({ data: { prs: [] } }),
		POST: (...args: unknown[]) => postMock(...args),
	},
	apiErrorMessage: (_error: unknown, fallback: string) => fallback,
}));

vi.mock("../lib/platform", async (importOriginal) => {
	const actual = await importOriginal<typeof import("../lib/platform")>();
	return {
		...actual,
		usesBoardActionsInPanel: () => true,
	};
});

vi.mock("../lib/bridge", () => ({
	aoBridge: {
		clipboard: { writeText: vi.fn() },
		notifications: {
			show: (...args: unknown[]) => notificationShowMock(...args),
		},
	},
}));

import { StandaloneArchiveView } from "./StandaloneArchiveView";
import { TooltipProvider } from "./ui/tooltip";

function renderArchive() {
	const queryClient = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	render(
		<QueryClientProvider client={queryClient}>
			<TooltipProvider>
				<StandaloneArchiveView />
			</TooltipProvider>
		</QueryClientProvider>,
	);
	return queryClient;
}

beforeEach(() => {
	navigateMock.mockReset();
	postMock.mockReset().mockResolvedValue({ data: {} });
	workspaceQueryMock
		.mockReset()
		.mockReturnValue({ data: [], isError: false, isSuccess: true });
	usageQueryMock.mockReset().mockReturnValue({ data: new Map() });
	notificationShowMock.mockReset().mockResolvedValue(undefined);
});

describe("StandaloneArchiveView", () => {
	it("shows archived standalone sessions as a list without kanban lanes", () => {
		workspaceQueryMock.mockReturnValue({
			data: [
				workspaceWithSessions([
					terminatedSession({ id: "project-dead", title: "project archived" }),
				]),
				standaloneWorkspaceWithSessions([
					standaloneSession({
						id: "standalone-live",
						title: "active ad hoc",
						status: "idle",
					}),
					terminatedSession({
						id: "standalone-dead",
						title: "archived ad hoc",
						workspaceId: STANDALONE_WORKSPACE_ID,
						workspaceName: "Scratchpad",
						branch: undefined,
					}),
				]),
			],
			isError: false,
			isSuccess: true,
		});

		renderArchive();

		expect(screen.getByTestId("standalone-archive-view")).toBeInTheDocument();
		expect(screen.getByTestId("standalone-archive-title")).toHaveTextContent("1 Archived Session");
		expect(screen.getByRole("button", { name: "New agent" }).closest(".workspace-topbar-actions")).not.toBeNull();
		expect(screen.queryByText("1 archived")).not.toBeInTheDocument();
		expect(screen.queryByText("Terminated")).not.toBeInTheDocument();
		const list = screen.getByRole("list", { name: "Archived sessions" });
		expect(list).toHaveClass("standalone-archive-grid");
		expect(within(list).getByText("archived ad hoc")).toBeInTheDocument();
		expect(screen.queryByText("active ad hoc")).not.toBeInTheDocument();
		expect(screen.queryByText("project archived")).not.toBeInTheDocument();
		expect(screen.queryByText("Building")).not.toBeInTheDocument();
		expect(screen.queryByText("Validating")).not.toBeInTheDocument();
	});

	it("restores a standalone archived session to the projectless session route", async () => {
		workspaceQueryMock.mockReturnValue({
			data: [
				standaloneWorkspaceWithSessions([
					terminatedSession({
						id: "standalone-dead",
						title: "archived ad hoc",
						workspaceId: STANDALONE_WORKSPACE_ID,
						workspaceName: "Scratchpad",
						branch: undefined,
					}),
				]),
			],
			isError: false,
			isSuccess: true,
		});

		renderArchive();
		await userEvent.click(
			screen.getByRole("button", { name: "Restore archived ad hoc" }),
		);

		await waitFor(() =>
			expect(postMock).toHaveBeenCalledWith(
				"/api/v1/sessions/{sessionId}/restore",
				{
					params: { path: { sessionId: "standalone-dead" } },
				},
			),
		);
		expect(navigateMock).toHaveBeenCalledWith({
			to: "/sessions/$sessionId",
			params: { sessionId: "standalone-dead" },
		});
	});
});

function workspaceWithSessions(sessions: WorkspaceSession[]): WorkspaceSummary {
	return {
		id: "p1",
		name: "radic",
		kind: "single_repo",
		path: "/repo",
		sessions,
	};
}

function standaloneWorkspaceWithSessions(
	sessions: WorkspaceSession[],
): WorkspaceSummary {
	return {
		id: STANDALONE_WORKSPACE_ID,
		name: "Scratchpad",
		kind: STANDALONE_PROJECT_KIND,
		path: "Not attached to a project",
		sessions,
	};
}

function standaloneSession(
	overrides: Pick<WorkspaceSession, "id" | "title" | "status"> &
		Partial<WorkspaceSession>,
): WorkspaceSession {
	return {
		workspaceId: STANDALONE_WORKSPACE_ID,
		workspaceName: "Scratchpad",
		provider: "claude-code",
		branch: undefined,
		kanbanColumn: toKanbanColumn(undefined, overrides.status),
		updatedAt: "2026-01-01T00:00:00Z",
		prs: [],
		...overrides,
	};
}

function terminatedSession(
	overrides: Partial<WorkspaceSession> = {},
): WorkspaceSession {
	return {
		id: "s-dead",
		workspaceId: "p1",
		workspaceName: "radic",
		title: "dead worker",
		issueId: "github:INT-17",
		provider: "claude-code",
		kind: "worker",
		branch: "ao/dead-worker",
		status: "terminated",
		kanbanColumn: "archive",
		isTerminated: true,
		updatedAt: "2026-01-01T00:00:00Z",
		prs: [],
		...overrides,
	};
}
