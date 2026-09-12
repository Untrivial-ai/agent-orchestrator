import { spawn } from "node:child_process";
import { createHash, randomBytes } from "node:crypto";
import { access, chmod, mkdir, mkdtemp, readFile, readdir, readlink, rm, stat, symlink, utimes, writeFile } from "node:fs/promises";
import path from "node:path";
import { AgentBrowserCDPBridge, type AgentBrowserTargetProvider } from "./agent-browser-cdp-bridge";

const MAX_ARGUMENTS = 100;
const MAX_ARGUMENT_CHARS = 16_384;
const MAX_OUTPUT_BYTES = 1 << 20;
const MAX_SCREENSHOT_BYTES = 5 << 20;
const COMMAND_TIMEOUT_MS = 60_000;
const CLOSE_TIMEOUT_MS = 10_000;
const BRIDGE_CLOSE_TIMEOUT_MS = 5_000;
export const BROWSER_RUNTIME_RECLAIM_GRACE_MS = 15 * 60_000;
const RUNTIME_OWNER_MARKER = "AO_BROWSER_RUNTIME_V1";
const RUNTIME_OWNER_FILE = "owner.json";
const RUNTIME_ROOT_PATTERN = /^(?:run-(\d+)-[0-9a-f]{12}|r-[0-9a-f]{10})$/;
const SOCKET_ALIAS_PATTERN = /^ao-br-(\d+)-[0-9a-f]{12}$/;
const STREAM_ALREADY_DISABLED_MESSAGE = "Streaming is not enabled for this session";
/** Agent Browser's Unix preflight uses the macOS-sized 103-byte limit on all Unix builds. */
export const AGENT_BROWSER_UNIX_SOCKET_PATH_MAX_BYTES = 103;

const NATIVE_ENV_ALLOWLIST = new Set([
	"path",
	"pathext",
	"systemroot",
	"windir",
	"comspec",
	"temp",
	"tmp",
	"tmpdir",
	"lang",
	"lc_all",
	"lc_ctype",
	"term",
	"colorterm",
	"no_color",
	"force_color",
]);

const ALLOWED_COMMANDS = new Set([
	"open",
	"snapshot",
	"click",
	"dblclick",
	"focus",
	"type",
	"fill",
	"press",
	"keyboard",
	"keydown",
	"keyup",
	"hover",
	"select",
	"check",
	"uncheck",
	"scroll",
	"scrollintoview",
	"drag",
	"screenshot",
	"wait",
	"get",
	"is",
	"find",
	"tab",
	"frame",
	"dialog",
	"console",
	"errors",
	"highlight",
	"diff",
]);

const FORBIDDEN_FLAGS = [
	"--cdp",
	"--auto-connect",
	"--session",
	"--namespace",
	"--profile",
	"--state",
	"--restore",
	"--executable-path",
	"--extension",
	"--init-script",
	"--args",
	"--headers",
	"--proxy",
	"--plugin",
	"--allowed-domains",
];

export type AgentBrowserRunResult = {
	command: string;
	stdout: string;
	stderr: string;
	exitCode: number;
	untrustedExternalContent: true;
};

export type NativeProcessResult = Pick<AgentBrowserRunResult, "stdout" | "stderr" | "exitCode">;

export type AgentBrowserRuntimeOptions = {
	binaryPath: string;
	dataDir: string;
	/** Internal seam for testing path validation; production derives a per-run private directory. */
	socketDir?: string;
	log?: (message: string) => void;
	/** Internal seams used by lifecycle tests; production uses the defaults. */
	platform?: NodeJS.Platform;
	processRunner?: NativeProcessRunner;
	bridgeFactory?: (provider: AgentBrowserTargetProvider) => AgentBrowserBridge;
	processAlive?: (pid: number) => boolean;
};

export type AgentBrowserJSONResult = Record<string, unknown>;

export type AgentBrowserBridge = Pick<AgentBrowserCDPBridge, "start" | "close">;

export type NativeProcessRunner = (
	binaryPath: string,
	args: string[],
	environment: NodeJS.ProcessEnv,
	signal?: AbortSignal,
	timeoutMs?: number,
) => Promise<NativeProcessResult>;

type SessionRuntime = {
	bridge: AgentBrowserBridge;
	endpoint: string;
	namespace: string;
	runtimeDir: string;
	configPath: string;
};

type RuntimeOwner = {
	marker: typeof RUNTIME_OWNER_MARKER;
	pid: number;
	startedAt: string;
	token: string;
};

