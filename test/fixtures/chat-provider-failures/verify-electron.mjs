// Opt-in native Electron verification against the isolated fixture setup in README.md.
import { chromium, expect } from "../../../frontend/node_modules/@playwright/test/index.mjs";
import { mkdir, writeFile } from "node:fs/promises";

const artifacts = new URL("../../../docs/pr-evidence/pr-5227", import.meta.url).pathname;
await mkdir(artifacts, { recursive: true });
const browser = await chromium.connectOverCDP("http://127.0.0.1:9337");
const page = browser.contexts()[0].pages().find((page) => page.url().startsWith("http://localhost:5173"));
if (!page) throw new Error("test Electron renderer not found");
expect(await page.evaluate(() => Boolean(window.ao))).toBe(true);
const api = async (path) => {
	const response = await fetch(`http://127.0.0.1:3307/api/v1${path}`);
	if (!response.ok) throw new Error(`${path}: ${response.status}`);
	return response.json();
};
for (const harness of ["claude-code", "codex"]) {
	const response = await fetch("http://127.0.0.1:3307/api/v1/sessions", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ projectId: "pr5227-evidence", harness, mode: "chat", displayName: harness === "codex" ? "Codex verification" : "Claude verification", prompt: "quota" }) });
	const created = await response.json();
	if (!response.ok) throw new Error(JSON.stringify(created));
	const session = created.session;
	const snapshot = () => api(`/sessions/${session.id}/conversation`);
	await expect.poll(async () => (await snapshot()).turns.at(-1)?.state, { timeout: 30000 }).toBe("failed");
	await page.goto(`http://localhost:5173/#/projects/pr5227-evidence/sessions/${session.id}`);
	await expect(page.getByRole("log", { name: "Conversation" })).toBeVisible({ timeout: 30000 });
	await expect(page.getByText(/Usage limit reached \(simulated\)/)).toHaveCount(1);
	await expect(page.getByRole("link", { name: "https://example.com/billing" })).toBeVisible();
	await expect(page.getByText(/Reconnecting 1\/5/)).toHaveCount(0);
	await page.screenshot({ path: `${artifacts}/${harness}-quota.png` });
	await page.reload();
	await expect(page.getByText(/Usage limit reached \(simulated\)/)).toHaveCount(1);
	const editor = page.getByRole("combobox", { name: "Message the agent" });
	await editor.fill("success");
	await editor.press("Enter");
	await expect(page.getByText(/Reconnecting 1\/5/)).toBeVisible({ timeout: 15000 });
	if (harness === "codex") await page.screenshot({ path: `${artifacts}/codex-retrying.png` });
	await expect(page.getByText("Recovered successfully. This was a scripted provider response.")).toBeVisible({ timeout: 30000 });
	await expect.poll(async () => (await snapshot()).turns.at(-1)?.state).toBe("completed");
	await editor.fill("cancel");
	await editor.press("Enter");
	await page.getByRole("button", { name: "Stop turn", exact: true }).click();
	await expect.poll(async () => (await snapshot()).turns.at(-1)?.state, { timeout: 15000 }).toBe("interrupted");
	expect((await snapshot()).turns.at(-1).errorMessage).toBeUndefined();
	await editor.fill("episodes");
	await editor.press("Enter");
	await expect.poll(async () => (await snapshot()).turns.at(-1)?.state, { timeout: 30000 }).toBe("failed");
	const episodeSnapshot = await snapshot();
	const episodes = episodeSnapshot.activities.filter((activity) => activity.turnId === episodeSnapshot.turns.at(-1).id && activity.detail?.event === "provider.failure");
	expect(episodes).toHaveLength(2);
	expect(episodes.map((activity) => Boolean(activity.detail.superseded))).toEqual([false, true]);
	expect(episodes[0].providerItemId).not.toBe(episodes[1].providerItemId);
	await expect(page.getByText(/Second retry episode \(simulated\)/)).toHaveCount(0);
	await page.reload();
	await expect(page.getByText("Recovered first episode.")).toBeVisible();
	await expect(page.getByText(/Reconnecting 1\/5/)).toHaveCount(3); // success, cancellation, recovered episode A
	await editor.fill("auth");
	await editor.press("Enter");
	await expect(page.getByText(/Login expired \(simulated\)/)).toHaveCount(1, { timeout: 30000 });
	await expect(page.getByText(/Sign in again to keep going/)).toBeVisible();
	await expect.poll(async () => Boolean((await snapshot()).account?.reauthRequiredAt)).toBe(true);
	await page.screenshot({ path: `${artifacts}/${harness}-auth.png` });
	const result = await snapshot();
	await writeFile(`${artifacts}/${harness}-snapshot.json`, JSON.stringify({ harness, controller: result.controller, turns: result.turns, activities: result.activities, account: result.account }, null, 2) + "\n");
	console.log(`${harness}: quota, one terminal row, safe link, reload, retry progress, recovery, cancellation, repeated episodes, auth PASS`);
}
await browser.close();
