import { expect, test } from "@playwright/test";
import { installFakeAgent } from "./support/fake-bridge";

const sessionId = "chat-scroll";
const now = "2026-08-08T08:00:00Z";

const turns = Array.from({ length: 3 }, (_, index) => ({
	id: `turn-${index}`,
	state: "completed",
	requestedAt: now,
	startedAt: now,
	completedAt: now,
}));

const messages = turns.flatMap((turn, index) => [
	{
		kind: "message",
		id: `user-${index}`,
		turnId: turn.id,
		sequence: index * 2 + 1,
		revision: 0,
		role: "user",
		origin: "human",
		text: `Investigate the chat scroll marker for turn ${index + 1}.`,
		streaming: false,
		createdAt: now,
	},
	{
		kind: "message",
		id: `assistant-${index}`,
		turnId: turn.id,
		sequence: index * 2 + 2,
		revision: 0,
		role: "assistant",
		origin: "provider",
		text: "The conversation needs enough durable history to make its minimap interactive. ".repeat(4),
		streaming: false,
		createdAt: now,
	},
]);

test("chat minimap does not make the session pane scroll horizontally", async ({ page }) => {
	await installFakeAgent(page, {
		workers: [{ id: sessionId, title: "Debug chat scroll", mode: "chat" }],
	});

	await page.route(`**/api/v1/sessions/${sessionId}/**`, async (route) => {
		const path = new URL(route.request().url()).pathname;
		if (path.endsWith("/conversation")) {
			await route.fulfill({
				json: {
					conversationId: "conversation-chat-scroll",
					sessionId,
					harness: "codex",
					mode: "chat",
					controller: "ready",
					latestSequence: messages.length,
					oldestSequence: 1,
					hasMoreBefore: false,
					turns,
					messages,
					activities: [],
					settings: {},
				},
			});
			return;
		}
		if (path.endsWith("/conversation/models")) {
			await route.fulfill({ json: { models: [], selected: {} } });
			return;
		}
		if (path.endsWith("/conversation/skills")) {
			await route.fulfill({ json: { skills: [] } });
			return;
		}
		if (path.endsWith("/workspace/files")) {
			await route.fulfill({ json: { files: [], truncated: false } });
			return;
		}
		if (path.endsWith("/interface-transition")) {
			await route.fulfill({ json: { supported: true, targetMode: "tui" } });
			return;
		}
		await route.fulfill({ status: 404, json: { error: { code: "NOT_FOUND", message: "not found" } } });
	});

	await page.goto(`/#/projects/fake-proj/sessions/${sessionId}`);
	await expect(page.getByRole("log", { name: "Conversation" })).toBeVisible();

	// Minimap markers only render when the inspector rail is closed.
	await page.getByRole("button", { name: "Close inspector panel" }).click();

	const marker = page.locator("[data-chat-scroll-marker]").first();
	await expect(marker).toBeVisible();
	await marker.hover();
	const activeMarker = marker.locator(".chat-scroll-marker");
	await expect(activeMarker).toHaveClass(/chat-scroll-marker-active/);
	await expect.poll(() => activeMarker.evaluate((node) => node.getBoundingClientRect().width))
		.toBeGreaterThan(27.5);

	const pane = await page.locator("#terminal > div").evaluate((node) => ({
		overflow: node.scrollWidth - node.clientWidth,
		overflowX: getComputedStyle(node).overflowX,
	}));
	// Keep the assertion red-capable: the marker still crosses the pane edge, but
	// the pane must clip that visual flourish instead of offering a native scrollbar.
	expect(pane.overflow).toBeGreaterThan(0);
	expect(pane.overflowX).toBe("hidden");
});
