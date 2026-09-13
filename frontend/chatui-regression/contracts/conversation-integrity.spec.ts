import { expect, message, test, turn, type JsonObject } from "../support/test";

test.describe("ChatUI conversation integrity", () => {
	test.describe("MQA-07 synthetic root branch", () => {
		test.use({ chatUIOptions: { sessionId: "chatui-synthetic-branch" } });

		test("activates the advertised synthetic root as one encoded route segment", async ({ chatUI, page }) => {
			chatUI.conversation = {
				...chatUI.conversation,
				activeBranchId: "branch-edited",
				branchedFromEarlierMessage: true,
				latestSequence: 2,
				oldestSequence: 1,
				turns: [turn("turn-edited", "completed", { providerTurnId: "provider-edited" })],
				messages: [
					message("message-edited-user", "turn-edited", 1, "user", "Edited request"),
					message("message-edited-assistant", "turn-edited", 2, "assistant", "Edited response"),
				],
				branchPoints: [
					{
						turnId: "turn-edited",
						position: 2,
						total: 2,
						previousBranchId: "conversation:root",
					},
				],
			};

			await chatUI.open();
			await page.getByRole("button", { name: "Previous conversation branch" }).click();

			await expect.poll(() =>
				chatUI.requests.some(
					(request) =>
						request.method === "POST" &&
						request.pathname.endsWith("/branches/conversation%3Aroot/activate"),
				),
			).toBe(true);
			await expect(page.getByRole("alert")).toHaveCount(0);
		});
	});

	test.describe("MQA-02 queued-turn controls", () => {
		test.use({ chatUIOptions: { sessionId: "chatui-queued-turns", status: "working", activity: "active" } });
		const conversationWithQueue = (base: JsonObject): JsonObject => ({
			...base,
			controller: "busy",
			latestSequence: 3,
			oldestSequence: 1,
			turns: [
				turn("turn-running", "running"),
				turn("turn-queued-one", "queued"),
				turn("turn-queued-two", "queued"),
			],
			queuedTurns: [
				{ turnId: "turn-queued-one", text: "First queued follow-up", origin: "human" },
				{ turnId: "turn-queued-two", text: "Second queued follow-up", origin: "human" },
			],
			messages: [
				message("message-running", "turn-running", 1, "user", "Active work"),
				message("message-queued-one", "turn-queued-one", 2, "user", "First queued follow-up"),
				message("message-queued-two", "turn-queued-two", 3, "user", "Second queued follow-up"),
			],
		});

		test("keeps queue-only actions separate from destructive Stop", async ({ chatUI, page }) => {
			chatUI.conversation = conversationWithQueue(chatUI.conversation);

			await chatUI.open();
			await expect(page.getByTestId("queued-message-dock")).toBeVisible();

			const cancelButtons = page.getByRole("button", { name: /Cancel queued message:/ });
			const promoteButtons = page.getByRole("button", { name: /Use as next message:/ });
			await expect(cancelButtons).toHaveCount(2);
			await expect(promoteButtons).toHaveCount(2);
			await expect(page.getByRole("button", { name: "Stop turn" })).toHaveAccessibleDescription(
				/also cancels 2 queued messages/i,
			);

			await page
				.getByRole("button", { name: "Cancel queued message: First queued follow-up" })
				.click();
			await expect
				.poll(
					() =>
						chatUI.requests.filter(
							(request) =>
								request.method === "POST" &&
								request.pathname.endsWith("/conversation/turns/turn-queued-one/cancel"),
						).length,
				)
				.toBe(1);
			expect(chatUI.requestsMatching("POST", "/conversation/interrupt")).toHaveLength(0);
			await expect(page.getByText("Active work", { exact: true })).toBeVisible();
			await expect(page.getByText("First queued follow-up", { exact: true })).toHaveCount(0);
			await expect(page.getByTestId("queued-message-turn-queued-two")).toBeVisible();

			await page
				.getByRole("button", { name: "Use as next message: Second queued follow-up" })
				.click();
			await expect
				.poll(() =>
					chatUI.requests.some(
						(request) =>
							request.method === "POST" &&
							request.pathname.endsWith("/conversation/turns/turn-queued-two/steer"),
					),
				)
				.toBe(true);
			expect(chatUI.requestsMatching("POST", "/conversation/interrupt")).toHaveLength(0);
			await expect(page.getByText("Active work", { exact: true })).toBeVisible();
			await expect(page.getByTestId("queued-message-turn-queued-two")).toHaveCount(0);
			await expect(page.getByTestId("queued-message-dock")).toHaveCount(0);
			await expect(page.getByText("Second queued follow-up", { exact: true })).toBeVisible();
		});

		test("confirms destructive Stop and submits the exact queued scope", async ({ chatUI, page }) => {
			chatUI.conversation = conversationWithQueue(chatUI.conversation);

			await chatUI.open();
			await page.getByRole("button", { name: "Stop turn" }).click();
			await expect(
				page.getByRole("dialog", { name: "Stop turn and cancel 2 queued messages?" }),
			).toBeVisible();
			await page.getByRole("button", { name: "Stop all" }).click();

			await expect.poll(() => chatUI.requestsMatching("POST", "/conversation/interrupt").length).toBe(1);
			expect(chatUI.requestsMatching("POST", "/conversation/interrupt")[0]?.body).toEqual({
				queuedTurnIds: ["turn-queued-one", "turn-queued-two"],
			});
			await expect(page.getByTestId("queued-message-dock")).toHaveCount(0);
		});
	});

	test.describe("MQA-10 attachment settlement and identity", () => {
		test.use({ chatUIOptions: { sessionId: "chatui-attachments" } });

		test("waits for an in-flight FileReader before sending", async ({ chatUI, page }) => {
			await chatUI.delayFileReads(250);
			await chatUI.open();

			await page.locator('input[type="file"]').setInputFiles({
				name: "slow-original.png",
				mimeType: "image/png",
				buffer: Buffer.from("slow-image-evidence"),
			});
			await page.getByRole("combobox", { name: "Message the agent" }).fill("Inspect this image");
			await page.getByRole("button", { name: "Send message" }).click();

			await expect.poll(() => chatUI.requestsMatching("POST", "/attachments").length).toBe(1);
			await expect.poll(() => chatUI.requestsMatching("POST", "/conversation/messages").length).toBe(1);
			const attachmentIndex = chatUI.requests.findIndex(
				(request) => request.method === "POST" && request.pathname.endsWith("/attachments"),
			);
			const messageIndex = chatUI.requests.findIndex(
				(request) => request.method === "POST" && request.pathname.endsWith("/conversation/messages"),
			);
			expect(attachmentIndex).toBeGreaterThanOrEqual(0);
			expect(messageIndex).toBeGreaterThan(attachmentIndex);
			const sent = chatUI.requestsMatching("POST", "/conversation/messages").at(0)?.body as JsonObject | undefined;
			expect(String(sent?.text ?? "")).toContain(".ao/attachments/");
		});

		test("discloses whether an image is sent by path, native bytes, or both", async ({ chatUI, page }) => {
			await chatUI.open();
			await page.locator('input[type="file"]').setInputFiles({
				name: "delivery-route.png",
				mimeType: "image/png",
				buffer: Buffer.from("delivery-route-evidence"),
			});

			const attachedFile = page.getByRole("list", { name: "Attached files" }).getByRole("listitem");
			await expect(attachedFile).toContainText(/worktree path.*native image|native image.*worktree path/i);
			const freshDelivery = attachedFile.getByRole("status");
			await expect(freshDelivery).toHaveAttribute("aria-live", "polite");
			await expect(freshDelivery).toHaveAttribute("aria-atomic", "true");

			await page.reload();
			const restoredFile = page
				.getByRole("list", { name: "Attached files" })
				.getByRole("listitem");
			await expect(restoredFile).toContainText("Worktree path · agent must read");
			await expect(restoredFile).not.toContainText("native image");
			await expect(restoredFile.getByRole("status")).toHaveText("Worktree path · agent must read");
			await page.getByRole("combobox", { name: "Message the agent" }).fill("Inspect after reload");
			await page.getByRole("button", { name: "Send message" }).click();

			await expect.poll(() => chatUI.requestsMatching("POST", "/conversation/messages").length).toBe(1);
			const sent = chatUI.requestsMatching("POST", "/conversation/messages")[0]?.body as JsonObject;
			expect(String(sent.text ?? "")).toContain(".ao/attachments/");
			expect(sent.attachments ?? []).toEqual([]);
		});

		test("preserves the original filename in staged and native image payloads", async ({ chatUI, page }) => {
			await chatUI.open();
			await page.locator('input[type="file"]').setInputFiles({
				name: "original.png",
				mimeType: "image/png",
				buffer: Buffer.from("image-evidence"),
			});
			await expect(page.getByRole("button", { name: "Remove original.png" })).toBeVisible();
			await page.getByRole("combobox", { name: "Message the agent" }).fill("Inspect this image");
			await page.getByRole("button", { name: "Send message" }).click();

			await expect.poll(() => chatUI.requestsMatching("POST", "/attachments").length).toBe(1);
			const staged = chatUI.requestsMatching("POST", "/attachments")[0]?.body as JsonObject;
			const stagedAttachments = staged.attachments as JsonObject[];
			expect.soft(stagedAttachments[0]).toMatchObject({
				name: "original.png",
				mimeType: "image/png",
			});

			await expect.poll(() => chatUI.requestsMatching("POST", "/conversation/messages").length).toBe(1);
			const sent = chatUI.requestsMatching("POST", "/conversation/messages")[0]?.body as JsonObject;
			const nativeAttachments = sent.attachments as JsonObject[];
			expect.soft(nativeAttachments[0]).toMatchObject({
				name: "original.png",
				mimeType: "image/png",
			});
		});
	});

	test.describe("MQA-09 at-sign path references", () => {
		test.use({ chatUIOptions: { sessionId: "chatui-file-references" } });

		test("discloses that an at-sign chip is a path reference, not supplied context", async ({ chatUI, page }) => {
			chatUI.workspaceFiles = [
				{ path: "frontend/src/renderer/components/chat/ChatComposer.tsx", status: "modified" },
			];
			await chatUI.open();

			const composer = page.getByRole("combobox", { name: "Message the agent" });
			await composer.fill("Review @ChatComposer");
			await page.keyboard.press("Enter");

			const token = composer.locator('[data-composer-token="file"]');
			await expect(token).toBeVisible();
			await expect.soft(token).toHaveAccessibleDescription(
				/Path reference only.*agent reads it with normal permissions/i,
			);
			await expect.soft(token).toHaveAttribute(
				"data-value",
				"frontend/src/renderer/components/chat/ChatComposer.tsx",
			);
		});

		test("refreshes the file catalog after workspace change events", async ({ chatUI, page }) => {
			chatUI.workspaceFiles = [{ path: "src/legacy-file.ts", status: "modified" }];
			await chatUI.open();

			const composer = page.getByRole("combobox", { name: "Message the agent" });
			await composer.fill("@legacy");
			await expect(page.getByRole("option", { name: /legacy-file\.ts/ })).toBeVisible();
			await page.keyboard.press("Escape");
			await composer.fill("");

			chatUI.workspaceFiles = [{ path: "src/new-file.ts", status: "untracked" }];
			await chatUI.emitCDC("workspace_changed");
			await composer.fill("@new-file");

			await expect(page.getByRole("option", { name: /new-file\.ts/ })).toBeVisible();
			await expect(page.getByRole("option", { name: /legacy-file\.ts/ })).toHaveCount(0);
		});
	});
});