export class AgentBrowserRuntime {
	private readonly sessions = new Map<string, SessionRuntime>();
	private readonly initializing = new Map<string, Promise<SessionRuntime>>();
	private readonly closing = new Map<string, Promise<void>>();
	private readonly log: (message: string) => void;
	private readonly processRunner: NativeProcessRunner;
	private readonly bridgeFactory: (provider: AgentBrowserTargetProvider) => AgentBrowserBridge;
	private readonly processAlive: (pid: number) => boolean;
	private readonly platform: NodeJS.Platform;
	private readonly socketDirOverride?: string;
	private socketDir: string | null = null;
	private socketDirAlias: string | null = null;
	private runtimeRootPromise: Promise<void> | null = null;
	private runtimeRoot: string | null = null;
	private disposed = false;
	private disposePromise: Promise<void> | null = null;

	constructor(private readonly options: AgentBrowserRuntimeOptions) {
		this.log = options.log ?? (() => undefined);
		this.processRunner = options.processRunner ?? runNativeProcess;
		this.bridgeFactory = options.bridgeFactory ?? ((provider) => new AgentBrowserCDPBridge(provider));
		this.processAlive = options.processAlive ?? defaultProcessAlive;
		this.platform = options.platform ?? process.platform;
		this.socketDirOverride = options.socketDir;
	}

	/** Reclaim confirmed-dead run roots before the browser UI is created. */
	async prepare(): Promise<void> {
		if (this.disposed) throw runtimeError("AGENT_BROWSER_RUNTIME_CLOSED", "Browser automation runtime is closed");
		if (this.platform !== "win32") {
			await scavengeBrowserSocketAliases(this.options.dataDir, this.processAlive, this.log);
		}
		await scavengeBrowserRuntime(this.options.dataDir, this.processAlive, this.log);
	}

	async run(
		sessionId: string,
		args: string[],
		provider: AgentBrowserTargetProvider,
		signal?: AbortSignal,
	): Promise<AgentBrowserRunResult> {
		if (this.disposed) throw runtimeError("AGENT_BROWSER_RUNTIME_CLOSED", "Browser automation runtime is closed");
		await this.assertBinary();
		validateAgentBrowserArguments(args);
		const runtime = await this.ensureSession(sessionId, provider);
		await this.touchRuntimeRoot();
		const environment = this.environment(runtime);
		// The native daemon can expire and be recreated independently while this
		// AO session runtime remains alive. Its replacement starts streaming by
		// default, so reassert the input-surface policy immediately before every
		// command. agent-browser reports "already disabled" as a non-zero result;
		// that state is the policy we need, while every other failure remains fatal.
		const disabled = await this.processRunner(
			this.options.binaryPath,
			["stream", "disable"],
			environment,
			signal,
		);
		if (disabled.exitCode !== 0 && !streamAlreadyDisabled(disabled)) {
			throw runtimeError(
				"AGENT_BROWSER_START_FAILED",
				disabled.stderr.trim() || "Unable to disable agent-browser streaming",
			);
		}
		const result = await this.processRunner(this.options.binaryPath, args, environment, signal);
		if (result.exitCode !== 0) {
			throw runtimeError(
				"AGENT_BROWSER_COMMAND_FAILED",
				result.stderr.trim() || result.stdout.trim() || `agent-browser exited with code ${result.exitCode}`,
			);
		}
		return { ...result, command: args[0], untrustedExternalContent: true };
	}

	async runAction(
		sessionId: string,
		action: string,
		args: Record<string, unknown>,
		provider: AgentBrowserTargetProvider,
		signal?: AbortSignal,
	): Promise<AgentBrowserJSONResult> {
		const nativeArgs = nativeArgumentsForAction(action, args);
		const result = await this.run(sessionId, [...nativeArgs, "--json"], provider, signal);
		return parseAgentBrowserJSON(result.stdout);
	}

	async screenshot(
		sessionId: string,
		provider: AgentBrowserTargetProvider,
		signal?: AbortSignal,
	): Promise<{ data: string; width: number; height: number; untrustedExternalContent: true }> {
		const runtime = await this.ensureSession(sessionId, provider);
		await this.touchRuntimeRoot();
		const directory = await mkdtemp(path.join(runtime.runtimeDir, "screenshot-"));
		const target = path.join(directory, "screenshot.png");
		try {
			await this.run(sessionId, ["screenshot", target, "--json"], provider, signal);
			const image = await readFile(target);
			if (image.length > MAX_SCREENSHOT_BYTES) {
				throw runtimeError("AGENT_BROWSER_OUTPUT_TOO_LARGE", "Browser screenshot exceeded AO's size limit");
			}
			const { width, height } = pngDimensions(image);
			return { data: image.toString("base64"), width, height, untrustedExternalContent: true };
		} finally {
			await removePath(directory, this.log, "screenshot directory");
		}
	}

	async closeSession(sessionId: string): Promise<void> {
		const existing = this.closing.get(sessionId);
		if (existing) return existing;
		const cleanup = this.closeSessionInternal(sessionId);
		this.closing.set(sessionId, cleanup);
		try {
			await cleanup;
		} finally {
			if (this.closing.get(sessionId) === cleanup) this.closing.delete(sessionId);
			await this.removeRuntimeRootIfIdle();
		}
	}

