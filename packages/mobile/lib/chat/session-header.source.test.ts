import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

function source(relativePath: string): string {
	return readFileSync(fileURLToPath(new URL(relativePath, import.meta.url)), "utf8");
}

// The conversation-actions control used to be a bare Pressable next to a glass
// back button. It read as a different, brighter control in the same row — the
// reason this is pinned is that "the header looks unlike the home page" is a
// regression you only catch on device.
describe("session header controls", () => {
	const session = source("./ChatSessionScreen.tsx");

	it("renders conversation actions through the shared glass header button", () => {
		expect(session).toContain('import { NativeHeaderButton } from "../native-header-button";');
		expect(session).toContain('icon="more" label="Conversation actions"');
	});

	it("leaves no raw control inside headerRight", () => {
		const start = session.indexOf("headerRight: () => (");
		expect(start).toBeGreaterThan(-1);
		expect(session.slice(start, start + 400)).not.toContain("Pressable");
	});

	it("gives every platform an icon for the more control", () => {
		expect(source("../native-header-button.tsx")).toContain('"more"');
		expect(source("../native-header-button.ios.tsx")).toContain('icon === "more"');
		expect(source("../native-header-button.android.tsx")).toContain('more: "more-horizontal"');
	});
});
