import { execFile } from "node:child_process";
import { readFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";

type CredentialReaderOptions = {
	platform?: NodeJS.Platform;
	homeDir?: string;
	runSecurity?: () => Promise<string>;
};

function readKeychainCredential(): Promise<string> {
	return new Promise((resolve, reject) => {
		execFile(
			"security",
			["find-generic-password", "-s", "Claude Code-credentials", "-w"],
			{ encoding: "utf8", maxBuffer: 512 * 1024 },
			(error, stdout) => error ? reject(error) : resolve(stdout),
		);
	});
}

function validCredentials(raw: string): boolean {
	try {
		const parsed = JSON.parse(raw) as { claudeAiOauth?: unknown };
		return typeof parsed === "object" && parsed !== null &&
			typeof parsed.claudeAiOauth === "object" && parsed.claudeAiOauth !== null;
	} catch {
		return false;
	}
}

// Reads the credential from the same OS-protected location Claude Code uses.
// This runs in Electron main so the credential never crosses into the renderer.
export async function readClaudeCredentials(options: CredentialReaderOptions = {}): Promise<Buffer> {
	const platform = options.platform ?? process.platform;
	const homeDir = options.homeDir ?? os.homedir();
	let raw = "";
	if (platform === "darwin") {
		try {
			raw = await (options.runSecurity ?? readKeychainCredential)();
		} catch {
			// Older Claude Code installs may still use the file fallback.
		}
	}
	if (!raw.trim()) {
		try {
			raw = await readFile(path.join(homeDir, ".claude", ".credentials.json"), "utf8");
		} catch {
			throw new Error("Claude Code is not signed in on this computer. Run `claude login` and try again.");
		}
	}
	if (!validCredentials(raw) || Buffer.byteLength(raw) > 256 * 1024) {
		throw new Error("Claude Code credentials on this computer are invalid. Run `claude login` and try again.");
	}
	return Buffer.from(raw, "utf8");
}
