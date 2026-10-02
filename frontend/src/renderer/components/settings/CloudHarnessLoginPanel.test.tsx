import { render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { appI18n } from "../../i18n";
import { CloudHarnessLoginPanel, type CloudHarness } from "./CloudHarnessLoginPanel";

const bridgeMocks = vi.hoisted(() => ({
	connectProviderAuth: vi.fn(),
	cancelProviderAuth: vi.fn(),
	openExternal: vi.fn(),
}));

const cloudMocks = vi.hoisted(() => ({
	putUserAgentConnection: vi.fn(),
}));

vi.mock("../../lib/bridge", () => ({
	aoBridge: {
		app: { openExternal: bridgeMocks.openExternal },
		cloud: {
			connectProviderAuth: bridgeMocks.connectProviderAuth,
			cancelProviderAuth: bridgeMocks.cancelProviderAuth,
		},
	},
}));

vi.mock("../../hooks/useCloudCp", () => ({
	useCloudCp: () => ({
		client: { putUserAgentConnection: cloudMocks.putUserAgentConnection },
		ready: true,
		baseUrl: "https://cloud.example.test",
	}),
}));

// Mirror the main process: cancelling aborts the pending login, whose IPC call
// then rejects with the cancellation error.
function pendingLogin() {
	let reject: (err: Error) => void = () => {};
	bridgeMocks.connectProviderAuth.mockImplementation(
		() =>
			new Promise((_resolve, rej) => {
				reject = rej;
			}),
	);
	bridgeMocks.cancelProviderAuth.mockImplementation(async () => {
		reject(new Error("Error invoking remote method 'cloud:connectProviderAuth': Error: Login was cancelled."));
	});
}

function renderPanel(agent: CloudHarness = "claude-code") {
	const onClose = vi.fn();
	render(
		<QueryClientProvider client={new QueryClient()}>
			<CloudHarnessLoginPanel agent={agent} onClose={onClose} />
		</QueryClientProvider>,
	);
	return { onClose };
}

describe("CloudHarnessLoginPanel", () => {
	beforeEach(async () => {
		await appI18n.changeLanguage("en");
		bridgeMocks.connectProviderAuth.mockReset();
		bridgeMocks.cancelProviderAuth.mockReset();
		cloudMocks.putUserAgentConnection.mockReset();
		bridgeMocks.openExternal.mockReset();
	});

	it("defaults Claude Code to logging in with Anthropic and offers the fallbacks", () => {
		renderPanel();
		expect(screen.getByRole("button", { name: "Log in with Anthropic" })).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Setup token" })).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Anthropic API key" })).toBeInTheDocument();
		expect(screen.queryByRole("textbox")).toBeNull();
	});

	it("logs in through the browser as a personal credential that also covers local sessions", async () => {
		bridgeMocks.connectProviderAuth.mockResolvedValue(undefined);
		const user = userEvent.setup();
		const { onClose } = renderPanel();
		await user.click(screen.getByRole("button", { name: "Log in with Anthropic" }));

		expect(bridgeMocks.connectProviderAuth).toHaveBeenCalledWith({
			baseUrl: "https://cloud.example.test",
			provider: "claude-code",
			persistLocalClaudeToken: true,
		});
		await waitFor(() => expect(onClose).toHaveBeenCalled());
	});

	it("cancels a pending browser login without showing an error", async () => {
		pendingLogin();
		const user = userEvent.setup();
		const { onClose } = renderPanel();
		await user.click(screen.getByRole("button", { name: "Log in with Anthropic" }));
		expect(screen.getByRole("button", { name: "Waiting for browser…" })).toBeDisabled();

		await user.click(screen.getByRole("button", { name: "Cancel" }));

		expect(bridgeMocks.cancelProviderAuth).toHaveBeenCalledTimes(1);
		expect(await screen.findByRole("button", { name: "Log in with Anthropic" })).toBeEnabled();
		expect(screen.queryByRole("alert")).toBeNull();
		expect(onClose).not.toHaveBeenCalled();
	});

	it("still shows a real login failure", async () => {
		bridgeMocks.connectProviderAuth.mockRejectedValue(new Error("Claude sign-in did not complete."));
		const user = userEvent.setup();
		renderPanel();
		await user.click(screen.getByRole("button", { name: "Log in with Anthropic" }));

		expect(await screen.findByRole("alert")).toHaveTextContent("Claude sign-in did not complete.");
	});

	it("explains how to get a setup token when pasting one", async () => {
		const user = userEvent.setup();
		renderPanel();
		await user.click(screen.getByRole("button", { name: "Setup token" }));

		expect(screen.getByText("claude setup-token")).toBeInTheDocument();
		expect(screen.getByText("sk-ant-oat")).toBeInTheDocument();
		expect(screen.getByLabelText("Setup token")).toHaveAttribute("placeholder", "Paste your setup token");
		expect(screen.getByRole("button", { name: "Log in with Anthropic" })).toBeInTheDocument();
	});

	it("says which API key to paste and links to where it is created", async () => {
		const user = userEvent.setup();
		renderPanel("codex");
		await user.click(screen.getByRole("button", { name: "OpenAI API key" }));

		expect(screen.getByPlaceholderText("Paste your OpenAI API key")).toBeInTheDocument();
		expect(screen.getByText(/then paste it here/)).toHaveTextContent("Create a key at platform.openai.com, then paste it here.");
		await user.click(screen.getByRole("button", { name: "platform.openai.com" }));
		expect(bridgeMocks.openExternal).toHaveBeenCalledWith("https://platform.openai.com/api-keys");
	});

	it("lets OpenCode connect a key for any of its providers", async () => {
		cloudMocks.putUserAgentConnection.mockResolvedValue({ providerConnection: { validationState: "valid" } });
		const user = userEvent.setup();
		renderPanel("opencode");
		expect(screen.getByPlaceholderText("Paste your OpenCode API key")).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "OpenRouter API key" }));
		await user.type(screen.getByPlaceholderText("Paste your OpenRouter API key"), "or-key");
		await user.click(screen.getByRole("button", { name: "Connect" }));

		expect(cloudMocks.putUserAgentConnection).toHaveBeenCalledWith("opencode", { credentialType: "openrouter_api_key", secret: "or-key" });
	});

	it("translates method names and placeholders whole, inserting only the provider brand", async () => {
		await appI18n.changeLanguage("de");
		const user = userEvent.setup();
		renderPanel();
		await user.click(screen.getByRole("button", { name: "Setup-Token" }));
		expect(screen.getByPlaceholderText("Füge dein Setup-Token ein")).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Anthropic-API-Schlüssel" }));
		expect(screen.getByPlaceholderText("Füge deinen Anthropic-API-Schlüssel ein")).toBeInTheDocument();
	});

	it("saves a pasted Cursor API key as the user's personal connection", async () => {
		cloudMocks.putUserAgentConnection.mockResolvedValue({ providerConnection: { validationState: "valid" } });
		const user = userEvent.setup();
		const { onClose } = renderPanel("cursor");
		expect(screen.queryByText("Or use")).toBeNull();
		await user.type(screen.getByPlaceholderText("Paste your Cursor API key"), " cursor-key ");
		await user.click(screen.getByRole("button", { name: "Connect" }));

		expect(cloudMocks.putUserAgentConnection).toHaveBeenCalledWith("cursor", { credentialType: "api_key", secret: "cursor-key" });
		await waitFor(() => expect(onClose).toHaveBeenCalled());
	});
});
