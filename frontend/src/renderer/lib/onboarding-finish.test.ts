import { beforeEach, describe, expect, it, vi } from "vitest";
import { hasCompletedOnboarding, markOnboardingComplete, runOnboardingFinish } from "./onboarding-finish";
import type { OnboardingFinishRequest } from "../stores/ui-store";

const request: OnboardingFinishRequest = {
	nonce: 1,
	orchestratorAgent: "claude-code",
	path: "/tmp/acme/project",
	workerAgent: "codex",
};

beforeEach(() => {
	window.localStorage.clear();
});

describe("runOnboardingFinish", () => {
	it("marks onboarding complete only after the project is registered", async () => {
		const createProject = vi.fn().mockResolvedValue(undefined);
		const outcome = await runOnboardingFinish(request, {
			createProject,
		});

		expect(outcome).toEqual({ ok: true });
		expect(createProject).toHaveBeenCalledWith({
			asWorkspace: undefined,
			clonePreparationId: undefined,
			orchestratorAgent: "claude-code",
			path: "/tmp/acme/project",
			workerAgent: "codex",
		});
		expect(hasCompletedOnboarding()).toBe(true);
	});

	it("leaves onboarding incomplete and reports the reason when the project fails", async () => {
		const outcome = await runOnboardingFinish(request, {
			createProject: vi.fn().mockRejectedValue(new Error("clone failed: repository not found")),
		});

		expect(outcome).toEqual({ message: "clone failed: repository not found", ok: false });
		expect(hasCompletedOnboarding()).toBe(false);
	});

	it("survives storage being unavailable", () => {
		const setItem = vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
			throw new Error("quota exceeded");
		});
		expect(() => markOnboardingComplete()).not.toThrow();
		setItem.mockRestore();
	});
});
