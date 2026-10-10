import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { useUiStore } from "../stores/ui-store";
import type { ProjectSettingsSaveState } from "./ProjectSettingsForm";
import { SettingsDialog } from "./SettingsPageTestHarness";
import { globalSettingsItemsFor, visibleGlobalSettings } from "./settings/settingsCatalog";

const { getMock, postMock, cloudProjectsState, localWorkspacesState } = vi.hoisted(() => ({
	getMock: vi.fn(),
	postMock: vi.fn(),
	cloudProjectsState: {
		data: [] as Array<{ id: string; orgId?: string; displayName: string }> | undefined,
		isLoading: false,
		isError: false,
		error: null as Error | null,
		refetch: vi.fn(),
	},
	localWorkspacesState: { ids: [] as string[] },
}));

vi.mock("../lib/api-client", () => ({
	apiClient: { GET: getMock, POST: postMock },
	apiErrorCode: (error: { code?: string }) => error?.code,
	apiErrorMessage: () => "request failed",
	hasTrustedApiBaseUrl: () => true,
}));

vi.mock("./ProjectSettingsForm", () => ({
	ProjectSettingsForm: ({
		cloudOrgId,
		onSaveState,
	}: {
		cloudOrgId?: string;
		onSaveState?: (state: ProjectSettingsSaveState) => void;
	}) => (
		<>
			{cloudOrgId ? <div data-testid="cloud-project-settings">{cloudOrgId}</div> : null}
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
			<button
				type="button"
				onClick={() =>
					onSaveState?.({
						phase: "failed",
						error: "Display name must be 100 characters or fewer",
					})
				}
			>
				Trigger failed save
			</button>
			<button type="button" onClick={() => onSaveState?.({ phase: "saved" })}>
				Complete save
			</button>
		</>
	),
}));

vi.mock("./GlobalSettingsForm", () => ({
	GlobalSettingsForm: ({ focusAgentId, hostId, section }: { focusAgentId?: string; hostId?: string; section: string }) => (
		<div data-focus-agent={focusAgentId} data-host={hostId} data-testid="global-settings-section">{section}</div>
	),
}));

// Cloud projects come from the control plane; none by default, so the
// local-daemon form stays in play unless a test seeds one.
vi.mock("../hooks/useWorkspaceQuery", () => ({
	useCloudProjectsQuery: () => cloudProjectsState,
	workspaceQueryOptions: {
		queryKey: ["workspaces"],
		queryFn: () => Promise.resolve(localWorkspacesState.ids.map((id: string) => ({ id }))),
	},
}));

vi.mock("./CuesDialog", () => ({
	CuesSettings: ({ projectId }: { projectId: string }) => <div data-testid="project-cues-settings">{projectId}</div>,
}));

vi.mock("./ProjectScriptsSettings", () => ({
	ProjectScriptsSettings: ({ projectId }: { projectId: string }) => <div data-testid="project-scripts-settings">{projectId}</div>,
}));

// The dialog reads the cloud gate to decide whether the Cloud nav page exists;
// mocked so these tests need no QueryClientProvider (same pattern as Sidebar).
vi.mock("../hooks/useCloudGate", () => ({
	useCloudGate: () => ({ cloudEnabled: false, localEnabled: true }),
}));

// The dialog reads the cloud session email to gate the 11x-only Coder page.
// Signed out here, so that page is never visible.
vi.mock("../lib/cloud-session", () => ({
	useCloudSession: () => ({ status: "unauthenticated", session: null }),
}));

