import { expect, test } from "@playwright/test";
import type { AoBridge } from "../src/preload";
import { installFakeBridge } from "./support/fake-bridge";

// Renderer-only coverage. Account and connection responses are fixtures; this
// verifies settings/dialog interaction without signing in or sending secrets.
test("cloud settings: manage an agent and connect GitHub", async ({ page }, testInfo) => {
	const port = Number(process.env.AO_E2E_PORT ?? 5173);
	await installFakeBridge(page, { daemonPort: port });
	await page.addInitScript(() => {
		const bridge = (window as unknown as { ao: AoBridge }).ao;
		const user = { id: "user-test", email: "review@example.test", displayName: "UI review" };
		const organizations = [{ id: "org-test", slug: "review", displayName: "UI review", role: "owner" }];
		bridge.cloud.getSession = async () => ({ authProvider: "workos", user, storedAt: "2026-09-30T00:00:00Z" });
		bridge.cloudCp.request = async ({ path }) => {
			const connection = (provider: string, credentialType: string) => ({ id: provider, provider, label: "default", config: { credentialType }, validationState: "valid", createdAt: "", updatedAt: "" });
		const body = path.endsWith("/me") ? { user, organizations, sandboxProviders: { available: ["nodeops", "coder"], default: "nodeops" } }
			: path.endsWith("/provider-connections") ? { providerConnections: [connection("codex", "auth_json"), connection("cursor", "api_key")] }
				: path.endsWith("/github/installations") ? { installations: [] }
					: { items: [] };
			return { status: 200, headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) };
		};
	});
	await page.route("**/api/v1/settings*", (route) => route.fulfill({ json: {
		defaultSessionMode: "tui", chatHarnesses: ["claude-code"], client: "", localEnabled: true,
		cloudOffering: true, cloudEnabled: true, cloudControlPlaneUrl: "https://cloud.test",
	} }));
	await page.goto("/");
	await page.getByRole("button", { name: "Settings", exact: true }).click();
	await page.getByRole("button", { name: "Cloud", exact: true }).click();
	await expect(page.getByText("ChatGPT account", { exact: true })).toBeVisible();
	await expect(page.getByRole("button", { name: "Connect GitHub" })).toBeVisible();
	await expect(page.getByLabel("GitHub personal access token")).toHaveCount(0);
	await page.screenshot({ path: testInfo.outputPath("cloud-settings.png") });

	await page.getByRole("button", { name: "Manage Cursor connection" }).click();
	const dialog = page.getByRole("dialog", { name: "Connect Cursor" });
	await expect(dialog).toBeVisible();
	await expect(dialog.getByLabel("Cursor API key")).toBeEnabled();
	await expect(dialog.getByRole("button", { name: "Connection method" })).toHaveCount(0);
	await dialog.getByLabel("Cursor API key").fill("example-key");
	await expect(dialog.getByRole("button", { name: "Connect", exact: true })).toBeEnabled();
	await dialog.getByLabel("Cursor API key").clear();
	await page.screenshot({ path: testInfo.outputPath("cursor-credential.png") });
	await dialog.getByRole("button", { name: "Cancel" }).click();

	await page.getByRole("button", { name: "Close settings" }).click();
	await page.getByRole("button", { name: "New project" }).click();
	await page.getByRole("button", { name: "New cloud project" }).click();
	await expect(page.getByRole("button", { name: /^Connect GitHub/ })).toBeVisible();
	await expect(page.getByRole("button", { name: "Manually setup" })).toHaveCount(0);
	await expect(page.getByLabel("GitHub PAT")).toHaveCount(0);
	await page.screenshot({ path: testInfo.outputPath("cloud-project-github.png") });
});
