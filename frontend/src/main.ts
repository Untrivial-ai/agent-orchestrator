import {
	app,
	BrowserWindow,
	clipboard,
	dialog,
	ipcMain,
	Menu,
	nativeTheme,
	net,
	nativeImage,
	Notification as ElectronNotification,
	protocol,
	shell,
	WebContentsView,
	webContents,
	type OpenDialogOptions,
} from "electron";
import {
	startAutoUpdates,
	ensureUpdatePrefs,
	checkForUpdatesNow,
	downloadUpdateNow,
	quitAndInstallUpdate,
	getUpdateStatus,
	setUpdateSettings,
	returnToHome,
	type UpdateCheckOptions,
} from "./main/auto-updater";
import { listFeatureBuilds, getActiveFeatureBuild } from "./main/feature-builds";
import { readUpdateSettings, type UpdateSettings, type UpdateStatus } from "./main/update-settings";
import { readKeybindingOverrides, writeKeybindingOverrides } from "./main/keybinding-settings";
import { coerceUiSettings, readUiSettings, writeUiSettings, type UiSettings } from "./main/ui-settings";
import { THEME_CHANGED_CHANNEL } from "./shared/ui-locale";
import { spawn, type ChildProcess } from "node:child_process";
import { randomBytes, randomUUID } from "node:crypto";
import { closeSync, existsSync, openSync, readFileSync } from "node:fs";
import { mkdir, readFile, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { pathToFileURL } from "node:url";
import { type DaemonLaunchSpec, resolveDaemonLaunch } from "./shared/daemon-launch";
import { createListenPortScanner, defaultRunFilePath, parseRunFile } from "./shared/daemon-discovery";
import type { DaemonStatus } from "./shared/daemon-status";
import { attachAppShortcuts } from "./main/app-shortcuts";
import {
	KEYBOARD_SHORTCUTS_HELP_CHANNEL,
	OPEN_SETTINGS_SHORTCUT_CHANNEL,
	SET_CLOSE_SHELL_TERMINAL_SHORTCUT_ENABLED_CHANNEL,
	type KeybindingOverrides,
} from "./shared/shortcuts";
import { createTrayController, type TrayController } from "./main/tray";
import { createTrayLifecycle, isTrayEnabled } from "./main/tray-lifecycle";
import {
	TRAY_RENDERER_READY_CHANNEL,
	TRAY_SET_ATTENTION_STATE_CHANNEL,
} from "./shared/tray";
import {
	type DaemonProbe,
	expectedDaemonPort,
	parseDaemonProbe,
	resolveDaemonFromPort,
	resolveDaemonFromRunFile,
} from "./shared/daemon-attach";
import { browserDaemonOwnershipDecision, shouldReplacePortHolder } from "./shared/daemon-takeover";
import { buildDaemonEnv, resolveShellEnv, type ShellRunner } from "./shared/shell-env";
import { DEFAULT_POSTHOG_HOST, DEFAULT_POSTHOG_PROJECT_KEY } from "./shared/posthog-config";
import { buildTelemetryBootstrap } from "./shared/telemetry";
import { createBrowserViewHost, type BrowserViewHost } from "./main/browser-view-host";
import { AgentBrowserRuntime } from "./main/agent-browser-runtime";
import { sameBrowserRuntimeIdentity, type BrowserRuntimeIdentity } from "./main/browser-runtime-identity";
import { connectSupervisor, type SupervisorLinkHandle } from "./main/supervisor-link";
import { connectBrowserRuntime, type BrowserRuntimeLinkHandle } from "./main/browser-runtime-link";
import { keepDaemonAlive, shouldLinkOnAttach } from "./main/daemon-owner";
import { readMigrationState, updateMigration, writeAppStateMarker, type MigrationState } from "./main/app-state";
import { isAllowedAppExternalURL, openAllowedAppExternalURL } from "./main/external-open";
import { shouldSignalAttention, shouldToast } from "./main/notification-signals";
import { buildWindowsAppMenuTemplate } from "./main/menu";
import { ancestorRepositorySetupWarning, scanImportFolder } from "./main/import-folder-scan";

// Globals injected at compile time by @electron-forge/plugin-vite.
declare const MAIN_WINDOW_VITE_DEV_SERVER_URL: string | undefined;
declare const MAIN_WINDOW_VITE_NAME: string;

// Windows GUI launches (e.g. from a Start-menu/desktop shortcut) have no attached
// console, so process.stdout and process.stderr are dead pipes. The daemon-output
// console.log/console.error calls
// below then fail with EPIPE, and with no "error" listener that surfaces as an
// uncaught exception that crashes the main process. Swallow broken-pipe write
// errors on the std streams: a dropped log line is harmless, the crash is not.
const ignoreStdStreamError = (err: NodeJS.ErrnoException): void => {
	if (err.code === "EPIPE") return;
};
process.stdout.on("error", ignoreStdStreamError);
process.stderr.on("error", ignoreStdStreamError);

// Must run before app ready so the About panel and default-menu role labels use it.
app.setName("Agent Orchestrator");

// Windows shows native toasts only when the app declares an AppUserModelID that
// matches its installer shortcut (the NSIS maker's appId). Without it,
// Notification.isSupported() still returns true but show() silently drops the
// toast, so notifications never appear. No-op on macOS/Linux.
if (process.platform === "win32") {
	app.setAppUserModelId("dev.agent-orchestrator.desktop");
}

// Pin ALL Electron-owned state (Chromium cache, cookies, local/session storage,
// crash dumps) under the canonical AO home at ~/.ao instead of Electron's macOS
// default ~/Library/Application Support/<name>. Keeps the app's entire footprint
// inside ~/.ao alongside the daemon's data dir and running.json. sessionData and
// crashDumps derive from userData, so this one override reparents them all.
// Must run before app ready.
// Dev runs get their own profile under the same ~/.ao root: the packaged app
// keeps this directory open, and two Chromium instances sharing one profile
// corrupt its LevelDB stores. Mirrors how dev already isolates running.json and
// the daemon data dir into ~/.ao/dev.
app.setPath(
	"userData",
	app.isPackaged ? path.join(os.homedir(), ".ao", "electron") : path.join(os.homedir(), ".ao", "dev", "electron"),
);

let mainWindow: BrowserWindow | null = null;
let trayController: TrayController | null = null;
const trayLifecycle = createTrayLifecycle({
	getWindow: () => mainWindow,
	getTrayController: () => trayController,
	focusWindow: () => focusMainWindow(),
});
let daemonProcess: ChildProcess | null = null;
let daemonStoppingProcess: ChildProcess | null = null;
let daemonRestartAfterExitProcess: ChildProcess | null = null;
let daemonStartPromise: Promise<DaemonStatus> | null = null;
let daemonStartEpoch = 0;
let daemonStatus: DaemonStatus = { state: "stopped" };
let daemonOutput = "";
let browserViewHost: BrowserViewHost | null = null;
const browserCleanupPromises = new Set<Promise<void>>();
let browserQuitCleanupPromise: Promise<void> | null = null;
let browserCleanupComplete = false;
let browserQuitRequested = false;
let createWindowPromise: Promise<void> | null = null;
let browserRuntimeLink: BrowserRuntimeLinkHandle | null = null;
let browserRuntimeLinkIdentity: BrowserRuntimeIdentity | null = null;
let keybindingOverrides: KeybindingOverrides = {};
let keybindingRecordingActive = false;
let closeShellTerminalShortcutEnabled = false;
// Held for the app lifetime. Dropping it (on any exit) triggers daemon self-stop.
let supervisorLink: SupervisorLinkHandle | null = null;
// Guard: prevents stacking multiple flashFrame(true) calls when notifications arrive rapidly.
let isFlashing = false;

const isDev = !app.isPackaged;

// Dev mode uses a separate port and state subdirectory so it never collides with
// a concurrently running installed-app daemon. The subdir also isolates supervise.sock
// on Unix (backend derives it as dir(RunFilePath)/supervise.sock) and the named pipe
// on Windows (supervisorPipeFromRunFile derives it from the same dir basename).
const DEV_DAEMON_PORT = 3002;
const DEV_STATE_SUBDIR = "dev"; // ~/.ao/dev/

// Height (px) of the custom Windows title bar. Must stay in sync with
// --size-window-titlebar (tokens.css) and .window-titlebar, plus the Window
// Controls Overlay height passed to BrowserWindow, so the native min/max/close
// buttons line up with the app's bar.
const TITLEBAR_HEIGHT = 36;
// Traffic lights stay fixed across sidebar expand/collapse. Y matches the
// natural macOS titlebar band (TitlebarNav is h-traffic-light-clearance).
const MAC_WINDOW_BUTTON_X = 14;
const MAC_WINDOW_BUTTON_Y = 12;

const RENDERER_SCHEME = "app";
const RENDERER_HOST = "renderer";
const RENDERER_ORIGIN = `${RENDERER_SCHEME}://${RENDERER_HOST}`;

// The packaged renderer is served from a custom standard scheme, not file://.
// A file:// page has the opaque "null" origin, which the daemon must never
// trust (every sandboxed iframe on any website also presents "null"), so its
// fetch/EventSource calls to the loopback API would be CORS-blocked.
// app://renderer is an origin only this app can present, so the daemon's CORS
// allowlist can name it. A standard scheme also makes the build's absolute
// asset URLs (/assets/…) and history-API routing resolve, which file:// breaks.
// Must run before app ready.
protocol.registerSchemesAsPrivileged([
	{
		scheme: RENDERER_SCHEME,
		privileges: { standard: true, secure: true, supportFetchAPI: true },
	},
]);

// Maps app://renderer/<path> to the built renderer in dist/. Paths without a
// file extension are client-side routes and fall back to index.html (SPA).
function registerRendererProtocol(): void {
	const distRoot = path.join(__dirname, `../renderer/${MAIN_WINDOW_VITE_NAME}`);
	protocol.handle(RENDERER_SCHEME, async (request) => {
		const url = new URL(request.url);
		if (url.host !== RENDERER_HOST) {
			return new Response("Not found", { status: 404 });
		}
		const resolved = path.resolve(path.join(distRoot, decodeURIComponent(url.pathname)));
		if (resolved !== distRoot && !resolved.startsWith(distRoot + path.sep)) {
			return new Response("Forbidden", { status: 403 });
		}
		const target = path.extname(resolved) === "" ? path.join(distRoot, "index.html") : resolved;
		try {
			return await net.fetch(pathToFileURL(target).toString());
		} catch {
			return new Response("Not found", { status: 404 });
		}
	});
}

function rendererUrl(): string {
	if (typeof MAIN_WINDOW_VITE_DEV_SERVER_URL !== "undefined" && MAIN_WINDOW_VITE_DEV_SERVER_URL) {
		return MAIN_WINDOW_VITE_DEV_SERVER_URL;
	}

	return `${RENDERER_ORIGIN}/index.html`;
}

function preloadPath(): string {
	return path.join(__dirname, "preload.js");
}

function annotatePreloadPath(): string {
	return path.join(__dirname, "annotate-preload.js");
}

// Runtime window/taskbar icon for Linux and Windows. macOS ignores this and
// uses the .app bundle's .icns instead. Packaged: shipped via extraResource to
// resources/icon.png; dev: the source asset under frontend/assets.
function windowIconPath(): string | undefined {
	const iconFile = process.platform === "win32" ? "icon.ico" : "icon.png";
	const candidate = app.isPackaged
		? path.join(process.resourcesPath, iconFile)
		: path.join(__dirname, `../../assets/${iconFile}`);
	if (existsSync(candidate)) return candidate;
	const fallback = app.isPackaged
		? path.join(process.resourcesPath, "icon.png")
		: path.join(__dirname, "../../assets/icon.png");
	return existsSync(fallback) ? fallback : undefined;
}

function applyRuntimeAppIcon(): void {
	if (process.platform !== "darwin") return;
	const iconPath = windowIconPath();
	if (!iconPath) return;
	const icon = nativeImage.createFromPath(iconPath);
	if (!icon.isEmpty()) {
		app.dock.setIcon(icon);
	}
}

function focusMainWindow(): void {
	if (!mainWindow) {
		createWindow();
		return;
	}
	if (mainWindow.isMinimized()) mainWindow.restore();
	mainWindow.show();
	mainWindow.focus();
}

function setDaemonStatus(nextStatus: DaemonStatus): void {
	if (nextStatus.state !== "ready") disposeBrowserRuntimeLink();
	daemonStatus = nextStatus;
	mainWindow?.webContents.send("daemon:status", daemonStatus);
	if (nextStatus.state === "ready" && browserViewHost) {
		establishBrowserRuntimeLink();
	}
}

const MAX_DAEMON_OUTPUT_CHARS = 12_000;

function appendDaemonOutput(text: string): void {
	daemonOutput = (daemonOutput + text).slice(-MAX_DAEMON_OUTPUT_CHARS);
}

// Menu installed on Windows where the native menu bar is hidden. The bar stays
// out of sight, but the roles keep their accelerators alive (Reload, zoom, full
// screen, edit commands). DevTools uses the AO browser toggle so the focused
// Browser panel opens the same detached window as the toolbar and shortcut.
function buildWindowsAppMenu(): Menu {
	return Menu.buildFromTemplate(
		buildWindowsAppMenuTemplate(() => {
			const fallback = () => mainWindow?.webContents.toggleDevTools();
			void browserViewHost?.toggleDevToolsForLastFocused().then((state) => {
				if (!state) fallback();
			}).catch(fallback);
		}),
	);
}

async function disposeBrowserViewHost(): Promise<void> {
	const host = browserViewHost;
	browserViewHost = null;
	if (!host) return;
	const cleanup = Promise.resolve()
		.then(() => host.dispose())
		.catch((error) => {
			console.error("browser host cleanup failed:", error);
		});
	browserCleanupPromises.add(cleanup);
	void cleanup.then(
		() => browserCleanupPromises.delete(cleanup),
		() => browserCleanupPromises.delete(cleanup),
	);
	await cleanup;
}

async function disposeAllBrowserViewHosts(): Promise<void> {
	for (;;) {
		if (browserViewHost) await disposeBrowserViewHost();
		const pending = [...browserCleanupPromises];
		if (pending.length === 0 && !browserViewHost) return;
		if (pending.length > 0) await Promise.all(pending);
	}
}

async function createWindowInternal(): Promise<void> {
	await disposeAllBrowserViewHosts();
	const agentBrowserRuntime = new AgentBrowserRuntime({
		binaryPath: resolveAgentBrowserBinaryPath(),
		// Agent Browser creates Unix sockets below each run root. Keep this base
		// deliberately short so the namespace/session suffix stays below macOS's
		// 103-byte sockaddr_un limit; all AO state remains under ~/.ao.
		dataDir: path.join(os.homedir(), ".ao", ...(app.isPackaged ? ["br"] : ["dev", "br"])),
		log: (message) => console.log(`AO: ${message}`),
	});
	await agentBrowserRuntime.prepare();
	if (browserQuitRequested) {
		await agentBrowserRuntime.dispose();
		return;
	}
	mainWindow = new BrowserWindow({
		width: 1320,
		height: 860,
		minWidth: 960,
		minHeight: 640,
		title: "Agent Orchestrator",
		icon: windowIconPath(),
		backgroundColor: "#0f1014",
		// Windows goes frameless with a Window Controls Overlay: Electron still draws
		// native min/max/close on the right, while the renderer paints its own
		// VS Code-style title bar (logo + menu) on the left. macOS/Linux keep the
		// inset traffic-light chrome. Overlay colours are re-synced to the active
		// theme from the renderer via the window:setOverlay IPC.
		...(process.platform === "win32"
			? {
					titleBarStyle: "hidden" as const,
					// Hide the native menu bar. A role-based menu is still installed (for
					// accelerators) below; the visible menu is painted by WindowTitlebar.
					autoHideMenuBar: true,
					titleBarOverlay: { color: "#17181c", symbolColor: "#c7ccd4", height: TITLEBAR_HEIGHT },
				}
			: {
					titleBarStyle: "hiddenInset" as const,
					// Fixed natural titlebar position — never moved on sidebar toggle.
					trafficLightPosition: { x: MAC_WINDOW_BUTTON_X, y: MAC_WINDOW_BUTTON_Y },
				}),
		webPreferences: {
			preload: preloadPath(),
			contextIsolation: true,
			nodeIntegration: false,
			sandbox: true,
		},
	});

	// On Windows the app paints its own title bar (WindowTitlebar), so the native
	// menu bar is hidden (autoHideMenuBar above). The role-based menu is still
	// installed so its accelerators keep working and act on the focused pane;
	// setMenuBarVisibility(false) keeps the strip itself out of view. macOS/Linux
	// keep their native menus.
	if (process.platform === "win32") {
		Menu.setApplicationMenu(buildWindowsAppMenu());
		mainWindow.setMenuBarVisibility(false);
	}

	// Harden navigation: never let renderer/terminal content open in-app windows or
	// navigate the privileged window away from the app origin. External links go to
	// the OS browser. Keep this in place before exposing any daemon output to the renderer.
	mainWindow.webContents.setWindowOpenHandler(({ url }) => {
		if (isAllowedAppExternalURL(url)) {
			void shell.openExternal(url);
		}
		return { action: "deny" };
	});

	mainWindow.webContents.on("will-navigate", (event, url) => {
		if (url !== mainWindow?.webContents.getURL()) {
			event.preventDefault();
		}
	});

	// Application shortcuts are handled here so they fire no matter which web
	// contents holds focus — the shell renderer, xterm's helper textarea, or a
	// browser-preview view (wired per-view in the browser host).
	const isMac = process.platform === "darwin";
	attachAppShortcuts(
		mainWindow.webContents,
		isMac,
		mainWindow.webContents,
		false,
		() => keybindingOverrides,
		() => keybindingRecordingActive,
		() => true,
		(id) => {
			if (id !== "toggle-browser-devtools") return;
			void browserViewHost?.toggleDevToolsForLastFocused().catch(() => undefined);
		},
	);

	browserViewHost = createBrowserViewHost({
		mainWindow,
		createDevToolsWindow: () =>
			new BrowserWindow({
				show: false,
				width: 1100,
				height: 760,
				minWidth: 720,
				minHeight: 480,
				title: "Browser DevTools",
				autoHideMenuBar: true,
				webPreferences: {
					contextIsolation: true,
					nodeIntegration: false,
					sandbox: false,
				},
			}),
		ipcMain,
		shell,
		WebContentsView,
		annotatePreloadPath: annotatePreloadPath(),
		rendererOrigin: new URL(rendererUrl()).origin,
		isMac,
		getKeybindingOverrides: () => keybindingOverrides,
		isKeybindingRecording: () => keybindingRecordingActive,
		agentBrowserRuntime,
		isCloseShellTerminalShortcutEnabled: () => closeShellTerminalShortcutEnabled,
	});
	if (daemonStatus.state === "ready") establishBrowserRuntimeLink();

	void mainWindow.loadURL(rendererUrl());

	if (isDev && process.env.AO_OPEN_DEVTOOLS === "1") {
		mainWindow.webContents.once("did-frame-finish-load", () => {
			mainWindow?.webContents.openDevTools({ mode: "detach" });
		});
	}

	// macOS: traffic lights vanish in native fullscreen, so the renderer drops
	// the clearance pad above TitlebarNav. Push state so the sidebar can react
	// without polling isFullScreen().
	const pushFullScreen = () => {
		if (!mainWindow) return;
		mainWindow.webContents.send("window:fullscreen", mainWindow.isFullScreen());
	};
	mainWindow.on("enter-full-screen", pushFullScreen);
	mainWindow.on("leave-full-screen", pushFullScreen);
	mainWindow.on("blur", () => {
		keybindingRecordingActive = false;
	});
	mainWindow.webContents.on("render-process-gone", () => {
		keybindingRecordingActive = false;
	});
	mainWindow.webContents.on("did-start-loading", () => trayLifecycle.clear());
	mainWindow.webContents.on("render-process-gone", () => trayLifecycle.clear());

	mainWindow.on("closed", () => {
		disposeBrowserRuntimeLink();
		keybindingRecordingActive = false;
		void disposeBrowserViewHost();
		mainWindow = null;
		trayLifecycle.clearPendingTarget();
	});
}

function createWindow(): Promise<void> {
	if (createWindowPromise) return createWindowPromise;
	const pending = createWindowInternal();
	createWindowPromise = pending;
	void pending.then(
		() => {
			if (createWindowPromise === pending) createWindowPromise = null;
		},
		() => {
			if (createWindowPromise === pending) createWindowPromise = null;
		},
	);
	return pending;
}

function resolveAgentBrowserBinaryPath(): string {
	const override = process.env.AO_AGENT_BROWSER_PATH?.trim();
	if (override) return path.resolve(override);
	const binary = process.platform === "win32" ? "agent-browser.exe" : "agent-browser";
	return app.isPackaged
		? path.join(process.resourcesPath, "agent-browser", binary)
		: path.join(app.getAppPath(), "agent-browser", binary);
}

// How long the supervisor waits for the daemon to confirm its bound port (via
// the listen log line or running.json). A daemon that cannot confirm startup in
// this window is reported with its captured output instead of being treated as
// ready on an assumed port.
const PORT_DISCOVERY_TIMEOUT_MS = 30_000;
const DAEMON_RESTART_STOP_TIMEOUT_MS = 5_000;
const RUN_FILE_POLL_MS = 300;
// Accept run-files stamped slightly before our spawn timestamp: the daemon's
// clock reading and ours race within normal scheduling jitter.
const RUN_FILE_FRESHNESS_SKEW_MS = 2_000;
const DAEMON_PROBE_TIMEOUT_MS = 2_000;

function runFilePath(): string | null {
	if (process.env.AO_RUN_FILE) return process.env.AO_RUN_FILE;
	if (isDev) return path.join(os.homedir(), ".ao", DEV_STATE_SUBDIR, "running.json");
	return defaultRunFilePath(process.platform, process.env, os.homedir());
}

// How long to wait for the login shell to print its env before giving up. A
// misconfigured rc that blocks (or a slow nvm/pyenv chain) must not hang startup;
// the daemon then falls back to the static PATH floor.
const SHELL_ENV_TIMEOUT_MS = 3_000;

// The login-shell env resolved once at startup (see docs/daemon-environment.md),
// or null when the probe failed/timed out. Read synchronously by daemonEnv().
let cachedShellEnv: Record<string, string> | null = null;
// Memoize the in-flight resolution so concurrent/repeat awaits are cheap.
let shellEnvPromise: Promise<void> | null = null;

// Telemetry defaults stamped on the daemon env on every platform; explicit env
// always wins.
//
// Unpackaged builds keep local event recording but never export to PostHog: a
// dev loop or a CI job driving the real app would otherwise bill production
// events and inflate install/DAU counts. Set AO_TELEMETRY_REMOTE explicitly to
// exercise the export path from a dev build.
function telemetryOverrides(): Record<string, string> {
	return {
		AO_TELEMETRY_EVENTS: process.env.AO_TELEMETRY_EVENTS ?? "on",
		AO_TELEMETRY_REMOTE: process.env.AO_TELEMETRY_REMOTE ?? (isDev ? "off" : "posthog"),
		AO_TELEMETRY_POSTHOG_KEY: process.env.AO_TELEMETRY_POSTHOG_KEY ?? DEFAULT_POSTHOG_PROJECT_KEY,
		AO_TELEMETRY_POSTHOG_HOST: process.env.AO_TELEMETRY_POSTHOG_HOST ?? DEFAULT_POSTHOG_HOST,
		// The daemon binary has no version of its own that release tooling sets,
		// so without this every daemon event lands unattributable to a release.
		AO_TELEMETRY_APP_VERSION: process.env.AO_TELEMETRY_APP_VERSION ?? app.getVersion(),
		// Kill switch: forwarded so a noisy stream can be silenced by env on an
		// install that already exists, without shipping a new build.
		...(process.env.AO_TELEMETRY_DISABLED_EVENTS
			? { AO_TELEMETRY_DISABLED_EVENTS: process.env.AO_TELEMETRY_DISABLED_EVENTS }
			: {}),
	};
}

// Run the user's login shell to dump its env. stdin is ignored so an rc that
// reads input hits EOF instead of hanging; stderr is ignored to drop banner
// noise. Never rejects: resolves null on spawn error, non-zero exit, or timeout
// (SIGKILLed), so the caller degrades to the static PATH floor.
const runLoginShell: ShellRunner = (shellPath, args) =>
	new Promise((resolve) => {
		let settled = false;
		const finish = (value: string | null) => {
			if (settled) return;
			settled = true;
			resolve(value);
		};
		let child: ReturnType<typeof spawn>;
		try {
			child = spawn(shellPath, args, { stdio: ["ignore", "pipe", "ignore"] });
		} catch {
			finish(null);
			return;
		}
		const timer = setTimeout(() => {
			child.kill("SIGKILL");
			finish(null);
		}, SHELL_ENV_TIMEOUT_MS);
		let stdout = "";
		// stdout may be typed Readable | null under this stdio config; guard it.
		child.stdout?.on("data", (chunk: Buffer) => {
			stdout += chunk.toString("utf8");
		});
		child.once("error", () => {
			clearTimeout(timer);
			finish(null);
		});
		child.once("exit", (code) => {
			clearTimeout(timer);
			finish(code === 0 ? stdout : null);
		});
	});

// Resolve the login-shell env once and cache it. No-op on Windows (the launchd
// shell split does not apply; a static PATH floor suffices). Awaited at the
// daemon-spawn chokepoint so the cache is populated before the first spawn.
function ensureShellEnv(): Promise<void> {
	if (process.platform === "win32") return Promise.resolve();
	if (!shellEnvPromise) {
		shellEnvPromise = resolveShellEnv(process.env, runLoginShell).then((resolved) => {
			cachedShellEnv = resolved;
			if (!resolved) {
				console.error("AO: could not read the login-shell environment; falling back to a static PATH floor.");
			}
		});
	}
	return shellEnvPromise;
}

// One id per app launch, minted eagerly so every daemon spawn in this process
// (including supervisor restarts) reports the same run. An explicit
// AO_APP_RUN_ID in the environment wins, which lets a test or a wrapper pin it.
const appRunId = process.env.AO_APP_RUN_ID ?? `apprun-${randomUUID()}`;
const browserRuntimeToken = randomBytes(32).toString("base64url");

function daemonEnv(forceKeep = keepDaemonAlive(process.env)): NodeJS.ProcessEnv {
	// AO_OWNER is the daemon's durable spawn-mode record: the daemon writes it
	// into running.json and the attach path reads it to decide the supervisor
	// link from the daemon's own state (not this Electron process's env, which
	// differs across launches). A keep-alive daemon is "persistent" (never
	// re-linked, survives app quit); a normal app-owned daemon is "app";
	// headless `ao start` sets none (stays unlinked, persistent by default).
	//
	// AO_APP_RUN_ID identifies THIS app launch. It is constant for the process
	// lifetime, so a daemon the supervisor restarts inherits the same id and its
	// standalone shell terminals survive; a later app launch gets a new id, which
	// is how the daemon recognises the previous run's shells as orphans and
	// destroys them (see internal/service/shellterm).
	const AO_OWNER = forceKeep ? "persistent" : "app";
	const ownerTag = {
		AO_OWNER,
		AO_APP_RUN_ID: appRunId,
		// The browser runtime token is handed over through the child's private
		// stdin pipe below. Never put it in the daemon environment, where a
		// same-UID worker could inspect the parent process.
		AO_BROWSER_RUNTIME_TOKEN: "",
		AO_BROWSER_RUNTIME_TOKEN_STDIN: "1",
		// Claude Code Chat uses AO's packaged ACP adapter + Node runtime. The
		// provider executable itself is resolved by the daemon from the user's PATH
		// and passed through CLAUDE_CODE_EXECUTABLE; it is not part of this resource.
		AO_ACP_RUNTIME_DIR:
			process.env.AO_ACP_RUNTIME_DIR ??
			(app.isPackaged
				? path.join(process.resourcesPath, "acp-runtime")
				: path.join(app.getAppPath(), "resources", "acp-runtime")),
	};
	// In dev mode, inject isolation defaults so the dev daemon never collides with
	// the installed app. User-set env vars take priority (checked first).
	const devExtras: Record<string, string> = {};
	if (isDev) {
		if (!process.env.AO_PORT) devExtras.AO_PORT = String(DEV_DAEMON_PORT);
		if (!process.env.AO_RUN_FILE) devExtras.AO_RUN_FILE = runFilePath() ?? "";
		if (!process.env.AO_DATA_DIR) devExtras.AO_DATA_DIR = path.join(os.homedir(), ".ao", DEV_STATE_SUBDIR, "data");
	}
	// Windows keeps the old behavior exactly: no shell probe, no unix PATH floor.
	if (process.platform === "win32") {
		return { ...process.env, ...devExtras, ...telemetryOverrides(), ...ownerTag };
	}
	return buildDaemonEnv(process.env, cachedShellEnv, { ...devExtras, ...telemetryOverrides(), ...ownerTag });
}

function pathKey(value: string): string {
	const resolved = path.resolve(value);
	return process.platform === "win32" ? resolved.toLowerCase() : resolved;
}

function samePath(a: string, b: string): boolean {
	return pathKey(a) === pathKey(b);
}

function pathInside(child: string, parent: string): boolean {
	const childKey = pathKey(child);
	const parentKey = pathKey(parent);
	return childKey === parentKey || childKey.startsWith(parentKey + path.sep);
}

function processAlive(pid: number): boolean {
	if (!pid) return false;
	try {
		process.kill(pid, 0);
		return true;
	} catch {
		return false;
	}
}

async function readDaemonProbe(port: number, endpoint: "healthz" | "readyz"): Promise<DaemonProbe | null> {
	const controller = new AbortController();
	const timer = setTimeout(() => controller.abort(), DAEMON_PROBE_TIMEOUT_MS);
	try {
		const response = await net.fetch(`http://127.0.0.1:${port}/${endpoint}`, { signal: controller.signal });
		if (!response.ok) return null;
		return parseDaemonProbe(endpoint, await response.json());
	} catch {
		return null;
	} finally {
		clearTimeout(timer);
	}
}

function daemonIdentityError(launch: DaemonLaunchSpec, probe: DaemonProbe): string | null {
	if (launch.source === "dev") {
		const cwdMatches = probe.workingDirectory ? samePath(probe.workingDirectory, launch.cwd) : false;
		const startupCwdMatches = probe.startupWorkingDirectory
			? samePath(probe.startupWorkingDirectory, launch.cwd)
			: false;
		const executableMatches = probe.executablePath ? pathInside(probe.executablePath, launch.cwd) : false;
		if (!probe.workingDirectory && !probe.startupWorkingDirectory && !probe.executablePath) {
			return "An older AO daemon is already running, but it does not report its checkout identity. Stop it and restart this app.";
		}
		if (!cwdMatches && !startupCwdMatches && !executableMatches) {
			const actual =
				probe.startupWorkingDirectory ?? probe.workingDirectory ?? probe.executablePath ?? "an unknown location";
			return `Another AO daemon is already running from ${actual}; expected this checkout at ${launch.cwd}. Stop the other daemon before using this checkout.`;
		}
		return null;
	}

	if (launch.source === "bundled") {
		if (!probe.executablePath) {
			return "An older AO daemon is already running, but it does not report its binary path. Stop it and restart this app.";
		}
		if (!samePath(probe.executablePath, launch.command)) {
			return `Another AO daemon is already running from ${probe.executablePath}; expected ${launch.command}. Stop the other daemon before using this app.`;
		}
	}
	return null;
}

/**
 * Establish (or re-establish) the OS-native liveness link to the daemon's
 * supervisor socket. Holding this connection keeps the daemon alive: when
 * Electron exits for any reason (Cmd+Q, crash, SIGKILL), the OS closes the fd
 * and the daemon detects EOF, then self-stops after its ~5s grace period.
 *
 * Called unconditionally on the spawn path (we always own that daemon).
 * Called on the attach path only when the daemon is app-owned (owner === "app");
 * headless `ao start` daemons stay unlinked so they remain persistent after
 * app quit.
 */
function supervisorPipeFromRunFile(rfp: string | null): string {
	if (!rfp) return "\\\\.\\pipe\\ao-supervise";
	const dir = path.basename(path.dirname(rfp));
	if (dir === ".ao" || dir === "." || dir === "") return "\\\\.\\pipe\\ao-supervise";
	return "\\\\.\\pipe\\ao-supervise-" + dir.replace(/[^a-zA-Z0-9-]/g, "-");
}

function disposeBrowserRuntimeLink(): void {
	browserRuntimeLink?.dispose();
	browserRuntimeLink = null;
	browserRuntimeLinkIdentity = null;
}

function establishBrowserRuntimeLink(): void {
	if (!browserViewHost) return;
	const rfp = runFilePath();
	if (!rfp) {
		console.warn("AO: browser runtime link skipped; run-file path unavailable");
		return;
	}
	let runInfo: ReturnType<typeof parseRunFile> = null;
	try {
		runInfo = parseRunFile(readFileSync(rfp, "utf8"));
	} catch {
		// Daemon readiness will retry this after running.json becomes readable.
	}
	const address = runInfo?.browserRuntimeAddress;
	if (!address) {
		console.warn("AO: browser runtime link skipped; daemon did not publish an address");
		return;
	}
	const token = browserRuntimeToken;
	const identity = {
		pid: runInfo?.pid ?? 0,
		startedAtMs: runInfo?.startedAtMs ?? 0,
		address,
		token,
	};
	if (browserRuntimeLink && browserRuntimeLinkIdentity && sameBrowserRuntimeIdentity(browserRuntimeLinkIdentity, identity)) {
		return;
	}
	if (browserRuntimeLink) disposeBrowserRuntimeLink();
	browserRuntimeLink = connectBrowserRuntime(address, {
		token,
		execute: (command, signal) => {
			const host = browserViewHost;
			if (!host) {
				throw Object.assign(new Error("Browser target owner is unavailable"), {
					code: "BROWSER_TARGET_UNAVAILABLE",
				});
			}
			return host.execute(command.sessionId, command.action, command.args, signal);
		},
		log: (message) => console.log(`AO: ${message}`),
	});
	browserRuntimeLinkIdentity = identity;
}

function establishSupervisorLink(): void {
	const rfp = runFilePath();
	const addr =
		process.platform === "win32"
			? supervisorPipeFromRunFile(rfp)
			: rfp
				? path.join(path.dirname(rfp), "supervise.sock")
				: null;
	if (addr) {
		supervisorLink?.dispose();
		supervisorLink = connectSupervisor(addr, {
			log: (msg) => console.log(`AO: ${msg}`),
		});
	} else {
		console.warn("AO: supervisor link skipped; run-file path unavailable");
	}
}

async function inspectExistingDaemon(
	launch: DaemonLaunchSpec,
): Promise<{ status: DaemonStatus; owner: string | undefined; appRunId: string | undefined } | null> {
	const handshakePath = runFilePath();
	let runFileContents: string | null = null;
	if (handshakePath) {
		try {
			runFileContents = await readFile(handshakePath, "utf8");
		} catch {
			runFileContents = null;
		}
	}
	const status = await resolveDaemonFromRunFile({
		runFileContents,
		isProcessAlive: processAlive,
		probe: readDaemonProbe,
		identityError: (probe) => daemonIdentityError(launch, probe),
	});
	if (!status) return null;
	const info = runFileContents ? parseRunFile(runFileContents) : null;
	return { status, owner: info?.owner, appRunId: info?.appRunId };
}

async function gracefullyReplaceDaemonForBrowser(status: DaemonStatus): Promise<void> {
	if (!status.port) throw new Error("the running daemon did not report a port");
	const response = await fetch(`http://127.0.0.1:${status.port}/shutdown`, { method: "POST" });
	if (!response.ok) throw new Error(`daemon shutdown returned HTTP ${response.status}`);

	const deadline = Date.now() + 8_000;
	while (Date.now() < deadline) {
		if (!(await readDaemonProbe(status.port, "healthz"))) return;
		await new Promise<void>((resolve) => setTimeout(resolve, 200));
	}
	throw new Error("the previous daemon did not stop within 8 seconds");
}

async function refreshDaemonStatus(): Promise<DaemonStatus> {
	if (daemonProcess) {
		return daemonStatus;
	}
	const launch = resolveDaemonLaunch(
		process.env,
		app.isPackaged,
		process.resourcesPath,
		app.getAppPath(),
		os.homedir(),
		process.platform,
	);
	if (!launch) return daemonStatus;
	const existing = await inspectExistingDaemon(launch);
	if (existing) {
		if (browserDaemonOwnershipDecision(appRunId, existing).action === "replace") {
			return startDaemon();
		}
		setDaemonStatus(existing.status);
	} else if (
		daemonStatus.state === "ready" ||
		(daemonStatus.state === "error" && (daemonStatus.pid || daemonStatus.port))
	) {
		setDaemonStatus({
			state: "stopped",
			message: "AO daemon is no longer reachable.",
			code: "daemon_unreachable",
		});
	}
	return daemonStatus;
}

async function startDaemon(): Promise<DaemonStatus> {
	if (daemonStartPromise) {
		return daemonStartPromise;
	}
	const startEpoch = daemonStartEpoch;
	const promise = startDaemonInner(startEpoch).finally(() => {
		if (daemonStartPromise === promise) {
			daemonStartPromise = null;
		}
	});
	daemonStartPromise = promise;
	return daemonStartPromise;
}

// The port this Electron instance expects the daemon to bind. In dev mode a
// separate port isolates the dev daemon from the installed-app daemon.
// AO_PORT always wins if set explicitly.
function resolvedDaemonPort(): number {
	return isDev && !process.env.AO_PORT ? DEV_DAEMON_PORT : expectedDaemonPort(process.env);
}

async function startDaemonInner(startEpoch: number): Promise<DaemonStatus> {
	if (daemonProcess) {
		return daemonStatus;
	}

	// Single chokepoint: make sure the login-shell env is resolved before the
	// daemon is spawned, so a Finder/Dock launch hands the daemon a real PATH and
	// shell-exported credentials rather than launchd's minimal env.
	await ensureShellEnv();

	const launch = resolveDaemonLaunch(
		process.env,
		app.isPackaged,
		process.resourcesPath,
		app.getAppPath(),
		os.homedir(),
		process.platform,
	);
	if (!launch) {
		setDaemonStatus({
			state: "stopped",
			message: "AO_DAEMON_COMMAND is not configured; renderer uses loopback REST when available.",
			code: "not_configured",
		});
		return daemonStatus;
	}

	let replacementKeepAlive: boolean | undefined;
	const existing = await inspectExistingDaemon(launch);
	if (startEpoch !== daemonStartEpoch) {
		return daemonStatus;
	}
	if (existing) {
		const ownership = browserDaemonOwnershipDecision(appRunId, existing);
		if (ownership.action === "replace") {
			try {
				await gracefullyReplaceDaemonForBrowser(existing.status);
				replacementKeepAlive = ownership.keepAlive;
			} catch (err) {
				setDaemonStatus({
					state: "error",
					message: `Could not take ownership of the browser runtime: ${(err as Error).message}`,
					code: "not_ready",
				});
				return daemonStatus;
			}
		} else {
			setDaemonStatus(existing.status);
			// Re-link the supervisor only when attaching to an app-owned daemon (one we
			// previously spawned). Headless `ao start` daemons (owner unset) stay unlinked
			// so they remain persistent after app quit.
			if (shouldLinkOnAttach(existing.owner)) {
				establishSupervisorLink();
			}
			return daemonStatus;
		}
	}

	// Defensive: inspectExistingDaemon only attaches when the run-file agrees with
	// a live daemon. Any divergence (missing/stale/unparseable run-file, dead PID,
	// health.pid mismatch) makes it return null — yet a daemon may still be serving
	// the port. Spawning then would just make the Go child refuse and exit 1. Probe
	// the expected port directly, independent of the run-file, and attach if a
	// daemon answers. The expected port (AO_PORT or the default) is exactly the
	// port the Go child would bind and collide on — probing a hardcoded 3001 would
	// miss an AO_PORT override.
	const directDaemon = await resolveDaemonFromPort({
		expectedPort: resolvedDaemonPort(),
		probe: readDaemonProbe,
		identityError: (probe) => daemonIdentityError(launch, probe),
	});
	if (startEpoch !== daemonStartEpoch) {
		return daemonStatus;
	}
	if (directDaemon) {
		let portAttachOwner: string | undefined;
		let portAttachAppRunId: string | undefined;
		// Re-link iff the daemon is app-owned. Read the run-file for the owner tag;
		// if unavailable (run-file absent or unreadable), treat as headless and skip.
		// ponytail: narrow TOCTOU here (the port was probed live, then the run-file
		// is read separately), so in theory a headless daemon could have replaced an
		// app-owned one in the gap. Acceptable: the window is tiny, the worst case is
		// linking a headless daemon, and establishSupervisorLink disposes any prior
		// link so nothing leaks.
		const rfp = runFilePath();
		if (rfp) {
			try {
				const info = parseRunFile(await readFile(rfp, "utf8"));
				portAttachOwner = info?.owner;
				portAttachAppRunId = info?.appRunId;
			} catch {
				// run-file absent or unreadable: treat as headless, skip link.
			}
		}
		const ownership = browserDaemonOwnershipDecision(appRunId, {
			owner: portAttachOwner,
			appRunId: portAttachAppRunId,
		});
		if (ownership.action === "replace") {
			try {
				await gracefullyReplaceDaemonForBrowser(directDaemon);
				replacementKeepAlive = ownership.keepAlive;
			} catch (err) {
				setDaemonStatus({
					state: "error",
					message: `Could not take ownership of the browser runtime: ${(err as Error).message}`,
					code: "not_ready",
				});
				return daemonStatus;
			}
		} else {
			setDaemonStatus(directDaemon);
			if (shouldLinkOnAttach(portAttachOwner)) establishSupervisorLink();
			return daemonStatus;
		}
	}

	// Wedged-orphan kill+replace: both attach paths returned null, but a process
	// may still be holding the port. The only reachable case here is a hung/wedged
	// holder whose run-file PID is still alive but is not answering /healthz (e.g.
	// our own daemon that bound the port and then deadlocked). Two cases are
	// intentionally NOT handled: an identity-mismatched but healthy AO daemon is
	// already surfaced as an error status upstream by resolveDaemonFromPort (not
	// killed here), and a foreign non-AO process holding the port with a dead
	// run-file PID is not replaced (out of scope). When no holder is detectable,
	// skip straight to spawn.
	const orphanProbe = await readDaemonProbe(resolvedDaemonPort(), "healthz");
	const runFilePath_ = runFilePath();
	let runFilePid: number | null = null;
	if (runFilePath_) {
		try {
			runFilePid = parseRunFile(await readFile(runFilePath_, "utf8"))?.pid ?? null;
		} catch {
			// run-file absent or unreadable; proceed without a PID.
		}
	}
	// process.kill(pid, 0) does not kill; it throws iff the PID is not live.
	let holderPidAlive = false;
	if (runFilePid) {
		try {
			process.kill(runFilePid, 0);
			holderPidAlive = true;
		} catch {
			holderPidAlive = false;
		}
	}
	if (shouldReplacePortHolder(orphanProbe, holderPidAlive)) {
		// Use the run-file PID when available; fall back to the probe's reported
		// PID as a last resort (a wedged daemon may not have written a fresh run-file).
		const pidToKill = runFilePid ?? orphanProbe?.pid ?? null;
		if (pidToKill) {
			try {
				process.kill(-pidToKill, "SIGTERM");
			} catch {
				try {
					process.kill(pidToKill, "SIGTERM");
				} catch {
					// process already gone; proceed
				}
			}
		}
		// Poll until the port is free (probe returns null) or 8 s elapses.
		const TAKEOVER_TIMEOUT_MS = 8_000;
		const TAKEOVER_POLL_MS = 200;
		const deadline = Date.now() + TAKEOVER_TIMEOUT_MS;
		while (Date.now() < deadline) {
			const still = await readDaemonProbe(resolvedDaemonPort(), "healthz");
			if (!still) break;
			await new Promise<void>((r) => setTimeout(r, TAKEOVER_POLL_MS));
		}
		// Remove the stale run-file so the new daemon can write a fresh one.
		if (runFilePath_) {
			await rm(runFilePath_, { force: true });
		}
	}

	if (launch.source === "bundled" && !existsSync(launch.command)) {
		setDaemonStatus({
			state: "error",
			message: `Bundled AO daemon binary was not found at ${launch.command}. Rebuild the desktop package.`,
			code: "binary_missing",
		});
		return daemonStatus;
	}

	daemonOutput = "";
	setDaemonStatus({ state: "starting" });
	if (launch.source === "bundled") {
		try {
			await mkdir(launch.cwd, { recursive: true, mode: 0o750 });
		} catch (err) {
			// A failure here (home unwritable, launch.cwd exists as a file) would
			// otherwise reject out of startDaemonInner; boot calls this via
			// `void startDaemon()`, so it would surface as an unhandled rejection
			// and leave the UI stuck on "starting". Report it as a failure instead.
			setDaemonStatus({
				state: "error",
				message: `Could not create the AO data directory at ${launch.cwd}: ${(err as Error).message}`,
				code: "datadir_unwritable",
			});
			return daemonStatus;
		}
	}

	// Capture the spawned handle locally so the async lifecycle listeners act only
	// on THIS process. Without this, a stale exit from an already-stopped daemon
	// could null out a newer daemonProcess started in the meantime, orphaning it.
	//
	// `detached` makes the child its own process-group leader. Because shell:true
	// runs the command through /bin/sh, a plain kill() would only signal the shell
	// wrapper and orphan the real daemon (which keeps holding the port). Killing
	// the whole group via killDaemon() reaches the daemon and any PTY children.
	//
	// AO_KEEP_DAEMON: the daemon must survive this app, so it cannot inherit
	// Electron-owned stdout/stderr pipes — when Electron exits, the pipe read
	// ends close and the daemon's next stderr log write (SIGPIPE/EPIPE) kills it,
	// defeating the keep-alive. Redirect stdio to ~/.ao/daemon.log and unref the
	// child so the parent does not wait on it. Port discovery then relies on the
	// running.json handshake (the log pipe scan is skipped).
	const keep = replacementKeepAlive ?? keepDaemonAlive(process.env);
	let keepDaemonLogFd: number | undefined;
	let stdio: "pipe" | "ignore" | ["pipe", number | "ignore", number | "ignore"] = "pipe";
	if (keep) {
		const logPath = path.join(os.homedir(), ".ao", "daemon.log");
		try {
			keepDaemonLogFd = openSync(logPath, "a");
			stdio = ["pipe", keepDaemonLogFd, keepDaemonLogFd];
		} catch {
			// Log redirect failed (e.g. ~/.ao not creatable, permission denied):
			// fall back to "ignore" so the daemon still runs, but warn — otherwise
			// a long-lived keep-alive daemon would run with zero log output.
			console.warn(`AO: keep-daemon log redirect failed; daemon will run with stdio disabled: ${logPath}`);
			keepDaemonLogFd = undefined;
			stdio = ["pipe", "ignore", "ignore"];
		}
	}
	let child: ChildProcess;
	try {
		child = spawn(launch.command, launch.args, {
			cwd: launch.cwd,
			env: daemonEnv(keep),
			shell: launch.shell,
			detached: true,
			// Hide the daemon's console on a Windows GUI launch (no flashing terminal).
			windowsHide: true,
			stdio,
		});
	} catch (err) {
		// spawn can throw synchronously for invalid args; don't leak the log fd.
		if (keepDaemonLogFd !== undefined) {
			try {
				closeSync(keepDaemonLogFd);
			} catch {
				// best-effort — the throw below is the real error
			}
		}
		throw err;
	}
	// The child inherited its own copy of the log fd via stdio at spawn time;
	// close the parent's copy now so each keep-alive start/stop cycle does not
	// accumulate open descriptors until the process limit.
	if (keepDaemonLogFd !== undefined) {
		try {
			closeSync(keepDaemonLogFd);
		} catch {
			// best-effort — the child still holds its inherited copy
		}
		keepDaemonLogFd = undefined;
	}
	// daemonEnv deliberately contains only a marker. Deliver the actual token
	// over the inherited pipe, then close it; Go does not pass this descriptor to
	// worker processes when it spawns them.
	if (child.stdin) {
		child.stdin.on("error", () => undefined);
		child.stdin.end(`${browserRuntimeToken}\n`);
	} else {
		console.warn("AO: browser runtime token handoff pipe was unavailable");
	}
	if (keep) child.unref();
	daemonProcess = child;

	// Discover the port the daemon ACTUALLY bound rather than trusting AO_PORT:
	// the daemon may fall back to a different port than the one requested. Two
	// confirmed sources race — the "daemon listening" slog line (stderr, but both
	// streams are scanned) and the running.json handshake — first one wins.
	const spawnedAtMs = Date.now();
	let portConfirmed = false;
	let runFileTimer: ReturnType<typeof setInterval> | undefined;
	let fallbackTimer: ReturnType<typeof setTimeout> | undefined;

	const stopDiscovery = () => {
		if (runFileTimer) clearInterval(runFileTimer);
		runFileTimer = undefined;
		if (fallbackTimer) clearTimeout(fallbackTimer);
		fallbackTimer = undefined;
	};

	const reportBoundPort = (port: number) => {
		if (portConfirmed || daemonProcess !== child || daemonStoppingProcess === child) return;
		portConfirmed = true;
		stopDiscovery();
		setDaemonStatus({ state: "ready", port });

		// Establish the OS-native liveness link on the spawn path (we own this
		// daemon). Holding the connection keeps the daemon alive; when Electron
		// exits for any reason, the OS closes the fd and the daemon detects EOF,
		// then self-stops after its ~5s grace period. The attach paths link only
		// when the daemon is app-owned (see establishSupervisorLink +
		// shouldLinkOnAttach); headless `ao start` daemons stay unlinked so they
		// remain persistent across app quit.
		//
		// AO_KEEP_DAEMON opts out of the link entirely: the daemon is spawned but
		// survives the window closing, stopping only on an explicit `ao stop`.
		// Reuse the `keep` captured at spawn rather than re-reading process.env
		// here: the flag is a property of this spawn, not a value that should be
		// able to flip between spawn and port-confirmation. (The process.on("exit")
		// orphan-cleanup below re-reads process.env because this `keep` is scoped
		// to the spawn function — AO_KEEP_DAEMON is set once at startup and never
		// mutated, so both reads agree.)
		if (!keep) {
			establishSupervisorLink();
		}
	};

	// One scanner per stream: each keeps its own partial-line buffer.
	// Skipped under AO_KEEP_DAEMON: stdio is redirected to a log file (no pipes
	// to scan), so port discovery falls back to the running.json handshake below.
	if (!keep) {
		const scanStdout = createListenPortScanner(reportBoundPort);
		const scanStderr = createListenPortScanner(reportBoundPort);

		child.stdout?.on("data", (chunk: Buffer) => {
			const text = chunk.toString("utf8");
			appendDaemonOutput(text);
			console.log(text.trimEnd());
			scanStdout(text);
		});

		child.stderr?.on("data", (chunk: Buffer) => {
			const text = chunk.toString("utf8");
			appendDaemonOutput(text);
			console.error(text.trimEnd());
			scanStderr(text);
		});
	}

	const handshakePath = runFilePath();
	if (handshakePath) {
		runFileTimer = setInterval(() => {
			readFile(handshakePath, "utf8")
				.then((contents) => {
					const info = parseRunFile(contents);
					// Ignore a stale handshake left by a previous daemon: only trust a
					// file written at/after this spawn.
					if (info && info.startedAtMs >= spawnedAtMs - RUN_FILE_FRESHNESS_SKEW_MS) {
						reportBoundPort(info.port);
					}
				})
				.catch(() => undefined); // absent until the daemon binds; keep polling
		}, RUN_FILE_POLL_MS);
	}

	// Neither source confirmed startup. Surface the captured process output so
	// the renderer can explain why boot stalled instead of spinning forever or
	// attempting workspace requests against an assumed port.
	fallbackTimer = setTimeout(() => {
		if (portConfirmed || daemonProcess !== child || daemonStoppingProcess === child) return;
		// Keep running.json polling alive after surfacing the timeout. In
		// keep-daemon mode there are no stdout/stderr scanners, so the run file is
		// the only way a slow-but-successful boot can still recover to ready.
		fallbackTimer = undefined;
		setDaemonStatus({
			state: "error",
			message: "AO daemon did not finish starting within 30 seconds.",
			details:
				daemonOutput.trim() ||
				[
					"No startup output was captured.",
					`Executable: ${launch.command}`,
					`Working directory: ${launch.cwd}`,
					`Expected port confirmation from: ${handshakePath ?? "running.json"}`,
				].join("\n"),
			code: "not_ready",
			executablePath: launch.command,
			workingDirectory: launch.cwd,
		});
	}, PORT_DISCOVERY_TIMEOUT_MS);

	child.once("error", (error) => {
		stopDiscovery();
		if (daemonProcess !== child) return;
		daemonProcess = null;
		if (daemonStoppingProcess === child) daemonStoppingProcess = null;
		setDaemonStatus({
			state: "error",
			message: error.message,
			details: daemonOutput.trim() || undefined,
			code: "spawn_failed",
		});
	});

	child.once("exit", (code, signal) => {
		stopDiscovery();
		if (daemonProcess !== child) return;
		daemonProcess = null;
		// An explicit stopDaemon() already set a clean `{ state: "stopped" }`.
		// daemon-telemetry reports any status carrying a `code` as
		// ao.renderer.daemon_failure, so don't stamp `code: "exited"` on a stop
		// the user or app asked for — that would count intentional stops as
		// failures. Preserve the clean stopped status instead.
		if (daemonStoppingProcess === child) {
			daemonStoppingProcess = null;
			if (daemonRestartAfterExitProcess === child) {
				daemonRestartAfterExitProcess = null;
				void startDaemonForRestart();
				return;
			}
			setDaemonStatus({ state: "stopped" });
			return;
		}
		setDaemonStatus({
			state: "stopped",
			message: signal ? `Daemon exited with ${signal}` : `Daemon exited with code ${code ?? "unknown"}`,
			details: daemonOutput.trim() || undefined,
			code: "exited",
			exitCode: code,
			signal,
		});
	});

	return daemonStatus;
}

// Signal the daemon's whole process group so the kill reaches the real daemon
// behind the /bin/sh wrapper (and any PTY children it forked), not just the
// shell. Falls back to a direct kill if the group signal can't be delivered
// (e.g. the process already exited).
function killDaemon(child: ChildProcess): void {
	if (child.pid === undefined) return;
	try {
		process.kill(-child.pid, "SIGTERM");
	} catch {
		child.kill("SIGTERM");
	}
}

function stopDaemon(): DaemonStatus {
	daemonStartEpoch += 1;
	daemonStartPromise = null;
	// An explicit stop (or a newer restart request) cancels any deferred restart
	// left waiting for a previously slow child to exit.
	daemonRestartAfterExitProcess = null;
	if (!daemonProcess) {
		setDaemonStatus({ state: "stopped" });
		return daemonStatus;
	}

	daemonStoppingProcess = daemonProcess;
	// Drop the liveness link: an explicit stop is not a frontend death, so stop
	// holding the socket open (and stop the reconnect loop retrying a dead daemon).
	// A later daemon:start re-establishes the link via reportBoundPort.
	supervisorLink?.dispose();
	supervisorLink = null;
	disposeBrowserRuntimeLink();
	killDaemon(daemonProcess);
	setDaemonStatus({ state: "stopped" });
	return daemonStatus;
}

function reportDaemonRestartFailure(error: unknown): DaemonStatus {
	setDaemonStatus({
		state: "error",
		message: `Could not restart the AO daemon: ${error instanceof Error ? error.message : String(error)}`,
		details: daemonOutput.trim() || undefined,
		code: "spawn_failed",
	});
	return daemonStatus;
}

async function startDaemonForRestart(): Promise<DaemonStatus> {
	try {
		return await startDaemon();
	} catch (error) {
		return reportDaemonRestartFailure(error);
	}
}

async function restartDaemon(): Promise<DaemonStatus> {
	const child = daemonProcess;
	if (!child) return startDaemonForRestart();

	const exited = new Promise<boolean>((resolve) => {
		let settled = false;
		const finish = (didExit: boolean) => {
			if (settled) return;
			settled = true;
			clearTimeout(timer);
			child.off("exit", onExit);
			resolve(didExit);
		};
		const onExit = () => finish(true);
		const timer = setTimeout(() => finish(false), DAEMON_RESTART_STOP_TIMEOUT_MS);
		child.once("exit", onExit);
	});

	stopDaemon();
	// Register the deferred intent immediately after signaling the child. The
	// exit can race the five-second UI timeout, so waiting until after the
	// timeout to set this would occasionally miss the only exit event.
	daemonRestartAfterExitProcess = child;
	if (!(await exited)) {
		// The process may still honor SIGTERM after the UI timeout. Keep the
		// restart intent attached to that exact child so its eventual exit starts
		// a replacement instead of collapsing back to a code-less stopped state.
		setDaemonStatus({
			state: "error",
			message: "AO daemon is still stopping. It will restart automatically when shutdown completes.",
			details: daemonOutput.trim() || undefined,
			code: "not_ready",
		});
		return daemonStatus;
	}
	return startDaemonForRestart();
}

ipcMain.handle("daemon:getStatus", () => refreshDaemonStatus());
ipcMain.handle("daemon:start", () => startDaemon());
ipcMain.handle("daemon:stop", () => stopDaemon());
ipcMain.handle("daemon:restart", async () => {
	try {
		return await restartDaemon();
	} catch (error) {
		return reportDaemonRestartFailure(error);
	}
});
ipcMain.handle("app:getVersion", () => app.getVersion());
ipcMain.handle("app:openExternal", async (_event, url: string) => {
	await openAllowedAppExternalURL(url, shell);
});

// Re-tint the native window-button overlay (min/max/close) to match the active
// theme; the renderer calls this on theme change. No-op unless the window was
// created with a titleBarOverlay (Windows only).
ipcMain.handle("window:setOverlay", (_event, overlay: { color: string; symbolColor: string }) => {
	if (process.platform !== "win32" || !mainWindow) return;
	try {
		mainWindow.setTitleBarOverlay({ ...overlay, height: TITLEBAR_HEIGHT });
	} catch {
		// Window has no overlay on this platform; ignore.
	}
});

ipcMain.handle("window:isFullScreen", () => mainWindow?.isFullScreen() ?? false);

// Drive Electron's nativeTheme from the app's theme preference so embedded
// preview WebContentsViews (which follow prefers-color-scheme) flip in step with
// the shell. The three preference values map 1:1 onto themeSource; "system" keeps
// both the preview and the shell's own matchMedia following the OS.
ipcMain.handle("theme:set", async (_event, preference: "light" | "dark" | "system") => {
	if (preference !== "light" && preference !== "dark" && preference !== "system") return;
	nativeTheme.themeSource = preference;
	const runFile = runFilePath();
	if (runFile) {
		const dir = path.dirname(runFile);
		const current = await readUiSettings(dir);
		await writeUiSettings(dir, { ...current, themePreference: preference });
	}
	trayController?.setThemePreference(preference);
});

// Renderer calls this when focus lands on real shell UI (not the titlebar menu), so menu:action's panel fallback below doesn't go stale.
ipcMain.on("shell:focus", () => browserViewHost?.forgetLastFocusedPanel());

ipcMain.on(SET_CLOSE_SHELL_TERMINAL_SHORTCUT_ENABLED_CHANNEL, (_event, enabled: unknown) => {
	closeShellTerminalShortcutEnabled = enabled === true;
});

// Backs the custom title-bar menu (WindowTitlebar). Each item maps to the same
// action the native default menu would have performed.
ipcMain.handle("menu:action", (_event, action: string) => {
	const win = mainWindow;
	if (!win) return;
	// Clicking this shell-painted menu moves focus off the panel, so prefer the last-focused panel, else the focused contents, else the shell.
	const focused = webContents.getFocusedWebContents();
	const wc =
		(focused && focused !== win.webContents ? focused : browserViewHost?.getLastFocusedPanelContents()) ??
		win.webContents;
	switch (action) {
		case "edit.undo":
			return wc.undo();
		case "edit.redo":
			return wc.redo();
		case "edit.cut":
			return wc.cut();
		case "edit.copy":
			return wc.copy();
		case "edit.paste":
			return wc.paste();
		case "edit.selectAll":
			return wc.selectAll();
		case "view.reload":
			return wc.reload();
		case "view.devtools":
			return browserViewHost?.toggleDevToolsForLastFocused().then((state) => {
				if (state) return state;
				return wc.toggleDevTools();
			}).catch(() => wc.toggleDevTools()) ?? wc.toggleDevTools();
		case "view.zoomIn":
			return wc.setZoomLevel(wc.getZoomLevel() + 0.5);
		case "view.zoomOut":
			return wc.setZoomLevel(wc.getZoomLevel() - 0.5);
		case "view.zoomReset":
			return wc.setZoomLevel(0);
		case "view.fullscreen":
			return win.setFullScreen(!win.isFullScreen());
		case "window.minimize":
			return win.minimize();
		case "window.maximize":
			return win.isMaximized() ? win.unmaximize() : win.maximize();
		case "window.close":
			return win.close();
		case "app.quit":
			return app.quit();
		case "help.shortcuts":
			win.webContents.focus();
			return win.webContents.send(KEYBOARD_SHORTCUTS_HELP_CHANNEL);
		case "help.about":
			void dialog.showMessageBox(win, {
				type: "info",
				title: "About Agent Orchestrator",
				message: "Agent Orchestrator",
				detail: `Version ${app.getVersion()}`,
				buttons: ["OK"],
			});
			return;
	}
});
ipcMain.handle("telemetry:getBootstrap", () =>
	buildTelemetryBootstrap(process.env, app.getVersion(), process.platform, os.homedir(), app.isPackaged),
);
async function chooseDirectory(title: string): Promise<string | null> {
	const options: OpenDialogOptions = {
		properties: ["openDirectory"],
		title,
	};
	// On Windows, parenting the common file dialog forces a repaint of the main
	// window while Explorer initializes, which produces a visible white flash.
	// The unparented native dialog remains foregrounded by Electron without that
	// compositor handoff.
	const result = await dialog.showOpenDialog(options);

	if (result.canceled) return null;
	return result.filePaths[0] ?? null;
}

ipcMain.handle("app:chooseDirectory", async (_event, title?: string) => {
	return chooseDirectory(typeof title === "string" && title.trim() ? title : "Choose a git repository");
});
ipcMain.handle("app:scanImportFolder", async (_event, input: { path: string; mode: "project" | "workspace" }) => {
	await ensureShellEnv();
	return scanImportFolder(input.path, input.mode, { env: daemonEnv(), homeDir: os.homedir() });
});
ipcMain.handle("app:checkAncestorRepo", async (_event, path: string) => {
	await ensureShellEnv();
	return ancestorRepositorySetupWarning(path, { env: daemonEnv(), homeDir: os.homedir() });
});
ipcMain.handle("clipboard:writeText", (_event, text: string) => {
	clipboard.writeText(text, "clipboard");
	if (process.platform === "linux") {
		clipboard.writeText(text, "selection");
	}
});
ipcMain.handle("clipboard:readText", () => clipboard.readText());

// A file dropped onto the terminal is delivered as raw bytes (its original path
// is unavailable to the sandboxed renderer on macOS — see webUtils.getPathForFile
// regressions in Electron 30-33). Stash the bytes under the app's own state dir
// and return the path so the terminal can insert it, mirroring how a native
// terminal inserts a dropped file's path.
ipcMain.handle("terminal:saveDroppedFile", async (_event, input: { name: string; bytes: Uint8Array }) => {
	const dir = path.join(app.getPath("userData"), "terminal-drops");
	await mkdir(dir, { recursive: true });
	const base = path.basename(input.name || "").replace(/[^\w.-]+/g, "_") || "dropped";
	const target = path.join(dir, `${Date.now()}-${base}`);
	await writeFile(target, Buffer.from(input.bytes));
	return target;
});

ipcMain.handle("appState:getMigration", async (): Promise<MigrationState> => {
	const runFile = runFilePath();
	if (!runFile) return { status: "pending" };
	return readMigrationState(path.dirname(runFile));
});
ipcMain.handle("appState:setMigration", async (_event, migration: MigrationState) => {
	const runFile = runFilePath();
	if (!runFile) return;
	await updateMigration({ stateDir: path.dirname(runFile), migration, now: () => new Date() });
});

ipcMain.handle("updateSettings:get", async (): Promise<UpdateSettings> => {
	const runFile = runFilePath();
	if (!runFile) return { enabled: false, channel: "latest", nightlyAck: false, feature: null };
	return readUpdateSettings(path.dirname(runFile));
});
ipcMain.handle("updateSettings:set", async (_event, settings: UpdateSettings) => {
	const runFile = runFilePath();
	if (!runFile) return;
	await setUpdateSettings(path.dirname(runFile), settings);
	trayController?.setUpdateSettings(settings);
});

ipcMain.handle("uiSettings:get", async (): Promise<UiSettings> => {
	const runFile = runFilePath();
	if (!runFile) return { locale: "en", themePreference: "system" };
	return readUiSettings(path.dirname(runFile));
});
	ipcMain.handle("uiSettings:set", async (_event, settings: Partial<UiSettings>): Promise<UiSettings> => {
		const runFile = runFilePath();
	const current = runFile ? await readUiSettings(path.dirname(runFile)) : coerceUiSettings({});
	const merged = coerceUiSettings({ ...current, ...settings });
	const result = !runFile ? merged : await writeUiSettings(path.dirname(runFile), merged);
	trayController?.setLocale(result.locale);
	trayController?.setThemePreference(result.themePreference);
	return result;
	});

ipcMain.handle("keybindings:get", (): KeybindingOverrides => keybindingOverrides);
ipcMain.handle("keybindings:set", async (_event, overrides: KeybindingOverrides): Promise<KeybindingOverrides> => {
	const runFile = runFilePath();
	if (!runFile) return keybindingOverrides;
	keybindingOverrides = await writeKeybindingOverrides(path.dirname(runFile), overrides);
	return keybindingOverrides;
});
ipcMain.handle("keybindings:setRecording", (event, active: unknown): void => {
	if (event.sender !== mainWindow?.webContents || typeof active !== "boolean") return;
	keybindingRecordingActive = active;
});

ipcMain.handle("featureBuilds:list", () => listFeatureBuilds());
ipcMain.handle("featureBuilds:getActive", () => getActiveFeatureBuild());

ipcMain.handle("updates:getStatus", (): UpdateStatus => getUpdateStatus());
ipcMain.handle("updates:check", async (_event, options?: UpdateCheckOptions) => {
	const runFile = runFilePath();
	if (!runFile) return;
	await checkForUpdatesNow(path.dirname(runFile), options);
});
ipcMain.handle("updates:returnHome", async (_event, requestId?: string) => {
	const runFile = runFilePath();
	if (!runFile) return;
	await returnToHome(path.dirname(runFile), requestId);
});
ipcMain.handle("updates:download", async (_event, requestId?: string) => {
	await downloadUpdateNow(requestId);
});
ipcMain.handle("updates:install", () => {
	quitAndInstallUpdate();
});

ipcMain.handle(
	"notifications:show",
	(_event, notification: { id: string; title: string; body?: string; type?: string }) => {
		if (!notification.id || !mainWindow) return;
		// Only signal when the window isn't already focused (the user is looking).
		if (mainWindow.isFocused()) return;
		// OS toast: a native banner the user can click to jump straight back to the
		// session. Fires for every backend notification type (see shouldToast), so a
		// new type in notification.go never silently loses its toast.
		if (shouldToast(notification, ElectronNotification.isSupported())) {
			const toast = new ElectronNotification({
				title: notification.title,
				body: notification.body,
				// AO logo as the notification icon on Windows/Linux. Omitted on macOS,
				// where a custom icon renders only as a redundant right-side content image —
				// macOS uses the app-bundle icon (the AO logo in a packaged build) as the
				// single main icon.
				icon: process.platform === "darwin" ? undefined : windowIconPath(),
			});
			toast.on("click", () => {
				if (!mainWindow) return;
				if (mainWindow.isMinimized()) mainWindow.restore();
				mainWindow.show();
				mainWindow.focus();
				mainWindow.webContents.send("notifications:click", notification.id);
			});
			toast.show();
		}

		// Dock (macOS) / taskbar (Windows/Linux) attention signal — only for the
		// actionable types. A merged/closed PR still toasts above, but shouldn't
		// bounce the dock as insistently as an agent blocked waiting on the user.
		if (shouldSignalAttention(notification.type)) {
			if (process.platform === "darwin" && app.dock) {
				app.dock.bounce("informational");
			} else if (process.platform === "win32" || process.platform === "linux") {
				if (!isFlashing) {
					isFlashing = true;
					mainWindow.flashFrame(true);
					mainWindow.once("focus", () => {
						isFlashing = false;
						mainWindow?.flashFrame(false);
					});
				}
			}
		}
	},
);

// Dev-only: force attention signal regardless of window focus (for testing)
if (!app.isPackaged) {
	ipcMain.handle("notifications:devBounce", () => {
		if (!mainWindow) return;
		if (process.platform === "darwin") {
			const id = app.dock?.bounce("critical");
			setTimeout(() => {
				if (id !== undefined) app.dock?.cancelBounce(id);
			}, 2000);
		} else if (process.platform === "win32" || process.platform === "linux") {
			mainWindow.flashFrame(true);
			setTimeout(() => {
				mainWindow?.flashFrame(false);
			}, 2000);
		}
	});
}

ipcMain.handle("notifications:setBadge", (_event, count: number) => {
	if (!mainWindow) return { error: "no mainWindow" };
	const n = Number.isFinite(count) && count > 0 ? Math.floor(count) : 0;
	if (process.platform === "darwin") {
		const dock = app.dock;
		if (!dock) return { error: "no app.dock" };
		try {
			dock.setBadge(n > 0 ? String(n) : "");
		} catch (e) {
			return { error: String(e) };
		}
	} else if (process.platform === "win32") {
		if (n > 0) {
			// Pre-built red dot PNG overlay — indicates unread without needing Canvas.
			const badgeDataUrl =
				"data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAABAAAAAQCAYAAAAf8/9hAAAAN0lEQVR42mNgoAW4o6b2HxumSDNRhhDSjNcQYjVjNYRUzRiGjBpABQMojkaqJCSqJGWqZCZSAQASKE/UCaPI8AAAAABJRU5ErkJggg==";
			const icon = nativeImage.createFromDataURL(badgeDataUrl);
			mainWindow.setOverlayIcon(icon, `${n} unread`);
		} else {
			mainWindow.setOverlayIcon(null, "");
		}
	} else if (process.platform === "linux") {
		// setBadgeCount drives a numeric launcher badge on Unity only, and only
		// when the app ships a matching .desktop entry. Other Linux launchers
		// (KDE, GNOME) expose no equivalent API, so the call is a no-op there.
		// Surface the boolean result so the renderer can tell the badge was not
		// applied instead of assuming unconditional success.
		return { ok: app.setBadgeCount(n) };
	}
	return { ok: true };
});

ipcMain.on(TRAY_SET_ATTENTION_STATE_CHANNEL, (event, state) => trayLifecycle.handleSetAttentionState(event, state));

ipcMain.on(TRAY_RENDERER_READY_CHANNEL, (event) => trayLifecycle.handleRendererReady(event));

// Auto-update only runs for packaged builds reading the GitHub Releases feed
// (see forge.config.ts publishers). In dev there is no feed, so it is skipped.
// A live updater additionally requires a signed + notarized build — see
// frontend/docs/desktop-release.md.
function initAutoUpdates(): void {
	if (!app.isPackaged) return;
	const runFile = runFilePath();
	if (!runFile) return;
	const stateDir = path.dirname(runFile);
	void ensureUpdatePrefs(stateDir).then(() => startAutoUpdates(stateDir));
}

// Resolve the bundle path `ao start` will later `open` and stat as a usable app.
// On macOS process.execPath is .../Agent Orchestrator.app/Contents/MacOS/<exe>;
// the thing `ao start` opens is the enclosing `.app` directory, so walk up three
// levels (MacOS -> Contents -> .app). app.getAppPath() is WRONG here: it returns
// the app.asar archive path inside the bundle, not the bundle itself.
// On win32/linux there is no .app wrapper, so record execPath; a richer
// resolveApp() for those platforms lands in T6/T7.
function resolveBundlePath(): string {
	if (process.platform === "darwin") {
		return path.resolve(process.execPath, "..", "..", "..");
	}
	return process.execPath;
}

// `ao start` opens the app with `--installed-via=<value>` so the app can record
// how it arrived on first marker creation. Parse it out of argv; absent => the
// marker defaults installSource to "unknown".
function parseInstalledVia(argv: string[]): string | undefined {
	const flag = argv.find((a) => a.startsWith("--installed-via="));
	return flag ? flag.slice("--installed-via=".length) : undefined;
}

// Write ~/.ao/app-state.json so `ao start`'s resolveApp() can find this bundle
// (spec §7.1). The app is the sole writer (invariant 3) and writes every launch.
// A failure here must NOT block startup, so the caller wraps this in try/catch;
// we still surface it via the log.
async function writeAppStateOnLaunch(): Promise<void> {
	// Reuse the same ~/.ao resolution as running.json; the marker lives beside it
	// (the Go side computes its dir as dirname(RunFilePath)). runFilePath() returns
	// null only when the home dir is unresolvable, in which case we cannot place
	// the marker; the caller's try/catch logs it.
	const runFile = runFilePath();
	if (!runFile) {
		throw new Error("cannot resolve ~/.ao run-file path; skipping app-state marker");
	}
	const stateDir = path.dirname(runFile);
	await writeAppStateMarker({
		stateDir,
		appPath: resolveBundlePath(),
		version: app.getVersion(),
		installedVia: parseInstalledVia(process.argv),
		now: () => new Date(),
	});
}

app.whenReady().then(async () => {
	// Capture install provenance BEFORE relocation. moveToApplicationsFolder()
	// relaunches from /Applications WITHOUT forwarding our --installed-via arg, and
	// code past a successful move never runs in this instance, so a post-move-only
	// write would record installSource="unknown" and the sticky logic in
	// writeAppStateMarker would then lock it there forever. Writing now (only when
	// the arg is present, i.e. the npm-bootstrap launch) persists the source so the
	// post-move instance preserves it while refreshing appPath to /Applications.
	if (parseInstalledVia(process.argv)) {
		try {
			await writeAppStateOnLaunch();
		} catch (err) {
			console.error("failed to write pre-relocation app-state marker:", err);
		}
	}

	if (process.platform === "darwin" && app.isPackaged) {
		try {
			// On success this restarts the app from /Applications, so code past
			// here only runs when no move happened (already there, or declined).
			app.moveToApplicationsFolder();
		} catch (err) {
			console.error("relocation to Applications failed:", err);
		}
	}

	// Refresh the marker post-relocation so appPath records the final bundle path;
	// the sticky installSource preserves the value captured above. A marker-write
	// failure is non-fatal: log and continue so the app still boots.
	try {
		await writeAppStateOnLaunch();
	} catch (err) {
		console.error("failed to write app-state marker:", err);
	}

	const keybindingRunFile = runFilePath();
	if (keybindingRunFile) {
		keybindingOverrides = await readKeybindingOverrides(path.dirname(keybindingRunFile));
	}

	registerRendererProtocol();
	applyRuntimeAppIcon();
	if (isTrayEnabled(process.platform, app.isPackaged, app.getVersion())) {
		const initialUiSettings = keybindingRunFile ? await readUiSettings(path.dirname(keybindingRunFile)) : coerceUiSettings({});
		const initialUpdateSettings = keybindingRunFile
			? await readUpdateSettings(path.dirname(keybindingRunFile))
			: { enabled: false, channel: "latest" as const, nightlyAck: false, feature: null };
		trayController = createTrayController({
			focusWindow: focusMainWindow,
			openSession: trayLifecycle.openSession,
			openSettings: () => {
				focusMainWindow();
				mainWindow?.webContents.send(OPEN_SETTINGS_SHORTCUT_CHANNEL);
			},
			locale: initialUiSettings.locale,
			themePreference: initialUiSettings.themePreference,
			onThemeSelect: (preference) => {
				void (async () => {
					nativeTheme.themeSource = preference;
					const runFile = runFilePath();
					if (runFile) {
						const dir = path.dirname(runFile);
						const current = await readUiSettings(dir);
						await writeUiSettings(dir, { ...current, themePreference: preference });
					}
					trayController?.setThemePreference(preference);
					mainWindow?.webContents.send(THEME_CHANGED_CHANNEL, preference);
				})();
			},
			updateSettings: initialUpdateSettings,
			onUpdateChannelSelect: (channel) => {
				void (async () => {
					const runFile = runFilePath();
					if (!runFile) return;
					const dir = path.dirname(runFile);
					const current = await readUpdateSettings(dir);
					const next: UpdateSettings = { ...current, channel };
					await setUpdateSettings(dir, next);
					trayController?.setUpdateSettings(next);
				})();
			},
			onUpdateEnabledToggle: (enabled) => {
				void (async () => {
					const runFile = runFilePath();
					if (!runFile) return;
					const dir = path.dirname(runFile);
					const current = await readUpdateSettings(dir);
					const next: UpdateSettings = { ...current, enabled };
					await setUpdateSettings(dir, next);
					trayController?.setUpdateSettings(next);
				})();
			},
			onCheckForUpdates: () => {
				const runFile = runFilePath();
				if (!runFile) return;
				void checkForUpdatesNow(path.dirname(runFile), {});
			},
		});
	}
	await createWindow();
	void startDaemon();
	initAutoUpdates();

	app.on("activate", () => {
		if (BrowserWindow.getAllWindows().length === 0) {
			void createWindow().catch((error) => console.error("failed to recreate main window:", error));
		}
	});
});

// Daemon teardown is now handled via the OS-native supervisor socket: the daemon
// self-stops ~5s after the last client (this process) drops its connection.
// The supervisorLink fd is NOT explicitly closed on quit; the OS closes it when
// the process exits for any reason (Cmd+Q, crash, SIGKILL). Sessions survive.
app.on("before-quit", (event) => {
	browserQuitRequested = true;
	disposeBrowserRuntimeLink();
	trayLifecycle.dispose();
	trayController = null;
	if (!browserCleanupComplete) {
		event.preventDefault();
		if (!browserQuitCleanupPromise) {
			browserQuitCleanupPromise = disposeAllBrowserViewHosts().finally(() => {
				browserCleanupComplete = true;
				browserQuitCleanupPromise = null;
				app.quit();
			});
		}
		return;
	}
});

// Last resort: if the OS-native supervisor link is not actually connected
// (daemon socket never bound, e.g. UDS path-length limit, or addr was null),
// the dropped fd will NOT stop the daemon on quit, so kill it here to avoid an
// orphan. Safe because Phase A made the daemon's SIGTERM non-destructive: it
// exits without tearing down sessions, which survive for the next boot to adopt.
// When the link IS connected we do nothing here and rely on the OS closing the
// fd on exit, which covers crash and SIGKILL uniformly.
//
// AO_KEEP_DAEMON opts out entirely: the daemon is deliberately spawned without a
// supervisor link so it persists across app quit, so this orphan-cleanup kill
// must be skipped — otherwise it would defeat the whole point on quit.
process.on("exit", () => {
	if (daemonProcess && !supervisorLink?.connected && !keepDaemonAlive(process.env)) {
		killDaemon(daemonProcess);
	}
});

app.on("window-all-closed", () => {
	if (process.platform !== "darwin") {
		app.quit();
	}
});