	async dispose(): Promise<void> {
		if (this.disposePromise) return this.disposePromise;
		this.disposed = true;
		this.disposePromise = (async () => {
			const sessionIds = new Set([...this.sessions.keys(), ...this.initializing.keys(), ...this.closing.keys()]);
			await Promise.all([...sessionIds].map((sessionId) => this.closeSession(sessionId)));
			await Promise.all([...this.closing.values()]);
			await this.removeRuntimeRootIfIdle();
		})();
		return this.disposePromise;
	}

	private async ensureSession(
		sessionId: string,
		provider: AgentBrowserTargetProvider,
	): Promise<SessionRuntime> {
		if (this.disposed) throw runtimeError("AGENT_BROWSER_RUNTIME_CLOSED", "Browser automation runtime is closed");
		const existing = this.sessions.get(sessionId);
		if (existing) return existing;
		if (this.closing.has(sessionId)) {
			throw runtimeError("AGENT_BROWSER_SESSION_CLOSING", "Browser session is closing");
		}
		const pending = this.initializing.get(sessionId);
		if (pending) return pending;
		const creation = this.createSession(sessionId, provider);
		this.initializing.set(sessionId, creation);
		try {
			const runtime = await creation;
			if (this.disposed || this.closing.has(sessionId)) {
				await this.closeRuntime(sessionId, runtime);
				throw runtimeError("AGENT_BROWSER_SESSION_CLOSING", "Browser session is closing");
			}
			this.sessions.set(sessionId, runtime);
			return runtime;
		} finally {
			if (this.initializing.get(sessionId) === creation) this.initializing.delete(sessionId);
			await this.removeRuntimeRootIfIdle();
		}
	}

	private async createSession(sessionId: string, provider: AgentBrowserTargetProvider): Promise<SessionRuntime> {
		await this.ensureRuntimeRoot();
		const namespace = `${sessionNamespace(sessionId)}-${randomBytes(6).toString("hex")}`;
		assertAgentBrowserSocketPath(this.socketDir!, namespace, this.platform);
		const bridge = this.bridgeFactory(provider);
		let runtimeDir: string | undefined;
		try {
			const endpoint = await bridge.start();
			runtimeDir = path.join(this.runtimeRoot!, namespace);
			const configPath = path.join(runtimeDir, "config.json");
			await mkdir(runtimeDir, { recursive: true, mode: 0o700 });
			await writeFile(configPath, "{}\n", "utf8");
			return { bridge, endpoint, namespace, runtimeDir, configPath };
		} catch (error) {
			try {
				await closeBridgeWithTimeout(bridge);
			} catch (closeError) {
				this.log(`agent-browser bridge cleanup failed during startup: ${String(closeError)}`);
			}
			if (runtimeDir) await removePath(runtimeDir, this.log, "failed session directory");
			throw error;
		}
	}

	private async closeSessionInternal(sessionId: string): Promise<void> {
		const pending = this.initializing.get(sessionId);
		if (pending) {
			try {
				await pending;
			} catch {
				return;
			}
		}
		const runtime = this.sessions.get(sessionId);
		if (!runtime) return;
		this.sessions.delete(sessionId);
		await this.closeRuntime(sessionId, runtime);
	}

	private async closeRuntime(sessionId: string, runtime: SessionRuntime): Promise<void> {
		try {
			await this.processRunner(
				this.options.binaryPath,
				["close"],
				this.environment(runtime),
				undefined,
				CLOSE_TIMEOUT_MS,
			);
		} catch (error) {
			this.log(`agent-browser close failed for ${sessionId}: ${String(error)}`);
		}
		try {
			await closeBridgeWithTimeout(runtime.bridge);
		} catch (error) {
			this.log(`agent-browser bridge close failed for ${sessionId}: ${String(error)}`);
		} finally {
			await removePath(runtime.runtimeDir, this.log, `session directory for ${sessionId}`);
		}
	}

