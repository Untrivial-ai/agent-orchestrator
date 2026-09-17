import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { useUiStore } from "../stores/ui-store";
import { TooltipProvider } from "./ui/tooltip";

const { paramsMock, useWorkspaceQueryMock } = vi.hoisted(() => ({
	paramsMock: { projectId: undefined as string | undefined, sessionId: undefined as string | undefined },
	useWorkspaceQueryMock: vi.fn(),
}));

vi.mock("@tanstack/react-router", async (importOriginal) => {
	const actual = await importOriginal<typeof import("@tanstack/react-router")>();
	return {
		...actual,
		useNavigate: () => vi.fn(),
		useParams: () => paramsMock,
	};
});

vi.mock("../hooks/useWorkspaceQuery", () => ({
	useWorkspaceScope: () => ({ ...useWorkspaceQueryMock(), data: {} }),
	workspaceQueryKey: ["workspaces"],
}));

vi.mock("../lib/platform", async (importOriginal) => {
	const actual = await importOriginal<typeof import("../lib/platform")>();
	return {
		...actual,
		isLinuxPlatform: () => true,
		isMacPlatform: () => false,
		usesBoardActionsInPanel: () => false,
		hidesShellTopbar: () => false,
	};
});

vi.mock("./NotificationCenter", () => ({
	NotificationCenter: () => <button aria-label="Notifications" type="button" />,
}));

const { ShellTopbar } = await import("./ShellTopbar");

describe("ShellTopbar on Linux", () => {
	beforeEach(() => {
		paramsMock.projectId = undefined;
		paramsMock.sessionId = undefined;
		useWorkspaceQueryMock.mockReturnValue({ data: [], isError: false, isLoading: false });
		useUiStore.setState({ isSidebarOpen: true });
	});

	it("pads the Board label past the collapse arrows when the sidebar is off-canvas", () => {
		useUiStore.setState({ isSidebarOpen: false });
		render(
			<QueryClientProvider client={new QueryClient()}>
				<TooltipProvider>
					<ShellTopbar />
				</TooltipProvider>
			</QueryClientProvider>,
		);

		const header = screen.getByTestId("board-topbar-label").closest("header");
		// Cluster left (26) + cluster width (92) + content gap (12) - panel inline inset (16).
		expect(header).toHaveStyle({ paddingLeft: "114px" });
		expect(screen.getByTestId("board-topbar-label")).toHaveTextContent("Board");
	});

	it("keeps the default title padding while the sidebar is open", () => {
		render(
			<QueryClientProvider client={new QueryClient()}>
				<TooltipProvider>
					<ShellTopbar />
				</TooltipProvider>
			</QueryClientProvider>,
		);

		const header = screen.getByTestId("board-topbar-label").closest("header");
		expect(header).toHaveStyle({ paddingLeft: "18px" });
	});
});
