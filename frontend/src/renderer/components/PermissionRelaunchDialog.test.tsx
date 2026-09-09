import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { PermissionRelaunchDialog } from "./PermissionRelaunchDialog";

const { getMock, postMock } = vi.hoisted(() => ({ getMock: vi.fn(), postMock: vi.fn() }));

vi.mock("../lib/api-client", () => ({
	apiClient: { GET: getMock, POST: postMock },
	apiErrorMessage: (error: { message?: string }) => error.message ?? "Request failed",
}));

describe("PermissionRelaunchDialog", () => {
	it("lists affected workers and relaunches them only after confirmation", async () => {
		getMock.mockResolvedValue({
			data: {
				count: 1,
				affected: [{ sessionId: "worker-1", title: "Fix login", kind: "worker", fromMode: "auto", toMode: "bypass-permissions" }],
			},
			error: undefined,
		});
		postMock.mockResolvedValue({ data: { results: [{ sessionId: "worker-1", ok: true }], relaunched: 1, failed: 0 }, error: undefined });
		const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
		const onOpenChange = vi.fn();
		render(
			<QueryClientProvider client={queryClient}>
				<PermissionRelaunchDialog open projectId="project-1" onOpenChange={onOpenChange} />
			</QueryClientProvider>,
		);

		expect(await screen.findByText("Fix login")).toBeInTheDocument();
		expect(postMock).not.toHaveBeenCalled();
		fireEvent.click(screen.getByRole("button", { name: "Relaunch 1 session" }));
		await waitFor(() => expect(postMock).toHaveBeenCalledWith("/api/v1/projects/{id}/permission-relaunch", {
			params: { path: { id: "project-1" } },
		}));
		expect(onOpenChange).toHaveBeenCalledWith(false);
	});
});