	private async ensureRuntimeRoot(): Promise<void> {
		if (this.runtimeRootPromise) return this.runtimeRootPromise;
		this.runtimeRootPromise = (async () => {
			await this.prepare();
			await ensurePrivateDirectory(this.options.dataDir);
			const rootName = `r-${randomBytes(5).toString("hex")}`;
			const root = path.join(this.options.dataDir, rootName);
			await mkdir(root, { recursive: false, mode: 0o700 });
			const owner: RuntimeOwner = {
				marker: RUNTIME_OWNER_MARKER,
				pid: process.pid,
				startedAt: new Date().toISOString(),
				token: randomBytes(16).toString("hex"),
			};
			try {
				await writeFile(path.join(root, RUNTIME_OWNER_FILE), `${JSON.stringify(owner)}\n`, {
					encoding: "utf8",
					flag: "wx",
				});
				const actualSocketDir = this.socketDirOverride ?? path.join(root, "s");
				await ensurePrivateDirectory(actualSocketDir);
				let socketDir = actualSocketDir;
				let socketDirAlias: string | null = null;
				if (!this.socketDirOverride && process.platform !== "win32") {
					// agent-browser applies the macOS 103-byte Unix socket limit on
					// every Unix build. Keep the real runtime state under ~/.ao, but
					// hand the native process a short symlink path whose length does
					// not depend on the user's home directory or username.
					socketDirAlias = path.join("/tmp", `ao-br-${process.pid}-${randomBytes(6).toString("hex")}`);
					await symlink(actualSocketDir, socketDirAlias, "dir");
					socketDir = socketDirAlias;
				}
				this.socketDir = socketDir;
				this.socketDirAlias = socketDirAlias;
			} catch (error) {
				if (this.socketDirAlias) {
					await removePath(this.socketDirAlias, this.log, "failed short socket alias");
					this.socketDirAlias = null;
				}
				await removePath(root, this.log, "failed runtime root");
				throw error;
			}
			this.runtimeRoot = root;
		})();
		try {
			await this.runtimeRootPromise;
		} catch (error) {
			this.runtimeRootPromise = null;
			throw error;
		}
	}

	private async removeRuntimeRootIfIdle(): Promise<void> {
		if (!this.runtimeRoot || this.sessions.size > 0 || this.initializing.size > 0 || this.closing.size > 0) return;
		const root = this.runtimeRoot;
		const socketDirAlias = this.socketDirAlias;
		this.runtimeRoot = null;
		this.runtimeRootPromise = null;
		this.socketDirAlias = null;
		if (socketDirAlias) await removePath(socketDirAlias, this.log, "short socket alias");
		await removePath(root, this.log, "runtime root");
		this.socketDir = null;
	}

	private async touchRuntimeRoot(): Promise<void> {
		if (!this.runtimeRoot) return;
		try {
			const now = new Date();
			await utimes(path.join(this.runtimeRoot, RUNTIME_OWNER_FILE), now, now);
		} catch (error) {
			this.log(`agent-browser runtime heartbeat failed: ${String(error)}`);
		}
	}

	private environment(runtime: SessionRuntime): NodeJS.ProcessEnv {
		// The native runtime is a third-party executable. Inheriting Electron's
		// complete environment would expose shell credentials, cloud/API tokens,
		// proxy credentials, and runtime injection flags to it. Keep only process
		// execution/locale essentials and add AO's explicitly scoped contract below.
		const environment: NodeJS.ProcessEnv = {};
		for (const [name, value] of Object.entries(process.env)) {
			if (value !== undefined && NATIVE_ENV_ALLOWLIST.has(name.toLowerCase())) environment[name] = value;
		}
		Object.assign(environment, {
			HOME: runtime.runtimeDir,
			USERPROFILE: runtime.runtimeDir,
			XDG_CONFIG_HOME: runtime.runtimeDir,
			XDG_CACHE_HOME: runtime.runtimeDir,
			TEMP: runtime.runtimeDir,
			TMP: runtime.runtimeDir,
			TMPDIR: runtime.runtimeDir,
			AGENT_BROWSER_CONFIG: runtime.configPath,
			AGENT_BROWSER_CDP: runtime.endpoint,
			AGENT_BROWSER_SOCKET_DIR: this.socketDir!,
			AGENT_BROWSER_SESSION: runtime.namespace,
			AGENT_BROWSER_NAMESPACE: runtime.namespace,
			AGENT_BROWSER_CONTENT_BOUNDARIES: "1",
			AGENT_BROWSER_MAX_OUTPUT: "50000",
			AGENT_BROWSER_IDLE_TIMEOUT_MS: "300000",
			AGENT_BROWSER_AUTO_CONNECT: "0",
		});
		return environment;
	}

	private async assertBinary(): Promise<void> {
		try {
			await access(this.options.binaryPath);
		} catch {
			throw runtimeError(
				"AGENT_BROWSER_NOT_INSTALLED",
				`AO's browser automation component was not found at ${this.options.binaryPath}. Reinstall or rebuild the desktop app.`,
			);
		}
	}
}

