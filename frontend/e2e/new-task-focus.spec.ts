import { expect, test, type Page } from "@playwright/test";
import { agentReadiness } from "../src/renderer/test/agent-readiness-fixtures";
import { installFakeAgent } from "./support/fake-bridge";
import { openSwitchAgentDialog } from "./support/open-switch-agent-menu";

// Focus hand-off between a closing Radix menu and the surface its item opened.
// A menu traps focus for the commit in which it closes and restores focus to
// its trigger a tick after it unmounts. Both of those used to land on top of a
// dialog opened from a menu item, leaving the New task composer with no caret
// until the user clicked it (#focus regression, reported 2026-09-03).

const projectId = "task-focus";
const sessionA = "focus-session-a";
const sessionB = "focus-session-b";

function conversation(sessionId: string) {
	const completedAt = "2026-08-26T06:00:00Z";
	return {
		conversationId: `conversation-${sessionId}`,
		sessionId,
		harness: "codex",
		mode: "chat",
		controller: "ready",
		latestSequence: 1,
		oldestSequence: 1,
		hasMoreBefore: false,
		turns: [
			{ id: `${sessionId}-turn-1`, state: "completed", requestedAt: completedAt, startedAt: completedAt, completedAt },
		],
		messages: [
			{
				kind: "message",
				id: `${sessionId}-message-1`,
				turnId: `${sessionId}-turn-1`,
				sequence: 1,
				revision: 0,
				role: "user",
				origin: "human",
				text: `Existing conversation in ${sessionId}`,
				streaming: false,
				createdAt: completedAt,
			},
		],
		activities: [],
		settings: {},
	};
}

async function setup(page: Page, { animated = false } = {}) {
	// The menu's exit animation decides whether Radix's deferred focus restore
	// lands before or after the dialog mounts, so both paths are worth covering.
	await page.emulateMedia({ reducedMotion: animated ? "no-preference" : "reduce" });
	await installFakeAgent(page, {
		projectId,
		projectName: projectId,
		workers: [
			{ id: sessionA, provider: "codex", title: "Session A", mode: "chat" },
			{ id: sessionB, provider: "codex", title: "Session B", mode: "chat" },
		],
	});
	await page.route("http://127.0.0.1:8080/api/v1/**", async (route) => {
		const pathname = new URL(route.request().url()).pathname;
		if (pathname === "/api/v1/agents/readiness" || pathname === "/api/v1/agents/readiness/ensure") {
			await route.fulfill({ json: { agents: [agentReadiness("codex", "Codex")] } });
			return;
		}
		if (pathname === `/api/v1/projects/${projectId}`) {
			await route.fulfill({
				json: {
					status: "ok",
					project: { id: projectId, agent: "codex", config: { worker: { agent: "codex" } } },
				},
			});
			return;
		}
		for (const id of [sessionA, sessionB]) {
			if (pathname === `/api/v1/sessions/${id}/conversation`) {
				await route.fulfill({ json: conversation(id) });
				return;
			}
			if (pathname === `/api/v1/sessions/${id}/conversation/models`) {
				await route.fulfill({ json: { models: [], selected: {} } });
				return;
			}
			if (pathname === `/api/v1/sessions/${id}/conversation/skills`) {
				await route.fulfill({ json: { skills: [] } });
				return;
			}
			if (pathname === `/api/v1/sessions/${id}/workspace/files`) {
				await route.fulfill({ json: { files: [], truncated: false } });
				return;
			}
			if (pathname === `/api/v1/sessions/${id}/interface-transition`) {
				await route.fulfill({ json: { supported: true, targetMode: "tui" } });
				return;
			}
		}
		await route.fulfill({ json: { status: "ok" } });
	});
}


function activeElementInfo(page: Page) {
	return page.evaluate(() => {
		const active = document.activeElement as HTMLElement | null;
		return {
			label: active?.getAttribute("aria-label") ?? active?.tagName ?? "none",
			inDialog: Boolean(active?.closest("[role='dialog']")),
			inTerminal: Boolean(active?.closest(".xterm")),
		};
	});
}

