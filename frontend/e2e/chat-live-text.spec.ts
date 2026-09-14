import { expect, test } from "@playwright/test";
import { installFakeAgent } from "./support/fake-bridge";

test("@P0 chat text remains visible while saved-history requests wait", async ({ page }) => {
	const sessionId = "live-text";
	await installFakeAgent(page, { workers: [{ id: sessionId, title: "Live text before saving", mode: "chat" }] });
	await page.addInitScript(() => {
		const add = EventSource.prototype.addEventListener;
		EventSource.prototype.addEventListener = function (type: string, callback: ((event: MessageEvent) => void) | EventListenerOrEventListenerObject, options?: boolean | AddEventListenerOptions) {
			if (type === "conversation_text") {
				(window as unknown as { requestSavedText: () => void }).requestSavedText = () => this.onopen?.call(this, new Event("open"));
				(window as unknown as { sendLiveText: (frame: unknown) => void }).sendLiveText = (frame) => {
					const event = new MessageEvent(type, { data: JSON.stringify(frame) });
					if (typeof callback === "function") callback.call(this, event);
					else callback?.handleEvent(event);
				};
			}
			return add.call(this, type, callback as EventListenerOrEventListenerObject, options);
		};
	});
	let saved = false;
	let hold = false;
	let release!: () => void;
	let requestBlocked!: () => void;
	const blocked = new Promise<void>((resolve) => { requestBlocked = resolve; });
	const waiting = new Promise<void>((resolve) => { release = resolve; });
	const now = "2026-09-10T00:00:00Z";
	await page.route(`**/api/v1/sessions/${sessionId}/**`, async (route) => {
		if (new URL(route.request().url()).pathname.endsWith("/conversation")) {
			if (hold) { requestBlocked(); await waiting; }
			await route.fulfill({ json: {
				conversationId: "conversation", sessionId, harness: "codex", mode: "chat", controller: "busy",
				activeBranchId: "branch", liveGeneration: "generation", liveSequence: saved ? 2 : 0,
				latestSequence: saved ? 2 : 1, oldestSequence: 1, hasMoreBefore: false, settings: {}, activities: [],
				turns: [{ id: "turn", providerTurnId: "provider-turn", state: "running", requestedAt: now }],
				messages: [
					{ kind: "message", id: "prompt", turnId: "turn", sequence: 1, revision: 1, role: "user", origin: "human", text: "Show the reply as it arrives.", streaming: false, createdAt: now },
					...(saved ? [{ kind: "message", id: "saved-reply", providerItemId: "reply", turnId: "turn", sequence: 2, revision: 1, role: "assistant", origin: "provider", text: "This text arrived before saving finished.", streaming: true, createdAt: now }] : []),
				],
			} });
		} else await route.fulfill({ json: {} });
	});
	await page.goto(`/#/projects/fake-proj/sessions/${sessionId}`);
	const log = page.getByRole("log", { name: "Conversation" });
	await expect(log.getByText("Show the reply as it arrives.")).toBeVisible({ timeout: 15_000 });
	hold = true;
	await page.evaluate(() => (window as unknown as { requestSavedText: () => void }).requestSavedText());
	await blocked;
	await page.evaluate(() => {
		(window as unknown as { sendLiveText: (frame: unknown) => void }).sendLiveText({
			generation: "generation", conversationId: "conversation", branchId: "branch", afterSequence: 0, resetSequence: 0, sequence: 2,
			events: ["This text arrived ", "before saving finished."].map((delta, index) => ({
				sequence: index + 1, kind: "message.delta", providerItemId: "reply", providerTurnId: "provider-turn", delta, createdAt: "2026-09-10T00:00:01Z",
			})),
		});
	});
	await expect(log.getByText("This text arrived before saving finished.", { exact: true })).toBeVisible();
	saved = true;
	release();
	await page.evaluate((session) => window.__aoFakeAgent!.setStatus(session, "working"), sessionId);
	await expect(log.getByText("This text arrived before saving finished.", { exact: true })).toHaveCount(1);
});
