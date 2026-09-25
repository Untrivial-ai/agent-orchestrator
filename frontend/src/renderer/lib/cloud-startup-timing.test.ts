import { describe, expect, it } from "vitest";
import {
	beginCloudStartupAttempt,
	bindCloudStartupAttempt,
	completeCloudStartupAttempt,
} from "./cloud-startup-timing";

describe("cloud startup timing", () => {
	it("measures from the user action and consumes the attempt once", () => {
		const attempt = beginCloudStartupAttempt("attempt-1", 1_000);
		bindCloudStartupAttempt("session-1", attempt, 1_050);

		expect(completeCloudStartupAttempt("session-1", 2_234)).toEqual({
			attemptId: "attempt-1",
			elapsedMs: 1_234,
		});
		expect(completeCloudStartupAttempt("session-1", 2_500)).toBeUndefined();
	});

	it("does not report an expired attempt", () => {
		const attempt = beginCloudStartupAttempt("attempt-expired", 0);
		bindCloudStartupAttempt("session-expired", attempt, 1);

		expect(completeCloudStartupAttempt("session-expired", 30 * 60_000 + 1)).toBeUndefined();
	});

	it("clamps a backward monotonic clock to zero", () => {
		const attempt = beginCloudStartupAttempt("attempt-clock", 500);
		bindCloudStartupAttempt("session-clock", attempt, 500);

		expect(completeCloudStartupAttempt("session-clock", 400)?.elapsedMs).toBe(0);
	});
});
