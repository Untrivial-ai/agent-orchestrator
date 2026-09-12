import { render, screen, fireEvent } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

const navigateMock = vi.fn();

vi.mock("@tanstack/react-router", () => ({
	useNavigate: () => navigateMock,
}));

import { ProjectWorkspaceNav } from "./ProjectWorkspaceNav";

describe("ProjectWorkspaceNav", () => {
	it("renders Sessions and Workflow tabs", () => {
		render(<ProjectWorkspaceNav active="sessions" projectId="proj-1" />);
		expect(screen.getByTestId("workspace-tab-sessions")).toBeInTheDocument();
		expect(screen.getByTestId("workspace-tab-workflow")).toBeInTheDocument();
	});

	it("highlights the active sessions tab", () => {
		render(<ProjectWorkspaceNav active="sessions" projectId="proj-1" />);
		expect(screen.getByTestId("workspace-tab-sessions").className).toContain("text-foreground");
		expect(screen.getByTestId("workspace-tab-workflow").className).toContain("text-muted-foreground");
	});

	it("highlights the active workflow tab", () => {
		render(<ProjectWorkspaceNav active="workflow" projectId="proj-1" />);
		expect(screen.getByTestId("workspace-tab-workflow").className).toContain("text-foreground");
		expect(screen.getByTestId("workspace-tab-sessions").className).toContain("text-muted-foreground");
	});

	it("navigates to workflow route when Workflow tab is clicked", () => {
		render(<ProjectWorkspaceNav active="sessions" projectId="proj-1" />);
		fireEvent.click(screen.getByTestId("workspace-tab-workflow"));
		expect(navigateMock).toHaveBeenCalledWith({
			to: "/projects/$projectId/workflow",
			params: { projectId: "proj-1" },
		});
	});

	it("navigates to sessions route when Sessions tab is clicked from workflow view", () => {
		render(<ProjectWorkspaceNav active="workflow" projectId="proj-1" />);
		fireEvent.click(screen.getByTestId("workspace-tab-sessions"));
		expect(navigateMock).toHaveBeenCalledWith({
			to: "/projects/$projectId",
			params: { projectId: "proj-1" },
		});
	});

	it("does not navigate when clicking the already-active tab", () => {
		navigateMock.mockClear();
		render(<ProjectWorkspaceNav active="sessions" projectId="proj-1" />);
		fireEvent.click(screen.getByTestId("workspace-tab-sessions"));
		expect(navigateMock).not.toHaveBeenCalled();
	});
});
