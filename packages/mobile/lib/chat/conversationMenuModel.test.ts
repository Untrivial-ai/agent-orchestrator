import { describe, expect, it } from "vitest";
import { conversationMenuSections, normalizeConversationTitle } from "./conversationMenuModel";

describe("conversation overflow menu", () => {
	it("groups every supported session action by user intent", () => {
		const sections = conversationMenuSections({
			canRename: true,
			canPin: true,
			canCompact: true,
			canReloadMcp: true,
			canDelete: true,
		});

		expect(sections).toEqual([
			{ title: "Workspace", actions: ["shell", "preview", "pull_requests"] },
			{ title: "Conversation", actions: ["map", "refresh", "settings", "rename", "pin", "compact"] },
			{ title: "Agent", actions: ["terminal_ui", "reload_mcp"] },
			// Last, and alone: the only entry here that destroys something.
			{ title: "Session", actions: ["delete"] },
		]);
	});

	it("omits capability-gated maintenance actions without disturbing the core menu", () => {
		const sections = conversationMenuSections({
			canRename: false,
			canPin: false,
			canCompact: false,
			canReloadMcp: false,
			canDelete: false,
		});

		expect(sections[1]?.actions).toEqual(["map", "refresh", "settings"]);
		expect(sections[2]?.actions).toEqual(["terminal_ui"]);
		// An orchestrator has no delete here, so the section goes rather than
		// rendering an empty group.
		expect(sections.some((section) => section.title === "Session")).toBe(false);
	});
});

describe("conversation rename", () => {
	it("trims a useful title and rejects an empty one", () => {
		expect(normalizeConversationTitle("  Release follow-up  ")).toBe("Release follow-up");
		expect(normalizeConversationTitle("   ")).toBeUndefined();
	});
});
