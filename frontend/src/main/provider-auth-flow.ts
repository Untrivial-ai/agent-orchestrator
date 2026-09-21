import { type ChildProcess, spawn, type SpawnOptions } from "node:child_process";
import { constants as fsConstants } from "node:fs";
import { access, chmod, lstat, mkdtemp, mkdir, readdir, readFile, rm } from "node:fs/promises";
import os from "node:os";
import path from "node:path";

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
			await new Promise<void>((resolve, reject) => {
				const child = spawnAgentBinary(binary.path, ["-c", 'cli_auth_credentials_store="file"', "login"], {
					env: { ...process.env, PATH: binary.pathEnv, CODEX_HOME: codexHome },
					stdio: "ignore",
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
					reject(new Error('Codex could not start. Connect with the "API key" credential type instead.'));
				});
				child.once("exit", (code) => {
					cleanup();
					code === 0 ? resolve() : reject(new Error("Codex sign-in did not complete."));
				});
			});
			const authPath = await findCodexAuthFile(codexHome);
			if (!authPath) {
				throw new Error('Codex sign-in did not create a credential. Connect with the "API key" credential type instead.');
			}
			const authFile = await readFile(authPath);
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
			await new Promise<void>((resolve, reject) => {
				const child = spawnAgentBinary(binary.path, ["setup-token"], {
					env: { ...process.env, PATH: binary.pathEnv, CLAUDE_CONFIG_DIR: pending },
					stdio: ["ignore", "pipe", "pipe"],
				});
				const capture = (chunk: Buffer) => {
					if (captured.length <= MAX_AUTH_DOCUMENT_BYTES) captured += chunk.toString();
				};
				child.stdout?.on("data", capture);
				child.stderr?.on("data", capture);

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
					reject(new Error('Claude Code could not start. Connect with the "API key" credential type instead.'));
				});
				child.once("exit", (code) => {
					cleanup();
					code === 0 ? resolve() : reject(new Error("Claude sign-in did not complete."));
				});
			});

			// The token normally arrives on stdout; fall back to any file setup-token
			// wrote into the isolated config dir so a storage change cannot break this.
			const secret = extractClaudeOAuthToken(captured) ?? (await readClaudeOAuthTokenFromDir(pending));
			if (!secret) {
				throw new Error('Claude sign-in did not return a token. Connect with the "API key" credential type instead.');
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
