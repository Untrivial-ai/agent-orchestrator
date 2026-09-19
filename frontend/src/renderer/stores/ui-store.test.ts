import { beforeEach, describe, expect, it } from "vitest";
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

describe("onboarding handoff", () => {
	it("keeps a newer request when an older handoff finishes", () => {
		const store = useUiStore.getState();
		store.requestOnboardingFinish({ path: "/one", orchestratorAgent: "codex", workerAgent: "codex" });
		const firstNonce = useUiStore.getState().onboardingFinishRequest?.nonce;
		store.requestOnboardingFinish({ path: "/two", orchestratorAgent: "codex", workerAgent: "codex" });

		useUiStore.getState().clearOnboardingFinishRequest(firstNonce ?? -1);
		expect(useUiStore.getState().onboardingFinishRequest?.path).toBe("/two");

		const currentNonce = useUiStore.getState().onboardingFinishRequest?.nonce;
		useUiStore.getState().clearOnboardingFinishRequest(currentNonce ?? -1);
		expect(useUiStore.getState().onboardingFinishRequest).toBeNull();
	});
});
