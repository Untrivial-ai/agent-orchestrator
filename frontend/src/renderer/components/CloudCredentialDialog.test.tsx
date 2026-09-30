import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { appI18n } from "../i18n";
import { useCredentialDialogStore } from "../stores/credential-dialog-store";
import { CloudCredentialDialog } from "./CloudCredentialDialog";

const mocks = vi.hoisted(() => ({
	putAgentConnection: vi.fn(),
	connectProviderAuth: vi.fn(),
	cancelProviderAuth: vi.fn(),
}));

vi.mock("../hooks/useCloudCp", () => ({
	useCloudCp: () => ({
		client: { putAgentConnection: mocks.putAgentConnection },
		baseUrl: "https://cloud.example.com",
		ready: true,
	}),
}));

vi.mock("../hooks/useCloudOrg", () => ({
	useCloudOrg: () => ({
		org: { id: "org-1", slug: "acme", displayName: "Acme", role: "member" },
		isLoading: false,
		error: undefined,
		ready: true,
	}),
}));

vi.mock("../lib/bridge", () => ({
	aoBridge: {
		cloud: {
			connectProviderAuth: mocks.connectProviderAuth,
			cancelProviderAuth: mocks.cancelProviderAuth,
		},
	},
}));

function renderDialog() {
	const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	return render(
		<QueryClientProvider client={queryClient}>
			<CloudCredentialDialog />
		</QueryClientProvider>,
	);
}

function openDialog(targetAgent?: string, credentialType?: string) {
	act(() => useCredentialDialogStore.getState().openDialog(targetAgent, credentialType));
}

async function chooseMethod(label: string) {
	await userEvent.click(screen.getByRole("button", { name: "Connection method" }));
	await userEvent.click(await screen.findByRole("menuitem", { name: label }));
}

describe("CloudCredentialDialog", () => {
	beforeEach(async () => {
		await appI18n.changeLanguage("en");
		useCredentialDialogStore.getState().closeDialog();
		mocks.putAgentConnection.mockReset().mockResolvedValue({ providerConnection: { validationState: "valid" } });
		mocks.connectProviderAuth.mockReset().mockResolvedValue(undefined);
		mocks.cancelProviderAuth.mockReset().mockResolvedValue(undefined);
	});

	afterEach(() => {
		vi.clearAllMocks();
	});

	it("shows method-specific Claude fields and keeps browser login free of key instructions", async () => {
		renderDialog();
		openDialog();

		expect(screen.getByRole("heading", { name: "Connect a coding agent" })).toBeInTheDocument();
		expect(screen.getByLabelText("Claude Code setup token")).toHaveAttribute("placeholder", "Paste the token from claude setup-token");
		expect(screen.getByText("Run claude setup-token in your terminal, then paste the token here.")).toBeInTheDocument();

		await chooseMethod("Anthropic API key");
		expect(screen.getByLabelText("Anthropic API key")).toHaveAttribute("placeholder", "Paste your Anthropic API key");
		expect(screen.getByText("Create an API key in the Anthropic Console.")).toBeInTheDocument();

		await chooseMethod("Log in with Anthropic");
		expect(screen.queryByLabelText(/API key|setup token/i)).not.toBeInTheDocument();
		expect(screen.getByText("A browser window will open so you can sign in to your Anthropic account.")).toBeInTheDocument();
		expect(screen.getByRole("heading", { name: "Connect a coding agent" })).toBeInTheDocument();
	});

	it("uses an agent-specific locked form for Cursor without a one-option method menu", async () => {
		renderDialog();
		openDialog("cursor");

		expect(screen.getByRole("heading", { name: "Connect Cursor" })).toBeInTheDocument();
		expect(screen.getByText("Use your Cursor API key to run Cursor in AO Cloud.")).toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Connection method" })).not.toBeInTheDocument();
		const input = screen.getByLabelText("Cursor API key");
		expect(input).toBeEnabled();

		await userEvent.type(input, "cursor-secret");
		fireEvent.click(screen.getByRole("button", { name: "Connect" }));
		await waitFor(() => expect(mocks.putAgentConnection).toHaveBeenCalledWith("org-1", "cursor", {
			credentialType: "api_key",
			secret: "cursor-secret",
		}));
	});

	it("sends the selected OpenCode provider key type with the entered secret", async () => {
		renderDialog();
		openDialog("opencode");
		await chooseMethod("OpenRouter API key");

		expect(screen.getByLabelText("OpenRouter API key")).toHaveAttribute("placeholder", "Paste your OpenRouter API key");
		await userEvent.type(screen.getByLabelText("OpenRouter API key"), "router-secret");
		fireEvent.click(screen.getByRole("button", { name: "Connect" }));

		await waitFor(() => expect(mocks.putAgentConnection).toHaveBeenCalledWith("org-1", "opencode", {
			credentialType: "openrouter_api_key",
			secret: "router-secret",
		}));
	});

	it.each(["auth_json", "access_token"])("preselects ChatGPT login for a Codex %s connection", (storedType) => {
		renderDialog();
		openDialog("codex", storedType);

		expect(screen.getByRole("heading", { name: "Connect Codex" })).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Connection method" })).toHaveTextContent("Log in with ChatGPT");
		expect(screen.getByRole("button", { name: "Log in with ChatGPT" })).toBeEnabled();
		expect(screen.queryByLabelText("OpenAI API key")).not.toBeInTheDocument();
	});

	it("preselects the stored OpenRouter method for OpenCode", () => {
		renderDialog();
		openDialog("opencode", "openrouter_api_key");

		expect(screen.getByRole("button", { name: "Connection method" })).toHaveTextContent("OpenRouter API key");
		expect(screen.getByLabelText("OpenRouter API key")).toBeInTheDocument();
	});

	it("keeps Claude setup tokens selected and falls back for unknown types", () => {
		renderDialog();
		openDialog("claude-code", "oauth_token");
		expect(screen.getByLabelText("Claude Code setup token")).toBeInTheDocument();

		openDialog("codex", "unknown_type");
		expect(screen.getByRole("button", { name: "Connection method" })).toHaveTextContent("OpenAI API key");
		expect(screen.getByLabelText("OpenAI API key")).toBeInTheDocument();
	});

	it("resets to the generic default and clears the secret on reopen", async () => {
		renderDialog();
		openDialog("opencode", "openrouter_api_key");
		await userEvent.type(screen.getByLabelText("OpenRouter API key"), "router-secret");

		act(() => useCredentialDialogStore.getState().closeDialog());
		openDialog();

		expect(screen.getByRole("heading", { name: "Connect a coding agent" })).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Connection method" })).toHaveTextContent("Claude Code setup token");
		expect(screen.getByLabelText("Claude Code setup token")).toHaveValue("");
	});

	it.each([
		["claude-code", "Log in with Anthropic", "A browser window will open so you can sign in to your Anthropic account."],
		["codex", "Log in with ChatGPT", "A browser window will open so you can sign in to your ChatGPT account. Use the account with your Codex subscription."],
	] as const)("dispatches browser login for %s without a secret payload", async (agent, loginLabel, description) => {
		renderDialog();
		openDialog(agent);
		await chooseMethod(loginLabel);

		expect(screen.getByText(description)).toBeInTheDocument();
		expect(screen.queryByLabelText(/API key|setup token/i)).not.toBeInTheDocument();
		fireEvent.click(screen.getByRole("button", { name: loginLabel }));

		await waitFor(() => expect(mocks.connectProviderAuth).toHaveBeenCalledWith({
			baseUrl: "https://cloud.example.com",
			orgId: "org-1",
			provider: agent,
		}));
	});
});
