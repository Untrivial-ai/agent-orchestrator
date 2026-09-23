import { expect, test } from "@playwright/test";
import { installFakeAgent } from "./support/fake-bridge";

const sessionId = "queued-attachments";
const path = ".ao/attachments/attachment-queue.png";
const png = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jRZkAAAAASUVORK5CYII=";

test("queued image edits preserve attachments and the ordinary draft @T0", async ({ page }) => {
	await installFakeAgent(page, { workers: [{ id: sessionId, title: "Queued images", mode: "chat" }] });
	const now = "2026-09-06T10:00:00Z";
	let message = {
		id: "message-queued",
		turnId: "queued-1",
		sequence: 2,
		revision: 0,
		role: "user",
		origin: "human",
		text: "Inspect the screenshot",
		streaming: false,
		createdAt: now,
		content: [] as { type: string; mimeType: string }[],
	};
	const edits: {
		clientMessageId: string;
		text: string;
		attachments?: { mimeType: string; data: string }[];
		retainedContent: number[];
		expectedRevision: number;
	}[] = [];
	const receipts = new Map<string, (typeof edits)[number]>();
	await page.route(`**/api/v1/sessions/${sessionId}/**`, async (route) => {
		const url = new URL(route.request().url());
		if (url.pathname.endsWith("/conversation") && route.request().method() === "GET") {
			await route.fulfill({
				json: {
					conversationId: "conversation-queued",
					sessionId,
					harness: "codex",
					mode: "chat",
					controller: "busy",
					capabilities: ["images", "steer"],
					latestSequence: 2,
					oldestSequence: 1,
					hasMoreBefore: false,
					turns: [
						{
							id: "running-1",
							state: "running",
							providerTurnId: "provider-1",
							requestedAt: now,
							startedAt: now,
						},
						{ id: "queued-1", state: "queued", requestedAt: now },
					],
					messages: [message],
					activities: [],
					settings: {},
				},
			});
			return;
		}
		if (url.pathname.endsWith("/attachments")) {
			expect(route.request().postDataJSON().attachments[0].data).toBe(png);
			await route.fulfill({ json: { paths: [path] } });
			return;
		}
		if (url.pathname.endsWith("/queue/edit")) {
			const edit = route.request().postDataJSON();
			edits.push(edit);
			const receipt = receipts.get(edit.clientMessageId);
			if (receipt) {
				expect(edit).toEqual(receipt);
				await route.fulfill({ status: 204 });
				return;
			}
			expect(edit.expectedRevision).toBe(message.revision);
			message = {
				...message,
				text: edit.text,
				revision: message.revision + 1,
				content: [
					...message.content.filter((_, index) => edit.retainedContent.includes(index)),
					...(edit.attachments ?? []).map((attachment: { mimeType: string }) => ({
						type: "image",
						mimeType: attachment.mimeType,
					})),
				],
			};
			receipts.set(edit.clientMessageId, edit);
			// Commit the first edit, then lose its 204 response. A reload must
			// replay the same receipt even though the daemon revision advanced.
			if (edits.length === 1) await route.abort("failed");
			else await route.fulfill({ status: 204 });
			return;
		}
		if (url.pathname.endsWith("/preview/files/" + path)) {
			await route.fulfill({ contentType: "image/png", body: Buffer.from(png, "base64") });
			return;
		}
		if (url.pathname.endsWith("/conversation/models")) {
			await route.fulfill({ json: { models: [], selected: {} } });
			return;
		}
		if (url.pathname.endsWith("/conversation/skills")) {
			await route.fulfill({ json: { skills: [] } });
			return;
		}
		if (url.pathname.endsWith("/workspace/files")) {
			await route.fulfill({ json: { files: [], truncated: false } });
			return;
		}
		if (url.pathname.endsWith("/interface-transition")) {
			await route.fulfill({ json: { supported: true, targetMode: "tui" } });
			return;
		}
		await route.fulfill({ status: 404, json: { error: { code: "NOT_FOUND", message: "not found" } } });
	});
	await page.goto(`/#/projects/fake-proj/sessions/${sessionId}`);
	const field = page.getByRole("combobox", { name: "Message the agent" });
	await expect(field).toBeVisible();
	await field.fill("Keep my ordinary draft");
	await page
		.locator('input[type="file"]')
		.setInputFiles({ name: "ordinary.png", mimeType: "image/png", buffer: Buffer.from(png, "base64") });
	await expect(page.getByLabel("Remove ordinary.png")).toBeVisible();
	await page.getByRole("button", { name: "Edit queued message" }).click();
	await expect(field).toHaveText("Inspect the screenshot");
	await expect(page.getByLabel("Remove ordinary.png")).toHaveCount(0);
	await page
		.locator('input[type="file"]')
		.setInputFiles({ name: "screenshot.png", mimeType: "image/png", buffer: Buffer.from(png, "base64") });
	await expect(page.getByLabel("Remove screenshot.png")).toBeVisible();
	await page.getByRole("button", { name: "Send message", exact: true }).click();
	await expect.poll(() => edits.length).toBe(1);
	expect(edits[0]).toEqual({
		clientMessageId: expect.any(String),
		text: `Inspect the screenshot\n\nAttached files (read these files in the workspace):\n- ${path}`,
		attachments: [{ mimeType: "image/png", data: png }],
		retainedContent: [],
		expectedRevision: 0,
	});
	await expect(page.getByRole("button", { name: "Retry edit safely", exact: true })).toBeEnabled();
	expect(message.revision).toBe(1);
	await page.reload();
	await page.getByRole("button", { name: "Retry edit safely", exact: true }).click();
	await expect.poll(() => edits.length).toBe(2);
	expect(edits[1]).toEqual(edits[0]);
	expect(message.revision).toBe(1);
	await expect(field).toHaveText("Keep my ordinary draft");
	await expect(page.getByLabel("Remove ordinary.png")).toBeVisible();
	await expect(page.getByText("Queued message edits cannot include attachments.")).toHaveCount(0);

	await page.getByRole("button", { name: "Edit queued message" }).click();
	await expect(page.getByLabel("Remove attachment-queue.png")).toBeVisible();
	await expect(field).toHaveText("Inspect the screenshot");
	await field.fill("Inspect it carefully");
	await page.getByRole("button", { name: "Send message", exact: true }).click();
	await expect.poll(() => edits.length).toBe(3);
	expect(edits[2].retainedContent).toEqual([0]);
	expect(edits[2].attachments).toBeUndefined();
	expect(message.content).toHaveLength(1);
	await page.getByRole("button", { name: "Edit queued message" }).click();
	await page.getByLabel("Remove attachment-queue.png").click();
	await page.getByLabel("Remove Image 1", { exact: true }).click();
	await page.getByRole("button", { name: "Send message", exact: true }).click();
	await expect.poll(() => edits.length).toBe(4);
	expect(edits[3]).toEqual({ clientMessageId: expect.any(String), text: "Inspect it carefully", retainedContent: [], expectedRevision: 2 });
	expect(message.content).toHaveLength(0);
	await expect(field).toHaveText("Keep my ordinary draft");
	await page.getByRole("button", { name: "Edit queued message" }).click();
	await field.fill("Discard this edit");
	await page.getByRole("button", { name: "Cancel edit" }).click();
	await expect(field).toHaveText("Keep my ordinary draft");
	await expect(page.getByLabel("Remove ordinary.png")).toBeVisible();
});
