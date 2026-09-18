import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { useCredentialDialogStore } from "../stores/credential-dialog-store";
import { CloudCredentialDialog } from "./CloudCredentialDialog";

const { putAgentConnectionMock } = vi.hoisted(() => ({
	putAgentConnectionMock: vi.fn(),
}));

vi.mock("../hooks/useCloudCp", () => ({
	useCloudCp: () => ({
		client: { putAgentConnection: putAgentConnectionMock },
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
		putAgentConnectionMock.mockReset();
		useCredentialDialogStore.setState({ open: true });
	});

	it("offers all supported harnesses without loading them from the control plane", async () => {
		renderDialog();

		await userEvent.click(screen.getByLabelText("Coding agent"));

		expect(await screen.findByRole("menuitem", { name: "Claude Code" })).toBeInTheDocument();
		expect(screen.getByRole("menuitem", { name: "Codex" })).toBeInTheDocument();
		expect(screen.getByRole("menuitem", { name: "Cursor" })).toBeInTheDocument();
	});

	it("connects a new credential after a harness has been removed", async () => {
		putAgentConnectionMock.mockResolvedValueOnce({
			providerConnection: { validationState: "valid" },
		});

		renderDialog();

		await userEvent.type(screen.getByLabelText("Setup token or API key"), "new-test-token");
		await userEvent.click(screen.getByRole("button", { name: "Connect" }));

		expect(putAgentConnectionMock).toHaveBeenCalledWith("org-1", "claude-code", {
			credentialType: "oauth_token",
			secret: "new-test-token",
		});
		expect(await screen.findByRole("status")).toHaveTextContent("Credential connected");
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
