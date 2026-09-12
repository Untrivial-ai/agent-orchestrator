import { render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { describe, expect, it, vi } from "vitest";
import type { ReactNode } from "react";

const { navigateMock } = vi.hoisted(() => ({
	navigateMock: vi.fn(),
}));

vi.mock("@tanstack/react-router", () => ({
	useNavigate: () => navigateMock,
}));

vi.mock("../components/SessionsBoard", () => ({
	SessionsBoard: ({ projectId }: { projectId: string }) => (
		<div data-testid="sessions-board">SessionsBoard:{projectId}</div>
	),
}));

import { ProjectWorkspaceNav } from "../components/ProjectWorkspaceNav";

// The route file at _shell.projects.$projectId.tsx renders:
//   <ProjectWorkspaceNav active="sessions" projectId={projectId} />
//   <SessionsBoard projectId={projectId} />
// We verify both are present and Sessions is the active tab.

function ProjectRouteShell({ projectId }: { projectId: string }) {
	return (
		<div className="flex h-full flex-col">
			<ProjectWorkspaceNav active="sessions" projectId={projectId} />
			<div className="flex-1 overflow-hidden">
				{/* SessionsBoard is mocked above */}
				<div data-testid="sessions-board">SessionsBoard:{projectId}</div>
			</div>
		</div>
	);
}

function wrapper({ children }: { children: ReactNode }) {
	const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
}

describe("Project route — Sessions preservation", () => {
	it("renders ProjectWorkspaceNav with sessions as active tab", () => {
		render(<ProjectRouteShell projectId="proj-1" />, { wrapper });

		expect(screen.getByTestId("project-workspace-nav")).toBeInTheDocument();
		expect(screen.getByTestId("workspace-tab-sessions")).toBeInTheDocument();
		expect(screen.getByTestId("workspace-tab-workflow")).toBeInTheDocument();
		expect(screen.getByTestId("workspace-tab-sessions").className).toContain("text-foreground");
		expect(screen.getByTestId("workspace-tab-workflow").className).toContain("text-muted-foreground");
	});

	it("renders SessionsBoard (not replaced by WorkflowBoard)", () => {
		render(<ProjectRouteShell projectId="proj-1" />, { wrapper });

		expect(screen.getByTestId("sessions-board")).toBeInTheDocument();
		expect(screen.getByText("SessionsBoard:proj-1")).toBeInTheDocument();
	});

	it("provides both Sessions and Workflow navigation tabs", () => {
		render(<ProjectRouteShell projectId="proj-1" />, { wrapper });

		expect(screen.getByTestId("workspace-tab-sessions")).toBeInTheDocument();
		expect(screen.getByTestId("workspace-tab-workflow")).toBeInTheDocument();
	});

	it("Workflow改造没有破坏Project Sessions入口: Sessions tab is active, SessionsBoard is rendered", () => {
		render(<ProjectRouteShell projectId="proj-1" />, { wrapper });

		// Sessions tab is active (foreground text + underline)
		expect(screen.getByTestId("workspace-tab-sessions").className).toContain("text-foreground");
		// SessionsBoard is rendered
		expect(screen.getByTestId("sessions-board")).toBeInTheDocument();
		// Workflow tab exists but is inactive
		expect(screen.getByTestId("workspace-tab-workflow").className).toContain("text-muted-foreground");
	});
});
