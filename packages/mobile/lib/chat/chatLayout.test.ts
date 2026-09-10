import { describe, expect, it } from "vitest";
import { centeredConversationMenu } from "./chatLayout";

describe("conversation popover layout", () => {
	it("uses equal horizontal insets instead of anchoring to one side", () => {
		expect(centeredConversationMenu.left).toBe(centeredConversationMenu.right);
		expect(centeredConversationMenu).not.toHaveProperty("width");
	});
});