export async function scavengeBrowserRuntime(
	dataDir: string,
	processAlive: (pid: number) => boolean = defaultProcessAlive,
	log: (message: string) => void = () => undefined,
): Promise<void> {
	let entries;
	try {
		entries = await readdir(dataDir, { withFileTypes: true });
	} catch (error) {
		if ((error as NodeJS.ErrnoException).code === "ENOENT") return;
		log(`agent-browser runtime scan failed: ${String(error)}`);
		return;
	}

	const removals: Promise<void>[] = [];
	for (const entry of entries) {
		if (!entry.isDirectory()) continue;
		const match = RUNTIME_ROOT_PATTERN.exec(entry.name);
		if (!match) continue;
		const runDir = path.join(dataDir, entry.name);
		const ownerPath = path.join(runDir, RUNTIME_OWNER_FILE);
		let owner: RuntimeOwner;
		try {
			owner = JSON.parse(await readFile(ownerPath, "utf8")) as RuntimeOwner;
		} catch {
			// An unmarked directory may belong to another process that is between
			// mkdir and owner-marker creation. Preserve it rather than guessing.
			continue;
		}
		if (!validRuntimeOwner(owner, match[1])) continue;
		if (processAlive(owner.pid)) continue;
		try {
			const ownerStat = await stat(ownerPath);
			if (Date.now() - ownerStat.mtimeMs < BROWSER_RUNTIME_RECLAIM_GRACE_MS) continue;
		} catch (error) {
			log(`agent-browser runtime scan skipped ${entry.name}: ${String(error)}`);
			continue;
		}
		removals.push(removePath(runDir, log, `stale runtime root ${entry.name}`));
	}
	await Promise.all(removals);
}

function streamAlreadyDisabled(result: NativeProcessResult): boolean {
	return result.stderr.includes(STREAM_ALREADY_DISABLED_MESSAGE) || result.stdout.includes(STREAM_ALREADY_DISABLED_MESSAGE);
}

/** Remove only confirmed-dead frontend aliases owned by this AO data root. */
export async function scavengeBrowserSocketAliases(
	dataDir: string,
	processAlive: (pid: number) => boolean = defaultProcessAlive,
	log: (message: string) => void = () => undefined,
	aliasRoot = "/tmp",
): Promise<void> {
	let entries;
	try {
		entries = await readdir(aliasRoot, { withFileTypes: true });
	} catch (error) {
		if ((error as NodeJS.ErrnoException).code !== "ENOENT") {
			log(`agent-browser socket alias scan failed: ${String(error)}`);
		}
		return;
	}
	const resolvedDataDir = path.resolve(dataDir);
	for (const entry of entries) {
		const match = SOCKET_ALIAS_PATTERN.exec(entry.name);
		if (!match || !entry.isSymbolicLink()) continue;
		const pid = Number(match[1]);
		if (!Number.isSafeInteger(pid) || pid <= 0 || processAlive(pid)) continue;
		const aliasPath = path.join(aliasRoot, entry.name);
		try {
			const target = await readlink(aliasPath);
			const resolvedTarget = path.resolve(aliasRoot, target);
			const relative = path.relative(resolvedDataDir, resolvedTarget);
			const parts = relative.split(path.sep);
			if (
				relative.startsWith(`..${path.sep}`) ||
				path.isAbsolute(relative) ||
				parts.length !== 2 ||
				!/^r-[0-9a-f]{10}$/.test(parts[0]) ||
				parts[1] !== "s"
			) {
				continue;
			}
			await rm(aliasPath);
		} catch (error) {
			if ((error as NodeJS.ErrnoException).code !== "ENOENT") {
				log(`agent-browser socket alias cleanup failed for ${entry.name}: ${String(error)}`);
			}
		}
	}
}

function validRuntimeOwner(value: unknown, expectedPid?: string): value is RuntimeOwner {
	if (!isRecord(value)) return false;
	return (
		value.marker === RUNTIME_OWNER_MARKER &&
		numberIsInteger(value.pid) &&
		(!expectedPid || String(value.pid) === expectedPid) &&
		typeof value.startedAt === "string" &&
		/^[0-9a-f]{32}$/.test(String(value.token))
	);
}

function numberIsInteger(value: unknown): value is number {
	return typeof value === "number" && Number.isSafeInteger(value) && value > 0;
}

function defaultProcessAlive(pid: number): boolean {
	try {
		process.kill(pid, 0);
		return true;
	} catch (error) {
		return (error as NodeJS.ErrnoException).code === "EPERM";
	}
}

async function removePath(target: string, log: (message: string) => void, label: string): Promise<void> {
	try {
		await rm(target, { recursive: true, force: true, maxRetries: 4, retryDelay: 100 });
	} catch (error) {
		log(`agent-browser ${label} cleanup failed: ${String(error)}`);
	}
}

async function ensurePrivateDirectory(target: string): Promise<void> {
	await mkdir(target, { recursive: true, mode: 0o700 });
	if (process.platform !== "win32") await chmod(target, 0o700);
}

async function closeBridgeWithTimeout(bridge: AgentBrowserBridge): Promise<void> {
	let timer: ReturnType<typeof setTimeout> | undefined;
	try {
		await Promise.race([
			bridge.close(),
			new Promise<never>((_, reject) => {
				timer = setTimeout(() => reject(new Error("bridge close timed out")), BRIDGE_CLOSE_TIMEOUT_MS);
			}),
		]);
	} finally {
		if (timer) clearTimeout(timer);
	}
}

