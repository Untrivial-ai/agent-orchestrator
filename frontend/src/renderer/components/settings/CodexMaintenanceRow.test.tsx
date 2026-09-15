import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { CodexMaintenanceRow } from "./CodexMaintenanceRow";
import type { CodexInstallJob, CodexMaintenanceStatus } from "../../hooks/useCodexMaintenanceQuery";

const { useCodexMaintenanceQuery, startCodexUpdate, invalidateCodexMaintenance, apiClientGet } = vi.hoisted(() => ({
	useCodexMaintenanceQuery: vi.fn(),
	startCodexUpdate: vi.fn(),
	invalidateCodexMaintenance: vi.fn(),
	apiClientGet: vi.fn(),
}));

vi.mock("../../hooks/useCodexMaintenanceQuery", () => ({
	useCodexMaintenanceQuery,
	startCodexUpdate,
	invalidateCodexMaintenance,
}));

vi.mock("../../lib/api-client", () => ({
	apiClient: { GET: apiClientGet },
	apiErrorCode: (error: unknown) => (typeof error === "object" && error !== null ? (error as { code?: string }).code : undefined),
	apiErrorMessage: (error: unknown, fallback: string) =>
		typeof error === "object" && error !== null && typeof (error as { message?: unknown }).message === "string"
			? (error as { message: string }).message
			: fallback,
}));

const upToDateStatus: CodexMaintenanceStatus = {
	ownership: "npm",
	installedVersion: "0.153.4",
	latestVersion: "0.153.4",
	updateAvailable: false,
	updateSupported: true,
	checkedAt: "2026-09-14T00:00:00Z",
};

const updateAvailableStatus: CodexMaintenanceStatus = {
	ownership: "npm",
	installedVersion: "0.149.1",
	latestVersion: "0.153.4",
	updateAvailable: true,
	updateSupported: true,
	updateCommand: "npm install -g @openai/codex@latest",
	checkedAt: "2026-09-14T00:00:00Z",
};

function renderRow() {
	const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	return render(
		<QueryClientProvider client={queryClient}>
			<CodexMaintenanceRow />
		</QueryClientProvider>,
	);
}

beforeEach(() => {
	useCodexMaintenanceQuery.mockReset();
	startCodexUpdate.mockReset();
	invalidateCodexMaintenance.mockReset();
	apiClientGet.mockReset();
});

describe("CodexMaintenanceRow", () => {
	it("renders nothing when no update is available", () => {
		useCodexMaintenanceQuery.mockReturnValue({ data: upToDateStatus });
		const { container } = renderRow();
		expect(container).toBeEmptyDOMElement();
	});

	it("shows an Update now button and starts the update with the resolved ownership", async () => {
		useCodexMaintenanceQuery.mockReturnValue({ data: updateAvailableStatus });
		const job: CodexInstallJob = { target: "codex", status: "installing", method: "npm" };
		startCodexUpdate.mockResolvedValue(job);

		renderRow();
		expect(screen.getByText(/Codex CLI update available/)).toBeVisible();

		fireEvent.click(screen.getByRole("button", { name: /update now/i }));

		await waitFor(() => expect(startCodexUpdate).toHaveBeenCalledWith("npm"));
		await waitFor(() => expect(screen.getByRole("status")).toHaveTextContent("Updating Codex CLI"));
	});

	it("shows the ownership-changed message and refreshes the advisory on conflict", async () => {
		useCodexMaintenanceQuery.mockReturnValue({ data: updateAvailableStatus });
		startCodexUpdate.mockRejectedValue({ code: "CODEX_OWNERSHIP_CHANGED", message: "conflict" });

		renderRow();
		fireEvent.click(screen.getByRole("button", { name: /update now/i }));

		await waitFor(() => expect(screen.getByText(/Codex installation changed/)).toBeVisible());
		expect(invalidateCodexMaintenance).toHaveBeenCalled();
	});

	it("shows a generic failure message for other errors", async () => {
		useCodexMaintenanceQuery.mockReturnValue({ data: updateAvailableStatus });
		startCodexUpdate.mockRejectedValue({ code: "SOME_OTHER_ERROR", message: "network is down" });

		renderRow();
		fireEvent.click(screen.getByRole("button", { name: /update now/i }));

		await waitFor(() => expect(screen.getByText("network is down")).toBeVisible());
	});
});
