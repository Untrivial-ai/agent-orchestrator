import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { useCredentialDialogStore } from "../stores/credential-dialog-store";
import { CloudCredentialDialog } from "./CloudCredentialDialog";

const { putAgentConnection, refetchOrg } = vi.hoisted(() => ({
	putAgentConnection: vi.fn(),
	refetchOrg: vi.fn(),
}));

vi.mock("../hooks/useCloudCp", () => ({
	useCloudCp: () => ({
		client: { putAgentConnection },
		ready: true,
		baseUrl: "https://staging-api.aoagents.dev",
	}),
}));

vi.mock("../hooks/useCloudOrg", () => ({
	useCloudOrg: () => ({
		org: undefined,
		isLoading: true,
		error: undefined,
		refetch: refetchOrg,
		ready: true,
	}),
}));

describe("CloudCredentialDialog", () => {
	beforeEach(() => {
		putAgentConnection.mockReset().mockResolvedValue({
			providerConnection: { validationState: "valid" },
		});
		refetchOrg.mockReset().mockResolvedValue({ data: { id: "org-1" } });
		useCredentialDialogStore.setState({ open: true });
	});

	it("allows connecting while the org query is still settling and retries it on submit", async () => {
		const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
		render(
			<QueryClientProvider client={queryClient}>
				<CloudCredentialDialog />
			</QueryClientProvider>,
		);

		const user = userEvent.setup();
		await user.type(screen.getByLabelText(/setup token or api key/i), "sk-test");
		const connect = screen.getByRole("button", { name: "Connect" });
		expect(connect).toBeEnabled();

		await user.click(connect);

		expect(refetchOrg).toHaveBeenCalledOnce();
		expect(putAgentConnection).toHaveBeenCalledWith("org-1", "claude-code", {
			credentialType: "oauth_token",
			secret: "sk-test",
		});
	});
});
