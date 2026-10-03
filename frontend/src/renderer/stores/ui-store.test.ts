import { beforeEach, describe, expect, it, vi } from "vitest";
import { rememberedFileDisplayMode, sidebarIsVisible, sidebarOccupiesLayout, useUiStore } from "./ui-store";
import { sessionUiKey } from "../lib/hosts";

describe("sidebar visibility", () => {
	beforeEach(() => {
		window.localStorage.clear();
		useUiStore.setState({ isSidebarOpen: true });
	});

	it("changes only through the explicit toggle and persists the preference", () => {
		useUiStore.getState().toggleSidebar();

		let state = useUiStore.getState();
		expect(state.isSidebarOpen).toBe(false);
		expect(sidebarIsVisible(state)).toBe(false);
		expect(sidebarOccupiesLayout(state)).toBe(false);
		expect(window.localStorage.getItem("ao.sidebar.open")).toBe("false");

		useUiStore.getState().toggleSidebar();
		state = useUiStore.getState();
		expect(state.isSidebarOpen).toBe(true);
		expect(sidebarIsVisible(state)).toBe(true);
		expect(sidebarOccupiesLayout(state)).toBe(true);
		expect(window.localStorage.getItem("ao.sidebar.open")).toBe("true");
	});
});

describe("global settings deep links", () => {
	it("stores an optional harness focus target without changing existing calls", () => {
		useUiStore.getState().openGlobalSettings("harness", { focusAgentId: "codex" });
		expect(useUiStore.getState().settingsModal).toEqual({
			scope: "global",
			section: "harness",
			focusAgentId: "codex",
		});

		useUiStore.getState().openGlobalSettings("agents");
		expect(useUiStore.getState().settingsModal).toEqual({ scope: "global", section: "agents" });
	});

	it("preserves project settings only for explicit recovery navigation", () => {
		useUiStore.getState().openProjectSettings("project-1");
		useUiStore.getState().openGlobalSettings("general");
		useUiStore.getState().closeSettings();
		expect(useUiStore.getState().settingsModal).toBeNull();

		useUiStore.getState().openProjectSettings("project-1");
		useUiStore.getState().openGlobalSettings("harness", { focusAgentId: "codex", preserveProject: true });
		expect(useUiStore.getState().settingsModal).toEqual({
			scope: "global",
			section: "harness",
			focusAgentId: "codex",
			returnTo: { scope: "project", projectId: "project-1" },
		});
		useUiStore.getState().closeSettings();
		expect(useUiStore.getState().settingsModal).toEqual({ scope: "project", projectId: "project-1" });
	});
});

// A fresh module sees exactly what a booting renderer sees: only storage.
async function bootStore() {
	return (await import("./ui-store")).useUiStore;
}

describe("remoteHosts flag", () => {
	beforeEach(() => {
		window.localStorage.clear();
		vi.resetModules();
		useUiStore.setState({ remoteHosts: false });
	});

	it("is off until the user turns it on", async () => {
		expect((await bootStore()).getState().remoteHosts).toBe(false);
	});

	it("persists the switch so the choice survives a restart", () => {
		useUiStore.getState().setRemoteHosts(true);
		expect(useUiStore.getState().remoteHosts).toBe(true);
		expect(window.localStorage.getItem("ao.remoteHosts")).toBe("true");
		useUiStore.getState().setRemoteHosts(false);
	});

	it("reads a stored choice back at startup", async () => {
		window.localStorage.setItem("ao.remoteHosts", "true");
		expect((await bootStore()).getState().remoteHosts).toBe(true);
	});
});

describe("terminalCopyOnSelect flag", () => {
	beforeEach(() => {
		window.localStorage.clear();
		vi.resetModules();
		useUiStore.setState({ terminalCopyOnSelect: true });
	});

	it("is on until the user turns it off", async () => {
		expect((await bootStore()).getState().terminalCopyOnSelect).toBe(true);
	});

	it("persists the switch so the choice survives a restart", () => {
		useUiStore.getState().setTerminalCopyOnSelect(false);
		expect(useUiStore.getState().terminalCopyOnSelect).toBe(false);
		expect(window.localStorage.getItem("ao.terminalCopyOnSelect")).toBe("false");
		useUiStore.getState().setTerminalCopyOnSelect(true);
	});

	it("reads a stored opt-out back at startup", async () => {
		window.localStorage.setItem("ao.terminalCopyOnSelect", "false");
		expect((await bootStore()).getState().terminalCopyOnSelect).toBe(false);
	});
});

