import { describe, expect, it } from "vitest";
import {
	UPDATE_CHECK_TIMEOUT_MS,
	UPDATE_STATE_KIND,
	UPDATE_STATE_RECONCILES,
	UPDATE_STATE_RESOLVES_CHANNEL_SWITCH,
	isReportedFailure,
	isUpdateInProgress,
	resolvesChannelSwitch,
	shouldReconcileUpdateStatus,
	type UpdateState,
} from "./update-settings";

// These Records exist because six guards used to spell out their own subset of
// UpdateState and every one of them silently excluded a newly added member. The
// Record makes omission a build error; these tests make a WRONG entry a test
// failure. Without them, flipping any single value still passes the whole suite,
// including the channel-switch entry whose misclassification already shipped
// once and cleared the switch banner on a build that was ready to install.
describe("update state classification", () => {
	const states = Object.keys(UPDATE_STATE_KIND) as UpdateState[];

	it("classifies exactly the states that exist, with no extras", () => {
		expect(states.sort()).toEqual(
			[
				"available",
				"checking",
				"downloaded",
				"downloading",
				"error",
				"idle",
				"not-available",
				"preparing",
				"retry-scheduled",
				"unsupported",
			].sort(),
		);
		expect(Object.keys(UPDATE_STATE_RECONCILES).sort()).toEqual(states.sort());
		expect(Object.keys(UPDATE_STATE_RESOLVES_CHANNEL_SWITCH).sort()).toEqual(states.sort());
	});

	it("treats both failure states, and only those, as already reported", () => {
		const failures = states.filter(isReportedFailure);
		expect(failures.sort()).toEqual(["error", "retry-scheduled"]);
	});

	it("treats only live operations as in progress", () => {
		expect(states.filter(isUpdateInProgress).sort()).toEqual(["checking", "downloading", "preparing"]);
	});

	it("keeps polling in every state where the status can still move", () => {
		expect(states.filter((s) => !shouldReconcileUpdateStatus(s)).sort()).toEqual([
			"idle",
			"not-available",
			"unsupported",
		]);
	});

	it("retires a channel switch only when nothing is left to install", () => {
		// The trap: "settled" is not the same as "resolved". available and
		// downloaded are settled, but they mean the switch FOUND something, so the
		// "update and restart to switch" prompt has to stay up.
		expect(resolvesChannelSwitch("available")).toBe(false);
		expect(resolvesChannelSwitch("downloaded")).toBe(false);
		expect(states.filter(resolvesChannelSwitch).sort()).toEqual([
			"error",
			"not-available",
			"retry-scheduled",
			"unsupported",
		]);
	});

	it("gives the feed long enough that a renderer watchdog can outlast it", () => {
		expect(UPDATE_CHECK_TIMEOUT_MS).toBeGreaterThan(60_000);
	});
});
