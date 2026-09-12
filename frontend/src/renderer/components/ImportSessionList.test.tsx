import { act, fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ImportSessionList } from "./ImportSessionList";
import { useImportRunStore } from "../stores/import-run-store";
const h = vi.hoisted(() => ({
	query: vi.fn(),
	post: vi.fn(),
	refetch: vi.fn(),
}));
vi.mock("react-i18next", () => ({
	useTranslation: () => ({ t: (key: string) => key }),
}));
vi.mock("../hooks/useImportableSessions", () => ({
	importableSessionsQueryKey: ["importable-sessions"],
	useImportableSessions: h.query,
}));
vi.mock("../lib/api-client", () => ({
	apiClient: { POST: h.post },
	apiErrorCode: (e: { code?: string }) => e?.code,
	apiErrorMessage: (e: Error) => e.message,
}));
vi.mock("../lib/query-client", () => ({
	queryClient: { invalidateQueries: vi.fn(), setQueryData: vi.fn() },
}));
const session = {
	provider: "claude-code",
	nativeSessionId: "one",
	title: "Existing work",
	cwd: "/repo",
	lastActivity: new Date().toISOString(),
	messageCount: 0,
	tokenCount: 16000,
	sizeBytes: 10,
	alreadyImported: false,
};
beforeEach(() => {
	vi.clearAllMocks();
	useImportRunStore.setState({ runs: {} });
	h.query.mockReturnValue({
		data: [session],
		isFetching: false,
		isLoading: false,
		isError: false,
		refetch: h.refetch,
	});
});
describe("project import dialog list", () => {
	it("shows immediate loading with an explicit project scope", () => {
		h.query.mockReturnValue({ isLoading: true });
		render(<ImportSessionList projectId="a" />);
		expect(h.query).toHaveBeenCalledWith("a", true);
		expect(screen.getByRole("status")).toHaveTextContent(
			"importSession.loading",
		);
	});
	it("offers actionable discovery errors", () => {
		h.query.mockReturnValue({
			isError: true,
			error: new Error("Cannot read provider history"),
			refetch: h.refetch,
		});
		render(<ImportSessionList projectId="a" />);
		expect(screen.getByRole("alert")).toHaveTextContent(
			"Cannot read provider history",
		);
		fireEvent.click(screen.getByRole("button", { name: "files.retry" }));
		expect(h.refetch).toHaveBeenCalledOnce();
	});
	it("shows pending feedback immediately and retains the project queue across unmounts", async () => {
		h.post.mockReturnValue(new Promise(() => {}));
		const view = render(<ImportSessionList projectId="a" />);
		fireEvent.click(screen.getByTestId("import-all"));
		expect(
			screen.getByRole("button", { name: "importSession.stop" }),
		).toBeEnabled();
		expect(
			screen.getByRole("button", { name: "importSession.import" }),
		).toBeDisabled();
		expect(h.post).toHaveBeenCalledTimes(1);
		view.unmount();
		render(<ImportSessionList projectId="a" />);
		expect(
			screen.getByRole("button", { name: "importSession.stop" }),
		).toBeEnabled();
		await act(async () => {
			fireEvent.click(
				screen.getByRole("button", { name: "importSession.stop" }),
			);
		});
		expect(screen.getByRole("status")).toHaveTextContent(
			"importSession.stopped",
		);
		expect(screen.getByRole("alert")).toHaveTextContent(
			"current session may have completed",
		);
	});
});
