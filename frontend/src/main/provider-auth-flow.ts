import { spawn } from "node:child_process";
import { chmod, mkdtemp, mkdir, readFile, rm } from "node:fs/promises";
import { createServer, type Server } from "node:http";
import path from "node:path";
import { shell } from "electron";
import crypto from "node:crypto";

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

// GitHub OAuth scopes requested by Agent Orchestrator:
//   repo        – full control of public and private repos (clone, push, pull, PRs, issues, hooks)
//   read:org    – read org membership and team membership
//   repo_hook   – full control of repo webhooks (needed for some cloud features)
const GITHUB_OAUTH_SCOPES = "repo read:org repo_hook";

const GITHUB_CALLBACK_HTML = (title: string, body: string): string =>
	`<!doctype html><meta charset="utf-8"><title>${title}</title>` +
	`<body style="font:15px -apple-system,system-ui,sans-serif;max-width:32rem;margin:15vh auto;padding:0 1.5rem;color:#111">` +
	`<h1 style="font-size:1.25rem">${title}</h1><p style="color:#555">${body}</p></body>`;

const githubAuthFlow: ProviderAuthFlow = {
	provider: "github",
	async authenticate(_dataDir: string, signal?: AbortSignal): Promise<ProviderAuthCredential> {
		const clientId = process.env.AO_GITHUB_OAUTH_CLIENT_ID?.trim();
		if (!clientId) {
			throw new Error(
				"GitHub OAuth is not configured. Set AO_GITHUB_OAUTH_CLIENT_ID in your environment, or use a Personal Access Token instead.",
			);
		}
		const state = crypto.randomBytes(16).toString("hex");
		let server: Server | null = null;

		return new Promise<ProviderAuthCredential>((resolve, reject) => {
			const timeout = setTimeout(() => {
				server?.close();
				reject(new Error("GitHub sign-in timed out after 5 minutes."));
			}, 5 * 60 * 1000);

			const cleanup = () => {
				clearTimeout(timeout);
				signal?.removeEventListener("abort", onAbort);
			};

			const onAbort = () => {
				server?.close();
				cleanup();
				reject(new Error("GitHub sign-in was cancelled."));
			};
			if (signal?.aborted) return onAbort();
			signal?.addEventListener("abort", onAbort, { once: true });

			server = createServer((req, res) => {
				const url = new URL(req.url ?? "/", "http://127.0.0.1");
				if (url.pathname !== "/callback") {
					res.writeHead(404, { "Content-Type": "text/plain" });
					res.end("Not found");
					return;
				}
				const errorParam = url.searchParams.get("error");
				if (errorParam) {
					cleanup();
					server?.close();
					reject(new Error(url.searchParams.get("error_description") || `GitHub sign-in failed: ${errorParam}`));
					res.writeHead(200, { "Content-Type": "text/html; charset=utf-8" });
					res.end(GITHUB_CALLBACK_HTML("Sign-in failed", "Return to Agent Orchestrator and try signing in again."));
					return;
				}

				const code = url.searchParams.get("code");
				const returnedState = url.searchParams.get("state");
				if (!code || returnedState !== state) {
					cleanup();
					server?.close();
					reject(new Error("GitHub sign-in callback is invalid."));
					res.writeHead(400, { "Content-Type": "text/html; charset=utf-8" });
					res.end(GITHUB_CALLBACK_HTML("Sign-in failed", "Return to Agent Orchestrator and try signing in again."));
					return;
				}

				void (async () => {
					try {
						const clientSecret = process.env.AO_GITHUB_OAUTH_CLIENT_SECRET?.trim();
						if (!clientSecret) throw new Error("GitHub OAuth client secret is not configured.");

						const tokenRes = await fetch("https://github.com/login/oauth/access_token", {
							method: "POST",
							headers: {
								Accept: "application/json",
								"Content-Type": "application/json",
								"User-Agent": "Agent-Orchestrator",
							},
							body: JSON.stringify({
								client_id: clientId,
								client_secret: clientSecret,
								code,
							}),
						});
						if (!tokenRes.ok) throw new Error(`GitHub token exchange failed (HTTP ${tokenRes.status}).`);
						const tokenBody = (await tokenRes.json()) as Record<string, unknown>;
						if (typeof tokenBody.error === "string") {
							throw new Error((tokenBody.error_description as string) || `GitHub OAuth error: ${tokenBody.error}`);
						}
						const accessToken = tokenBody.access_token;
						if (typeof accessToken !== "string" || !accessToken) {
							throw new Error("GitHub did not return an access token.");
						}

						const userRes = await fetch("https://api.github.com/user", {
							headers: { Authorization: `Bearer ${accessToken}`, "User-Agent": "Agent-Orchestrator" },
						});
						if (!userRes.ok) throw new Error("GitHub token verification failed.");
						const user = (await userRes.json()) as { login?: string };
						const login = user.login || "unknown";

						cleanup();
						server?.close();
						res.writeHead(200, { "Content-Type": "text/html; charset=utf-8" });
						res.end(GITHUB_CALLBACK_HTML(
							"Signed in to Agent Orchestrator",
							`Authenticated as <strong>${login}</strong>. You can close this tab and return to Agent Orchestrator.`,
						));
						resolve({ provider: "github", credentialType: "access_token", secret: accessToken });
					} catch (err) {
						cleanup();
						server?.close();
						reject(err instanceof Error ? err : new Error(String(err)));
						res.writeHead(400, { "Content-Type": "text/html; charset=utf-8" });
						res.end(GITHUB_CALLBACK_HTML("Sign-in failed", "Return to Agent Orchestrator and try signing in again."));
					}
				})();
			});

			server.listen(0, "127.0.0.1", () => {
				const addr = server!.address();
				if (typeof addr === "string" || addr === null) {
					cleanup();
					server?.close();
					reject(new Error("Failed to start local callback server."));
					return;
				}
				const port = addr.port;
				const redirectUri = `http://127.0.0.1:${port}/callback`;
				const authUrl =
					`https://github.com/login/oauth/authorize` +
					`?client_id=${encodeURIComponent(clientId)}` +
					`&redirect_uri=${encodeURIComponent(redirectUri)}` +
					`&scope=${encodeURIComponent(GITHUB_OAUTH_SCOPES)}` +
					`&state=${encodeURIComponent(state)}` +
					`&prompt=consent`;
				void shell.openExternal(authUrl);
			});

			server.on("error", (err) => {
				cleanup();
				server?.close();
				reject(err);
			});
		});
	},
};

const flows = new Map<string, ProviderAuthFlow>([
	[codexAuthFlow.provider, codexAuthFlow],
	[claudeAuthFlow.provider, claudeAuthFlow],
	[githubAuthFlow.provider, githubAuthFlow],
]);

export function providerAuthFlow(provider: string): ProviderAuthFlow {
	const flow = flows.get(provider);
	if (!flow) throw new Error(`No browser authentication flow is available for ${provider}.`);
	return flow;
}
