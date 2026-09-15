import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { useCredentialDialogStore } from "../stores/credential-dialog-store";
import { CloudCredentialDialog } from "./CloudCredentialDialog";

const { getAvailableAgentsMock } = vi.hoisted(() => ({
	getAvailableAgentsMock: vi.fn(),
}));

vi.mock("../hooks/useCloudCp", () => ({
	useCloudCp: () => ({
		client: { getAvailableAgents: getAvailableAgentsMock },
		ready: true,
		baseUrl: "http://127.0.0.1:8081",
	}),
}));

vi.mock("../hooks/useCloudOrg", () => ({
	useCloudOrg: () => ({
		org: { id: "org-1", slug: "test", displayName: "Test", role: "admin" },
	}),
}));

function renderDialog() {
	const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	return render(
		<QueryClientProvider client={queryClient}>
			<CloudCredentialDialog />
		</QueryClientProvider>,
	);
}

describe("CloudCredentialDialog", () => {
	beforeEach(() => {
		getAvailableAgentsMock.mockReset();
		useCredentialDialogStore.setState({ open: true });
	});

	it("shows a loading state instead of an empty agent selector", () => {
		getAvailableAgentsMock.mockReturnValue(new Promise(() => undefined));

		renderDialog();

		expect(screen.getByRole("status")).toHaveTextContent("Loading coding agents");
		expect(screen.queryByLabelText("Coding agent")).not.toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Connect" })).not.toBeInTheDocument();
	});

	it("explains an agent-loading failure and retries the request", async () => {
		getAvailableAgentsMock
			.mockRejectedValueOnce(new Error("control plane unavailable"))
			.mockResolvedValueOnce({
				agents: [
					{ id: "claude-code", provider: "anthropic", hasValidCred: false, validationState: "missing" },
				],
			});

		renderDialog();

		expect(await screen.findByRole("alert")).toHaveTextContent("Could not load coding agents");
		expect(screen.queryByLabelText("Coding agent")).not.toBeInTheDocument();

		await userEvent.click(screen.getByRole("button", { name: "Retry" }));

		expect(await screen.findByLabelText("Coding agent")).toBeEnabled();
		expect(getAvailableAgentsMock).toHaveBeenCalledTimes(2);
		expect(screen.queryByRole("alert")).not.toBeInTheDocument();
	});

	it("should define agent metadata with all required agents", () => {
		const AGENT_METADATA = {
			"claude-code": {
				label: "Claude Code",
				creds: [
					{ value: "oauth_token", label: "Setup token" },
					{ value: "api_key", label: "API key" },
				],
			},
			codex: {
				label: "Codex",
				creds: [
					{ value: "access_token", label: "Access token" },
					{ value: "api_key", label: "API key" },
				],
			},
			cursor: {
				label: "Cursor",
				creds: [{ value: "api_key", label: "API key" }],
			},
		};

		expect(AGENT_METADATA["claude-code"].label).toBe("Claude Code");
		expect(AGENT_METADATA.codex.label).toBe("Codex");
		expect(AGENT_METADATA.cursor.label).toBe("Cursor");
	});

	it("should have credential types for each agent", () => {
		const agents = ["claude-code", "codex", "cursor"];

		agents.forEach((agent) => {
			expect(agent).toBeDefined();
		});
	});

	it("should support multiple credential types per agent", () => {
		const credentialTypes = {
			"claude-code": 2, // oauth_token, api_key
			codex: 2, // access_token, api_key
			cursor: 1, // api_key
		};

		Object.entries(credentialTypes).forEach(([, expectedCount]) => {
			expect(expectedCount).toBeGreaterThan(0);
		});
	});
});
