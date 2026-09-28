import { type ChildProcess, spawn, type SpawnOptions } from "node:child_process";
import { constants as fsConstants } from "node:fs";
import { access, chmod, lstat, mkdtemp, mkdir, readdir, readFile, rm } from "node:fs/promises";
import { createServer, type Server } from "node:http";
import os from "node:os";
import path from "node:path";
import { shell } from "electron";
import crypto from "node:crypto";

const MAX_AUTH_DOCUMENT_BYTES = 64 << 10;

// A Claude Code setup token. `claude setup-token` emits one of these for use in
// headless/cloud contexts; matching on the token shape (rather than a specific
// storage file) keeps extraction stable across claude versions, which have moved
// the credential between settings.json, .credentials.json, and the OS keychain.
const CLAUDE_OAUTH_TOKEN_PATTERN = /sk-ant-oat[0-9A-Za-z_-]{10,}/;

export function extractClaudeOAuthToken(text: string): string | null {
	const match = text.match(CLAUDE_OAUTH_TOKEN_PATTERN);
	return match ? match[0] : null;
}

// Fallback for claude builds that write the setup token to a file instead of (or
// in addition to) stdout. Scans only the per-login isolated config dir, never the
// user's real ~/.claude, for a token in any file it created.
export async function readClaudeOAuthTokenFromDir(dir: string): Promise<string | null> {
	let entries: string[];
	try {
		entries = await readdir(dir);
	} catch {
		return null;
	}
	for (const entry of entries) {
		const full = path.join(dir, entry);
		try {
			const stat = await lstat(full);
			if (!stat.isFile() || stat.size === 0 || stat.size > MAX_AUTH_DOCUMENT_BYTES) continue;
			const token = extractClaudeOAuthToken(await readFile(full, "utf8"));
			if (token) return token;
		} catch {
			// unreadable entry; keep scanning
		}
	}
	return null;
}

// Codex writes auth.json to CODEX_HOME (forced via cli_auth_credentials_store).
// Locate it defensively: the direct path first, then a shallow scan, so a codex
// version that nests the store does not reintroduce a "no such file" failure.
export async function findCodexAuthFile(codexHome: string): Promise<string | null> {
	const direct = path.join(codexHome, "auth.json");
	try {
		await access(direct, fsConstants.R_OK);
		return direct;
	} catch {
		// fall through to a shallow scan
	}
	let entries: string[];
	try {
		entries = await readdir(codexHome);
	} catch {
		return null;
	}
	for (const entry of entries) {
		if (entry === "auth.json") return path.join(codexHome, entry);
		const nested = path.join(codexHome, entry, "auth.json");
		try {
			await access(nested, fsConstants.R_OK);
			return nested;
		} catch {
			// not here; keep scanning
		}
	}
	return null;
}

// A macOS app launched from Finder/Dock inherits a minimal PATH
// (/usr/bin:/bin:/usr/sbin:/sbin), not the user's shell PATH, so an agent CLI
// installed by Homebrew, npm, or an install script is invisible to a bare
// spawn("claude"). Search these common install locations in addition to the
// inherited PATH before giving up. (A dev app started from a terminal already
// inherits the full PATH, which is why the login flow works there.)
function knownBinDirs(): string[] {
	const home = os.homedir();
	return [
		path.join(home, ".local", "bin"),
		"/opt/homebrew/bin",
		"/usr/local/bin",
		path.join(home, ".npm-global", "bin"),
		path.join(home, ".bun", "bin"),
		path.join(home, ".volta", "bin"),
		"/opt/local/bin",
		"/usr/bin",
		"/bin",
	];
}

export async function firstExecutable(name: string, dirs: readonly string[]): Promise<string | null> {
	const names = process.platform === "win32" ? [`${name}.cmd`, `${name}.exe`, name] : [name];
	const seen = new Set<string>();
	for (const dir of dirs) {
		if (!dir || seen.has(dir)) continue;
		seen.add(dir);
		for (const candidateName of names) {
			const candidate = path.join(dir, candidateName);
			try {
				await access(candidate, fsConstants.X_OK);
				return candidate;
			} catch {
				// keep looking
			}
		}
	}
	return null;
}