function openProjectMenu(page: Page) {
	return page
		.getByRole("button", { name: new RegExp(`Project actions for ${projectId}`) })
		.first()
		.click({ force: true });
}

async function expectPromptTakesTyping(page: Page) {
	const prompt = page.getByRole("dialog").getByLabel("Task");
	await expect(prompt).toBeVisible();
	await page.keyboard.type("caret is here");
	await expect(prompt).toHaveValue("caret is here");
}

test("renderer: the board New task button focuses the composer prompt @T0", async ({ page }) => {
	await setup(page);
	await page.goto(`/#/projects/${projectId}`);
	await page.getByRole("button", { name: "New task" }).first().click();
	await expect(page.getByRole("dialog")).toBeVisible();
	await expectPromptTakesTyping(page);
});

for (const animated of [false, true]) {
	const motion = animated ? "animated" : "reduced motion";

	test(`renderer: New task from the sidebar project menu focuses the composer prompt (${motion}) @T0`, async ({
		page,
	}) => {
		await setup(page, { animated });
		await page.goto(`/#/projects/${projectId}/sessions/${sessionA}`);
		await expect(page.getByRole("combobox", { name: "Message the agent" })).toBeVisible();
		await openProjectMenu(page);
		await page.getByRole("menuitem", { name: /New session/ }).click();
		await expect(page.getByRole("dialog")).toBeVisible();
		await expectPromptTakesTyping(page);
	});

	test(`renderer: closing a menu-opened dialog hands focus back to the menu trigger (${motion}) @T0`, async ({
		page,
	}) => {
		await setup(page, { animated });
		await page.goto(`/#/projects/${projectId}/sessions/${sessionA}`);
		await expect(page.getByRole("combobox", { name: "Message the agent" })).toBeVisible();
		await openProjectMenu(page);
		await page.getByRole("menuitem", { name: /New session/ }).click();
		await expect(page.getByRole("dialog")).toBeVisible();
		await page.keyboard.press("Escape");
		await expect(page.getByRole("dialog")).toBeHidden();
		// Focus has to land somewhere a keyboard user can carry on from, not on body.
		await expect
			.poll(async () => (await activeElementInfo(page)).label)
			.toBe(`Project actions for ${projectId}`);
	});
}

test("renderer: New task from the sidebar project context menu focuses the composer prompt @T0", async ({ page }) => {
	await setup(page);
	await page.goto(`/#/projects/${projectId}/sessions/${sessionA}`);
	await expect(page.getByRole("combobox", { name: "Message the agent" })).toBeVisible();
	await page
		.getByRole("button", { name: new RegExp(`Project actions for ${projectId}`) })
		.first()
		.click({ button: "right", force: true });
	await page.getByRole("menuitem", { name: /New session/ }).click();
	await expect(page.getByRole("dialog")).toBeVisible();
	await expectPromptTakesTyping(page);
});

test("renderer: New task from the command palette focuses the composer prompt @T0", async ({ page }) => {
	await setup(page);
	await page.goto(`/#/projects/${projectId}/sessions/${sessionA}`);
	await expect(page.getByRole("combobox", { name: "Message the agent" })).toBeVisible();
	await page.keyboard.press("ControlOrMeta+k");
	await page.getByRole("option", { name: /New task/ }).first().click();
	await expectPromptTakesTyping(page);
});

test("renderer: opening another session focuses its chat composer @T0", async ({ page }) => {
	await setup(page);
	await page.goto(`/#/projects/${projectId}/sessions/${sessionA}`);
	const composer = page.getByRole("combobox", { name: "Message the agent" });
	await expect(composer).toBeVisible();
	await expect.poll(async () => (await activeElementInfo(page)).label).toBe("Message the agent");

	await page.getByRole("button", { name: /Open Session B/ }).first().click();
	await expect(composer).toBeVisible();
	await expect.poll(async () => (await activeElementInfo(page)).label).toBe("Message the agent");
	await page.keyboard.type("typed after switching");
	await expect(composer).toHaveText("typed after switching");
});

