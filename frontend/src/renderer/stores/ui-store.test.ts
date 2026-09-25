import { beforeEach, describe, expect, it, vi } from "vitest";
import { sidebarIsVisible, sidebarOccupiesLayout, useUiStore } from "./ui-store";

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
