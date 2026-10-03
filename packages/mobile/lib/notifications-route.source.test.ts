import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

const source = readFileSync(fileURLToPath(new URL("../app/notifications.tsx", import.meta.url)), "utf8");

describe("notifications host routing", () => {
	it("clears only the item loaded from the active host", () => {
		expect(source).toContain("itemsHostId !== config.hostId || clearingIds.has(notification.id)");
		expect(source).toContain("await clearNotification(source, notification.id)");
		expect(source).toContain("if (currentConfig.current !== source) return");
	});
});
