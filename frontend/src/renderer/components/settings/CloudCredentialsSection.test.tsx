import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";
import "../../i18n";
import type { CloudCpProviderConnection } from "../../lib/cloud-cp";
import { useCredentialDialogStore } from "../../stores/credential-dialog-store";

const state = vi.hoisted(() => ({
	cloudEnabled: true,
	status: "authenticated",
	org: { id: "org-test" } as { id: string } | undefined,
	orgError: undefined as unknown,
	connections: [] as CloudCpProviderConnection[],
	isPending: false,
	isError: false,
}));
const client = vi.hoisted(() => ({
	listGitHubInstallations: vi.fn(),
	startGitHubInstallation: vi.fn(),
	syncGitHubInstallation: vi.fn(),
}));
const bridge = vi.hoisted(() => ({ openExternal: vi.fn() }));
vi.mock("../../lib/bridge", () => ({ aoBridge: { app: { openExternal: bridge.openExternal } } }));
vi.mock("../../hooks/useCloudGate", () => ({ useCloudGate: () => ({ cloudEnabled: state.cloudEnabled }) }));
vi.mock("../../lib/cloud-session", () => ({ useCloudSession: () => ({ status: state.status }) }));
vi.mock("../../hooks/useCloudOrg", () => ({ useCloudOrg: () => ({ org: state.org, error: state.orgError }) }));
vi.mock("../../hooks/useCloudCp", () => ({ useCloudCp: () => ({ client }) }));
vi.mock("../../hooks/useProviderConnections", () => ({
	useProviderConnections: () => ({ data: state.connections, isPending: state.isPending, isError: state.isError, isSuccess: !state.isPending && !state.isError }),
}));
import { CloudCredentialsSection } from "./CloudCredentialsSection";

function connection(provider: string, credentialType: string, validationState = "valid"): CloudCpProviderConnection {
	return { id: provider, provider, label: "default", config: { credentialType }, validationState, createdAt: "", updatedAt: "" };
}
function renderSettings() {
	const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	return render(<CloudCredentialsSection />, {
		wrapper: ({ children }) => <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>,
	});
}

beforeEach(() => {
	vi.clearAllMocks();
	state.cloudEnabled = true;
	state.status = "authenticated";
	state.org = { id: "org-test" };
	state.orgError = undefined;
	state.connections = [connection("codex", "auth_json"), connection("opencode", "openrouter_api_key", "invalid")];
	state.isPending = false;
	state.isError = false;
	client.listGitHubInstallations.mockResolvedValue({ installations: [] });
	client.startGitHubInstallation.mockResolvedValue({ installationUrl: "https://github.com/apps/ao/installations/new" });
	client.syncGitHubInstallation.mockResolvedValue({});
	bridge.openExternal.mockResolvedValue(undefined);
	useCredentialDialogStore.getState().closeDialog();
});

describe("Cloud settings connections", () => {
	it("shows readable methods and opens Manage for the selected agent", async () => {
		renderSettings();
		expect(screen.getByText("ChatGPT account")).toBeInTheDocument();
		expect(screen.getByText("OpenRouter API key")).toBeInTheDocument();
		expect(screen.getByText("Needs attention")).toBeInTheDocument();
		fireEvent.click(screen.getByRole("button", { name: "Manage OpenCode connection" }));
		expect(useCredentialDialogStore.getState()).toMatchObject({ open: true, targetAgent: "opencode", targetCredentialType: "openrouter_api_key" });
		fireEvent.click(screen.getByRole("button", { name: "Connect agent" }));
		expect(useCredentialDialogStore.getState()).toMatchObject({ open: true, targetAgent: null, targetCredentialType: null });
		await waitFor(() => expect(client.listGitHubInstallations).toHaveBeenCalled());
	});

	it("opens GitHub App installation without a personal token field", async () => {
		renderSettings();
		const connect = await screen.findByRole("button", { name: "Connect GitHub" });
		expect(screen.queryByLabelText("GitHub personal access token")).not.toBeInTheDocument();
		fireEvent.click(connect);
		await waitFor(() => expect(client.startGitHubInstallation).toHaveBeenCalledWith("org-test"));
		await waitFor(() => expect(bridge.openExternal).toHaveBeenCalledWith("https://github.com/apps/ao/installations/new"));
	});

	it("shows Connected when the browser completes the installation", async () => {
		const active = { id: "inst-1", status: "active", accountLogin: "acme", updatedAt: "now", syncStatus: "ready" };
		client.listGitHubInstallations
			.mockResolvedValueOnce({ installations: [] })
			.mockResolvedValueOnce({ installations: [] })
			.mockResolvedValue({ installations: [active] });
		renderSettings();
		fireEvent.click(await screen.findByRole("button", { name: "Connect GitHub" }));
		await waitFor(() => expect(bridge.openExternal).toHaveBeenCalled());
		await waitFor(() => expect(screen.getByRole("button", { name: "Manage repositories" })).toBeInTheDocument(), { timeout: 6000 });
		expect(screen.getAllByText("Connected")).toHaveLength(2);
	}, 10_000);

	it("shows connected GitHub account and repository management", async () => {
		client.listGitHubInstallations.mockResolvedValue({ installations: [{ id: "inst-1", status: "active", accountLogin: "acme", updatedAt: "now", syncStatus: "ready" }] });
		renderSettings();
		await screen.findByText("acme");
		fireEvent.click(screen.getByRole("button", { name: "Manage repositories" }));
		await waitFor(() => expect(client.startGitHubInstallation).toHaveBeenCalledWith("org-test"));
		await waitFor(() => expect(bridge.openExternal).toHaveBeenCalledWith("https://github.com/apps/ao/installations/new"));
		expect(screen.queryByLabelText("GitHub personal access token")).not.toBeInTheDocument();
	});

	it("shows a failed GitHub lookup instead of a disconnected state", async () => {
		client.listGitHubInstallations.mockRejectedValue(new Error("offline"));
		renderSettings();
		await screen.findByText("Could not load GitHub connection");
		expect(screen.queryByText("Not connected")).not.toBeInTheDocument();
	});

	it("shows loading and failure states instead of an empty connection list", async () => {
		state.connections = [];
		state.isPending = true;
		const view = renderSettings();
		expect(screen.getByRole("status")).toHaveTextContent("Loading connections");
		expect(screen.queryByText("No coding agents connected yet.")).not.toBeInTheDocument();
		view.unmount();
		state.isPending = false;
		state.isError = true;
		renderSettings();
		expect(screen.getByRole("alert")).toHaveTextContent("Could not load coding agent connections");
		await act(async () => {});
	});

	it("does not fetch credentials when Cloud is disabled or signed out", () => {
		state.cloudEnabled = false;
		const view = renderSettings();
		expect(view.container).toBeEmptyDOMElement();
		view.unmount();
		state.cloudEnabled = true;
		state.status = "unauthenticated";
		renderSettings();
		expect(screen.getByText(/Sign in to AO Cloud/)).toBeInTheDocument();
		expect(client.listGitHubInstallations).not.toHaveBeenCalled();
	});
});
