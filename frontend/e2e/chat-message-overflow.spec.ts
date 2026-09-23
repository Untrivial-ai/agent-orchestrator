import { expect, test, type Locator } from "@playwright/test";
import { installFakeAgent } from "./support/fake-bridge";

const sessionId = "chat-message-overflow";
const now = "2026-08-18T08:05:58Z";
const longMessage =
	'LABEL OCR OUTPUT:\n{"time":"2026-08-18T13:35:58.376284251+05:30","level":"INFO","msg":"request","method":"GET","path":"/v1/foods/barcode/890619087014","request_id":"a6ff589fb4fc214a","duration":154957090}';

async function expectBubbleToStayInsideViewport(bubble: Locator) {
	await expect(bubble).toBeVisible();
	const layout = await bubble.evaluate((node) => {
		const bubbleRect = node.getBoundingClientRect();
		const viewport = node.closest(".chat-scroll-viewport");
		if (!viewport) throw new Error("chat scroll viewport not found");
		const viewportRect = viewport.getBoundingClientRect();
		return {
			bubbleRight: bubbleRect.right,
			viewportRight: viewportRect.right,
			bubbleScrollWidth: node.scrollWidth,
			bubbleClientWidth: node.clientWidth,
		};
	});

	expect(layout.bubbleRight).toBeLessThanOrEqual(layout.viewportRight);
	expect(layout.bubbleScrollWidth).toBeLessThanOrEqual(layout.bubbleClientWidth);
}

test("long unbroken user messages stay inside their chat bubble @T0", async ({ page }) => {
	await installFakeAgent(page, {
		workers: [{ id: sessionId, title: "Debug chat input", mode: "chat" }],
	});

	await page.route(`**/api/v1/sessions/${sessionId}/**`, async (route) => {
		const path = new URL(route.request().url()).pathname;
		if (path.endsWith("/conversation")) {
			await route.fulfill({
				json: {
					conversationId: "conversation-chat-message-overflow",
					sessionId,
					harness: "codex",
					mode: "chat",
					controller: "ready",
					latestSequence: 2,
					oldestSequence: 1,
					hasMoreBefore: false,
					turns: [
						{
							id: "turn-1",
							state: "completed",
							requestedAt: now,
							startedAt: now,
							completedAt: now,
						},
					],
					messages: [
						{
							kind: "message",
							id: "user-1",
							turnId: "turn-1",
							sequence: 1,
							revision: 0,
							role: "user",
							origin: "human",
							text: longMessage,
							streaming: false,
							createdAt: now,
						},
					],
					activities: [
						{
							kind: "activity",
							id: "steer-1",
							turnId: "turn-1",
							sequence: 2,
							revision: 0,
							activityKind: "system",
							status: "completed",
							summary: longMessage,
							detail: { event: "steer", text: longMessage, origin: "human" },
							createdAt: now,
						},
					],
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
		await route.fulfill({
			status: 404,
			json: { error: { code: "NOT_FOUND", message: "not found" } },
		});
	});

	await page.goto(`/#/projects/fake-proj/sessions/${sessionId}`);
	const humanBubble = page
		.locator(".cursor-chat-human-message")
		.filter({ hasText: "LABEL OCR OUTPUT" });
	const steerBubble = page
		.getByText("Steered into the running turn", { exact: true })
		.locator("..")
		.locator(":scope > div")
		.first();

	await expectBubbleToStayInsideViewport(humanBubble);
	await expectBubbleToStayInsideViewport(steerBubble);
});