describe("project UI state across hosts", () => {
	beforeEach(() => {
		useUiStore.setState({
			restartingProjectIds: new Set(),
			provisioningProjectIds: new Set(),
			orchestratorStartupErrors: {},
			newTaskRequest: null,
		});
	});

	it("keeps equal project IDs on two hosts and local separate", () => {
		const state = useUiStore.getState();
		state.setProjectRestarting("project", true, "host-a");
		state.setProjectProvisioning("project", true, "host-a");
		state.setOrchestratorStartupError("project", "Host A failed", "host-a");
		state.setOrchestratorStartupError("project", "Host B failed", "host-b");

		expect(useUiStore.getState().restartingProjectIds.has(sessionUiKey("project", "host-a"))).toBe(true);
		expect(useUiStore.getState().restartingProjectIds.has(sessionUiKey("project", "host-b"))).toBe(false);
		expect(useUiStore.getState().restartingProjectIds.has("project")).toBe(false);
		expect(useUiStore.getState().orchestratorStartupErrors[sessionUiKey("project", "host-b")]).toBe("Host B failed");
		state.setOrchestratorStartupError("project", null, "host-a");
		expect(useUiStore.getState().orchestratorStartupErrors[sessionUiKey("project", "host-b")]).toBe("Host B failed");
	});

	it("blocks a new task only for the project still provisioning on its host", () => {
		const state = useUiStore.getState();
		state.setProjectProvisioning("project", true, "host-a");
		state.requestNewTask("project", "host-a");
		expect(useUiStore.getState().newTaskRequest).toBeNull();
		state.requestNewTask("project", "host-b");
		expect(useUiStore.getState().newTaskRequest).toMatchObject({ projectId: "project", hostId: "host-b" });
	});
});

describe("file display modes", () => {
	beforeEach(() => {
		useUiStore.setState({ inspectorSessions: {} });
	});

	it("restores a pick only for the same session, path, and open request", () => {
		useUiStore.getState().setFileDisplayMode("sess-1", "README.md", "rendered", 3);
		const state = useUiStore.getState();

		expect(rememberedFileDisplayMode(state, "sess-1", "README.md", 3)).toBe("rendered");
		expect(rememberedFileDisplayMode(state, "sess-1", "README.md", 4)).toBeUndefined();
		expect(rememberedFileDisplayMode(state, "sess-2", "README.md", 3)).toBeUndefined();
		expect(rememberedFileDisplayMode(state, "sess-1", "docs/guide.md", 3)).toBeUndefined();
		expect(rememberedFileDisplayMode(state, "sess-1", null, 3)).toBeUndefined();
	});

	it("keeps the rest of the session's Files state", () => {
		useUiStore.getState().setFilesChangedOnly("sess-1", false);
		useUiStore.getState().setFileDisplayMode("sess-1", "README.md", "rendered", 1);
		expect(useUiStore.getState().inspectorSessions["sess-1"]?.filesChangedOnly).toBe(false);
	});
});

describe("workspace file open requests", () => {
	beforeEach(() => {
		useUiStore.setState({ workspaceFileOpenRequest: null });
	});

	it("increments its nonce even when the same file is requested twice", () => {
		useUiStore.getState().requestWorkspaceFileOpen("session-1", "src/App.tsx", "host-a");
		const first = useUiStore.getState().workspaceFileOpenRequest;
		useUiStore.getState().requestWorkspaceFileOpen("session-1", "src/App.tsx", "host-a");
		const second = useUiStore.getState().workspaceFileOpenRequest;

		expect(first).toMatchObject({ sessionId: "session-1", hostId: "host-a", path: "src/App.tsx" });
		expect(second?.nonce).toBe((first?.nonce ?? 0) + 1);
	});

	it("clears only the matching request generation", () => {
		useUiStore.getState().requestWorkspaceFileOpen("session-1", "old.ts");
		const oldNonce = useUiStore.getState().workspaceFileOpenRequest!.nonce;
		useUiStore.getState().requestWorkspaceFileOpen("session-1", "new.ts");
		useUiStore.getState().clearWorkspaceFileOpenRequest(oldNonce);
		expect(useUiStore.getState().workspaceFileOpenRequest?.path).toBe("new.ts");

		useUiStore.getState().clearWorkspaceFileOpenRequest(oldNonce + 1);
		expect(useUiStore.getState().workspaceFileOpenRequest).toBeNull();
	});
});
