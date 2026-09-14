import { expect, test, type Page } from "@playwright/test";
import { installFakeAgent } from "./support/fake-bridge";

const sessionId = "chat-queued-controls";
const now = "2026-08-26T09:00:00Z";

type TurnState = "running" | "queued" | "completed" | "interrupted";

function turn(id: string, state: TurnState) {
	return {
		id,
		state,
		requestedAt: now,
		...(state === "running"
			? { providerTurnId: "provider-running", startedAt: now }
			: {}),
	};
}

function message(id: string, turnId: string, sequence: number, text: string) {
	return {
		id,
		turnId,
		sequence,
		revision: 0,
		role: "user",
		origin: "human",
		text,
		streaming: false,
		createdAt: now,
	};
}

async function installQueuedConversation(page: Page) {
	const turns = [
		turn("turn-running", "running"),
		turn("turn-queued-one", "queued"),
		turn("turn-queued-two", "queued"),
	];
	const messages = [
		message("message-running", "turn-running", 1, "Active work"),
		message(
			"message-queued-one",
			"turn-queued-one",
			2,
			"First queued follow-up",
		),
		message(
			"message-queued-two",
			"turn-queued-two",
			3,
			"Second queued follow-up",
		),
	];
	const retiredTurnIds = new Set<string>();
	const requests: string[] = [];
	const interruptBodies: string[][] = [];
	let rejectNextInterrupt = false;
	await installFakeAgent(page, {
		workers: [
			{
				id: sessionId,
				title: "Queued controls",
				mode: "chat",
				status: "working",
				activity: "active",
			},
		],
	});
	await page.route(`**/api/v1/sessions/${sessionId}/**`, async (route) => {
		const path = new URL(route.request().url()).pathname;
		if (path.endsWith("/conversation") && route.request().method() === "GET") {
			await route.fulfill({
				json: {
					conversationId: "conversation-queued-controls",
					sessionId,
					harness: "codex",
					mode: "chat",
					controller: "busy",
					capabilities: ["interrupt", "steer"],
					latestSequence: 3,
					oldestSequence: 1,
					hasMoreBefore: false,
					turns: turns.filter((entry) => !retiredTurnIds.has(entry.id)),
					queuedTurns: [
						{ turnId: "turn-queued-one", text: "First queued follow-up", origin: "human" },
						{ turnId: "turn-queued-two", text: "Second queued follow-up", origin: "human" },
					].filter((entry) => !retiredTurnIds.has(entry.turnId)),
					messages: messages.filter(
						(entry) => !retiredTurnIds.has(entry.turnId),
					),
					activities: [],
					settings: {},
				},
			});
			return;
		}
		if (
			path.endsWith("/turn-queued-one/cancel") &&
			route.request().method() === "POST"
		) {
			requests.push(path);
			retiredTurnIds.add("turn-queued-one");
			await route.fulfill({ status: 204 });
			return;
		}
		if (
			path.endsWith("/turn-queued-two/steer") &&
			route.request().method() === "POST"
		) {
			requests.push(path);
			retiredTurnIds.add("turn-queued-two");
			await route.fulfill({
				status: 202,
				json: {
					sourceTurnId: "turn-queued-two",
					providerTurnId: "provider-running",
					activityId: "activity-promoted",
				},
			});
			return;
		}
		if (
			path.endsWith("/conversation/interrupt") &&
			route.request().method() === "POST"
		) {
			requests.push(path);
			const body = route.request().postDataJSON() as { queuedTurnIds?: string[] };
			interruptBodies.push(body.queuedTurnIds ?? []);
			if (rejectNextInterrupt) {
				rejectNextInterrupt = false;
				await route.fulfill({
					status: 409,
					json: {
						error: "conflict",
						code: "CHAT_QUEUE_SCOPE_CHANGED",
						message: "the queued work changed; review the refreshed queue and confirm Stop again",
					},
				});
				return;
			}
			await route.fulfill({ status: 204 });
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
	await expect(page.getByTestId("queued-message-dock")).toBeVisible();
	return {
		requests,
		interruptBodies,
		rejectNextInterrupt: () => {
			rejectNextInterrupt = true;
		},
	};
}

test("queued turns can be cancelled or promoted independently and Stop confirms its scope @T0", async ({
	page,
}) => {
	const fixture = await installQueuedConversation(page);
	const evidenceDirectory = process.env.AO_QUEUE_EVIDENCE_DIR;
	if (evidenceDirectory) {
		await page.screenshot({
			path: `${evidenceDirectory}/queued-controls-visible.png`,
			fullPage: true,
		});
	}

	const stop = page.getByRole("button", {
		name: "Stop turn and cancel 2 queued messages",
	});
	await expect(stop).toHaveAccessibleDescription(
		/also cancels 2 queued messages/i,
	);
	await stop.click();
	const stopDialog = page.getByRole("dialog", {
		name: "Stop turn and cancel 2 queued messages?",
	});
	await expect(stopDialog).toContainText(
		"The active turn and both queued messages will be stopped",
	);
	if (evidenceDirectory) {
		await page.screenshot({
			path: `${evidenceDirectory}/queued-controls-stop-confirmation.png`,
			fullPage: true,
		});
	}
	await stopDialog.getByRole("button", { name: "Cancel" }).click();

	const queueToggle = page.getByTestId("queued-message-toggle");
	if ((await queueToggle.getAttribute("aria-expanded")) === "false") await queueToggle.click();
	const firstQueued = page.getByTestId("queued-message-turn-queued-one");
	await firstQueued.hover();
	await firstQueued.getByRole("button", { name: "Delete queued message", exact: true }).click();
	await expect(page.getByTestId("queued-message-turn-queued-one")).toHaveCount(
		0,
	);
	await expect(
		page.getByText("First queued follow-up", { exact: true }),
	).toHaveCount(0);
	await expect(page.getByText("Active work", { exact: true })).toBeVisible();
	await expect(
		page
			.getByTestId("queued-message-turn-queued-two")
			.getByText("Second queued follow-up", { exact: true }),
	).toBeVisible();

	const secondQueued = page.getByTestId("queued-message-turn-queued-two");
	await secondQueued.hover();
	await secondQueued.getByRole("button", { name: "Steer this queued message into the running turn", exact: true }).click();
	await expect(page.getByTestId("queued-message-dock")).toHaveCount(0);
	await expect(page.getByText("Active work", { exact: true })).toBeVisible();
	expect(fixture.requests).toEqual([
		`/api/v1/sessions/${sessionId}/conversation/turns/turn-queued-one/cancel`,
		`/api/v1/sessions/${sessionId}/conversation/turns/turn-queued-two/steer`,
	]);

	if (evidenceDirectory) {
		await page.screenshot({
			path: `${evidenceDirectory}/queued-controls-after-actions.png`,
			fullPage: true,
		});
	}
});

test("Stop sends the exact queue scope and requires reconfirmation after a conflict @T0", async ({
	page,
}) => {
	const fixture = await installQueuedConversation(page);
	fixture.rejectNextInterrupt();

	await page
		.getByRole("button", { name: "Stop turn and cancel 2 queued messages" })
		.click();
	let dialog = page.getByRole("dialog", {
		name: "Stop turn and cancel 2 queued messages?",
	});
	await dialog.getByRole("button", { name: "Stop all" }).click();

	await expect(
		page.getByText(
			"Queued work changed while Stop was awaiting confirmation. Review the refreshed queue and press Stop again.",
		),
	).toBeVisible();
	await expect(page.getByTestId("queued-message-turn-queued-one")).toBeVisible();
	await expect(page.getByTestId("queued-message-turn-queued-two")).toBeVisible();
	const evidenceDirectory = process.env.AO_QUEUE_EVIDENCE_DIR;
	if (evidenceDirectory) {
		await page.screenshot({
			path: `${evidenceDirectory}/queued-controls-scope-conflict.png`,
			fullPage: true,
		});
	}
	expect(fixture.interruptBodies).toEqual([
		["turn-queued-one", "turn-queued-two"],
	]);

	await page
		.getByRole("button", { name: "Stop turn and cancel 2 queued messages" })
		.click();
	dialog = page.getByRole("dialog", {
		name: "Stop turn and cancel 2 queued messages?",
	});
	const secondInterrupt = page.waitForResponse(
		(response) =>
			new URL(response.url()).pathname.endsWith("/conversation/interrupt") &&
			response.request().method() === "POST",
	);
	await dialog.getByRole("button", { name: "Stop all" }).click();
	await secondInterrupt;
	await expect(dialog).not.toBeVisible();
	expect(fixture.interruptBodies).toEqual([
		["turn-queued-one", "turn-queued-two"],
		["turn-queued-one", "turn-queued-two"],
	]);
});