export function nativeArgumentsForAction(action: string, args: Record<string, unknown>): string[] {
	const ref = () => nativeRef(stringValue(args.ref, "ref is required"));
	switch (action) {
		case "open":
			return ["open", httpURL(stringValue(args.url, "url is required"))];
		case "snapshot":
			return ["snapshot", ...(args.interactive === true ? ["--interactive"] : []), "--compact"];
		case "click":
		case "dblclick":
		case "focus":
		case "hover":
		case "highlight":
		case "scrollintoview":
		case "check":
		case "uncheck":
			return [action, ref()];
		case "fill":
		case "type":
			return [action, ref(), stringValue(args.text, "text is required", true)];
		case "press":
			return ["press", stringValue(args.key, "key is required")];
		case "drag":
			return ["drag", ref(), nativeRef(stringValue(args.targetRef, "target ref is required"))];
		case "select":
			return ["select", ref(), stringValue(args.value, "value is required", true)];
		case "tabs":
			return ["tab", "list"];
		case "tab-new": {
			const url = optionalStringValue(args.url);
			return ["tab", "new", ...(url ? [httpURL(url)] : [])];
		}
		case "tab-select":
			return ["tab", stringValue(args.tabId, "tabId is required")];
		case "tab-close": {
			const tabId = optionalStringValue(args.tabId);
			return ["tab", "close", ...(tabId ? [tabId] : [])];
		}
		case "scroll": {
			const direction = stringValue(args.direction, "direction is required").toLowerCase();
			if (!["up", "down", "left", "right"].includes(direction)) {
				throw runtimeError("INVALID_ARGUMENT", "direction must be up, down, left, or right");
			}
			const amount = numberValue(args.amount, 600, 1, 5_000);
			return ["scroll", direction, String(amount)];
		}
		case "get": {
			const property = stringValue(args.property, "property is required").toLowerCase();
			if (!["url", "title", "text", "value", "checked"].includes(property)) {
				throw runtimeError("INVALID_ARGUMENT", `Unsupported browser property: ${property}`);
			}
			const target = optionalStringValue(args.ref);
			if (["url", "title"].includes(property) && target) {
				throw runtimeError("INVALID_ARGUMENT", `${property} does not accept an element ref`);
			}
			if (["value", "checked"].includes(property) && !target) {
				throw runtimeError("REFERENCE_REQUIRED", `${property} requires an element ref`);
			}
			return ["get", property, ...(target ? [nativeRef(target)] : [])];
		}
		case "wait":
			return nativeWaitArguments(args);
		case "frame": {
			const target = stringValue(args.target, "frame target is required");
			return ["frame", target === "main" ? target : nativeRef(target)];
		}
		case "dialog": {
			const operation = stringValue(args.operation, "dialog operation is required").toLowerCase();
			if (!["accept", "dismiss", "status"].includes(operation)) {
				throw runtimeError("INVALID_ARGUMENT", "dialog operation must be accept, dismiss, or status");
			}
			const text = optionalStringValue(args.text);
			return ["dialog", operation, ...(text ? [text] : [])];
		}
		case "console":
		case "errors":
			return [action];
		default:
			throw runtimeError("INVALID_ARGUMENT", `Unsupported native browser action: ${action}`);
	}
}

function nativeWaitArguments(args: Record<string, unknown>): string[] {
	const timeout = String(numberValue(args.timeoutMs, 10_000, 1, 55_000));
	if (typeof args.text === "string" && args.text) return ["wait", "--text", args.text, "--timeout", timeout];
	if (typeof args.textGone === "string" && args.textGone) {
		return ["wait", `text=${args.textGone}`, "--state", "hidden", "--timeout", timeout];
	}
	if (typeof args.selector === "string" && args.selector) {
		return ["wait", args.selector, "--timeout", timeout];
	}
	if (typeof args.selectorGone === "string" && args.selectorGone) {
		return ["wait", args.selectorGone, "--state", "detached", "--timeout", timeout];
	}
	if (typeof args.url === "string" && args.url) return ["wait", "--url", `**${args.url}**`, "--timeout", timeout];
	if (args.load === true) return ["wait", "--load", "load", "--timeout", timeout];
	if (typeof args.stableMs === "number" && args.stableMs > 0) {
		const stableMs = numberValue(args.stableMs, 500, 1, 60_000);
		const expression = `(() => { const key = "__aoDomStability"; const now = performance.now(); let state = globalThis[key]; if (!state) { state = { lastMutation: now }; state.observer = new MutationObserver(() => { state.lastMutation = performance.now(); }); state.observer.observe(document, { subtree: true, childList: true, attributes: true, characterData: true }); globalThis[key] = state; } if (performance.now() - state.lastMutation < ${stableMs}) return false; state.observer.disconnect(); delete globalThis[key]; return true; })()`;
		return ["wait", "--fn", expression, "--timeout", timeout];
	}
	if (typeof args.ms === "number" && args.ms > 0) return ["wait", String(args.ms)];
	throw runtimeError("INVALID_ARGUMENT", "A wait condition is required");
}

