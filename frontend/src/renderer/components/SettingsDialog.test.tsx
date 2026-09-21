import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { useUiStore } from "../stores/ui-store";
import type { ProjectSettingsSaveState } from "./ProjectSettingsForm";
import { SettingsDialog } from "./SettingsDialog";

const { postMock } = vi.hoisted(() => ({ postMock: vi.fn() }));

const accountsResponse = {
	accountRevision: 0,
	accounts: [],
	capabilities: {},
	deviceReconciliation: { status: "verified", activeAccountVerified: false, reasonCode: "verified", retryable: false },
};

vi.mock("../lib/api-client", () => ({
	apiClient: { POST: postMock },
	apiErrorCode: (error: { code?: string }) => error?.code,
	apiErrorMessage: () => "request failed",
	hasTrustedApiBaseUrl: () => true,
}));

vi.mock("./ProjectSettingsForm", () => ({
	ProjectSettingsForm: ({
		onSaveState,
	}: {
		onSaveState?: (state: ProjectSettingsSaveState) => void;
	}) => (
		<button
			type="button"
			onClick={() =>
				onSaveState?.({
					phase: "pending",
				})
			}
		>
			Start pending save
		</button>
	),
}));

vi.mock("./GlobalSettingsForm", () => ({
	GlobalSettingsForm: ({ focusAgentId, section }: { focusAgentId?: string; section: string }) => (
		<div data-focus-agent={focusAgentId} data-testid="global-settings-section">{section}</div>
	),
}));

// The dialog reads the cloud gate to decide whether the Cloud nav page exists;
// mocked so these tests need no QueryClientProvider (same pattern as Sidebar).
vi.mock("../hooks/useCloudGate", () => ({
	useCloudGate: () => ({ cloudEnabled: false, localEnabled: true }),
}));

