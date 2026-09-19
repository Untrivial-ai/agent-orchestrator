import { describe, expect, it } from "vitest";
import { controllerStoppedBanner, errorBanner, mcpBanner, quotaBanner, reauthBanner, rolledBackBanner, threadBanner } from "./conversationBanners";

describe("conversation banners", () => {
	it("headlines tool servers by count and keeps the raw error out", () => {
		const copy = mcpBanner([
			{ name: "canva", status: "failed", error: "The canva MCP server is not logged in. Run `codex mcp login canva`." },
			{ name: "posthog", status: "failed", failureReason: "reauthenticationRequired", error: "long text" },
		]);
		expect(copy?.title).toBe("2 tool servers did not start");
		expect(copy?.body).toBe("canva, posthog · reauthenticationRequired. The agent works around them silently.");
		expect(copy?.body).not.toContain("codex mcp login");
		expect(mcpBanner([{ name: "canva", status: "failed" }])?.title).toBe("A tool server did not start");
		expect(mcpBanner([])).toBeUndefined();
	});

	it("brings a closed banner back only when what it reports changes", () => {
		const one = mcpBanner([{ name: "canva", status: "failed" }]);
		const same = mcpBanner([{ name: "canva", status: "failed" }]);
		const more = mcpBanner([{ name: "canva", status: "failed" }, { name: "posthog", status: "failed" }]);
		expect(one?.key).toBe(same?.key);
		expect(one?.key).not.toBe(more?.key);
		expect(quotaBanner({ percent: 80, severity: "warn" }).key).toBe(quotaBanner({ percent: 84, severity: "warn" }).key);
		expect(quotaBanner({ percent: 80, severity: "warn" }).key).not.toBe(quotaBanner({ percent: 92, severity: "critical" }).key);
	});

	it("uses the desktop's short headlines", () => {
		expect(reauthBanner("t", "codex login")).toMatchObject({ title: "Sign in again to keep going", body: "Run “codex login” on the AO host, then send again." });
		expect(threadBanner("system_error")?.title).toBe("The agent's thread hit an internal error");
		expect(threadBanner("closed")?.title).toBe("The agent closed this thread");
		expect(threadBanner("active")).toBeUndefined();
		expect(controllerStoppedBanner(true).title).toBe("This session is terminated");
		expect(quotaBanner({ percent: 91, severity: "critical", resetsInSeconds: 7200 })).toMatchObject({ title: "91% of quota used", body: "Resets in 2h." });
		expect(rolledBackBanner(2)?.title).toBe("2 turns were rolled back");
		expect(rolledBackBanner(0)).toBeUndefined();
		expect(errorBanner("action", "boom")).toMatchObject({ title: "That didn't work", body: "boom" });
	});
});
