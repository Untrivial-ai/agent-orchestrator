import { describe, expect, it } from "vitest";
import { shouldShowLoading } from "./configLoading";

describe("whether to show the loader while the config settles", () => {
	// The bug this exists for. `config` starts null and stays null until the
	// endpoint race finishes. The poll effect treated "no config yet" as "not
	// configured" and turned the loader off, so the screen rendered an empty
	// list — a black screen on a dark theme — for the whole race. That window
	// used to be a ~10ms storage read; racing the endpoints made it seconds.
	it("keeps the loader up while the config is still being resolved", () => {
		expect(shouldShowLoading({ resolved: false, configured: false })).toBe(true);
	});

	// Once resolution has finished with nothing to connect to, the loader must
	// give way to the connect prompt rather than spinning forever.
	it("drops the loader once resolution finishes with no machine paired", () => {
		expect(shouldShowLoading({ resolved: true, configured: false })).toBe(false);
	});

	// The poll is about to start and will clear this itself on its first tick.
	it("shows the loader when a machine is configured", () => {
		expect(shouldShowLoading({ resolved: true, configured: true })).toBe(true);
	});

	// A re-race while already connected keeps the existing config, so this state
	// means "we have somewhere to talk to" — not a reason to blank the screen.
	it("shows the loader when a config exists but a race is still running", () => {
		expect(shouldShowLoading({ resolved: false, configured: true })).toBe(true);
	});
});
