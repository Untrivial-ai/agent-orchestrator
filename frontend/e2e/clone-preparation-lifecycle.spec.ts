import { expect, test } from "@playwright/test";
import type { AoBridge } from "../src/preload";
import { agentReadiness } from "../src/renderer/test/agent-readiness-fixtures";
import { installFakeAgent } from "./support/fake-bridge";

for (const destinationMode of ["picker", "typed"] as const) {
	test(`renderer: clone cancel and retry use the matching preparation ID with ${destinationMode} destination @T0`, async ({ page }) => {
		await installFakeAgent(page);
		let preparation = 0;
		const cleanupRequests: Array<{ path: string; preparationId: string }> = [];
		let addRequest: Record<string, unknown> | null = null;

		await page.route("http://127.0.0.1:8080/api/v1/**", async (route) => {
			const pathname = new URL(route.request().url()).pathname;
			if (pathname.startsWith("/api/v1/agents/readiness")) {
				await route.fulfill({ json: { agents: [agentReadiness("codex", "Codex")] } });
				return;
			}
			if (pathname === "/api/v1/projects/clone/prepare") {
				expect(route.request().postDataJSON()).toEqual({
					remoteUrl: "https://example.com/example.git", destinationParent: "/repos",
				});
				preparation += 1;
				await route.fulfill({ json: {
					path: "/repos/example", remoteUrl: "https://example.com/example.git",
					preparationId: `prep-${preparation}`,
				} });
				return;
			}
			if (pathname === "/api/v1/projects/clone/cleanup") {
				cleanupRequests.push(route.request().postDataJSON());
				await route.fulfill({ status: 204 });
				return;
			}
			if (pathname === "/api/v1/imports/validate") {
				await route.fulfill({ json: {
					importKind: "project", isValid: true, blockingErrors: [], nextStep: "continue",
					root: { repoPath: "/repos/example", isRepo: true, hasCommit: true, hasOrigin: true, isEmptyFolder: false, needsGitInit: false, requiredActions: [], blockingErrors: [] },
				} });
				return;
			}
			if (pathname === "/api/v1/projects" && route.request().method() === "POST") {
				addRequest = route.request().postDataJSON();
				await route.fulfill({ status: 201, json: { project: {
					id: "example", name: "example", kind: "single_repo", path: "/repos/example", config: {},
				} } });
				return;
			}
			if (pathname === "/api/v1/sessions" && route.request().method() === "POST") {
				await route.fulfill({ status: 422, json: { error: "invalid", code: "START_FAILED", message: "test stop" } });
				return;
			}
			await route.fulfill({ json: {} });
		});

		await page.goto("/#/");
		await page.evaluate(() => {
			const bridge = (window as unknown as { ao: AoBridge }).ao;
			bridge.app.chooseDirectory = async () => "/repos";
			bridge.app.checkGitRepository = async () => true;
		});

		const selectDestination = async () => {
			if (destinationMode === "typed") {
				await page.getByRole("textbox", { name: "Destination folder" }).fill("/repos");
			} else {
				await page.getByRole("button", { name: "Choose where to clone the repository", exact: true }).click();
			}
			await expect(page.getByRole("textbox", { name: "Destination folder" })).toHaveValue("/repos");
		};

		const openClone = async () => {
			await page.getByRole("button", { name: "New project", exact: true }).first().click();
			await page.getByRole("button", { name: "Clone from Git", exact: true }).click();
			await page.getByRole("textbox", { name: "Repository URL" }).fill("https://example.com/example.git");
			await selectDestination();
			await page.getByRole("button", { name: "Continue", exact: true }).click();
		};

		await openClone();
		await page.getByRole("button", { name: "Back to clone details" }).click();
		await expect.poll(() => cleanupRequests).toEqual([{ path: "/repos/example", preparationId: "prep-1" }]);

		await page.getByRole("textbox", { name: "Repository URL" }).fill("https://example.com/example.git");
		await selectDestination();
		await page.getByRole("button", { name: "Continue", exact: true }).click();
		await page.getByRole("button", { name: "Clone", exact: true }).click();

		await expect.poll(() => addRequest).toMatchObject({
			path: "/repos/example",
			clonePreparationId: "prep-2",
		});
		expect(cleanupRequests).toEqual([{ path: "/repos/example", preparationId: "prep-1" }]);
	});
}
