import { expect, message, test, turn } from "../support/test";

test.describe("ChatUI interface switching", () => {
	test.describe("MQA-06 failed target-history checkpoint", () => {
		test.use({
			chatUIOptions: {
				mode: "tui",
				provider: "claude-code",
				sessionId: "chatui-history-checkpoint",
			},
		});

		test("offers a retryable, announced action for the interface-switch failure", async ({ chatUI, page }) => {
			chatUI.transitionStatus = {
				supported: true,
				targetMode: "chat",
				transition: {
					id: "transition-history-unsettled",
					sessionId: chatUI.sessionId,
					sourceMode: "tui",
					targetMode: "chat",
					policy: "drain",
					phase: "failed",
					errorCode: "TARGET_HISTORY_UNSETTLED",
					errorDetail: "Interface switch failed (AO-2L): target history is not settled.",
					createdAt: "2026-08-25T09:00:00.000Z",
					updatedAt: "2026-08-25T09:00:01.000Z",
					completedAt: "2026-08-25T09:00:01.000Z",
				},
			};

			await chatUI.open();

			const failure = page.getByText(/Interface switch failed \(AO-2L\)/);
			await expect(failure).toBeVisible();
			const retry = page.getByRole("button", { name: "Retry switch to Chat UI" });
			await Promise.all([
				expect.soft(page.getByRole("alert").filter({ hasText: "AO-2L" })).toBeVisible(),
				expect.soft(retry).toBeVisible(),
			]);
			if ((await retry.count()) === 0) return;
			await retry.click();
			await expect.poll(() => chatUI.requestsMatching("POST", "/interface-transition").length).toBe(1);
			expect(chatUI.requestsMatching("POST", "/interface-transition")[0]?.body).toMatchObject({
				policy: "drain",
				targetMode: "chat",
			});

			chatUI.transitionStatus = {
				supported: true,
				targetMode: "tui",
				transition: {
					id: "transition-requested",
					sessionId: chatUI.sessionId,
					sourceMode: "tui",
					targetMode: "chat",
					policy: "drain",
					phase: "completed",
					createdAt: "2026-08-25T09:00:00.000Z",
					updatedAt: "2026-08-25T09:00:02.000Z",
					completedAt: "2026-08-25T09:00:02.000Z",
				},
			};
			await chatUI.setMode("chat");
			await expect(page.getByRole("region", { name: "Chat" })).toBeVisible();
			expect(chatUI.requestsMatching("POST", "/interface-transition")).toHaveLength(1);
		});
	});

	test.describe("MQA-05 busy Chat to Terminal policy", () => {
		test.use({
			chatUIOptions: {
				activity: "active",
				mode: "chat",
				sessionId: "chatui-busy-switch",
				status: "working",
			},
		});

		test.beforeEach(async ({ chatUI }) => {
			chatUI.conversation = {
				...chatUI.conversation,
				controller: "busy",
				latestSequence: 2,
				oldestSequence: 1,
				turns: [
					turn("turn-running", "running"),
					turn("turn-queued", "queued"),
				],
				messages: [
					message("message-running", "turn-running", 1, "user", "Finish the active task"),
					message("message-queued", "turn-queued", 2, "user", "Then check the queued task"),
				],
			};
		});

		test("offers Finish work before submitting the non-destructive drain policy", async ({ chatUI, page }) => {
			await chatUI.open();
			await page.getByRole("button", { name: "Switch to terminal UI" }).click();

			const dialog = page.getByRole("dialog", { name: "Switch to Terminal UI?" });
			await expect(dialog).toBeVisible();
			expect(chatUI.requestsMatching("POST", "/interface-transition")).toHaveLength(0);
			await dialog.getByRole("button", { name: "Finish work, then switch" }).click();

			await expect.poll(() => chatUI.requestsMatching("POST", "/interface-transition").length).toBe(1);
			expect(chatUI.requestsMatching("POST", "/interface-transition")[0]?.body).toMatchObject({
				policy: "drain",
				targetMode: "tui",
			});
		});

		test("maps explicit Stop now consent to the destructive interrupt policy", async ({ chatUI, page }) => {
			await chatUI.open();
			await page.getByRole("button", { name: "Switch to terminal UI" }).click();

			const dialog = page.getByRole("dialog", { name: "Switch to Terminal UI?" });
			await expect(dialog).toBeVisible();
			expect(chatUI.requestsMatching("POST", "/interface-transition")).toHaveLength(0);
			await dialog.getByRole("button", { name: "Stop now and switch" }).click();

			await expect.poll(() => chatUI.requestsMatching("POST", "/interface-transition").length).toBe(1);
			expect(chatUI.requestsMatching("POST", "/interface-transition")[0]?.body).toMatchObject({
				policy: "interrupt",
				targetMode: "tui",
			});
		});
	});

	test.describe("MQA-04 composer durability", () => {
		const secondarySessionId = "chatui-draft-round-trip-secondary";
		test.use({
			chatUIOptions: {
				secondarySessionId,
				sessionId: "chatui-draft-round-trip",
			},
		});

		test("keeps drafts session-scoped across interface, navigation, reload, and accepted send", async ({ chatUI, page }) => {
			const primaryDraft = "draft-survives-interface-round-trip-1e67";
			const secondaryDraft = "secondary-session-draft-75c4";
			await chatUI.open();
			await page.getByRole("combobox", { name: "Message the agent" }).fill(primaryDraft);

			await chatUI.setMode("tui");
			await expect(page.getByRole("region", { name: "Chat" })).toHaveCount(0);
			await expect(page.getByTestId("terminal-interaction-surface")).toBeVisible();
			await chatUI.setMode("chat");

			let composer = page.getByRole("combobox", { name: "Message the agent" });
			await expect(composer).toBeVisible();
			await expect.soft(composer, "draft after full Chat surface unmount").toContainText(primaryDraft);
			if (!(await composer.textContent())?.includes(primaryDraft)) await composer.fill(primaryDraft);

			await chatUI.navigateToSession(secondarySessionId);
			composer = page.getByRole("combobox", { name: "Message the agent" });
			await expect.soft(composer, "primary draft must not leak into a second session").not.toContainText(primaryDraft);
			await composer.fill(secondaryDraft);

			await chatUI.navigateToSession(chatUI.sessionId);
			composer = page.getByRole("combobox", { name: "Message the agent" });
			await expect.soft(composer, "primary draft after session navigation").toContainText(primaryDraft);
			await expect.soft(composer, "secondary draft must remain session-scoped").not.toContainText(secondaryDraft);
			if (!(await composer.textContent())?.includes(primaryDraft)) await composer.fill(primaryDraft);

			await page.reload();
			await expect(page.getByRole("region", { name: "Chat" })).toBeVisible();
			composer = page.getByRole("combobox", { name: "Message the agent" });
			await expect.soft(composer, "primary draft after renderer reload").toContainText(primaryDraft);

			await chatUI.navigateToSession(secondarySessionId);
			composer = page.getByRole("combobox", { name: "Message the agent" });
			await expect.soft(composer, "secondary draft survives independently").toContainText(secondaryDraft);
			if (!(await composer.textContent())?.includes(secondaryDraft)) await composer.fill(secondaryDraft);

			await page.getByRole("button", { name: "Send message" }).click();
			await expect.poll(() => chatUI.requestsMatching("POST", "/conversation/messages").length).toBe(1);
			await expect(composer, "an accepted send clears only the submitted session draft").toHaveText("");

			await chatUI.navigateToSession(chatUI.sessionId);
			await expect
				.soft(page.getByRole("combobox", { name: "Message the agent" }), "sending the secondary draft preserves the primary draft")
				.toContainText(primaryDraft);
			await chatUI.navigateToSession(secondarySessionId);
			await expect(page.getByRole("combobox", { name: "Message the agent" })).toHaveText("");
		});
	});

	test.describe("MQA-08 imported Terminal outcome", () => {
		test.use({ chatUIOptions: { sessionId: "chatui-imported-outcome" } });

		test("uses a neutral imported-history label when completion metadata is unavailable", async ({ chatUI, page }) => {
			chatUI.conversation = {
				...chatUI.conversation,
				latestSequence: 2,
				oldestSequence: 1,
				turns: [
					turn("turn-imported", "recovered", {
						providerTurnId: "native-turn-1",
						importedFromTerminal: true,
					}),
				],
				messages: [
					message("message-imported-user", "turn-imported", 1, "user", "Run the native task"),
					message("message-imported-assistant", "turn-imported", 2, "assistant", "Native task output imported"),
				],
			};

			await chatUI.open();

			await expect.soft(
				page.getByText("Imported from Terminal UI — completion status unavailable", { exact: true }),
			).toBeVisible();
			await expect.soft(page.getByText("Outcome unknown", { exact: true })).toHaveCount(0);
		});
	});
});
