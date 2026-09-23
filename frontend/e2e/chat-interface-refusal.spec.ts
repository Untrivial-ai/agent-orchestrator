import { expect, test } from "@playwright/test";
import { agentReadiness } from "../src/renderer/test/agent-readiness-fixtures";
import { installFakeAgent } from "./support/fake-bridge";

const projectId = "chat-agent-switch";
const sessionId = "chat-switch-worker";

test("untouched Chat reports interface refusal and can dismiss and retry @T0", async ({ page }) => {
	await page.emulateMedia({ reducedMotion: "reduce" });
	await installFakeAgent(page, {
		projectId,
		projectName: projectId,
		workers: [{ id: sessionId, provider: "codex", title: "Chat switch worker", mode: "chat", status: "idle", activity: "idle" }],
	});
	await page.route("http://127.0.0.1:8080/api/v1/**", async (route) => {
		const pathname = new URL(route.request().url()).pathname;
		if (pathname === "/api/v1/agents/readiness" || pathname === "/api/v1/agents/readiness/ensure") {
			await route.fulfill({
				json: {
					agents: [agentReadiness("claude-code", "Claude Code"), agentReadiness("codex", "Codex")],
				},
			});
			return;
		}
		if (pathname === `/api/v1/projects/${projectId}`) {
			await route.fulfill({
				json: {
					status: "ok",
					project: {
						id: projectId,
						agent: "codex",
						config: { worker: { agent: "codex" } },
					},
				},
			});
			return;
		}
		if (pathname === `/api/v1/sessions/${sessionId}/conversation`) {
			await route.fulfill({
				json: {
					conversationId: "conversation-chat-switch",
					sessionId,
					harness: "codex",
					mode: "chat",
					controller: "ready",
					latestSequence: 0,
					oldestSequence: 0,
					hasMoreBefore: false,
					turns: [],
					messages: [],
					activities: [],
					settings: {},
				},
			});
			return;
		}
		if (pathname === `/api/v1/sessions/${sessionId}/conversation/models`) {
			await route.fulfill({ json: { models: [], selected: {} } });
			return;
		}
		if (pathname === `/api/v1/sessions/${sessionId}/conversation/skills`) {
			await route.fulfill({ json: { skills: [] } });
			return;
		}
		if (pathname === `/api/v1/sessions/${sessionId}/workspace/files`) {
			await route.fulfill({ json: { files: [], truncated: false } });
			return;
		}
		if (pathname === `/api/v1/sessions/${sessionId}/interface-transition`) {
			if (route.request().method() === "POST") {
				await route.fulfill({ status: 409, json: { error: "conflict", code: "NATIVE_SESSION_MISSING", message: "The agent has not exposed a native conversation that can resume in the other interface", requestId: "handoff-test" } });
			} else {
				await route.fulfill({ json: { supported: true, targetMode: "tui" } });
			}
			return;
		}
		if (pathname === "/api/v1/agents/claude-code/models") {
			await route.fulfill({
				json: {
					agentId: "claude-code",
					allowCustom: true,
					fetchedAt: "2026-08-18T00:00:00Z",
					models: [],
					selectionMode: "catalog",
					source: "test",
					stale: false,
				},
			});
			return;
		}
		await route.fulfill({ json: { status: "ok" } });
	});

	await page.goto(`/#/projects/${projectId}/sessions/${sessionId}`);
	await expect(page.getByRole("region", { name: "Chat" })).toBeVisible();
	await page.getByRole("button", { name: "Session actions", exact: true }).click();
	await page.getByRole("menuitem", { name: "Switch to terminal UI" }).click();
	const notice = page.getByRole("alert").filter({ hasText: "Could not switch interfaces" });
	await expect(notice).toContainText("The agent has not exposed a native conversation that can resume in the other interface");
	await expect(notice).not.toContainText("NATIVE_SESSION_MISSING");
	await expect(page.getByRole("dialog")).toHaveCount(0);
	await page.getByRole("button", { name: "Dismiss interface switch error" }).click();
	await expect(notice).toHaveCount(0);
	await page.getByRole("button", { name: "Session actions", exact: true }).click();
	await page.getByRole("menuitem", { name: "Switch to terminal UI" }).click();
	await expect(notice).toContainText("The agent has not exposed a native conversation that can resume in the other interface");
	await expect(notice).not.toContainText("NATIVE_SESSION_MISSING");
});
