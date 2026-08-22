import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { SessionFileExplorer } from "./SessionFileExplorer";
import { useUiStore } from "../stores/ui-store";

const { getMock, postMock } = vi.hoisted(() => ({ getMock: vi.fn(), postMock: vi.fn() }));

vi.mock("../lib/api-client", () => ({
	apiClient: { GET: getMock, POST: postMock },
	getApiBaseUrl: () => "",
	hasTrustedApiBaseUrl: () => false,
	subscribeApiBaseUrl: () => () => undefined,
	apiErrorMessage: (error: unknown, fallback = "Request failed") => {
		if (error instanceof Error) return error.message;
		return fallback;
	},
}));

vi.mock("./FileTree", () => ({
	FileTree: ({
		changedOnly,
		filterText,
		onSelectPath,
	}: {
		changedOnly: boolean;
		filterText: string;
		onSelectPath: (node: { path: string; type: "file" }) => void;
	}) => (
		<div>
			<span data-testid="tree-changed-only">{String(changedOnly)}</span>
			<span data-testid="tree-filter">{filterText}</span>
			<button onClick={() => onSelectPath({ path: "src/App.tsx", type: "file" })} type="button">
				select src/App.tsx
			</button>
		</div>
	),
}));

vi.mock("./FileContentPane", () => ({
	FileContentPane: ({ path }: { path: string | null }) => <div data-testid="content-pane">{path ?? "none"}</div>,
}));

function renderWithQuery(children: ReactNode) {
	const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	return render(<QueryClientProvider client={client}>{children}</QueryClientProvider>);
}

describe("SessionFileExplorer", () => {
	beforeEach(() => {
		getMock.mockReset().mockResolvedValue({ data: { sessionId: "sess-1", files: [], truncated: false } });
		postMock.mockReset();
	});

	it("passes the filter input down to the tree and shows the selected file in the content pane", async () => {
		renderWithQuery(<SessionFileExplorer sessionId="sess-explorer-1" />);

		const input = screen.getByRole("textbox", { name: "Filter files" });
		fireEvent.change(input, { target: { value: "app" } });
		expect(screen.getByTestId("tree-filter")).toHaveTextContent("app");

		expect(screen.getByTestId("content-pane")).toHaveTextContent("none");
		await userEvent.click(screen.getByRole("button", { name: "select src/App.tsx" }));
		expect(screen.getByTestId("content-pane")).toHaveTextContent("src/App.tsx");
	});

	it("toggles the changed-only setting in the ui store and reflects it in the tree", async () => {
		const sessionId = "sess-explorer-2";
		renderWithQuery(<SessionFileExplorer sessionId={sessionId} />);

		expect(screen.getByTestId("tree-changed-only")).toHaveTextContent("false");
		await userEvent.click(screen.getByRole("switch", { name: "Changed only" }));

		expect(screen.getByTestId("tree-changed-only")).toHaveTextContent("true");
		expect(useUiStore.getState().inspectorSessions[sessionId]?.filesChangedOnly).toBe(true);
	});

	it("toggles between unified and split diff layout", async () => {
		renderWithQuery(<SessionFileExplorer sessionId="sess-explorer-3" />);

		const toggle = screen.getByRole("button", { name: "Split diff view" });
		expect(toggle).toHaveAttribute("aria-pressed", "false");
		await userEvent.click(toggle);
		expect(screen.getByRole("button", { name: "Unified diff view" })).toHaveAttribute("aria-pressed", "true");
	});

	it("lets the caller toggle between rail and maximized layouts", async () => {
		const onToggleMaximized = vi.fn();
		renderWithQuery(<SessionFileExplorer onToggleMaximized={onToggleMaximized} sessionId="sess-explorer-4" />);

		await userEvent.click(screen.getByRole("button", { name: "Maximize files" }));
		expect(onToggleMaximized).toHaveBeenCalledWith(true);
	});
});
