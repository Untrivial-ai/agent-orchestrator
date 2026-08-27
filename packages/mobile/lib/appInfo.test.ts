import { describe, expect, it } from "vitest";
import { bugReportBody, formatUpdate, formatVersion, formatVersionLine } from "./appInfo";

describe("formatVersion", () => {
	it("combines version and build", () => {
		expect(formatVersion({ version: "1.2.0", build: "42" })).toBe("1.2.0 (42)");
	});

	it("omits a build number that only repeats the version", () => {
		// Expo Go reports both as the same string; "1.2.0 (1.2.0)" is noise.
		expect(formatVersion({ version: "1.2.0", build: "1.2.0" })).toBe("1.2.0");
	});

	it("omits a missing or blank build number", () => {
		expect(formatVersion({ version: "1.2.0" })).toBe("1.2.0");
		expect(formatVersion({ version: "1.2.0", build: "  " })).toBe("1.2.0");
	});

	it("falls back rather than rendering an empty row", () => {
		expect(formatVersion({})).toBe("unknown");
		expect(formatVersion({ build: "42" })).toBe("build 42");
	});
});

describe("formatUpdate", () => {
	const ota = { updateId: "a1b2c3d4-0000-4000-8000-000000000000", channel: "production", embedded: false };

	it("names the running OTA update and its channel", () => {
		expect(formatUpdate(ota)).toBe("a1b2c3d4 on production");
		expect(formatUpdate({ ...ota, channel: null })).toBe("a1b2c3d4");
	});

	it("says embedded when the build's own bundle is running", () => {
		expect(formatUpdate({ ...ota, embedded: true })).toBe("embedded");
	});

	it("is null where updates are off (dev builds)", () => {
		expect(formatUpdate({ version: "1.2.0" })).toBeNull();
	});
});

describe("formatVersionLine", () => {
	it("appends the short update id only for an OTA bundle", () => {
		const base = { version: "1.2.0", build: "42" };
		expect(formatVersionLine(base)).toBe("1.2.0 (42)");
		expect(formatVersionLine({ ...base, embedded: true, updateId: "a1b2c3d4-0000" })).toBe("1.2.0 (42)");
		expect(formatVersionLine({ ...base, embedded: false, updateId: "a1b2c3d4-0000" })).toBe("1.2.0 (42) · a1b2c3d4");
	});
});

describe("bugReportBody", () => {
	it("names the build and platform so a report is actionable", () => {
		const body = bugReportBody({ version: "1.2.0", build: "42" }, "ios", "18.2");
		expect(body).toContain("AO mobile: 1.2.0 (42)");
		expect(body).toContain("Platform: ios 18.2");
		expect(body).not.toContain("Update:");
	});

	it("includes the OTA update and runtime", () => {
		const body = bugReportBody(
			{ version: "1.2.0", build: "42", updateId: "a1b2c3d4-0000", channel: "preview", runtimeVersion: "d7e82fd0d167", embedded: false },
			"android",
			34,
		);
		expect(body).toContain("Update: a1b2c3d4 on preview (runtime d7e82fd0)");
	});

	it("leaves room above the metadata for the user to type", () => {
		expect(bugReportBody({ version: "1.0.0" }, "android", 34).startsWith("\n\n")).toBe(true);
	});
});
