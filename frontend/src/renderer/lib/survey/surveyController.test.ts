import { beforeEach, describe, expect, it, vi } from "vitest";

import { type Capture, INVITE_SNOOZE_MS, SurveyController, type Storage } from "./surveyController";

function memoryStorage(): Storage {
	const map = new Map<string, string>();
	return { getItem: (k) => map.get(k) ?? null, setItem: (k, v) => void map.set(k, v) };
}

describe("SurveyController.answer", () => {
	it("emits a single-choice answer event", () => {
		const capture = vi.fn();
		new SurveyController({ storage: memoryStorage(), capture }).answer("profile", "Developer");
		expect(capture).toHaveBeenCalledWith("ao.renderer.survey_answered", { survey: "profile", choice: "Developer" });
	});

	it("joins a multi-select answer and includes the raw list", () => {
		const capture = vi.fn();
		new SurveyController({ storage: memoryStorage(), capture }).answer("task-type", ["Bug fix", "Tests"]);
		expect(capture).toHaveBeenCalledWith("ao.renderer.survey_answered", {
			survey: "task-type",
			choice: "Bug fix, Tests",
			choices: ["Bug fix", "Tests"],
		});
	});
});

describe("SurveyController.markCompleted", () => {
	it("emits every answer as answer_<id> in one event", () => {
		const capture = vi.fn();
		const c = new SurveyController({ storage: memoryStorage(), capture });
		c.answer("profile", "Founder");
		c.answer("task-type", ["Bug fix", "Refactor"]);
		c.answer("wish", "auto-open a PR");
		c.markCompleted();
		expect(capture).toHaveBeenLastCalledWith("ao.renderer.survey_completed", {
			answer_profile: "Founder",
			answer_task_type: "Bug fix, Refactor",
			answer_wish: "auto-open a PR",
		});
	});
});

describe("SurveyController robustness", () => {
	it("still emits a whole-response completed event when storage writes are blocked", () => {
		const capture = vi.fn();
		const blocked: Storage = {
			getItem: () => null,
			setItem: () => {
				throw new Error("blocked");
			},
		};
		const c = new SurveyController({ storage: blocked, capture });
		c.answer("profile", "Founder");
		c.answer("wish", "auto-open a PR");
		c.markCompleted();
		expect(capture).toHaveBeenLastCalledWith("ao.renderer.survey_completed", {
			answer_profile: "Founder",
			answer_wish: "auto-open a PR",
		});
	});

	it("does not share answers between instances when stored state omits the answers key", () => {
		// A blob with no `answers` field must not alias a shared object; one
		// instance's answer must never leak into another's completed event.
		const keyless = JSON.stringify({ completed: false });
		const freshStorage = (): Storage => {
			const m = new Map<string, string>([["ao.survey.state.v1", keyless]]);
			return { getItem: (k) => m.get(k) ?? null, setItem: (k, v) => void m.set(k, v) };
		};
		new SurveyController({ storage: freshStorage(), capture: vi.fn() }).answer("profile", "Developer");
		const capture = vi.fn();
		new SurveyController({ storage: freshStorage(), capture }).markCompleted();
		expect(capture).toHaveBeenLastCalledWith("ao.renderer.survey_completed", {});
	});
});

describe("SurveyController.invite eligibility", () => {
	let storage: Storage;
	let capture: ReturnType<typeof vi.fn<Capture>>;
	let t: number;
	const make = () => new SurveyController({ storage, now: () => t, capture });
	beforeEach(() => {
		storage = memoryStorage();
		capture = vi.fn<Capture>();
		t = 1_000_000_000_000;
	});

	it("is eligible by default", () => {
		expect(make().inviteEligible()).toBe(true);
	});

	it("is never eligible once completed", () => {
		const c = make();
		c.markCompleted();
		t += 10 * INVITE_SNOOZE_MS;
		expect(c.inviteEligible()).toBe(false);
	});

	it("hushes for 48h after the invite is crossed, then returns", () => {
		const c = make();
		c.dismissInvite();
		expect(capture).toHaveBeenCalledWith("ao.renderer.survey_invite_dismissed", {});
		t += INVITE_SNOOZE_MS - 1;
		expect(c.inviteEligible()).toBe(false);
		t += 2;
		expect(c.inviteEligible()).toBe(true);
	});

	it("never returns after the user opts out", () => {
		const c = make();
		c.optOut();
		expect(capture).toHaveBeenCalledWith("ao.renderer.survey_invite_opted_out", {});
		t += 10 * INVITE_SNOOZE_MS;
		expect(c.inviteEligible()).toBe(false);
	});
});