export function parseAgentBrowserJSON(stdout: string): AgentBrowserJSONResult {
	let envelope: unknown;
	try {
		envelope = JSON.parse(stdout);
	} catch {
		throw runtimeError("AGENT_BROWSER_INVALID_OUTPUT", "Browser automation returned invalid structured output");
	}
	if (!isRecord(envelope)) throw runtimeError("AGENT_BROWSER_INVALID_OUTPUT", "Browser automation returned invalid output");
	if (envelope.success === false) {
		throw runtimeError(staleReferenceCode(envelope.error) ?? "AGENT_BROWSER_COMMAND_FAILED", stringError(envelope.error) || "Browser automation failed");
	}
	const boundary = validContentBoundary(envelope._boundary);
	const result: Record<string, unknown> = isRecord(envelope.data) ? { ...envelope.data } : { value: envelope.data };
	// `_boundary` is native-output metadata. Never forward a page-shaped field
	// with the same name as trusted metadata, but preserve the root field emitted
	// by agent-browser 0.33.1 so downstream adapters retain its nonce and origin.
	delete result._boundary;
	if (boundary) result._boundary = boundary;
	return { ...result, untrustedExternalContent: true };
}

function nativeRef(value: string): string {
	return /^@?e\d+$/i.test(value) ? `@${value.replace(/^@/, "")}` : value;
}

function stringValue(value: unknown, message: string, allowEmpty = false): string {
	if (typeof value !== "string" || (!allowEmpty && !value.trim())) throw runtimeError("INVALID_ARGUMENT", message);
	return allowEmpty ? value : value.trim();
}

function optionalStringValue(value: unknown): string | undefined {
	return typeof value === "string" && value.trim() ? value.trim() : undefined;
}

function numberValue(value: unknown, fallback: number, minimum: number, maximum: number): number {
	if (value === undefined) return fallback;
	if (typeof value !== "number" || !Number.isFinite(value) || value < minimum || value > maximum) {
		throw runtimeError("INVALID_ARGUMENT", `Numeric argument must be between ${minimum} and ${maximum}`);
	}
	return Math.round(value);
}

function httpURL(value: string): string {
	assertHTTPURL(value);
	return value;
}

function pngDimensions(image: Buffer): { width: number; height: number } {
	if (image.length < 24 || image.toString("ascii", 1, 4) !== "PNG") {
		throw runtimeError("AGENT_BROWSER_INVALID_OUTPUT", "Browser automation returned an invalid PNG screenshot");
	}
	return { width: image.readUInt32BE(16), height: image.readUInt32BE(20) };
}

function stringError(value: unknown): string {
	if (typeof value === "string") return value;
	if (isRecord(value) && typeof value.message === "string") return value.message;
	return "";
}

const STALE_REFERENCE_MESSAGE = /^(?:Unknown ref:|Could not locate element with)/;

// Preserve only the stale-reference signal needed by `act`. Other native
// command failures stay generic so tab drift and close recovery paths continue
// to recognize AGENT_BROWSER_COMMAND_FAILED.
function staleReferenceCode(value: unknown): "STALE_REFERENCE" | undefined {
	if (isRecord(value) && value.code === "STALE_REFERENCE") return "STALE_REFERENCE";
	return typeof value === "string" && STALE_REFERENCE_MESSAGE.test(value) ? "STALE_REFERENCE" : undefined;
}

function isRecord(value: unknown): value is Record<string, unknown> {
	return Boolean(value && typeof value === "object" && !Array.isArray(value));
}

function validContentBoundary(value: unknown): Record<string, string> | undefined {
	if (!isRecord(value) || typeof value.nonce !== "string" || !value.nonce || typeof value.origin !== "string") {
		return undefined;
	}
	return { nonce: value.nonce, origin: value.origin };
}

export function validateAgentBrowserArguments(args: string[]): void {
	if (args.length === 0) throw runtimeError("INVALID_ARGUMENT", "An agent-browser command is required");
	if (args.length > MAX_ARGUMENTS) throw runtimeError("INVALID_ARGUMENT", "Too many agent-browser arguments");
	if (args.some((arg) => typeof arg !== "string" || arg.length > MAX_ARGUMENT_CHARS)) {
		throw runtimeError("INVALID_ARGUMENT", "agent-browser arguments are invalid or too large");
	}
	const command = args[0].toLowerCase();
	if (!ALLOWED_COMMANDS.has(command)) {
		throw runtimeError("AGENT_BROWSER_COMMAND_BLOCKED", `agent-browser command is not enabled in AO: ${command}`);
	}
	for (const arg of args) {
		const lower = arg.toLowerCase();
		if (FORBIDDEN_FLAGS.some((flag) => lower === flag || lower.startsWith(`${flag}=`))) {
			throw runtimeError("AGENT_BROWSER_COMMAND_BLOCKED", `agent-browser flag is managed by AO: ${arg}`);
		}
	}
	if (command === "open" && args[1] && !args[1].startsWith("-")) {
		assertHTTPURL(args[1]);
	}
	if (command === "diff" && args[1]?.toLowerCase() !== "snapshot") {
		throw runtimeError("AGENT_BROWSER_COMMAND_BLOCKED", "Only snapshot diff is enabled in AO");
	}
	if (command === "get" && args[1]?.toLowerCase() === "cdp-url") {
		throw runtimeError("AGENT_BROWSER_COMMAND_BLOCKED", "The private AO CDP endpoint cannot be displayed");
	}
}

