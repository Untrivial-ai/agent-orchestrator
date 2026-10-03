import { describe, expect, it } from "vitest";
import { describeDesktopStatus } from "./desktopStatus";

describe("describeDesktopStatus", () => {
	it("offers setup when nothing is paired, whatever the poll says", () => {
		expect(describeDesktopStatus({ configured: false, connection: "closed", failure: "unreachable" })).toEqual({
			label: "Set up",
			tone: "neutral",
		});
	});

	it("reports a live connection", () => {
		expect(describeDesktopStatus({ configured: true, connection: "open", failure: null })).toEqual({
			label: "Connected",
			tone: "ok",
		});
	});

	it("reports an attempt in progress", () => {
		expect(describeDesktopStatus({ configured: true, connection: "connecting", failure: null }).label).toBe("Connecting…");
	});

	// The reported bug: a desktop that stopped answering still read "Paired".
	it("never calls an unreachable desktop paired", () => {
		expect(describeDesktopStatus({ configured: true, connection: "closed", failure: "unreachable" })).toEqual({
			label: "Unreachable",
			tone: "error",
		});
	});

	// Before the first poll, errorStatus is null exactly as it is after an
	// unreachable one; nothing has failed yet, so nothing is red.
	it("does not report a failure before any poll has failed", () => {
		expect(describeDesktopStatus({ configured: true, connection: "closed", failure: null })).toEqual({
			label: "Connecting…",
			tone: "neutral",
		});
	});

	it("names the cause when the machine answered with a rejection", () => {
		const label = (failure: Parameters<typeof describeDesktopStatus>[0]["failure"]) =>
			describeDesktopStatus({ configured: true, connection: "closed", failure }).label;
		expect(label("auth")).toBe("Password rejected");
		expect(label("rate-limited")).toBe("Locked out");
		expect(label("server-error")).toBe("Machine error");
		expect(label("tunnel-rotated")).toBe("Address changed");
	});
});