// Best-effort resolution through the user's login shell, covering PATHs set up
// by version managers (nvm, asdf) that neither the inherited PATH nor the static
// list above can know. Bounded by a hard timeout so a slow or prompting shell
// can never hang the login flow; any failure just falls through to "not found".
function resolveViaLoginShell(name: string): Promise<string | null> {
	if (process.platform === "win32") return Promise.resolve(null);
	const shell = process.env.SHELL || "/bin/zsh";
	return new Promise((resolve) => {
		let out = "";
		let settled = false;
		const child = spawn(shell, ["-lic", `command -v ${name} 2>/dev/null`], {
			stdio: ["ignore", "pipe", "ignore"],
		});
		const finish = (value: string | null) => {
			if (settled) return;
			settled = true;
			clearTimeout(timer);
			try {
				child.kill();
			} catch {
				// already gone
			}
			resolve(value);
		};
		const timer = setTimeout(() => finish(null), 4000);
		child.stdout.on("data", (chunk: Buffer) => {
			out += chunk.toString();
		});
		child.once("error", () => finish(null));
		child.once("exit", () => {
			const resolved = out
				.split("\n")
				.map((line) => line.trim())
				.filter(Boolean)
				.pop();
			finish(resolved && path.isAbsolute(resolved) ? resolved : null);
		});
	});
}

// Resolve an agent CLI to an absolute path and build a PATH the spawned CLI can
// use to find its own helpers (node, git). Returns null when the binary cannot
// be located anywhere, so the caller can surface an actionable error.
async function resolveProviderBinary(name: string): Promise<{ path: string; pathEnv: string } | null> {
	const inherited = (process.env.PATH ?? "").split(path.delimiter);
	let resolved = await firstExecutable(name, [...inherited, ...knownBinDirs()]);
	if (!resolved) resolved = await resolveViaLoginShell(name);
	if (!resolved) return null;
	const pathEnv = [path.dirname(resolved), ...knownBinDirs(), ...inherited]
		.filter(Boolean)
		.join(path.delimiter);
	return { path: resolved, pathEnv };
}

// Spawn a resolved absolute agent-CLI path. Windows needs a shell to execute a
// .cmd, but `shell:true` does not quote the program, so an absolute path with a
// space (C:\Program Files\..., or a username with a space) would be split at the
// space and fail to start. Quote it ourselves there. On POSIX the absolute path
// is spawned directly with no shell, so no quoting is needed.
function spawnAgentBinary(binaryPath: string, args: readonly string[], options: SpawnOptions): ChildProcess {
	const useShell = process.platform === "win32";
	return spawn(useShell ? `"${binaryPath}"` : binaryPath, [...args], { ...options, shell: useShell });
}

export interface ProviderAuthCredential {
	provider: string;
	credentialType: string;
	secret: string;
	// Populated for expiring GitHub App OAuth tokens: the refresh token and
	// lifetimes (seconds) GitHub returns alongside the access token, so the
	// daemon can renew the token without a manual reconnect.
	refreshToken?: string;
	expiresIn?: number;
	refreshTokenExpiresIn?: number;
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
			const binary = await resolveProviderBinary("codex");
			if (!binary) {
				throw new Error(
					'Codex is not installed or could not be found. Install the Codex CLI, or connect with the "API key" credential type instead.',
				);
			}
			// Read + validate codex's auth.json; returns the secret or null if it is
			// not yet present/valid. Used both while polling and on process exit.
			const readCodexCredential = async (): Promise<string | null> => {
				const authPath = await findCodexAuthFile(codexHome).catch(() => null);
				if (!authPath) return null;
				let authFile: Buffer;
				try {
					authFile = await readFile(authPath);
				} catch {
					return null;
				}
				if (authFile.byteLength === 0 || authFile.byteLength > MAX_AUTH_DOCUMENT_BYTES) return null;
				const secret = authFile.toString("utf8");
				try {
					const document: unknown = JSON.parse(secret);
					if (typeof document !== "object" || document === null || Array.isArray(document)) return null;
				} catch {
					return null;
				}
				return secret;
			};