async function runNativeProcess(
	binaryPath: string,
	args: string[],
	environment: NodeJS.ProcessEnv,
	signal?: AbortSignal,
	timeoutMs = COMMAND_TIMEOUT_MS,
): Promise<NativeProcessResult> {
	return new Promise((resolve, reject) => {
		const child = spawn(binaryPath, args, {
			env: environment,
			stdio: ["ignore", "pipe", "pipe"],
			windowsHide: true,
		});
		let stdout: Buffer<ArrayBufferLike> = Buffer.alloc(0);
		let stderr: Buffer<ArrayBufferLike> = Buffer.alloc(0);
		let settled = false;
		const finish = (error?: Error, exitCode = -1) => {
			if (settled) return;
			settled = true;
			clearTimeout(timer);
			signal?.removeEventListener("abort", abort);
			if (error) reject(error);
			else
				resolve({
					stdout: stdout.toString("utf8"),
					stderr: stderr.toString("utf8"),
					exitCode,
				});
		};
		const append = (
			current: Buffer<ArrayBufferLike>,
			chunk: Buffer<ArrayBufferLike>,
		): Buffer<ArrayBufferLike> => {
			if (current.length + chunk.length > MAX_OUTPUT_BYTES) {
				child.kill();
				finish(runtimeError("AGENT_BROWSER_OUTPUT_TOO_LARGE", "agent-browser output exceeded AO's limit"));
				return current;
			}
			return Buffer.concat([current, chunk]);
		};
		child.stdout.on("data", (chunk: Buffer) => {
			stdout = append(stdout, chunk);
		});
		child.stderr.on("data", (chunk: Buffer) => {
			stderr = append(stderr, chunk);
		});
		child.once("error", (error) => finish(error));
		child.once("close", (code) => finish(undefined, code ?? -1));
		// The native CLI starts a long-lived daemon. On Windows that daemon can
		// briefly retain inherited pipe handles after the short-lived CLI exits,
		// delaying Node's `close` event even though the command is complete.
		// `exit` is therefore the primary completion signal; one event-loop turn
		// still lets already-buffered stdout/stderr data handlers drain.
		child.once("exit", (code) => setImmediate(() => finish(undefined, code ?? -1)));
		const abort = () => {
			child.kill();
			finish(runtimeError("AGENT_BROWSER_CANCELLED", "agent-browser command was cancelled"));
		};
		signal?.addEventListener("abort", abort, { once: true });
		const timer = setTimeout(() => {
			child.kill();
			finish(runtimeError("AGENT_BROWSER_TIMEOUT", "agent-browser command timed out"));
		}, timeoutMs);
		if (signal?.aborted) abort();
	});
}

function assertHTTPURL(raw: string): void {
	let url: URL;
	try {
		url = new URL(raw);
	} catch {
		throw runtimeError("INVALID_URL", "agent-browser navigation requires an explicit HTTP(S) URL");
	}
	if (url.protocol !== "http:" && url.protocol !== "https:") {
		throw runtimeError("BROWSER_URL_FORBIDDEN", `Unsupported browser URL scheme: ${url.protocol}`);
	}
}

function sessionNamespace(sessionId: string): string {
	return `ao-${createHash("sha256").update(sessionId).digest("hex").slice(0, 4)}`;
}

export function agentBrowserSocketPath(socketDir: string, namespace: string): string {
	return path.join(socketDir, "namespaces", namespace, "run", `${namespace}.sock`);
}

function assertAgentBrowserSocketPath(socketDir: string, namespace: string, platform: NodeJS.Platform): void {
	if (platform === "win32") return;
	const socketPath = agentBrowserSocketPath(socketDir, namespace);
	const byteLength = Buffer.byteLength(socketPath, "utf8");
	if (byteLength > AGENT_BROWSER_UNIX_SOCKET_PATH_MAX_BYTES) {
		throw runtimeError(
			"AGENT_BROWSER_START_FAILED",
			`Agent Browser socket path is ${byteLength} bytes; Unix supports at most ${AGENT_BROWSER_UNIX_SOCKET_PATH_MAX_BYTES}. AO needs a shorter socket directory.`,
		);
	}
}

function runtimeError(code: string, message: string): Error & { code: string } {
	return Object.assign(new Error(message), { code });
}
