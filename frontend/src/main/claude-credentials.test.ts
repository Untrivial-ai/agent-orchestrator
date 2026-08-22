import { mkdtemp, mkdir, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import { readClaudeCredentials } from "./claude-credentials";

const tempDirs: string[] = [];

afterEach(async () => {
	await Promise.all(tempDirs.splice(0).map((dir) => rm(dir, { recursive: true, force: true })));
});

describe("Claude credential handoff", () => {
	it("reads the macOS Claude Code keychain credential", async () => {
		const credential = '{"claudeAiOauth":{"accessToken":"secret"}}';
		await expect(readClaudeCredentials({
			platform: "darwin",
			runSecurity: async () => credential,
		})).resolves.toEqual(Buffer.from(credential));
	});

	it("uses the Claude credential file on other platforms", async () => {
		const homeDir = await mkdtemp(path.join(os.tmpdir(), "ao-claude-credentials-"));
		tempDirs.push(homeDir);
		await mkdir(path.join(homeDir, ".claude"));
		await writeFile(path.join(homeDir, ".claude", ".credentials.json"), '{"claudeAiOauth":{}}');
		await expect(readClaudeCredentials({ platform: "linux", homeDir })).resolves.toBeInstanceOf(Buffer);
	});

	it("returns an actionable error when Claude Code is not signed in", async () => {
		const homeDir = await mkdtemp(path.join(os.tmpdir(), "ao-claude-credentials-"));
		tempDirs.push(homeDir);
		await expect(readClaudeCredentials({ platform: "linux", homeDir })).rejects.toThrow("claude login");
	});
});