			// Resolve as soon as codex writes auth.json, not when the CLI exits: like
			// `claude setup-token`, `codex login` can leave the process idling after
			// the browser round-trip, which would otherwise time out at 5 minutes even
			// though a valid credential is already on disk.
			const secret = await new Promise<string>((resolve, reject) => {
				const child = spawnAgentBinary(binary.path, ["-c", 'cli_auth_credentials_store="file"', "login"], {
					env: { ...process.env, PATH: binary.pathEnv, CODEX_HOME: codexHome },
					stdio: "ignore",
				});

				let settled = false;
				let timeout: NodeJS.Timeout;
				let poll: NodeJS.Timeout;
				const cleanup = () => {
					clearTimeout(timeout);
					clearInterval(poll);
					signal?.removeEventListener("abort", onAbort);
					try {
						child.kill();
					} catch {
						// already gone
					}
				};
				const succeed = (value: string) => {
					if (settled) return;
					settled = true;
					cleanup();
					resolve(value);
				};
				const fail = (err: Error) => {
					if (settled) return;
					settled = true;
					cleanup();
					reject(err);
				};

				poll = setInterval(() => {
					void readCodexCredential().then((value) => {
						if (value) succeed(value);
					});
				}, 1000);

				const onAbort = () => fail(new Error("Login was cancelled."));
				if (signal?.aborted) return onAbort();
				signal?.addEventListener("abort", onAbort);

				timeout = setTimeout(() => fail(new Error("Login timed out after 5 minutes.")), 5 * 60 * 1000);

				child.once("error", () =>
					fail(new Error('Codex could not start. Connect with the "API key" credential type instead.')),
				);
				child.once("exit", (code) => {
					void readCodexCredential().then((value) => {
						if (value) return succeed(value);
						fail(
							new Error(
								code === 0
									? 'Codex sign-in did not create a credential. Connect with the "API key" credential type instead.'
									: "Codex sign-in did not complete.",
							),
						);
					});
				});
			});
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
			const binary = await resolveProviderBinary("claude");
			if (!binary) {
				throw new Error(
					'Claude Code is not installed or could not be found. Install Claude Code, or connect with the "API key" credential type instead.',
				);
			}
			// Use `claude setup-token`, the purpose-built command for exporting a
			// long-lived token, instead of `auth login` + scraping a version-specific
			// credential file. Modern claude stores the login credential in the OS
			// keychain, so no file is written and the old settings.json read fails.
			// setup-token opens the browser for OAuth and, on completion, emits the
			// token; capture stdout/stderr so we can read it.
			let captured = "";
			// Resolve as soon as the setup token MATERIALIZES, not when the CLI exits.
			// `claude setup-token` emits the sk-ant-oat token the instant OAuth
			// completes, but recent builds do not reliably exit afterward (they can
			// idle holding the browser session open). Waiting on process `exit` then
			// timed out at 5 minutes even though the token was already in hand - the
			// exact failure the browser-login button hit. So watch stdout AND the
			// isolated config dir, and finish the moment a token appears; the process
			// `exit` becomes only the terminal-error signal.
			const secret = await new Promise<string>((resolve, reject) => {
				const child = spawnAgentBinary(binary.path, ["setup-token"], {
					env: { ...process.env, PATH: binary.pathEnv, CLAUDE_CONFIG_DIR: pending },
					stdio: ["ignore", "pipe", "pipe"],
				});

				let settled = false;
				let timeout: NodeJS.Timeout;
				let poll: NodeJS.Timeout;
				const cleanup = () => {
					clearTimeout(timeout);
					clearInterval(poll);
					signal?.removeEventListener("abort", onAbort);
					try {
						child.kill();
					} catch {
						// already gone
					}
				};
				const succeed = (token: string) => {
					if (settled) return;
					settled = true;
					cleanup();
					resolve(token);
				};
				const fail = (err: Error) => {
					if (settled) return;
					settled = true;
					cleanup();
					reject(err);
				};

				const capture = (chunk: Buffer) => {
					if (captured.length <= MAX_AUTH_DOCUMENT_BYTES) captured += chunk.toString();
					const token = extractClaudeOAuthToken(captured);
					if (token) succeed(token);
				};
				child.stdout?.on("data", capture);
				child.stderr?.on("data", capture);

				// Some builds write the token to a file in the isolated config dir
				// instead of stdout; poll for it so that path resolves promptly too.
				poll = setInterval(() => {
					void readClaudeOAuthTokenFromDir(pending)
						.then((token) => {
							if (token) succeed(token);
						})
						.catch(() => {});
				}, 1000);

				const onAbort = () => fail(new Error("Login was cancelled."));
				if (signal?.aborted) return onAbort();
				signal?.addEventListener("abort", onAbort);

				timeout = setTimeout(() => fail(new Error("Login timed out after 5 minutes.")), 5 * 60 * 1000);

				child.once("error", () =>
					fail(new Error('Claude Code could not start. Connect with the "API key" credential type instead.')),
				);
				child.once("exit", (code) => {
					// Last-chance check for a token the CLI wrote just before exiting,
					// then treat the exit as terminal.
					const token = extractClaudeOAuthToken(captured);
					if (token) return succeed(token);
					void readClaudeOAuthTokenFromDir(pending)
						.then((fileToken) => {
							if (fileToken) return succeed(fileToken);
							fail(
								new Error(
									code === 0
										? 'Claude sign-in did not return a token. Connect with the "API key" credential type instead.'
										: "Claude sign-in did not complete.",
								),
							);
						})
						.catch(() => fail(new Error("Claude sign-in did not complete.")));
				});
			});
			return { provider: "claude-code", credentialType: "oauth_token", secret };
		} finally {
			await rm(pending, { recursive: true, force: true });
		}
	},
};

