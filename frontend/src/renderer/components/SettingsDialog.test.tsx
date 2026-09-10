import { render, screen } from "@testing-library/react";
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

vi.mock("./GlobalSettingsForm", () => ({
	GlobalSettingsForm: ({ section }: { section: string }) => <div data-testid="global-settings-section">{section}</div>,
}));

// The dialog reads the cloud gate to decide whether the Cloud nav page exists;
// mocked so these tests need no QueryClientProvider (same pattern as Sidebar).
vi.mock("../hooks/useCloudGate", () => ({
	useCloudGate: () => ({ cloudEnabled: false, localEnabled: true }),
}));

// The dialog also asks where a project lives, to decide whether a save control
// makes sense. Mocked for the same reason as the gate above.
const cloudProjectMock = vi.hoisted(() => ({ isCloudProject: false, isResolving: false }));

vi.mock("../hooks/useCloudProject", () => ({
	useCloudProject: () => ({
		project: cloudProjectMock.isCloudProject ? { id: "cloud-1" } : undefined,
		isResolving: cloudProjectMock.isResolving,
		isKnownLocal: !cloudProjectMock.isCloudProject && !cloudProjectMock.isResolving,
	}),
}));

describe("SettingsDialog", () => {
	beforeEach(() => {
		postMock.mockReset().mockResolvedValue({ data: { operationId: "login-1", status: "cancelled" } });
		useUiStore.setState({ settingsModal: null });
		cloudProjectMock.isCloudProject = false;
		cloudProjectMock.isResolving = false;
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

	it("offers the save control for a local project", async () => {
		useUiStore.getState().openProjectSettings("proj-1");
		renderSettingsDialog();

		expect(await screen.findByRole("button", { name: "Save changes" })).toBeInTheDocument();
	});

	it("withholds the save control for a cloud project", async () => {
		cloudProjectMock.isCloudProject = true;
		useUiStore.getState().openProjectSettings("cloud-1");
		renderSettingsDialog();

		expect(await screen.findByRole("button", { name: "Close settings" })).toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Save changes" })).not.toBeInTheDocument();
	});

	it("withholds the save control until the project is known to be local", async () => {
		cloudProjectMock.isResolving = true;
		useUiStore.getState().openProjectSettings("proj-1");
		renderSettingsDialog();

		expect(await screen.findByRole("button", { name: "Close settings" })).toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Save changes" })).not.toBeInTheDocument();
	});

});
