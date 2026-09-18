import { spawn } from "node:child_process";
import { chmod, mkdtemp, mkdir, readFile, rm } from "node:fs/promises";
import path from "node:path";

const MAX_AUTH_DOCUMENT_BYTES = 64 << 10;

export interface ProviderAuthCredential {
	provider: string;
	credentialType: string;
	secret: string;
}

export interface ProviderAuthFlow {
	provider: string;
	authenticate(dataDir: string, signal?: AbortSignal): Promise<ProviderAuthCredential>;
}

const codexAuthFlow: ProviderAuthFlow = {
	provider: "codex",
	async authenticate(dataDir: string, signal?: AbortSignal): Promise<ProviderAuthCredential> {
		// mkdtemp does not create its parent. Keep this temporary, credential-bearing
		// directory within AO's data root and private even on a fresh install.
		await mkdir(dataDir, { recursive: true, mode: 0o700 });
		await chmod(dataDir, 0o700);
		const pending = await mkdtemp(path.join(dataDir, "codex-cloud-login-"));
		const codexHome = path.join(pending, "home");
		try {
			await mkdir(codexHome, { recursive: true, mode: 0o700 });
			await chmod(codexHome, 0o700);
			await new Promise<void>((resolve, reject) => {
				const child = spawn("codex", ["-c", 'cli_auth_credentials_store="file"', "login"], {
					env: { ...process.env, CODEX_HOME: codexHome },
					stdio: "ignore",
					shell: process.platform === "win32",
				});
				
				let timeout: NodeJS.Timeout;
				const cleanup = () => {
					clearTimeout(timeout);
					signal?.removeEventListener("abort", onAbort);
				};

				const onAbort = () => {
					child.kill();
					cleanup();
					reject(new Error("Login was cancelled."));
				};

				if (signal?.aborted) return onAbort();
				signal?.addEventListener("abort", onAbort);

				timeout = setTimeout(() => {
					child.kill();
					cleanup();
					reject(new Error("Login timed out after 5 minutes."));
				}, 5 * 60 * 1000);

				child.once("error", () => {
					cleanup();
					reject(new Error("Codex is not installed or could not start."));
				});
				child.once("exit", (code) => {
					cleanup();
					code === 0 ? resolve() : reject(new Error("Codex sign-in did not complete."));
				});
			});
			const authFile = await readFile(path.join(codexHome, "auth.json"));
			if (authFile.byteLength === 0 || authFile.byteLength > MAX_AUTH_DOCUMENT_BYTES) {
				throw new Error("Codex did not create a valid authentication credential.");
			}
			const secret = authFile.toString("utf8");
			try {
				const document: unknown = JSON.parse(secret);
				if (typeof document !== "object" || document === null || Array.isArray(document)) throw new Error();
			} catch {
				throw new Error("Codex did not create a valid authentication credential.");
			}
			return { provider: "codex", credentialType: "auth_json", secret };
		} finally {
			await rm(pending, { recursive: true, force: true });
		}
	},
};

const claudeAuthFlow: ProviderAuthFlow = {
	provider: "claude-code",
	async authenticate(dataDir: string, signal?: AbortSignal): Promise<ProviderAuthCredential> {
		await mkdir(dataDir, { recursive: true, mode: 0o700 });
		await chmod(dataDir, 0o700);
		const pending = await mkdtemp(path.join(dataDir, "claude-cloud-login-"));
		try {
			await new Promise<void>((resolve, reject) => {
				const child = spawn("claude", ["auth", "login"], {
					env: { ...process.env, CLAUDE_CONFIG_DIR: pending },
					stdio: "ignore",
					shell: process.platform === "win32",
				});
				
				let timeout: NodeJS.Timeout;
				const cleanup = () => {
					clearTimeout(timeout);
					signal?.removeEventListener("abort", onAbort);
				};

				const onAbort = () => {
					child.kill();
					cleanup();
					reject(new Error("Login was cancelled."));
				};

				if (signal?.aborted) return onAbort();
				signal?.addEventListener("abort", onAbort);

				timeout = setTimeout(() => {
					child.kill();
					cleanup();
					reject(new Error("Login timed out after 5 minutes."));
				}, 5 * 60 * 1000);

				child.once("error", () => {
					cleanup();
					reject(new Error("Claude Code is not installed or could not start."));
				});
				child.once("exit", (code) => {
					cleanup();
					code === 0 ? resolve() : reject(new Error("Claude sign-in did not complete."));
				});
			});
			
			const authFile = await readFile(path.join(pending, "settings.json"));
			if (authFile.byteLength === 0 || authFile.byteLength > MAX_AUTH_DOCUMENT_BYTES) {
				throw new Error("Claude did not create a valid authentication credential.");
			}
			const secretData = authFile.toString("utf8");
			let secret = "";
			try {
				const document = JSON.parse(secretData) as Record<string, string>;
				secret = document.primaryToken || document.oauthToken || document.token || "";
				if (!secret || typeof secret !== "string") throw new Error();
			} catch {
				throw new Error("Claude did not create a valid authentication credential or token was missing.");
			}
			return { provider: "claude-code", credentialType: "oauth_token", secret };
		} finally {
			await rm(pending, { recursive: true, force: true });
		}
	},
};

const flows = new Map<string, ProviderAuthFlow>([
	[codexAuthFlow.provider, codexAuthFlow],
	[claudeAuthFlow.provider, claudeAuthFlow],
]);

export function providerAuthFlow(provider: string): ProviderAuthFlow {
	const flow = flows.get(provider);
	if (!flow) throw new Error(`No browser authentication flow is available for ${provider}.`);
	return flow;
}
