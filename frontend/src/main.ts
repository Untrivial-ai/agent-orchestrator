import {
	app,
	BaseWindow,
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
	session,
	WebContentsView,
	webContents,
	type WebContents,
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
	setMacDifferentialUpdates,
	returnToHome,
	type UpdateCheckOptions,
} from "./main/auto-updater";
import { listFeatureBuilds, getActiveFeatureBuild } from "./main/feature-builds";
import { initMainSentry, sanitizeRendererCapture } from "./main/sentry-main";
import { TelemetryPolicyAuthority, resolveDesktopDataDir } from "./main/telemetry-policy-file";
import { DaemonTelemetryPolicyClient } from "./main/daemon-telemetry-policy-client";
import { DesktopTelemetryController } from "./main/desktop-telemetry-controller";
import { AgentSwitchVisibilityController } from "./main/agent-switch-observability";
import { readUpdateSettings, type UpdateSettings, type UpdateStatus } from "./main/update-settings";
import { readKeybindingOverrides, writeKeybindingOverrides } from "./main/keybinding-settings";
import { readEditorSettings, writeEditorPreference } from "./main/editor-settings";
import { createEditorHandoff } from "./main/editor-handoff";
import { launchCommand } from "./main/launch-command";
import {
	decideRelocation,
	inspectInstalledBundle,
	installedBundlePath,
} from "./main/relocation";
import {
	coerceUiSettings,
	DEFAULT_UI_SETTINGS,
	readUiSettings,
	writeUiSettings,
	type UiSettings,
} from "./main/ui-settings";
import { spawn, type ChildProcess } from "node:child_process";
import { randomBytes, randomUUID } from "node:crypto";
import { closeSync, existsSync, mkdirSync, openSync, readFileSync, renameSync, writeFileSync } from "node:fs";
import { chmod, copyFile, mkdir, readFile, rename, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { pathToFileURL } from "node:url";
import { type DaemonLaunchSpec, bundledDaemonIdentityError, resolveDaemonLaunch } from "./shared/daemon-launch";
import { createListenPortScanner, defaultRunFilePath, parseRunFile } from "./shared/daemon-discovery";
import type { DaemonStatus } from "./shared/daemon-status";
import {
	refreshSlowDaemonStartupDetails,
	slowDaemonStartupStatus,
} from "./shared/daemon-startup-status";
import { attachAppShortcuts } from "./main/app-shortcuts";
import {
	KEYBOARD_SHORTCUTS_HELP_CHANNEL,
	SET_CLOSE_SHELL_TERMINAL_SHORTCUT_ENABLED_CHANNEL,
	SET_TERMINAL_FOCUSED_CHANNEL,
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
import {
	buildDaemonEnv,
	devDaemonAllowedOrigins,
	resolveShellEnv,
	resolveShellEnvWithSpec,
	resolveWindowsShellProbe,
	type ShellRunner,
} from "./shared/shell-env";
import { DEFAULT_TERMINAL_SHELL, type TerminalShellPreference } from "./shared/ui-locale";
import { bundledTmuxBinaryPath, stableBundledTmuxBinaryPath } from "./shared/bundled-tmux";
import {
	handleCloudDeepLink,
	installCloudIPC,
	registerCloudProtocol,
	showCloudSignInFailure,
} from "./main/cloud-auth";
import { installCloudLocalAuthIPC } from "./main/cloud-auth-local";
import { installCloudCpProxy } from "./main/cloud-cp-proxy";
import { DEFAULT_POSTHOG_HOST, DEFAULT_POSTHOG_PROJECT_KEY } from "./shared/posthog-config";
import { DEFAULT_SENTRY_DSN } from "./shared/sentry-config";
import { buildTelemetryBootstrap, rendererTelemetryEnabled } from "./shared/telemetry";
import {
	TELEMETRY_CLEAR_RENDERER_QUEUES_CHANNEL,
	TELEMETRY_POLICY_CHANGED_CHANNEL,
	TELEMETRY_RENDERER_QUEUES_CLEARED_CHANNEL,
	type RendererTelemetryCapture,
	type TelemetryPolicyView,
} from "./shared/telemetry-policy";
import {
	createBrowserViewHost,
	shouldHandleAppShortcutInBrowserContext,
	type BrowserViewHost,
} from "./main/browser-view-host";
import { createBrowserProfileStore } from "./main/browser-profile-store";
import { BrowserHistoryStore } from "./main/browser-history-store";
import { BrowserProfileImportService } from "./main/browser-profile-import";
import {
	registerBrowserProfileIpc,
	type BrowserProfileIpc,
	type BrowserProfileMenuItem,
} from "./main/browser-profile-ipc";
import type { BrowserProfileMenuInput } from "./shared/browser-profiles";
import { createWindowComposition, type WindowComposition } from "./main/window-composition";
import { AgentBrowserRuntime } from "./main/agent-browser-runtime";
import { sameBrowserRuntimeIdentity, type BrowserRuntimeIdentity } from "./main/browser-runtime-identity";
import { connectSupervisor, type SupervisorLinkHandle } from "./main/supervisor-link";
import { connectBrowserRuntime, type BrowserRuntimeLinkHandle } from "./main/browser-runtime-link";
import { keepDaemonAlive, shouldLinkOnAttach } from "./main/daemon-owner";
import { readMigrationState, updateMigration, writeAppStateMarker, type MigrationState } from "./main/app-state";
import { isAllowedAppExternalURL, openAllowedAppExternalURL } from "./main/external-open";
import { dockBounceType, shouldReplaceBounce, shouldSignalAttention, shouldToast } from "./main/notification-signals";
import { buildMacAppMenuTemplate, buildWindowsAppMenuTemplate } from "./main/menu";
import { ancestorRepositorySetupWarning, resolveCheckedOutBranch, scanImportFolder } from "./main/import-folder-scan";
import { parseOpenFolderPathArg } from "./main/open-folder-arg";
import { AGENT_SWITCH_VISIBILITY_IPC_CHANNEL } from "./shared/agent-switch-observability";

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

// Escape hatch for hosts whose GPU driver stack crashes Chromium on startup
// (Linux/Wayland in practice, #3961). Chromium still runs a GPU process; what
// this drops is the vendor driver, falling the process back to Electron's
// bundled SwiftShader. There is no window yet when the crashes happen, so an
// in-app setting is unreachable and an env var is the only knob that works.
// Truthy allowlist mirrors keepDaemonAlive() so "off"/"no" cannot accidentally
// turn the GPU off. Must run before app ready — the call throws after that.
const disableGpu = process.env.AO_DISABLE_GPU?.trim().toLowerCase();
if (disableGpu === "1" || disableGpu === "true" || disableGpu === "yes" || disableGpu === "on") {
	app.disableHardwareAcceleration();
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
// AO_DEV_ELECTRON_DIR overrides the dev profile location. The default dev path
// is shared by every checkout, and Chromium puts a singleton lock in a profile,
// so a second worktree's `npm run dev` loses requestSingleInstanceLock() and
// exits immediately. That is a real constraint for a tool built around parallel
// worktree sessions: two sessions could not both run the app. Packaged builds
// are deliberately NOT overridable — their profile is part of the install.
app.setPath(
	"userData",
	app.isPackaged
		? path.join(os.homedir(), ".ao", "electron")
		: (process.env.AO_DEV_ELECTRON_DIR ?? path.join(os.homedir(), ".ao", "dev", "electron")),
);

// Resolve once against the launch cwd, before the daemon can chdir. The exact
// absolute value is shared by policy bootstrap and every daemon spawn.
const desktopLaunchWorkingDirectory = process.cwd();
const desktopDataDir = resolveDesktopDataDir(process.env, os.homedir(), desktopLaunchWorkingDirectory, app.isPackaged);
let telemetryPolicyController: DesktopTelemetryController | null = null;
let agentSwitchVisibilityController: AgentSwitchVisibilityController | null = null;
const trustedShellWebContents = new Map<number, WebContents>();
const pendingRendererQueuePurges = new Map<string, {
	senderId: number;
	contents: WebContents;
	resolve: () => void;
	reject: (error: Error) => void;
	timeout: ReturnType<typeof setTimeout>;
	onDestroyed: () => void;
}>();

function finishRendererQueuePurge(requestId: string, error?: Error): void {
	const pending = pendingRendererQueuePurges.get(requestId);
	if (!pending) return;
	pendingRendererQueuePurges.delete(requestId);
	clearTimeout(pending.timeout);
	pending.contents.removeListener("destroyed", pending.onDestroyed);
	if (error) pending.reject(error);
	else pending.resolve();
}

function requestRendererQueuePurge(contents: WebContents): Promise<void> {
	return new Promise((resolve, reject) => {
		const requestId = randomUUID();
		const onDestroyed = () => finishRendererQueuePurge(requestId, new Error("renderer destroyed before telemetry queue purge"));
		const timeout = setTimeout(() => finishRendererQueuePurge(requestId, new Error("renderer telemetry queue purge timed out")), 5_000);
		pendingRendererQueuePurges.set(requestId, { senderId: contents.id, contents, resolve, reject, timeout, onDestroyed });
		contents.once("destroyed", onDestroyed);
		try { contents.send(TELEMETRY_CLEAR_RENDERER_QUEUES_CHANNEL, { requestId }); }
		catch (error) { finishRendererQueuePurge(requestId, error instanceof Error ? error : new Error("renderer telemetry queue purge failed")); }
	});
}

async function clearRendererTelemetryQueues(): Promise<void> {
	const liveShells = [...trustedShellWebContents.values()].filter((contents) => !contents.isDestroyed());
	await Promise.all(liveShells.map(requestRendererQueuePurge));
}

let mainWindow: BaseWindow | null = null;
let trayController: TrayController | null = null;
const trayLifecycle = createTrayLifecycle({
	getWindow: () => null,
	getContents: () => getShellWebContents(),
	getTrayController: () => trayController,
	focusWindow: () => focusMainWindow(),
});
// Icon/taskbar-shortcut folder drop, mirroring VS Code: relayed to the
// renderer's global drop handling so it opens the same create-project flow.
const OPEN_FOLDER_PATH_CHANNEL = "app:openFolderPath";
// Folder path from a cold-start launch (icon/shortcut drop while not running)
// whose renderer isn't mounted yet. Flushed once the shell signals readiness
// via TRAY_RENDERER_READY_CHANNEL — see the OPEN_FOLDER_PATH_CHANNEL handler.
let pendingFolderPath: string | null = null;
let daemonProcess: ChildProcess | null = null;
let daemonStoppingProcess: ChildProcess | null = null;
let daemonRestartAfterExitProcess: ChildProcess | null = null;
let daemonStartPromise: Promise<DaemonStatus> | null = null;
let daemonStartEpoch = 0;
let daemonStatus: DaemonStatus = { state: "stopped" };
let daemonOutput = "";
let browserViewHost: BrowserViewHost | null = null;
let browserProfileIpc: BrowserProfileIpc | null = null;
let browserProfileImporter: BrowserProfileImportService | null = null;
let windowComposition: WindowComposition | null = null;
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
let terminalFocused = false;
// Held for the app lifetime. Dropping it (on any exit) triggers daemon self-stop.
let supervisorLink: SupervisorLinkHandle | null = null;
// Guard: prevents stacking multiple flashFrame(true) calls when notifications arrive rapidly.
let isFlashing = false;
// macOS: the in-flight dock bounce, cancelled once the window regains focus
// so a "critical" bounce stops as soon as the user looks at the app. Held as
// one object so the id and its criticality can never drift apart; a pending
// critical bounce is not replaced by a later informational notification.
let pendingBounce: { id: number; critical: boolean } | null = null;
// Live mirror of the persisted `soundNotificationsEnabled` UI setting, kept in sync by the
// uiSettings:set handler so a toggle flip takes effect without an app restart.
let soundNotificationsEnabled = DEFAULT_UI_SETTINGS.soundNotificationsEnabled;

const isDev = !app.isPackaged;

// Dev mode uses a separate port and state subdirectory so it never collides with
// a concurrently running installed-app daemon. The subdir also isolates supervise.sock
// on Unix (backend derives it as dir(RunFilePath)/supervise.sock) and the named pipe
// on Windows (supervisorPipeFromRunFile derives it from the same dir basename).
const DEV_DAEMON_PORT = 3002;
const DEV_STATE_SUBDIR = "dev"; // ~/.ao/dev/

// Traffic lights stay fixed across sidebar expand/collapse. Y matches the
// natural macOS titlebar band (TitlebarNav is h-traffic-light-clearance).
const MAC_WINDOW_BUTTON_X = 14;
const MAC_WINDOW_BUTTON_Y = 12;

const RENDERER_SCHEME = "app";
const RENDERER_HOST = "renderer";
const RENDERER_ORIGIN = `${RENDERER_SCHEME}://${RENDERER_HOST}`;
const NATIVE_WINDOW_BACKGROUND_DARK = "#0f1014";
const NATIVE_WINDOW_BACKGROUND_LIGHT = "#fbfbfb";

function getShellWebContents(): WebContents | null {
	return windowComposition?.shellWebContents ?? null;
}

function syncNativeWindowBackground(): void {
	if (!windowComposition || !mainWindow || mainWindow.isDestroyed()) return;
	mainWindow.setBackgroundColor(
		nativeTheme.shouldUseDarkColors ? NATIVE_WINDOW_BACKGROUND_DARK : NATIVE_WINDOW_BACKGROUND_LIGHT,
	);
}

function resolvedDaemonDataDir(): string {
	const override = process.env.AO_DATA_DIR?.trim();
	if (override) return override;
	if (isDev) return path.join(os.homedir(), ".ao", DEV_STATE_SUBDIR, "data");
	return path.join(os.homedir(), ".ao", "data");
}

// Cursor Agent reads TERM_THEME at process start. The daemon applies it from
// this file when spawning a PTY. The renderer writes the resolved light/dark
// scheme here so it matches the xterm palette (not Electron nativeTheme alone).
function persistTerminalThemeHint(scheme: "light" | "dark"): void {
	const dir = resolvedDaemonDataDir();
	try {
		mkdirSync(dir, { recursive: true, mode: 0o750 });
		const target = path.join(dir, "terminal-theme");
		const temporary = `${target}.${process.pid}.tmp`;
		writeFileSync(temporary, `${scheme}\n`, { mode: 0o600 });
		renameSync(temporary, target);
	} catch (error) {
		console.warn("AO: unable to persist terminal theme hint", error);
	}
}

nativeTheme.on("updated", syncNativeWindowBackground);

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

// Register ao-app:// as the deep-link protocol for WorkOS auth callbacks.
// Must run before app.whenReady().
registerCloudProtocol();
if (!app.requestSingleInstanceLock()) {
	app.exit(0);
}

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
	getShellWebContents()?.send("daemon:status", daemonStatus);
	if (nextStatus.state === "ready" && browserViewHost) {
		establishBrowserRuntimeLink();
	}
}

const MAX_DAEMON_OUTPUT_CHARS = 12_000;

function appendDaemonOutput(text: string): void {
	daemonOutput = (daemonOutput + text).slice(-MAX_DAEMON_OUTPUT_CHARS);
	const nextStatus = refreshSlowDaemonStartupDetails(daemonStatus, daemonOutput);
	if (nextStatus !== daemonStatus) setDaemonStatus(nextStatus);
}

// Menu installed on Windows where the native menu bar is hidden. The bar stays
// out of sight, but the roles keep their accelerators alive (Reload, zoom, full
// screen, edit commands). DevTools uses the AO browser toggle so the focused
// Browser panel opens the same native Chromium surface as the toolbar.
function buildWindowsAppMenu(): Menu {
	return Menu.buildFromTemplate(
		buildWindowsAppMenuTemplate(() => {
			const fallback = () => getShellWebContents()?.toggleDevTools();
			void browserViewHost?.toggleDevToolsForLastFocused().then((state) => {
				if (!state) fallback();
			}).catch(fallback);
		}),
	);
}

async function disposeBrowserViewHost(): Promise<void> {
	const host = browserViewHost;
	browserViewHost = null;
	const profileIpc = browserProfileIpc;
	browserProfileIpc = null;
	profileIpc?.dispose();
	const profileImporter = browserProfileImporter;
	browserProfileImporter = null;
	if (!host && !profileImporter) return;
	const cleanup = Promise.resolve()
		.then(async () => {
			await profileImporter?.dispose();
			await host?.dispose();
		})
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

function browserProfileStateDir(): string {
	const runFile = runFilePath();
	return path.dirname(runFile ?? path.join(os.homedir(), ".ao", "running.json"));
}

async function clearElectronBrowserProfileData(partition: string): Promise<void> {
	const browserSession = session.fromPartition(partition);
	await Promise.all([browserSession.clearStorageData(), browserSession.clearCache()]);
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
	const browserProfileStore = await createBrowserProfileStore({ stateDir: browserProfileStateDir() });
	const browserHistoryStore = new BrowserHistoryStore({ stateDir: browserProfileStateDir() });
	const profileImporter = new BrowserProfileImportService({
		stateDir: browserProfileStateDir(),
		profileStore: browserProfileStore,
		historyStore: browserHistoryStore,
		fromPartition: (partition) => session.fromPartition(partition),
	});
	await profileImporter.initialize();
	const windowOptions: Electron.BaseWindowConstructorOptions = {
		width: 1320,
		height: 860,
		minWidth: 960,
		minHeight: 640,
		title: "Agent Orchestrator",
		icon: windowIconPath(),
		backgroundColor: NATIVE_WINDOW_BACKGROUND_DARK,
		// Windows goes frameless and the renderer paints the whole titlebar,
		// including custom min/max/close controls. macOS/Linux keep the inset
		// traffic-light chrome.
		...(process.platform === "win32"
			? {
					titleBarStyle: "hidden" as const,
					// Hide the native menu bar. A role-based menu is still installed (for
					// accelerators) below; the visible menu is painted by WindowTitlebar.
					autoHideMenuBar: true,
				}
			: {
					titleBarStyle: "hiddenInset" as const,
					// Fixed natural titlebar position — never moved on sidebar toggle.
					trafficLightPosition: { x: MAC_WINDOW_BUTTON_X, y: MAC_WINDOW_BUTTON_Y },
				}),
	};
	mainWindow = new BaseWindow(windowOptions);
	const composition = createWindowComposition({
		mainWindow,
		WebContentsView,
		preload: preloadPath(),
	});
	windowComposition = composition;
	syncNativeWindowBackground();
	const shellWebContents = getShellWebContents();
	if (!shellWebContents) throw new Error("AO shell WebContents was not created");
	trustedShellWebContents.set(shellWebContents.id, shellWebContents);
	agentSwitchVisibilityController?.registerWindow(shellWebContents.id);
	shellWebContents.once("destroyed", () => {
		trustedShellWebContents.delete(shellWebContents.id);
		agentSwitchVisibilityController?.destroyWindow(shellWebContents.id);
	});

	// On Windows the app paints its own title bar (WindowTitlebar), so the native
	// menu bar is hidden (autoHideMenuBar above). The role-based menu is still
	// installed so its accelerators keep working and act on the focused pane;
	// setMenuBarVisibility(false) keeps the strip itself out of view. macOS gets
	// an explicit menu so DevTools avoids Electron's unsafe built-in role; Linux
	// keeps its native menu.
	if (process.platform === "win32") {
		Menu.setApplicationMenu(buildWindowsAppMenu());
		mainWindow.setMenuBarVisibility(false);
	} else if (process.platform === "darwin") {
		Menu.setApplicationMenu(
			Menu.buildFromTemplate(
				buildMacAppMenuTemplate(() => {
					const fallback = () => getShellWebContents()?.toggleDevTools();
					const host = browserViewHost;
					if (!host) {
						fallback();
						return;
					}
					void host.toggleDevToolsForLastFocused().then((state) => {
						if (!state) fallback();
					}).catch(fallback);
				}),
			),
		);
	}

	// Harden navigation: never let renderer/terminal content open in-app windows or
	// navigate the privileged window away from the app origin. External links go to
	// the OS browser. Keep this in place before exposing any daemon output to the renderer.
	shellWebContents.setWindowOpenHandler(({ url }) => {
		if (isAllowedAppExternalURL(url)) {
			void shell.openExternal(url);
		}
		return { action: "deny" };
	});

	shellWebContents.on("will-navigate", (event, url) => {
		if (url !== shellWebContents.getURL()) {
			event.preventDefault();
		}
	});

	// Application shortcuts are handled here so they fire no matter which web
	// contents holds focus — the shell renderer, xterm's helper textarea, or a
	// browser-preview view (wired per-view in the browser host).
	const isMac = process.platform === "darwin";
	attachAppShortcuts(
		shellWebContents,
		isMac,
		shellWebContents,
		false,
		() => keybindingOverrides,
		() => keybindingRecordingActive,
		(id, chord) =>
			!browserViewHost?.isLastUsedBrowser() ||
			shouldHandleAppShortcutInBrowserContext(id, chord, isMac),
		(id) => {
			if (id !== "toggle-browser-devtools") return;
			void browserViewHost?.toggleDevToolsForLastFocused().catch(() => undefined);
		},
		() => terminalFocused,
	);

	browserViewHost = createBrowserViewHost({
		mainWindow,
		shellWebContents,
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
		browserProfileStore,
		browserHistoryStore,
		clearBrowserProfileData: clearElectronBrowserProfileData,
	});
	browserProfileImporter = profileImporter;
	browserProfileIpc = registerBrowserProfileIpc({
		ipcMain,
		shellWebContents,
		mainWindow,
		store: browserProfileStore,
		importer: profileImporter,
		host: browserViewHost,
		buildMenu: (items: BrowserProfileMenuItem[]) => {
			const menu = Menu.buildFromTemplate(items as Electron.MenuItemConstructorOptions[]);
			return { popup: (options) => menu.popup(options as Electron.PopupOptions) };
		},
		confirmSwitch: async (labels: BrowserProfileMenuInput["labels"]) => {
			const result = await dialog.showMessageBox({
				type: "warning",
				title: labels.switchTitle,
				message: labels.switchMessage,
				detail: labels.switchDetail,
				buttons: [labels.cancel, labels.confirm],
				defaultId: 0,
				cancelId: 0,
			});
			return result.response === 1;
		},
	});
	if (daemonStatus.state === "ready") establishBrowserRuntimeLink();

	void shellWebContents.loadURL(rendererUrl());

	if (isDev && process.env.AO_OPEN_DEVTOOLS === "1") {
		shellWebContents.once("did-frame-finish-load", () => {
			shellWebContents.openDevTools({ mode: "detach" });
		});
	}

	// macOS: traffic lights vanish in native fullscreen, so the renderer drops
	// the clearance pad above TitlebarNav. Push state so the sidebar can react
	// without polling isFullScreen().
	const pushFullScreen = () => {
		if (!mainWindow) return;
		getShellWebContents()?.send("window:fullscreen", mainWindow.isFullScreen());
	};
	const pushMaximized = () => {
		if (!mainWindow) return;
		getShellWebContents()?.send("window:maximized", mainWindow.isMaximized());
	};
	mainWindow.on("enter-full-screen", pushFullScreen);
	mainWindow.on("leave-full-screen", pushFullScreen);
	mainWindow.on("maximize", pushMaximized);
	mainWindow.on("unmaximize", pushMaximized);
	mainWindow.on("blur", () => {
		keybindingRecordingActive = false;
	});
	shellWebContents.on("render-process-gone", () => {
		keybindingRecordingActive = false;
		terminalFocused = false;
	});
	shellWebContents.on("did-start-loading", () => {
		terminalFocused = false;
		trayLifecycle.clear();
	});
	shellWebContents.on("render-process-gone", () => trayLifecycle.clear());

	mainWindow.on("closed", () => {
		disposeBrowserRuntimeLink();
		keybindingRecordingActive = false;
		if (windowComposition === composition) windowComposition = null;
		void disposeBrowserViewHost().finally(() => {
			composition.dispose();
		});
		mainWindow = null;
		// Drop any pending dock bounce with the window it was attached to: its
		// focus listener died with the window, so leaving the id set would make
		// the next bounce skip attaching one to the replacement window.
		cancelDockBounce();
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

function editorStateDir(): string {
	const runFile = runFilePath();
	if (!runFile) throw new Error("AO state directory is not available.");
	return path.dirname(runFile);
}

async function resolveSessionWorkspaceForDesktop(sessionId: string): Promise<string> {
	if (daemonStatus.state !== "ready" || !daemonStatus.port) {
		throw new Error("AO daemon is not ready.");
	}
	const controller = new AbortController();
	const timer = setTimeout(() => controller.abort(), DAEMON_PROBE_TIMEOUT_MS);
	try {
		const response = await net.fetch(
			`http://127.0.0.1:${daemonStatus.port}/api/v1/desktop/sessions/${encodeURIComponent(sessionId)}/workspace`,
			{ signal: controller.signal },
		);
		const body = await response.json() as Record<string, unknown>;
		if (!response.ok) {
			const message = typeof body.message === "string" ? body.message : "Session workspace is not available.";
			throw new Error(message);
		}
		const workspacePath = body.workspacePath;
		if (typeof workspacePath !== "string" || !path.isAbsolute(workspacePath)) {
			throw new Error("Session workspace is not available.");
		}
		return workspacePath;
	} catch (error) {
		if (error instanceof Error && error.name === "AbortError") {
			throw new Error("Timed out while resolving the session workspace.");
		}
		throw error;
	} finally {
		clearTimeout(timer);
	}
}

const editorHandoff = createEditorHandoff({
	platform: process.platform,
	env: process.env,
	homeDir: os.homedir(),
	resolveWorkspace: resolveSessionWorkspaceForDesktop,
	readPreference: async () => (await readEditorSettings(editorStateDir())).preferredEditorId,
	writePreference: async (editorId) => {
		await writeEditorPreference(editorStateDir(), editorId);
	},
	launch: (command, args, cwd) => launchCommand(command, args, cwd),
	openDirectory: async (workspacePath) => {
		const error = await shell.openPath(workspacePath);
		if (error) throw new Error(error);
	},
	logError: (message, error) => console.error(message, error),
});

// How long to wait for the login shell to print its env before giving up. A
// misconfigured rc that blocks (or a slow nvm/pyenv chain) must not hang startup;
// the daemon then falls back to the static PATH floor.
const SHELL_ENV_TIMEOUT_MS = 3_000;

// The login-shell env resolved once at startup (see docs/daemon-environment.md),
// or null when the probe failed/timed out. Read synchronously by daemonEnv().
let cachedShellEnv: Record<string, string> | null = null;
// Memoize the in-flight resolution so concurrent/repeat awaits are cheap.
let shellEnvPromise: Promise<void> | null = null;
let terminalShellPreference: TerminalShellPreference = { ...DEFAULT_TERMINAL_SHELL };

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
		// Daemon-side Sentry (5xx + panics with Go stacks). Stamped on the daemon
		// env so the spawned daemon inherits the DSN; off in dev to match the
		// PostHog remote gate, and an explicit env always wins. A blank value
		// leaves the daemon's Sentry a no-op.
		AO_SENTRY_DSN: process.env.AO_SENTRY_DSN ?? (isDev ? "" : DEFAULT_SENTRY_DSN),
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
			child = spawn(shellPath, args, { stdio: ["ignore", "pipe", "ignore"], windowsHide: true });
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

function lookupWindowsExecutable(candidate: string): string | null {
	const trimmed = candidate.trim().replace(/^"|"$/g, "");
	if (!trimmed) return null;
	const hasDirectory = trimmed.includes("\\") || trimmed.includes("/") || path.isAbsolute(trimmed);
	const candidates = hasDirectory
		? [trimmed]
		: (process.env.PATH ?? process.env.Path ?? "")
				.split(path.delimiter)
				.filter(Boolean)
				.map((directory) => path.join(directory, trimmed));
	for (const resolved of candidates) {
		if (existsSync(resolved)) return resolved;
	}
	return null;
}

// Resolve the login-shell env once and cache it. On Windows the selected
// terminal shell is used for the probe too, so shell profiles and PATH exports
// stay consistent between standalone terminals and daemon startup.
function ensureShellEnv(): Promise<void> {
	if (!shellEnvPromise) {
		if (process.platform === "win32") {
			const probe =
				resolveWindowsShellProbe(terminalShellPreference, process.env, lookupWindowsExecutable) ??
				resolveWindowsShellProbe(DEFAULT_TERMINAL_SHELL, process.env, lookupWindowsExecutable);
			if (!probe) {
				shellEnvPromise = Promise.resolve();
				cachedShellEnv = null;
				return shellEnvPromise;
			}
			shellEnvPromise = resolveShellEnvWithSpec(probe, runLoginShell).then((resolved) => {
				cachedShellEnv = resolved;
				if (!resolved) {
					console.error("AO: could not read the login-shell environment; falling back to the process environment.");
				}
			});
			return shellEnvPromise;
		}
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
let stagedBundledTmuxBinary: string | null = null;

async function ensureBundledTmuxStaged(): Promise<void> {
	const source = bundledTmuxBinaryPath(app.isPackaged, process.resourcesPath, process.platform);
	const destination = stableBundledTmuxBinaryPath(
		app.isPackaged,
		process.env.AO_DATA_DIR?.trim() || path.join(os.homedir(), ".ao"),
		app.getVersion(),
		process.platform,
		process.arch,
	);
	if (!source || !destination) {
		stagedBundledTmuxBinary = null;
		return;
	}
	if (existsSync(destination)) {
		stagedBundledTmuxBinary = destination;
		return;
	}
	await mkdir(path.dirname(destination), { recursive: true, mode: 0o750 });
	const temporary = `${destination}.tmp-${process.pid}-${randomUUID()}`;
	try {
		await copyFile(source, temporary);
		await chmod(temporary, 0o755);
		await rename(temporary, destination);
	} finally {
		await rm(temporary, { force: true });
	}
	stagedBundledTmuxBinary = destination;
}

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
	const bundledTmuxBinary = stagedBundledTmuxBinary;
	const ownerTag = {
		AO_OWNER,
		AO_DATA_DIR: desktopDataDir,
		AO_APP_RUN_ID: appRunId,
		// The browser runtime token is handed over through the child's private
		// stdin pipe below. Never put it in the daemon environment, where a
		// same-UID worker could inspect the parent process.
		AO_BROWSER_RUNTIME_TOKEN: "",
		AO_BROWSER_RUNTIME_TOKEN_STDIN: "1",
		// Under AppImage, APPIMAGE is the stable outer .AppImage file path (the
		// FUSE mount in executablePath is random per launch). The daemon echoes
		// it as appImagePath in /healthz|/readyz so the identity check can
		// recognise its own daemon across a relaunch-to-update.
		...(process.env.APPIMAGE ? { AO_APPIMAGE: process.env.APPIMAGE } : {}),
		// Claude Code Chat uses AO's packaged ACP adapter + Node runtime. The
		// provider executable itself is resolved by the daemon from the user's PATH
		// and passed through CLAUDE_CODE_EXECUTABLE; it is not part of this resource.
		AO_ACP_RUNTIME_DIR:
			process.env.AO_ACP_RUNTIME_DIR ??
			(app.isPackaged
				? path.join(process.resourcesPath, "acp-runtime")
				: path.join(app.getAppPath(), "resources", "acp-runtime")),
		...(bundledTmuxBinary ? { AO_TMUX_BINARY: bundledTmuxBinary, AO_TMUX_SOCKET_NAME: "ao" } : {}),
	};
	// In dev mode, inject isolation defaults so the dev daemon never collides with
	// the installed app. User-set env vars take priority (checked first).
	const devExtras: Record<string, string> = {};
	if (isDev) {
		if (!process.env.AO_PORT) devExtras.AO_PORT = String(DEV_DAEMON_PORT);
		if (!process.env.AO_RUN_FILE) devExtras.AO_RUN_FILE = runFilePath() ?? "";
		if (!process.env.AO_DATA_DIR) devExtras.AO_DATA_DIR = path.join(os.homedir(), ".ao", DEV_STATE_SUBDIR, "data");
		devExtras.AO_ALLOWED_ORIGINS = devDaemonAllowedOrigins(
			process.env.AO_ALLOWED_ORIGINS,
			rendererUrl(),
		);
	}
	// Windows keeps its native environment semantics while overlaying values
	// exported by the selected login-shell probe.
	if (process.platform === "win32") {
		return { ...process.env, ...(cachedShellEnv ?? {}), ...devExtras, ...telemetryOverrides(), ...ownerTag };
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
		return bundledDaemonIdentityError(probe, launch.command, process.env.APPIMAGE, samePath);
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

function daemonLaunchEnv(): NodeJS.ProcessEnv {
	if (!isDev || process.platform !== "win32") {
		return process.env;
	}
	try {
		const daemonDir = path.resolve(app.getAppPath(), "daemon");
		const manifestPath = path.join(daemonDir, "dev-daemon.json");
		const parsed = JSON.parse(readFileSync(manifestPath, "utf8")) as { path?: unknown };
		if (typeof parsed.path === "string") {
			const daemonPath = path.resolve(parsed.path.trim());
			const relativePath = path.relative(daemonDir, daemonPath);
			if (relativePath && !relativePath.startsWith("..") && !path.isAbsolute(relativePath)) {
				return { ...process.env, AO_DEV_DAEMON_BINARY: daemonPath };
			}
		}
	} catch {
		// Fall back to the legacy fixed dev path; build-daemon writes the manifest.
	}
	return process.env;
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
		daemonLaunchEnv(),
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
	try {
		await ensureBundledTmuxStaged();
	} catch (err) {
		setDaemonStatus({
			state: "error",
			message: `Could not stage AO's bundled tmux under the AO data directory: ${(err as Error).message}`,
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

	// Neither source confirmed startup yet. The child is still alive and both
	// discovery mechanisms remain active, so surface this as slow progress rather
	// than a failure. A later listen line or running.json handshake still moves
	// the same launch to ready.
	fallbackTimer = setTimeout(() => {
		if (portConfirmed || daemonProcess !== child || daemonStoppingProcess === child) return;
		// Keep running.json polling alive after surfacing slow startup. In
		// keep-daemon mode there are no stdout/stderr scanners, so the run file is
		// the only way a slow-but-successful boot can still recover to ready.
		fallbackTimer = undefined;
		setDaemonStatus(slowDaemonStartupStatus({
			output: daemonOutput,
			executablePath: launch.command,
			workingDirectory: launch.cwd,
			handshakePath,
		}));
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
ipcMain.handle("editorHandoff:getState", (event, sessionId: string) => {
	if (event.sender !== getShellWebContents()) throw new Error("Untrusted editor handoff request.");
	return editorHandoff.getState(typeof sessionId === "string" ? sessionId : "");
});
ipcMain.handle("editorHandoff:open", (event, input) => {
	if (event.sender !== getShellWebContents()) throw new Error("Untrusted editor handoff request.");
	return editorHandoff.open(input && typeof input === "object" ? input : { sessionId: "" });
});
ipcMain.handle("app:getVersion", () => app.getVersion());
ipcMain.handle("app:openExternal", async (_event, url: string) => {
	await openAllowedAppExternalURL(url, shell);
});

ipcMain.handle("window:isFullScreen", () => mainWindow?.isFullScreen() ?? false);
ipcMain.handle("window:isMaximized", () => mainWindow?.isMaximized() ?? false);

// Drive Electron's nativeTheme from the app's theme preference so embedded
// preview WebContentsViews (which follow prefers-color-scheme) flip in step with
// the shell. The three preference values map 1:1 onto themeSource; "system" keeps
// both the preview and the shell's own matchMedia following the OS.
ipcMain.handle("theme:set", (_event, preference: "light" | "dark" | "system") => {
	if (preference === "light" || preference === "dark" || preference === "system") {
		nativeTheme.themeSource = preference;
		syncNativeWindowBackground();
	}
});

ipcMain.handle("theme:persist-terminal", (_event, scheme: unknown) => {
	if (scheme === "light" || scheme === "dark") {
		persistTerminalThemeHint(scheme);
	}
});

// Renderer calls this when focus lands on real shell UI (not the titlebar menu), so menu:action's panel fallback below doesn't go stale.
ipcMain.on("shell:focus", () => browserViewHost?.forgetLastFocusedPanel());

ipcMain.on("browser:overlay", (event, open: unknown) => {
	if (event.sender !== getShellWebContents() || typeof open !== "boolean") return;
	windowComposition?.setOverlayOpen(open);
	// Raising the shell can leave the live page's own compositor surface stale
	// (the same class of bug window-composition.ts already works around for
	// the shell itself) — nudge it the same way once the shell is on top.
	if (open) browserViewHost?.refreshLastFocusedPanelSurface();
});

ipcMain.on(SET_CLOSE_SHELL_TERMINAL_SHORTCUT_ENABLED_CHANNEL, (_event, enabled: unknown) => {
	closeShellTerminalShortcutEnabled = enabled === true;
});

ipcMain.on(SET_TERMINAL_FOCUSED_CHANNEL, (event, focused: unknown) => {
	if (event.sender !== getShellWebContents() || typeof focused !== "boolean") return;
	terminalFocused = focused;
});

// Backs the custom title-bar menu (WindowTitlebar). Each item maps to the same
// action the native default menu would have performed.
ipcMain.handle("menu:action", (_event, action: string) => {
	const win = mainWindow;
	if (!win) return;
	// Clicking this shell-painted menu moves focus off the panel, so prefer the last-focused panel, else the focused contents, else the shell.
	const focused = webContents.getFocusedWebContents();
	const shell = getShellWebContents();
	const wc =
		(focused && focused !== shell ? focused : browserViewHost?.getLastFocusedPanelContents()) ?? shell;
	if (!wc) return;
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
			if (browserViewHost) {
				return browserViewHost.toggleDevToolsForLastFocused().then((state) => {
					if (!state) wc?.toggleDevTools();
				}).catch(() => wc?.toggleDevTools());
			}
			return wc?.toggleDevTools();
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
			shell?.focus();
			return shell?.send(KEYBOARD_SHORTCUTS_HELP_CHANNEL);
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
ipcMain.handle("telemetry:getBootstrap", () => {
	if (!telemetryPolicyController) return null;
	return buildTelemetryBootstrap({ ...process.env, AO_DATA_DIR: desktopDataDir }, app.getVersion(), process.platform, os.homedir(), app.isPackaged, telemetryPolicyController.snapshot());
});
ipcMain.handle("telemetry:getPolicy", () => telemetryPolicyController?.snapshot() ?? failClosedTelemetryPolicyView());
ipcMain.handle("telemetry:setEventsEnabled", (_event, input: { eventsEnabled?: unknown; expectedGeneration?: unknown }) => {
	if (!telemetryPolicyController || typeof input?.eventsEnabled !== "boolean" || typeof input.expectedGeneration !== "string") throw new Error("invalid telemetry policy request");
	return telemetryPolicyController.setEventsEnabled(input.eventsEnabled, input.expectedGeneration);
});
ipcMain.on(TELEMETRY_RENDERER_QUEUES_CLEARED_CHANNEL, (event, input: unknown) => {
	const trustedSender = trustedShellWebContents.get(event.sender.id);
	if (!trustedSender || trustedSender !== event.sender || trustedSender.isDestroyed()) return;
	if (!input || typeof input !== "object" || Array.isArray(input)) return;
	const result = input as Record<string, unknown>;
	if (Object.keys(result).length !== 2 || typeof result.requestId !== "string" || typeof result.ok !== "boolean") return;
	const pending = pendingRendererQueuePurges.get(result.requestId);
	if (!pending || pending.senderId !== event.sender.id) return;
	finishRendererQueuePurge(result.requestId, result.ok ? undefined : new Error("renderer telemetry queue purge failed"));
});
ipcMain.handle("telemetry:capture", (_event, request: RendererTelemetryCapture) => {
	const sanitized = sanitizeRendererCapture(request);
	if (!telemetryPolicyController || !sanitized) return false;
	return telemetryPolicyController.capture(sanitized);
});
ipcMain.on(AGENT_SWITCH_VISIBILITY_IPC_CHANNEL, (event, request: unknown) => {
	const trustedSender = trustedShellWebContents.get(event.sender.id);
	if (trustedSender !== event.sender || trustedSender.isDestroyed()) return;
	agentSwitchVisibilityController?.signal(event.sender.id, request);
});

function failClosedTelemetryPolicyView(): TelemetryPolicyView {
	return { eventsEnabled: false, consentGeneration: "unavailable", updatedAt: new Date(0).toISOString(), acknowledged: false, state: "cleanup_failed", environmentVeto: true, durabilitySupported: false, reason: "invalid_authority" };
}
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
ipcMain.handle("app:getRepositoryBranch", async (_event, path: string) => {
	await ensureShellEnv();
	return resolveCheckedOutBranch(path, { env: daemonEnv(), homeDir: os.homedir() });
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
	if (!runFile) return { enabled: false, channel: "latest", nightlyAck: false, feature: null, macDifferentialUpdates: false };
	return readUpdateSettings(path.dirname(runFile));
});
ipcMain.handle("updateSettings:set", async (_event, settings: UpdateSettings) => {
	const runFile = runFilePath();
	if (!runFile) return;
	await setUpdateSettings(path.dirname(runFile), settings);
});
ipcMain.handle("updateSettings:setMacDifferentialUpdates", async (_event, enabled: unknown) => {
	if (typeof enabled !== "boolean") return;
	const runFile = runFilePath();
	if (!runFile) return;
	await setMacDifferentialUpdates(path.dirname(runFile), enabled);
});

ipcMain.handle("uiSettings:get", async (): Promise<UiSettings> => {
	const runFile = runFilePath();
	if (!runFile) return { ...DEFAULT_UI_SETTINGS };
	return readUiSettings(path.dirname(runFile));
});
	ipcMain.handle("uiSettings:set", async (_event, settings: Partial<UiSettings>): Promise<UiSettings> => {
		const runFile = runFilePath();
	const result = !runFile ? coerceUiSettings(settings) : await writeUiSettings(path.dirname(runFile), settings);
	trayController?.setLocale(result.locale);
	soundNotificationsEnabled = result.soundNotificationsEnabled;
	terminalShellPreference = result.terminalShell;
	shellEnvPromise = null;
	cachedShellEnv = null;
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
	if (event.sender !== getShellWebContents() || typeof active !== "boolean") return;
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

function cancelDockBounce(): void {
	if (pendingBounce === null) return;
	const { id } = pendingBounce;
	pendingBounce = null;
	app.dock?.cancelBounce(id);
}

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
				getShellWebContents()?.send("notifications:click", notification.id);
			});
			toast.show();
		}

		// Dock (macOS) / taskbar (Windows/Linux) attention signal. On macOS every
		// notification bounces the dock — urgency is carried by the bounce type,
		// so a merged PR bounces once while a blocked agent keeps bouncing. On
		// Windows/Linux only the actionable types flash (see
		// shouldSignalAttention), preserving the pre-existing behavior there.
		if (process.platform === "darwin" && app.dock) {
			// A pending critical bounce (agent blocked on the user) is never
			// replaced: a later informational notification must not downgrade it
			// to a one-shot bounce.
			if (shouldReplaceBounce(pendingBounce)) {
				// A focus listener from an earlier un-cancelled bounce still works for
				// the new id, so only attach one at a time.
				const hadPendingBounce = pendingBounce !== null;
				cancelDockBounce();
				const bounceType = dockBounceType(notification.type);
				const id = app.dock.bounce(bounceType);
				if (typeof id === "number" && id >= 0) {
					pendingBounce = { id, critical: bounceType === "critical" };
					if (!hadPendingBounce) mainWindow.once("focus", cancelDockBounce);
				}
			}
		} else if ((process.platform === "win32" || process.platform === "linux") && shouldSignalAttention(notification.type)) {
			if (!isFlashing) {
				isFlashing = true;
				mainWindow.flashFrame(true);
				mainWindow.once("focus", () => {
					isFlashing = false;
					mainWindow?.flashFrame(false);
				});
			}
		}
		if (shouldSignalAttention(notification.type) && soundNotificationsEnabled) {
			shell.beep();
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
		if (soundNotificationsEnabled) {
			shell.beep();
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

ipcMain.on(TRAY_RENDERER_READY_CHANNEL, (event) => {
	trayLifecycle.handleRendererReady(event);
	if (pendingFolderPath && event.sender === getShellWebContents()) {
		event.sender.send(OPEN_FOLDER_PATH_CHANNEL, pendingFolderPath);
		pendingFolderPath = null;
	}
});

// Cloud auth IPC — cloud:getSession, cloud:signIn, cloud:signOut.
// Data dir resolves to ~/.ao (prod) or ~/.ao/dev (dev) matching daemon conventions.
function cloudDataDir(): string {
	return isDev
		? path.join(os.homedir(), ".ao", DEV_STATE_SUBDIR)
		: path.join(os.homedir(), ".ao");
}

function notifyRenderersOfCloudSession(account: import("./shared/cloud-account").CloudAccount | null): void {
	const contents = getShellWebContents();
	if (!contents || contents.isDestroyed()) return;
	contents.send("cloud:sessionChanged", account);
}

installCloudIPC(cloudDataDir, notifyRenderersOfCloudSession);

// Dev-only local (email/password) sign-in against a loopback Docker control
// plane running AO_CLOUD_LOCAL_AUTH. Gated to unpackaged/dev builds + loopback
// CP inside the handlers; a no-op surface for production/WorkOS users.
installCloudLocalAuthIPC(cloudDataDir, notifyRenderersOfCloudSession);

// Cloud control-plane proxy IPC — cloudCp:request/openStream/closeStream.
// CP calls go through main so the WorkOS bearer token never reaches a renderer.
installCloudCpProxy(cloudDataDir);

function focusCloudWindow(): void {
	const window = BaseWindow.getAllWindows()[0];
	if (!window) return;
	if (window.isMinimized()) window.restore();
	window.show();
	window.focus();
}

async function handleCloudDeepLinkAndFocus(url: string): Promise<void> {
	focusCloudWindow();
	try {
		const session = await handleCloudDeepLink(url, cloudDataDir());
		if (!session) return;
		notifyRenderersOfCloudSession(session);
	} catch (error) {
		console.error("WorkOS callback failed:", error);
		await showCloudSignInFailure(error);
	}
}

// macOS: the OS sends the ao-app:// URL via the open-url event when the app is
// already running. If the app is not running, the URL is passed in process.argv
// on first launch (handled in app.whenReady below).
app.on("open-url", (event, url) => {
	event.preventDefault();
	void handleCloudDeepLinkAndFocus(url);
});

app.on("second-instance", (_event, argv) => {
	const deepLink = argv.find((value) => value.startsWith("ao-app://"));
	if (deepLink) {
		void handleCloudDeepLinkAndFocus(deepLink);
		return;
	}
	// A folder dropped on the taskbar icon/shortcut while already running.
	// Usually the renderer is already mounted and listening, so send directly
	// — but this can still fire while the first instance is early in
	// app.whenReady() or creating its window, before any shell WebContents
	// exists. Fall back to the same pendingFolderPath queue the cold-start
	// path uses rather than silently dropping it.
	const folderPath = parseOpenFolderPathArg(argv);
	if (folderPath) {
		const window = BaseWindow.getAllWindows()[0];
		if (window) {
			if (window.isMinimized()) window.restore();
			window.show();
			window.focus();
		}
		const contents = getShellWebContents();
		if (contents) {
			contents.send(OPEN_FOLDER_PATH_CHANNEL, folderPath);
		} else {
			pendingFolderPath = folderPath;
		}
		return;
	}
	const window = BaseWindow.getAllWindows()[0];
	if (!window) return;
	if (window.isMinimized()) window.restore();
	window.show();
	window.focus();
});
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
	const visibilityKillSwitched = (process.env.AO_TELEMETRY_DISABLED_EVENTS ?? "").split(",").some((name) => name.trim() === "ao.agent_switch.visibility_failure");
	// The approved release gate is intentionally closed. Tests inject the
	// dedicated no-cache sender; the shipping composition creates no visibility
	// network transport until that gate is separately approved.
	agentSwitchVisibilityController = new AgentSwitchVisibilityController({ send: async () => undefined, killSwitched: visibilityKillSwitched, diagnostic: (code) => console.warn("agent switch visibility diagnostic:", code) });
	for (const shellContents of trustedShellWebContents.values()) agentSwitchVisibilityController.registerWindow(shellContents.id);
	const authority = new TelemetryPolicyAuthority({ dataDir: desktopDataDir, packagedDefault: app.isPackaged, platform: process.platform });
	const daemonPolicy = new DaemonTelemetryPolicyClient(() => daemonStatus.state === "ready" && daemonStatus.port ? `http://127.0.0.1:${daemonStatus.port}` : null, (url, init) => net.fetch(url, init));
	const policyController = new DesktopTelemetryController({
		authority,
		daemon: daemonPolicy,
		environmentAllowsEvents: rendererTelemetryEnabled(process.env, app.isPackaged),
		transportFactory: () => initMainSentry(app.getVersion(), app.getPath("userData")),
		visibility: agentSwitchVisibilityController,
		clearRendererQueues: clearRendererTelemetryQueues,
		broadcast: (view) => {
			for (const shellContents of trustedShellWebContents.values()) if (!shellContents.isDestroyed()) shellContents.send(TELEMETRY_POLICY_CHANGED_CHANNEL, view);
		},
	});
	telemetryPolicyController = policyController;
	try { await policyController.initialize(); }
	catch (error) { console.error("telemetry policy bootstrap failed; reporting remains disabled:", error); }
	setInterval(() => { if (policyController.snapshot().state !== "applied") void policyController.retryPendingCleanup(); }, 1_000).unref();
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
		const bundlePath = resolveBundlePath();
		const action = decideRelocation({
			inApplicationsFolder: app.isInApplicationsFolder(),
			runningVersion: app.getVersion(),
			...inspectInstalledBundle(bundlePath),
		});
		if (action === "handoff") {
			// A stale copy (the original download, a mounted dmg) launching while a
			// newer build sits in /Applications. Relocating here would trash that
			// build and pin the user to this bundle's version forever, so open the
			// install and quit instead. Return before the marker write below: the
			// instance we just launched records the path and version.
			const installed = installedBundlePath(bundlePath);
			console.info(`newer install at ${installed}; handing off and quitting`);
			await shell.openPath(installed);
			app.quit();
			return;
		}
		if (action === "relocate") {
			try {
				// On success this restarts the app from /Applications, so code past
				// here only runs when no move happened (already there, or declined).
				app.moveToApplicationsFolder();
			} catch (err) {
				console.error("relocation to Applications failed:", err);
			}
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
	const initialUiSettings = keybindingRunFile
		? await readUiSettings(path.dirname(keybindingRunFile))
		: { ...DEFAULT_UI_SETTINGS };
	soundNotificationsEnabled = initialUiSettings.soundNotificationsEnabled;
	terminalShellPreference = initialUiSettings.terminalShell;
	if (isTrayEnabled(process.platform, app.isPackaged, app.getVersion())) {
		trayController = createTrayController({
			focusWindow: focusMainWindow,
			openSession: trayLifecycle.openSession,
			locale: initialUiSettings.locale,
		});
	}
	await createWindow();
	void startDaemon();
	initAutoUpdates();

	// Windows/Linux: on first launch, the deep-link URL may arrive as a
	// process.argv entry (e.g. ao-app://callback?token=...).
	const deepLinkArg = process.argv.find((a) => a.startsWith("ao-app://"));
	if (deepLinkArg) {
		void handleCloudDeepLinkAndFocus(deepLinkArg);
	}

	// Windows/Linux: a folder dropped on the taskbar icon/shortcut while the
	// app was not running launches it with the folder's path in argv. The
	// renderer isn't mounted yet — queue it, flushed on TRAY_RENDERER_READY_CHANNEL.
	const folderPathArg = parseOpenFolderPathArg(process.argv);
	if (folderPathArg) {
		pendingFolderPath = folderPathArg;
	}

	app.on("activate", () => {
		if (BaseWindow.getAllWindows().length === 0) {
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
			browserQuitCleanupPromise = Promise.all([
				disposeAllBrowserViewHosts(),
				telemetryPolicyController?.close() ?? Promise.resolve(),
			]).then(() => undefined).finally(() => {
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