// Cursor documents no third-party OAuth flow and no token-bearing CLI output —
// only CURSOR_API_KEY, which the control plane already validates. Rather than
// scrape `cursor-agent login` for a credential file AO cannot reliably locate,
// drive Cursor's own SDK browser flow, which mints a named, expiring user API key
// and returns it directly.
//
// The key is never written to this machine: `store: null` keeps the secret out of
// the data dir and it goes straight to the control plane, like every other
// provider's credential. `openBrowser` is supplied rather than left to the SDK so
// the login URL opens through Electron's shell, the supported way to leave a
// sandboxed app.
const CURSOR_LOGIN_API_KEY_NAME = "Agent Orchestrator";
const CURSOR_LOGIN_TIMEOUT_MS = 5 * 60 * 1000;

const cursorAuthFlow: ProviderAuthFlow = {
	provider: "cursor",
	async authenticate(_dataDir: string, signal?: AbortSignal): Promise<ProviderAuthCredential> {
		if (signal?.aborted) throw new Error("Login was cancelled.");
		// Loaded on demand so a browser login pulls the SDK in without costing every
		// app start, and so a resolution failure surfaces here — with a fallback the
		// user can act on — rather than at launch.
		const { Cursor } = await import("@cursor/sdk");
		const timeout = AbortSignal.timeout(CURSOR_LOGIN_TIMEOUT_MS);
		const active = signal ? AbortSignal.any([signal, timeout]) : timeout;
		let apiKey: string;
		try {
			({ apiKey } = await Cursor.auth.login({
				store: null,
				apiKeyName: CURSOR_LOGIN_API_KEY_NAME,
				openBrowser: (url) => void shell.openExternal(url),
				signal: active,
			}));
		} catch (error) {
			if (signal?.aborted) throw new Error("Login was cancelled.");
			if (timeout.aborted) throw new Error("Login timed out after 5 minutes.");
			throw new Error(
				`Cursor sign-in did not complete: ${error instanceof Error ? error.message : String(error)}. Connect with the "API key" credential type instead.`,
			);
		}
		if (!apiKey) {
			throw new Error('Cursor sign-in did not create an API key. Connect with the "API key" credential type instead.');
		}
		return { provider: "cursor", credentialType: "api_key", secret: apiKey };
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
						// GitHub App user tokens expire (8h default) and arrive with a
						// refresh token; keep it so the daemon can renew silently.
						const refreshToken = typeof tokenBody.refresh_token === "string" ? tokenBody.refresh_token : undefined;
						const expiresIn = typeof tokenBody.expires_in === "number" ? tokenBody.expires_in : undefined;
						const refreshTokenExpiresIn =
							typeof tokenBody.refresh_token_expires_in === "number" ? tokenBody.refresh_token_expires_in : undefined;

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
						resolve({ provider: "github", credentialType: "access_token", secret: accessToken, refreshToken, expiresIn, refreshTokenExpiresIn });
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
	[cursorAuthFlow.provider, cursorAuthFlow],
	[githubAuthFlow.provider, githubAuthFlow],
]);

export function providerAuthFlow(provider: string): ProviderAuthFlow {
	const flow = flows.get(provider);
	if (!flow) throw new Error(`No browser authentication flow is available for ${provider}.`);
	return flow;
}