async function setupSwitchAgentSession(page: Page) {
	await page.emulateMedia({ reducedMotion: "reduce" });
	await installFakeAgent(page, {
		projectId,
		projectName: projectId,
		workers: [{ id: "switch-worker", provider: "claude-code", title: "Switch worker" }],
	});
	await page.route("http://127.0.0.1:8080/api/v1/**", async (route) => {
		const pathname = new URL(route.request().url()).pathname;
		if (pathname === "/api/v1/agents/readiness" || pathname === "/api/v1/agents/readiness/ensure") {
			await route.fulfill({
				json: { agents: [agentReadiness("claude-code", "Claude Code"), agentReadiness("codex", "Codex")] },
			});
			return;
		}
		if (pathname === `/api/v1/projects/${projectId}`) {
			await route.fulfill({
				json: {
					status: "ok",
					project: { id: projectId, agent: "claude-code", config: { worker: { agent: "claude-code" } } },
				},
			});
			return;
		}
		await route.fulfill({ json: { status: "ok" } });
	});
	await page.goto(`/#/projects/${projectId}/sessions/switch-worker`);
}

test("renderer: a dialog opened from a session menu keeps focus inside itself @T0", async ({ page }) => {
	await setupSwitchAgentSession(page);
	const dialog = await openSwitchAgentDialog(page);
	// The menu trigger sits behind the dialog: focus landing back on it would put
	// the next Tab outside the dialog entirely.
	await expect.poll(async () => (await activeElementInfo(page)).inDialog).toBe(true);
	await expect(dialog).toBeVisible();
});

test("renderer: closing that dialog never strands focus on the page body @T0", async ({ page }) => {
	await setupSwitchAgentSession(page);
	const dialog = await openSwitchAgentDialog(page);
	await expect.poll(async () => (await activeElementInfo(page)).inDialog).toBe(true);

	await page.keyboard.press("Escape");
	await expect(dialog).toBeHidden();
	// Closing re-enables worker input, so the terminal underneath claims the
	// caret through its own effect and the menu hand-back stands down. Either
	// landing spot is fine; `document.body` is not, because Tab would then
	// restart from the top of the page.
	await expect
		.poll(async () => {
			const { label, inTerminal } = await activeElementInfo(page);
			return inTerminal || label === "Session actions";
		})
		.toBe(true);
});

test("renderer: a chat session hands focus back to the menu trigger when its dialog closes @T0", async ({
	page,
}) => {
	// No terminal underneath to claim the caret, and the chat composer only
	// autofocuses when the session changes, so this is the case the hand-back
	// exists for: without it focus is left on `document.body`.
	await setup(page);
	await page.goto(`/#/projects/${projectId}/sessions/${sessionA}`);
	await expect(page.getByRole("combobox", { name: "Message the agent" })).toBeVisible();
	const dialog = await openSwitchAgentDialog(page);
	await expect.poll(async () => (await activeElementInfo(page)).inDialog).toBe(true);

	await page.keyboard.press("Escape");
	await expect(dialog).toBeHidden();
	await expect.poll(async () => (await activeElementInfo(page)).label).toBe("Session actions");
});


test("renderer: a context-menu dialog returns focus to where the menu opened from @T0", async ({
	page,
}) => {
	// A context menu has no trigger to point back at, so the defined fallback is
	// the element that held focus when the menu opened, which is what Radix itself
	// restores to. Right-clicking the project row's action button focuses it, so
	// that button is the expected landing spot, and never `document.body`.
	await setup(page);
	await page.goto(`/#/projects/${projectId}/sessions/${sessionA}`);
	await expect(page.getByRole("combobox", { name: "Message the agent" })).toBeVisible();

	const openedFrom = `Project actions for ${projectId}`;
	await page.getByRole("button", { name: new RegExp(openedFrom) }).first().click({ button: "right", force: true });
	await page.getByRole("menuitem", { name: /New session/ }).click();
	await expect(page.getByRole("dialog")).toBeVisible();
	await page.keyboard.press("Escape");
	await expect(page.getByRole("dialog")).toBeHidden();

	await expect.poll(async () => (await activeElementInfo(page)).label).toBe(openedFrom);
});
