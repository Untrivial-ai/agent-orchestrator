import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { useUiStore } from "../stores/ui-store";
import type { ProjectSettingsSaveState } from "./ProjectSettingsForm";
import { SettingsDialog } from "./SettingsDialog";

const { postMock } = vi.hoisted(() => ({ postMock: vi.fn() }));

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

vi.mock("./CloudProjectSettingsForm", () => ({
	CloudProjectSettingsForm: ({ projectId }: { projectId: string }) => (
		<div data-testid="cloud-project-settings-form">{projectId}</div>
	),
}));

const { workspaceQueryDataMock } = vi.hoisted(() => ({
	workspaceQueryDataMock: vi.fn<() => Array<{ id: string; kind?: string }> | undefined>(() => undefined),
}));

vi.mock("../hooks/useWorkspaceQuery", () => ({
	useWorkspaceQuery: () => ({ data: workspaceQueryDataMock() }),
}));

vi.mock("./GlobalSettingsForm", () => ({
	GlobalSettingsForm: ({ section }: { section: string }) => <div data-testid="global-settings-section">{section}</div>,
}));

// The dialog reads the cloud gate to decide whether the Cloud nav page exists;
// mocked so these tests need no QueryClientProvider (same pattern as Sidebar).
vi.mock("../hooks/useCloudGate", () => ({
	useCloudGate: () => ({ cloudEnabled: false, localEnabled: true }),
}));

describe("SettingsDialog", () => {
	beforeEach(() => {
		postMock.mockReset().mockResolvedValue({ data: { operationId: "login-1", status: "cancelled" } });
		useUiStore.setState({ settingsModal: null });
		workspaceQueryDataMock.mockReset().mockReturnValue(undefined);
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

	it("routes a cloud project to the read-only cloud settings form with the full project-spec nav", async () => {
		workspaceQueryDataMock.mockReturnValue([{ id: "cloud-proj-1", kind: "cloud" }]);
		useUiStore.getState().openProjectSettings("cloud-proj-1");
		renderSettingsDialog();

		expect(await screen.findByTestId("cloud-project-settings-form")).toHaveTextContent("cloud-proj-1");
		expect(screen.queryByRole("button", { name: "Start pending save" })).not.toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Save changes" })).not.toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Identity" })).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Agents" })).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Workflow" })).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Intake" })).toBeInTheDocument();
	});

	it("keeps a local project on the local settings form with the full nav", async () => {
		workspaceQueryDataMock.mockReturnValue([{ id: "proj-1", kind: "single_repo" }]);
		useUiStore.getState().openProjectSettings("proj-1");
		renderSettingsDialog();

		expect(await screen.findByRole("button", { name: "Start pending save" })).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Agents" })).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Workflow" })).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Intake" })).toBeInTheDocument();
	});

	it("opens the requested global settings page", async () => {
		useUiStore.getState().openGlobalSettings("mobile");
		renderSettingsDialog();

		expect(await screen.findByTestId("global-settings-section")).toHaveTextContent("mobile");
		expect(screen.getByRole("button", { name: "Mobile" })).toHaveAttribute("aria-current", "page");
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
		expect(postMock).not.toHaveBeenCalled();
	});

	it("traps focus and closes from Escape or the backdrop", async () => {
		useUiStore.getState().openGlobalSettings("general");
		renderSettingsDialog();

		const dialog = await screen.findByRole("dialog");
		expect(dialog).toHaveAttribute("aria-modal", "true");
		await vi.waitFor(() => expect(dialog).toContainElement(document.activeElement as HTMLElement));
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
		fireEvent.keyDown(document.body, { key: "Escape" });
		expect(useUiStore.getState().settingsModal).not.toBeNull();

		fireEvent.keyDown(screen.getByRole("dialog"), { key: "Escape" });
		await vi.waitFor(() => expect(useUiStore.getState().settingsModal).toBeNull());
	});
});