describe("SettingsDialog", () => {
	beforeEach(() => {
		getMock.mockReset().mockResolvedValue({ data: { accounts: [] } });
		postMock.mockReset().mockResolvedValue({ data: { operationId: "login-1", status: "cancelled" } });
		useUiStore.setState({ developerMode: false, settingsModal: null });
		cloudProjectsState.data = [];
		cloudProjectsState.isLoading = false;
		cloudProjectsState.isError = false;
		cloudProjectsState.error = null;
		cloudProjectsState.refetch.mockReset();
		localWorkspacesState.ids = [];
	});

	function renderSettingsDialog() {
		const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
		return render(<QueryClientProvider client={queryClient}><SettingsDialog /></QueryClientProvider>);
	}

	it("offers setup and cleanup through one local Scripts page", async () => {
		useUiStore.getState().openProjectSettings("proj-1");
		renderSettingsDialog();
		await userEvent.click(await screen.findByRole("button", { name: "Scripts" }));
		expect(screen.getByTestId("project-scripts-settings")).toHaveTextContent("proj-1");
		expect(screen.queryByRole("button", { name: "Workspace setup" })).not.toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Workspace cleanup" })).not.toBeInTheDocument();
	});

	it("does not dismiss project settings while a save is pending", async () => {
		useUiStore.getState().openProjectSettings("proj-1");
		renderSettingsDialog();

		await userEvent.click(await screen.findByRole("button", { name: "Start pending save" }));
		const closeButton = screen.getByRole("button", { name: "Close settings" });
		await userEvent.click(closeButton);
		expect(useUiStore.getState().settingsModal).toEqual({ scope: "project", projectId: "proj-1" });

		await userEvent.keyboard("{Escape}");
		expect(useUiStore.getState().settingsModal).toEqual({ scope: "project", projectId: "proj-1" });
		await userEvent.click(screen.getByRole("button", { name: "Complete save" }));
		expect(useUiStore.getState().settingsModal).toBeNull();
	});

	it("renders visible error message when project settings save fails", async () => {
		useUiStore.getState().openProjectSettings("proj-1");
		renderSettingsDialog();

		await userEvent.click(await screen.findByRole("button", { name: "Trigger failed save" }));
		expect(await screen.findByRole("alert")).toHaveTextContent("Display name must be 100 characters or fewer");
	});

	it("keeps cue management in project settings without the project save action", async () => {
		useUiStore.getState().openProjectSettings("proj-1");
		renderSettingsDialog();

		const cuesSection = await screen.findByRole("button", { name: "Cues" });
		expect(cuesSection.querySelector(".lucide-play")).not.toBeNull();
		await userEvent.click(cuesSection);

		expect(screen.getByTestId("project-cues-settings")).toHaveTextContent("proj-1");
		expect(cuesSection).toHaveAttribute("data-active", "true");
		expect(screen.queryByRole("button", { name: "Save changes" })).not.toBeInTheDocument();
	});

	it("opens project settings on the cues page when the caller asks for it", async () => {
		useUiStore.getState().openProjectSettings("proj-1", { section: "cues" });
		renderSettingsDialog();

		expect(await screen.findByTestId("project-cues-settings")).toHaveTextContent("proj-1");
		expect(screen.getByRole("button", { name: "Cues" })).toHaveAttribute("data-active", "true");
	});

	it("loads a cloud project's settings from the control plane, not the local daemon", async () => {
		cloudProjectsState.data = [{ id: "cloud-1", orgId: "org-1", displayName: "ao-landing" }];
		useUiStore.getState().openProjectSettings("cloud-1");
		renderSettingsDialog();

		// Opened without a Cloud org, the project is still routed to its control-plane settings.
		expect(await screen.findByTestId("cloud-project-settings")).toHaveTextContent("org-1");
		expect(screen.getByRole("button", { name: "General" })).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Agents" })).toBeInTheDocument();
		for (const section of ["Scripts", "Environment", "Cues"]) {
			expect(screen.queryByRole("button", { name: section })).not.toBeInTheDocument();
		}
		expect(screen.queryByTestId("project-scripts-settings")).not.toBeInTheDocument();
	});

	it("waits for the cloud project list before falling back to local project settings", async () => {
		cloudProjectsState.isLoading = true;
		useUiStore.getState().openProjectSettings("cloud-1");
		renderSettingsDialog();

		expect(await screen.findByText("Loading project settings…")).toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Start pending save" })).not.toBeInTheDocument();
	});

	it("surfaces a failed cloud lookup instead of asking the local daemon for a cloud project", async () => {
		cloudProjectsState.data = undefined;
		cloudProjectsState.isError = true;
		cloudProjectsState.error = new Error("Cloud control plane request failed with status 503.");
		useUiStore.getState().openProjectSettings("cloud-1");
		renderSettingsDialog();

		expect(await screen.findByRole("alert")).toHaveTextContent("Couldn't load this cloud project. Cloud control plane request failed with status 503.");
		expect(screen.queryByRole("button", { name: "Start pending save" })).not.toBeInTheDocument();
		await userEvent.click(screen.getByRole("button", { name: "Retry" }));
		expect(cloudProjectsState.refetch).toHaveBeenCalledTimes(1);
	});

	it("keeps local project settings when the cloud lookup fails for a project the daemon lists", async () => {
		cloudProjectsState.data = undefined;
		cloudProjectsState.isError = true;
		cloudProjectsState.error = new Error("offline");
		localWorkspacesState.ids = ["proj-1"];
		useUiStore.getState().openProjectSettings("proj-1");
		renderSettingsDialog();

		expect(await screen.findByRole("button", { name: "Start pending save" })).toBeInTheDocument();
	});

	it("does not offer local environment or workspace scripts for a remote project", async () => {
		useUiStore.getState().openProjectSettings("proj-1", "box-a");
		renderSettingsDialog();

		expect(await screen.findByRole("button", { name: "Agents" })).toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Scripts" })).not.toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Environment" })).not.toBeInTheDocument();
	});

	it("opens the requested global settings page", async () => {
		useUiStore.getState().openGlobalSettings("mobile");
		renderSettingsDialog();

		expect(await screen.findByTestId("global-settings-section")).toHaveTextContent("mobile");
		expect(screen.getByRole("button", { name: "Mobile" })).toHaveAttribute("data-active", "true");
		// Opening settings re-checks every account, whichever page is shown, and reads the
		// plain list beside it so the Accounts page has its rows before usage arrives.
		await vi.waitFor(() => expect(getMock).toHaveBeenCalledWith(
			"/api/v1/provider-accounts",
			{ params: { query: { includeUsage: true, refresh: true } } },
		));
		expect(getMock).toHaveBeenCalledWith(
			"/api/v1/provider-accounts",
			{ params: { query: { includeUsage: false, refresh: false } } },
		);
	});

	it("shows Remote hosts with Developer mode on even while the connection switch is off", async () => {
		useUiStore.setState({ developerMode: true, remoteHosts: false });
		useUiStore.getState().openGlobalSettings("remoteHosts");
		renderSettingsDialog();

		expect(await screen.findByTestId("global-settings-section")).toHaveTextContent("remoteHosts");
		expect(screen.getByRole("button", { name: "Remote hosts" })).toHaveAttribute("data-active", "true");
	});

	it("hides Remote hosts and redirects its settings page when Developer mode is off", async () => {
		useUiStore.setState({ developerMode: false, remoteHosts: true });
		useUiStore.getState().openGlobalSettings("remoteHosts");
		renderSettingsDialog();

		expect(await screen.findByTestId("global-settings-section")).toHaveTextContent("general");
		expect(screen.queryByRole("button", { name: "Remote hosts" })).not.toBeInTheDocument();
	});

	it("opens Harness and forwards its agent focus target without redirecting to Codex Accounts", async () => {
		useUiStore.getState().openGlobalSettings("harness", { focusAgentId: "claude-code" });
		renderSettingsDialog();

		const form = await screen.findByTestId("global-settings-section");
		expect(form).toHaveTextContent("harness");
		expect(form).toHaveAttribute("data-focus-agent", "claude-code");
		expect(screen.getByRole("button", { name: "Harness" })).toHaveAttribute("data-active", "true");
		expect(screen.getByRole("button", { name: "Accounts" })).not.toHaveAttribute("data-active", "true");
	});

	it("forwards the remote host from a Manage agents action to Harness", async () => {
		useUiStore.getState().openGlobalSettings("harness", { focusAgentId: "codex", hostId: "box-a" });
		renderSettingsDialog();
		const form = await screen.findByTestId("global-settings-section");
		expect(form).toHaveAttribute("data-focus-agent", "codex");
		expect(form).toHaveAttribute("data-host", "box-a");
	});

	it("does not replay the Harness focus target after navigating away during the same modal opening", async () => {
		useUiStore.getState().openGlobalSettings("harness", { focusAgentId: "claude-code" });
		renderSettingsDialog();
		expect(await screen.findByTestId("global-settings-section")).toHaveAttribute("data-focus-agent", "claude-code");

		await userEvent.click(screen.getByRole("button", { name: "General" }));
		await userEvent.click(screen.getByRole("button", { name: "Harness" }));

		expect(screen.getByTestId("global-settings-section")).not.toHaveAttribute("data-focus-agent");
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

	it("opens Diagnostics as its own page, and leaves it out of the whole-settings view", async () => {
		useUiStore.setState({ developerMode: true, diagnostics: true });
		useUiStore.getState().openGlobalSettings("diagnostics");
		renderSettingsDialog();

		expect(await screen.findByTestId("global-settings-section")).toHaveTextContent("diagnostics");
		expect(screen.getByRole("button", { name: "Diagnostics" })).toBeInTheDocument();
		// The live monitor is a page of its own: the aggregate view never mounts it.
		expect(globalSettingsItemsFor("all", { cloudEnabled: true, developerMode: true, diagnostics: true, is11x: false }).map((item) => item.id)).not.toContain("diagnostics");
		expect(visibleGlobalSettings({ cloudEnabled: true, developerMode: true, diagnostics: true, is11x: false }).map((item) => item.id)).toContain("diagnostics");
	});

	it("hides Diagnostics until its toggle is on in Developer mode, falling back to General if asked for", async () => {
		useUiStore.setState({ developerMode: true, diagnostics: false });
		useUiStore.getState().openGlobalSettings("diagnostics");
		renderSettingsDialog();

		expect(await screen.findByTestId("global-settings-section")).toHaveTextContent("general");
		expect(screen.queryByRole("button", { name: "Diagnostics" })).not.toBeInTheDocument();
		expect(visibleGlobalSettings({ cloudEnabled: true, developerMode: true, diagnostics: false, is11x: false }).map((item) => item.id)).not.toContain("diagnostics");
	});

	it("hides Diagnostics outside Developer mode even with its toggle left on", async () => {
		useUiStore.setState({ developerMode: false, diagnostics: true });
		useUiStore.getState().openGlobalSettings("diagnostics");
		renderSettingsDialog();

		expect(await screen.findByTestId("global-settings-section")).toHaveTextContent("general");
		expect(screen.queryByRole("button", { name: "Diagnostics" })).not.toBeInTheDocument();
	});

	it("falls back to General when the Coder page is unavailable", async () => {
		useUiStore.getState().openGlobalSettings("coder11x");
		renderSettingsDialog();

		expect(await screen.findByTestId("global-settings-section")).toHaveTextContent("general");
		expect(screen.queryByRole("button", { name: "Coder" })).not.toBeInTheDocument();
	});

	it("opens Accounts, which reads its accounts itself, and closes without any account request", async () => {
		useUiStore.getState().openGlobalSettings("accountManager");
		renderSettingsDialog();

		expect(await screen.findByTestId("global-settings-section")).toHaveTextContent("accountManager");
		expect(screen.getByRole("button", { name: "General" })).toBeEnabled();
		await userEvent.click(screen.getByRole("button", { name: "Back" }));

		await vi.waitFor(() => expect(useUiStore.getState().settingsModal).toBeNull());
		expect(postMock).not.toHaveBeenCalled();
	});

	it("renders inside the page and closes from Escape or Back", async () => {
		useUiStore.getState().openGlobalSettings("general");
		renderSettingsDialog();

		expect(await screen.findByTestId("settings-page")).toBeInTheDocument();
		await userEvent.keyboard("{Escape}");
		await vi.waitFor(() => expect(useUiStore.getState().settingsModal).toBeNull());

		useUiStore.getState().openGlobalSettings("general");
		await userEvent.click(await screen.findByRole("button", { name: "Back" }));
		await vi.waitFor(() => expect(useUiStore.getState().settingsModal).toBeNull());
	});

	it("does not close when Escape is handled by a portaled nested menu", async () => {
		useUiStore.getState().openGlobalSettings("general");
		renderSettingsDialog();

		await screen.findByTestId("settings-page");
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

		fireEvent.keyDown(screen.getByTestId("settings-page"), { key: "Escape" });
		await vi.waitFor(() => expect(useUiStore.getState().settingsModal).toBeNull());
	});

	it("stays open when Escape cancels an inline edit inside it", async () => {
		useUiStore.getState().openGlobalSettings("browserProfiles");
		renderSettingsDialog();

		const dialog = await screen.findByTestId("settings-page");
		const inlineEdit = document.createElement("input");
		inlineEdit.setAttribute("data-settings-inline-edit", "");
		dialog.append(inlineEdit);
		inlineEdit.focus();
		fireEvent.keyDown(inlineEdit, { key: "Escape" });
		expect(useUiStore.getState().settingsModal).not.toBeNull();
		inlineEdit.remove();

		fireEvent.keyDown(screen.getByTestId("settings-page"), { key: "Escape" });
		await vi.waitFor(() => expect(useUiStore.getState().settingsModal).toBeNull());
	});

	it("closes from Escape when a Harness focus target has not moved focus inside", async () => {
		useUiStore.getState().openGlobalSettings("harness", { focusAgentId: "stale-agent" });
		renderSettingsDialog();

		await screen.findByTestId("settings-page");
		document.body.focus();
		fireEvent.keyDown(document.body, { key: "Escape" });
		await vi.waitFor(() => expect(useUiStore.getState().settingsModal).toBeNull());
	});
});
