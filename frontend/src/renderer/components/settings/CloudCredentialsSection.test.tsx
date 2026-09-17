import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import "../../i18n";
import { CloudCredentialsSection } from "./CloudCredentialsSection";

const state = vi.hoisted(() => ({
	connections: [] as Array<{ id: string; provider: string; label: string; validationState: string }>,
	deleteAgentConnection: vi.fn(),
}));

vi.mock("../../hooks/useCloudGate", () => ({
	useCloudGate: () => ({ cloudEnabled: true }),
}));

vi.mock("../../hooks/useCloudCp", () => ({
	useCloudCp: () => ({
		client: {
			listUserProviderConnections: vi.fn().mockResolvedValue({ providerConnections: [] }),
			deleteAgentConnection: state.deleteAgentConnection,
		},
		ready: true,
	}),
}));

vi.mock("../../hooks/useCloudOrg", () => ({
	useCloudOrg: () => ({ org: { id: "org-1" } }),
}));

vi.mock("../../lib/cloud-session", () => ({
	useCloudSession: () => ({ status: "authenticated" }),
}));

vi.mock("../../hooks/useProviderConnections", () => ({
	providerConnectionsQueryKey: (orgId: string) => ["cloud", "provider-connections", orgId],
	useProviderConnections: () => ({ data: state.connections, isSuccess: true }),
	hasValidAgentConnection: (connections: Array<{ validationState: string }>) =>
		connections.some((connection) => connection.validationState === "valid"),
}));

vi.mock("../../hooks/useCloudAvailableAgents", () => ({
	cloudAvailableAgentsQueryKey: (orgId: string) => ["cloud", "agents", "available", orgId],
}));

function renderSection() {
	const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	return render(
		<QueryClientProvider client={queryClient}>
			<CloudCredentialsSection />
		</QueryClientProvider>,
	);
}

describe("CloudCredentialsSection", () => {
	beforeEach(() => {
		state.connections = [
			{ id: "claude-connection", provider: "claude-code", label: "default", validationState: "valid" },
			{ id: "codex-connection", provider: "codex", label: "default", validationState: "valid" },
			{ id: "cursor-connection", provider: "cursor", label: "default", validationState: "valid" },
		];
		state.deleteAgentConnection.mockReset();
		state.deleteAgentConnection.mockResolvedValue(undefined);
	});

	it("removes credentials for Claude Code, Codex, and Cursor without sending a secret", async () => {
		renderSection();

		for (const agent of ["Claude Code", "Codex", "Cursor"]) {
			await userEvent.click(screen.getByRole("button", { name: `Remove ${agent} credential` }));
			await userEvent.click(screen.getByRole("button", { name: "Confirm remove" }));
			await waitFor(() => expect(state.deleteAgentConnection).toHaveBeenCalled());
		}

		expect(state.deleteAgentConnection).toHaveBeenNthCalledWith(1, "org-1", "claude-code");
		expect(state.deleteAgentConnection).toHaveBeenNthCalledWith(2, "org-1", "codex");
		expect(state.deleteAgentConnection).toHaveBeenNthCalledWith(3, "org-1", "cursor");
		for (const call of state.deleteAgentConnection.mock.calls) expect(call).toHaveLength(2);
	});

	it("shows a deletion error without exposing credential data", async () => {
		state.deleteAgentConnection.mockRejectedValueOnce(new Error("could not delete"));
		renderSection();

		await userEvent.click(screen.getByRole("button", { name: "Remove Claude Code credential" }));
		await userEvent.click(screen.getByRole("button", { name: "Confirm remove" }));

		expect(await screen.findByRole("alert")).toHaveTextContent("could not delete");
		expect(screen.queryByText("sk-ant-test-secret")).not.toBeInTheDocument();
	});
});
