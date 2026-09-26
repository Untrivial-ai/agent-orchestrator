import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import userEvent from "@testing-library/user-event";
import { render, screen, within } from "@testing-library/react";
import { expect, it, vi } from "vitest";
import type { CodexAccountsResponse } from "../hooks/useCodexAccountsQuery";
import { DropdownMenu, DropdownMenuContent } from "./ui/dropdown-menu";
import { CodexSessionAccountMenuItems } from "./CodexSessionAccountMenuItems";

const { getMock, postMock } = vi.hoisted(() => ({ getMock: vi.fn(), postMock: vi.fn() }));

vi.mock("../lib/api-client", () => ({
	apiClient: { GET: getMock, POST: postMock },
	apiErrorMessage: () => "request failed",
}));

const accountsResponse = {
	activeAccountId: "account-1",
	accountRevision: 1,
	deviceReconciliation: { status: "verified", activeAccountVerified: true, reasonCode: "verified", retryable: false },
	accounts: [
		{ id: "account-1", label: "active@example.com", accountEmail: "active@example.com", active: true, status: "valid", createdAt: "2026-08-31T09:00:00Z", authentication: { state: "authorized" } },
		{ id: "account-2", label: "other@example.com", accountEmail: "other@example.com", active: false, status: "valid", createdAt: "2026-08-31T09:05:00Z", authentication: { state: "authorized" } },
	],
} as unknown as CodexAccountsResponse;

it("marks the globally active account in the session account menu", async () => {
	getMock.mockResolvedValue({ data: accountsResponse });
	const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });

	render(
		<QueryClientProvider client={queryClient}>
			<DropdownMenu open>
				<DropdownMenuContent>
					<CodexSessionAccountMenuItems sessionId="session-1" enabled />
				</DropdownMenuContent>
			</DropdownMenu>
		</QueryClientProvider>,
	);

	await vi.waitFor(() => expect(getMock).toHaveBeenCalled());
	const activeItem = await screen.findByRole("menuitem", { name: /active@example\.com/ });
	expect(activeItem).toHaveAttribute("data-account-selected", "true");
	expect(within(activeItem).getByText("In use")).toBeInTheDocument();
	expect(screen.getByRole("menuitem", { name: /other@example\.com/ })).toHaveAttribute("data-account-selected", "false");
});

it("marks the session account after a session-local switch", async () => {
	getMock.mockResolvedValue({ data: accountsResponse });
	postMock.mockResolvedValue({ data: { sessionId: "session-1", accountId: "account-2" } });
	const user = userEvent.setup();
	const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });

	render(
		<QueryClientProvider client={queryClient}>
			<DropdownMenu open>
				<DropdownMenuContent>
					<CodexSessionAccountMenuItems sessionId="session-1" enabled />
				</DropdownMenuContent>
			</DropdownMenu>
		</QueryClientProvider>,
	);

	const otherItem = await screen.findByRole("menuitem", { name: /other@example\.com/ });
	await user.click(otherItem);
	await vi.waitFor(() => expect(postMock).toHaveBeenCalledWith("/api/v1/agents/codex/sessions/{sessionId}/account", expect.objectContaining({
		params: { path: { sessionId: "session-1" } },
		body: { accountId: "account-2" },
	})));
	await vi.waitFor(() => expect(otherItem).toHaveAttribute("data-account-selected", "true"));
	expect(within(otherItem).getByText("In use")).toBeInTheDocument();
	expect(screen.getByRole("menuitem", { name: /active@example\.com/ })).toHaveAttribute("data-account-selected", "false");
});