describe("SettingsDialog", () => {
	beforeEach(() => {
		postMock.mockReset().mockImplementation((path: string) => path === "/api/v1/agents/codex/accounts/ensure"
			? Promise.resolve({ data: accountsResponse })
			: Promise.resolve({ data: { operationId: "login-1", status: "cancelled" } }));
		useUiStore.setState({ settingsModal: null });
	});

	function renderSettingsDialog() {
		const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
		return render(<QueryClientProvider client={queryClient}><SettingsDialog /></QueryClientProvider>);
	}

	it("does not dismiss project settings while a save is pending", async () => {
		useUiStore.getState().openProjectSettings("proj-1");
		renderSettingsDialog();

		await userEvent.click(await screen.findByRole("button", { name: "Start pending save" }));
		const closeButton = screen.getByRole("button", { name: "Close settings" });
		expect(closeButton).toBeDisabled();

		await userEvent.keyboard("{Escape}");
		expect(useUiStore.getState().settingsModal).toEqual({ scope: "project", projectId: "proj-1" });
	});

	it("opens the requested global settings page", async () => {
		useUiStore.getState().openGlobalSettings("mobile");
		renderSettingsDialog();

		expect(await screen.findByTestId("global-settings-section")).toHaveTextContent("mobile");
		expect(screen.getByRole("button", { name: "Mobile" })).toHaveAttribute("aria-current", "page");
		await vi.waitFor(() => expect(postMock).toHaveBeenCalledWith(
			"/api/v1/agents/codex/accounts/ensure",
			{ body: { accountIds: [], includeUsage: true, forceAuthentication: true, forceDeviceReconciliation: true } },
		));
	});

	it("opens Harness and forwards its agent focus target without redirecting to Codex Accounts", async () => {
		useUiStore.getState().openGlobalSettings("harness", { focusAgentId: "claude-code" });
		renderSettingsDialog();

		const form = await screen.findByTestId("global-settings-section");
		expect(form).toHaveTextContent("harness");
		expect(form).toHaveAttribute("data-focus-agent", "claude-code");
		expect(screen.getByRole("button", { name: "Harness" })).toHaveAttribute("aria-current", "page");
		expect(screen.getByRole("button", { name: "Subscriptions" })).not.toHaveAttribute("aria-current", "page");
	});

	it("does not replay the Harness focus target after navigating away during the same modal opening", async () => {
		useUiStore.getState().openGlobalSettings("harness", { focusAgentId: "claude-code" });
		renderSettingsDialog();
		expect(await screen.findByTestId("global-settings-section")).toHaveAttribute("data-focus-agent", "claude-code");

		await userEvent.click(screen.getByRole("button", { name: "General" }));
		await userEvent.click(screen.getByRole("button", { name: "Harness" }));

		expect(screen.getByTestId("global-settings-section")).not.toHaveAttribute("data-focus-agent");
	});

	it("refreshes accounts once when global Settings opens, not when its pages change", async () => {
		useUiStore.getState().openGlobalSettings();
		renderSettingsDialog();

		await vi.waitFor(() => expect(postMock).toHaveBeenCalledTimes(1));
		await userEvent.click(screen.getByRole("button", { name: "Harness" }));
		expect(postMock).toHaveBeenCalledTimes(1);
	});

	it("mounts dialog chrome before the selected settings form", async () => {
		useUiStore.getState().openGlobalSettings("general");
		renderSettingsDialog();

		expect(screen.getByTestId("settings-dialog-body-pending")).toBeInTheDocument();
		expect(screen.queryByTestId("global-settings-section")).not.toBeInTheDocument();
		expect(await screen.findByTestId("global-settings-section")).toHaveTextContent("general");
	});

	it("does not expose Downloads as a standalone settings page", async () => {
		useUiStore.getState().openGlobalSettings("browserProfiles");
		renderSettingsDialog();

		expect(await screen.findByTestId("global-settings-section")).toHaveTextContent("browserProfiles");
		expect(screen.queryByRole("button", { name: "Downloads" })).not.toBeInTheDocument();
	});

	it("falls back to General when Cloud is unavailable", async () => {
		useUiStore.getState().openGlobalSettings("cloud");
		renderSettingsDialog();

		expect(await screen.findByTestId("global-settings-section")).toHaveTextContent("general");
		expect(screen.queryByRole("button", { name: "Cloud" })).not.toBeInTheDocument();
	});

	it("closes Settings without cancelling daemon-owned account login work", async () => {
		useUiStore.getState().openGlobalSettings("agents");
		renderSettingsDialog();

		expect(await screen.findByTestId("global-settings-section")).toHaveTextContent("agents");
		expect(screen.getByRole("button", { name: "General" })).toBeEnabled();
		await userEvent.click(screen.getByRole("button", { name: "Close settings" }));

		await vi.waitFor(() => expect(useUiStore.getState().settingsModal).toBeNull());
		expect(postMock.mock.calls.map(([path]) => path)).toEqual(["/api/v1/agents/codex/accounts/ensure"]);
	});

	it("traps focus and closes from Escape or the backdrop", async () => {
		useUiStore.getState().openGlobalSettings("general");
		renderSettingsDialog();

		const dialog = await screen.findByRole("dialog");
		expect(dialog).toHaveAttribute("aria-modal", "true");
		await vi.waitFor(() => expect(screen.getByRole("button", { name: "Close settings" })).toHaveFocus());
		await userEvent.keyboard("{Escape}");
		await vi.waitFor(() => expect(useUiStore.getState().settingsModal).toBeNull());

		useUiStore.getState().openGlobalSettings("general");
		fireEvent.pointerDown(await screen.findByTestId("settings-dialog-overlay"));
		await vi.waitFor(() => expect(useUiStore.getState().settingsModal).toBeNull());
	});

	it("does not close when Escape is handled by a portaled nested menu", async () => {
		useUiStore.getState().openGlobalSettings("general");
		renderSettingsDialog();

		await screen.findByRole("dialog");
		const nestedMenu = document.createElement("div");
		nestedMenu.setAttribute("role", "menu");
		const nestedItem = document.createElement("button");
		nestedItem.setAttribute("role", "menuitem");
		nestedMenu.append(nestedItem);
		document.body.append(nestedMenu);
		nestedItem.focus();
		fireEvent.keyDown(nestedItem, { key: "Escape" });
		expect(useUiStore.getState().settingsModal).not.toBeNull();
		nestedMenu.remove();

		fireEvent.keyDown(screen.getByRole("dialog"), { key: "Escape" });
		await vi.waitFor(() => expect(useUiStore.getState().settingsModal).toBeNull());
	});

	it("closes from Escape when a Harness focus target has not moved focus inside", async () => {
		useUiStore.getState().openGlobalSettings("harness", { focusAgentId: "stale-agent" });
		renderSettingsDialog();

		await screen.findByRole("dialog");
		document.body.focus();
		fireEvent.keyDown(document.body, { key: "Escape" });
		await vi.waitFor(() => expect(useUiStore.getState().settingsModal).toBeNull());
	});
});
