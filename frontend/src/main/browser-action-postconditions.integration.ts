import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { createServer } from "node:http";
import path from "node:path";
import { app, BrowserWindow, WebContentsView } from "electron";
import type { AgentBrowserRuntime } from "./agent-browser-runtime";
import { createBrowserViewHost } from "./browser-view-host";

function requiredEnvironmentVariable(name: string): string {
	const value = process.env[name];
	if (!value) throw new Error(`${name} is required`);
	return value;
}

const fixturePath = requiredEnvironmentVariable("AO_BROWSER_POSTCONDITION_FIXTURE");
const userDataPath = requiredEnvironmentVariable("AO_BROWSER_POSTCONDITION_USER_DATA");
app.setPath("userData", userDataPath);

async function run(): Promise<void> {
	await app.whenReady();
	const fixture = await readFile(fixturePath, "utf8");
	const server = createServer((request, response) => {
		if (request.url === "/fixture") {
			response.writeHead(200, {
				"content-type": "text/html; charset=utf-8",
				"content-length": Buffer.byteLength(fixture),
				connection: "close",
			});
			response.end(fixture);
			return;
		}
		if (request.url === "/destination") {
			const destination = "<!doctype html><title>Destination</title><p>Navigation completed</p>";
			response.writeHead(200, {
				"content-type": "text/html; charset=utf-8",
				"content-length": Buffer.byteLength(destination),
				connection: "close",
			});
			response.end(destination);
			return;
		}
		response.statusCode = 404;
		response.end("not found");
	});

	await new Promise<void>((resolve, reject) => {
		server.once("error", reject);
		server.listen(0, "127.0.0.1", resolve);
	});
	const address = server.address();
	if (!address || typeof address === "string") throw new Error("fixture server did not expose a TCP address");
	const fixtureURL = `http://127.0.0.1:${address.port}/fixture`;

	const window = new BrowserWindow({ show: true, width: 800, height: 600 });
	// Initialize the owning window before attaching a WebContentsView. On
	// Windows and Xvfb, a never-navigated BrowserWindow can leave the child
	// renderer unscheduled even though the native view is marked visible.
	await window.loadURL("data:text/html,<title>AO browser integration host</title>");
	window.show();
	let browserView: WebContentsView | undefined;
	const IntegrationWebContentsView = function (options: ConstructorParameters<typeof WebContentsView>[0]) {
		const view = new WebContentsView(options);
		browserView = view;
		// AO normally hides a new view off-screen until the renderer asks to show
		// the panel. This standalone fixture has no renderer, so keep a real surface
		// attached; Chromium otherwise may defer initialization indefinitely on
		// Windows and under Xvfb.
		const setBounds = view.setBounds.bind(view);
		view.setBounds = () => setBounds({ x: 0, y: 0, width: 800, height: 600 });
		const setVisible = view.setVisible.bind(view);
		view.setVisible = () => setVisible(true);
		const loadURL = view.webContents.loadURL.bind(view.webContents);
		view.webContents.loadURL = async (url, loadOptions) => {
			let timeout: ReturnType<typeof setTimeout> | undefined;
			let onDOMReady: (() => void) | undefined;
			let domReady: Promise<void> | undefined;
			if (url === "about:blank") {
				domReady = new Promise<void>((resolve) => {
					onDOMReady = resolve;
					view.webContents.once("dom-ready", onDOMReady);
				});
			}
			const deadline = new Promise<never>((_resolve, reject) => {
				timeout = setTimeout(() => reject(new Error(`timed out loading ${url}`)), 15_000);
			});
			const pendingLoad = loadURL(url, loadOptions);
			void pendingLoad.catch(() => undefined);
			try {
				if (url !== "about:blank") {
					await Promise.race([pendingLoad, deadline]);
					return;
				}
				// Electron 33 can leave a redundant initial about:blank load promise
				// unresolved after the document itself is ready. This exception is only
				// for the initialization URL; real fixture navigations await loadURL so
				// their committed navigation event cannot leak into the action baseline.
				if (!domReady) throw new Error("about:blank readiness observer was not installed");
				await Promise.race([pendingLoad, domReady, deadline]);
			} finally {
				if (timeout) clearTimeout(timeout);
				if (onDOMReady) view.webContents.off("dom-ready", onDOMReady);
			}
		};
		return view;
	} as unknown as typeof WebContentsView;

	function activeWebContents(): WebContentsView["webContents"] {
		if (!browserView) throw new Error("AO integration fixture has no active browser view");
		return browserView.webContents;
	}

	const runtime = {
		runAction: async (
			_sessionId: string,
			action: string,
			args: Record<string, unknown>,
		): Promise<Record<string, unknown>> => {
			switch (action) {
				case "tab-select":
					return {};
				case "open":
					await activeWebContents().loadURL(String(args.url));
					return {};
				case "snapshot":
					// A real provider snapshot cannot complete before Chromium has produced
					// a frame. Force the same readiness before dispatching native input.
					await activeWebContents().capturePage();
					return {
						snapshot: [
							'- button "Continue" [ref=e1]',
							'- button "Do nothing" [ref=e2]',
							'- button "Leave guarded page" [ref=e3]',
						].join("\n"),
						refs: {
							e1: { role: "button", name: "Continue" },
							e2: { role: "button", name: "Do nothing" },
							e3: { role: "button", name: "Leave guarded page" },
						},
					};
				case "click": {
					const element = args.ref === "e1" ? "continue" : args.ref === "e2" ? "no-effect" : "guarded";
					// Dispatch a real Chromium click through the DevTools input domain used by
					// browser automation runtimes. This gives the page the user activation that
					// beforeunload requires. Await the page's click event before returning so
					// the fixture synchronizes on browser evidence instead of renderer timing.
					const contents = activeWebContents();
					const clickObserved = contents.executeJavaScript(`
						new Promise((resolve) => {
							document.getElementById(${JSON.stringify(element)}).addEventListener(
								"click",
								() => resolve(true),
								{ once: true },
							);
						})
					`);
					const { x, y } = (await contents.executeJavaScript(`
						(() => {
							const rect = document.getElementById(${JSON.stringify(element)}).getBoundingClientRect();
							return { x: Math.round(rect.left + rect.width / 2), y: Math.round(rect.top + rect.height / 2) };
						})()
					`)) as { x: number; y: number };
					if (!contents.debugger.isAttached()) contents.debugger.attach("1.3");
					await contents.debugger.sendCommand("Input.dispatchMouseEvent", {
						type: "mouseMoved",
						x,
						y,
					});
					await contents.debugger.sendCommand("Input.dispatchMouseEvent", {
						type: "mousePressed",
						x,
						y,
						button: "left",
						clickCount: 1,
					});
					await contents.debugger.sendCommand("Input.dispatchMouseEvent", {
						type: "mouseReleased",
						x,
						y,
						button: "left",
						clickCount: 1,
					});
					let clickTimeout: ReturnType<typeof setTimeout> | undefined;
					try {
						await Promise.race([
							clickObserved,
							new Promise<never>((_resolve, reject) => {
								clickTimeout = setTimeout(
									() => reject(new Error(`Chromium did not dispatch a click to ${element}`)),
									5_000,
								);
							}),
						]);
					} finally {
						if (clickTimeout) clearTimeout(clickTimeout);
					}
					return { clicked: true };
				}
				default:
					throw new Error(`unexpected integration action: ${action}`);
			}
		},
		closeSession: async () => undefined,
		dispose: async () => undefined,
	} as unknown as AgentBrowserRuntime;

	const shellWebContents = {
		id: 1,
		focus: () => undefined,
		send: () => undefined,
		on: () => undefined,
	} as never;
	const host = createBrowserViewHost({
		mainWindow: {
			contentView: window.contentView,
			getContentBounds: () => ({ x: 0, y: 0, width: 800, height: 600 }),
		} as never,
		shellWebContents,
		ipcMain: {
			handle: () => undefined,
			on: () => undefined,
			removeHandler: () => undefined,
			off: () => undefined,
		} as never,
		shell: { openExternal: async () => undefined },
		WebContentsView: IntegrationWebContentsView,
		annotatePreloadPath: path.join(path.dirname(fixturePath), "empty-preload.cjs"),
		rendererOrigin: "http://127.0.0.1:1",
		agentBrowserRuntime: runtime,
	});

	try {
		await host.execute("integration-session", "open", { url: fixtureURL });
		const satisfied = (await host.execute("integration-session", "act", {
			instruction: "continue",
			postcondition: { kind: "navigation", timeoutMs: 1_000 },
		})) as Record<string, unknown>;
		assert.deepEqual(satisfied.postcondition, {
			status: "satisfied",
			kind: "navigation",
		});

		await host.execute("integration-session", "open", { url: fixtureURL });
		const unmet = (await host.execute("integration-session", "act", {
			instruction: "do nothing",
			postcondition: { kind: "navigation", timeoutMs: 250 },
		})) as Record<string, unknown>;
		assert.deepEqual(unmet.postcondition, {
			status: "unmet",
			kind: "navigation",
			reason: "timeout",
		});

		await host.execute("integration-session", "open", { url: fixtureURL });
		const cancelled = (await host.execute("integration-session", "act", {
			instruction: "leave guarded page",
			postcondition: { kind: "navigation", timeoutMs: 1_000 },
		})) as Record<string, unknown>;
		assert.deepEqual(cancelled.postcondition, {
			status: "cancelled",
			kind: "navigation",
			reason: "beforeunload",
		});
		assert.equal((cancelled.after as { url: string }).url, fixtureURL);
		await host.execute("integration-session", "snapshot", {
			interactive: true,
		});

		process.stdout.write("browser action postcondition integration passed\n");
	} finally {
		// Tear down native views synchronously before exiting. Electron can leave a
		// graceful app-level dispose pending after a beforeunload interaction even
		// though all assertions completed; the standalone process must not turn that
		// shutdown delay into a false test timeout.
		host.destroyAll();
		void host.dispose().catch(() => undefined);
		window.destroy();
		server.close();
		server.closeAllConnections();
	}
}

function reportIntegrationResult(code: number): void {
	// The outer runner owns process-tree termination. Keeping the Electron main
	// process alive until it consumes this marker lets it terminate Chromium's
	// renderer children too, even after the fixture exercised beforeunload.
	process.stdout.write(`AO_BROWSER_POSTCONDITION_RESULT:${code}\n`);
}

void run().then(
	() => reportIntegrationResult(0),
	(error) => {
		console.error(error);
		reportIntegrationResult(1);
	},
);
