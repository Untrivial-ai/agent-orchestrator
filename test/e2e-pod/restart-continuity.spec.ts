import {
  test,
  expect,
  _electron as electron,
  type ElectronApplication,
  type Page,
} from "@playwright/test";
import { createHash, randomUUID, timingSafeEqual } from "node:crypto";
import {
  execFile,
  execFileSync,
  spawn as spawnProcess,
  type ChildProcess,
} from "node:child_process";
import fs from "node:fs/promises";
import net from "node:net";
import os from "node:os";
import path from "node:path";
import { promisify } from "node:util";

const execFileAsync = promisify(execFile);
const RUN_REAL_RESTART_E2E = process.env.AO_RESTART_CONTINUITY_E2E === "1";
const APP_BIN = process.env.AO_APP_BIN ?? "";

type RunFile = { pid: number; port: number; owner?: string; appRunId?: string };
type SessionView = {
  id: string;
  displayName?: string;
  mode: "chat" | "tui";
  status: string;
  activity: { state: string };
};
type SessionRow = {
  id: string;
  session_mode: string;
  activity_state: string;
  runtime_handle_id: string;
  runtime_launch_id: string;
  agent_session_id: string;
  agent_session_id_launch_id: string;
  provider_conversation_id: string;
  controller_generation: string;
  workspace_path: string;
};
type NativeApp = {
  process: ChildProcess;
  application: ElectronApplication;
  renderer: NativeRenderer;
  appRunId: string;
};

type ExpectedSessionActivities = Readonly<Record<string, string>>;
type SessionAPIObservation = {
  stop: () => Promise<void>;
  stopAndAssert: (options?: {
    requireReady?: boolean;
    requireGateCode?:
      | "startup_recovery_in_progress"
      | "startup_recovery_failed";
    forbidReady?: boolean;
  }) => Promise<void>;
};

type PtyHostRegistryEntry = {
  sessionId: string;
  ptyHostPid: number;
  pipePath: string;
  launchId?: string;
  hostToken?: string;
  registeredAt?: string;
};

type PtyHostStatus = {
  alive: boolean;
  pid: number;
  protocolVersion: number;
  sessionId?: string;
  launchId?: string;
  hostPid?: number;
  hostToken?: string;
};

type ChatHostDescriptor = {
  version: number;
  sessionId: string;
  address: string;
  token: string;
  pid: number;
};

type TmuxWorkloadIdentity = {
  serverPid: number;
  sessionObjectId: string;
  paneObjectId: string;
  panePid: number;
  supervisorPid: number;
};

type TmuxFixture = {
  tmux: string;
  displayName: string;
  sessionId: string;
  launchId: string;
  namespaceArgs: string[];
  identity: TmuxWorkloadIdentity;
};

type TmuxHandlePayload = {
  session: string;
  target: "default" | "named" | "path";
  value?: string;
  legacy_binary?: boolean;
  owner_session: string;
  owner_launch: string;
  tmux_server_pid: number;
  tmux_session_id: string;
  tmux_pane_id: string;
};

type ShellTarget = { id: number; url: string };

const visibleElementSource = `
	(element) => {
		if (!(element instanceof HTMLElement)) return false;
		const style = getComputedStyle(element);
		const rect = element.getBoundingClientRect();
		return style.display !== "none" && style.visibility !== "hidden" && rect.width > 0 && rect.height > 0;
	}
`;

/**
 * Drives AO's real shell WebContentsView through Electron's main process.
 *
 * Playwright's Electron `windows()` API is centered on BrowserWindow. AO uses a
 * BaseWindow with an explicit WebContentsView, so waiting for a Playwright Page
 * is an unnecessary (and occasionally missed) transport event. The WebContents
 * itself is the durable identity and exposes the exact production renderer.
 */
class NativeRenderer {
  constructor(
    private readonly application: ElectronApplication,
    private readonly target: ShellTarget,
    readonly page?: Page,
  ) {}

  private async execute<R>(expression: string): Promise<R> {
    return this.application.evaluate(
      async ({ webContents }, input) => {
        const target = webContents.fromId(input.id);
        if (
          !target ||
          target.isDestroyed() ||
          !target.getURL().startsWith("app://renderer")
        ) {
          throw new Error("AO shell WebContents was replaced or destroyed");
        }
        return target.executeJavaScript(input.expression, true);
      },
      { id: this.target.id, expression },
    ) as Promise<R>;
  }

  async bodyContains(text: string): Promise<boolean> {
    return this.execute(
      `(document.body?.innerText ?? "").includes(${JSON.stringify(text)})`,
    );
  }

  async hasVisibleExactText(text: string): Promise<boolean> {
    return this.execute(`(() => {
			const visible = ${visibleElementSource};
			const needle = ${JSON.stringify(text)};
			const normalize = (value) => (value ?? "").replace(/\\s+/g, " ").trim();
			return Array.from(document.querySelectorAll("body *")).some(
				(element) => visible(element) && normalize(element.textContent) === needle,
			);
		})()`);
  }

  async clickExactText(text: string): Promise<void> {
    const clicked = await this.execute<boolean>(`(() => {
			const visible = ${visibleElementSource};
			const needle = ${JSON.stringify(text)};
			const normalize = (value) => (value ?? "").replace(/\\s+/g, " ").trim();
			const candidates = Array.from(document.querySelectorAll("body *")).filter(
				(element) => visible(element) && normalize(element.textContent) === needle,
			);
			const target = candidates.find((element) =>
				!Array.from(element.querySelectorAll("*")).some(
					(descendant) => visible(descendant) && normalize(descendant.textContent) === needle,
				),
			) ?? candidates[candidates.length - 1];
			if (!(target instanceof HTMLElement)) return false;
			target.click();
			return true;
		})()`);
    if (!clicked)
      throw new Error(`visible text ${JSON.stringify(text)} was not clickable`);
  }

  async clickSessionCard(name: string): Promise<void> {
    const clicked = await this.execute<boolean>(`(() => {
			const visible = ${visibleElementSource};
			const name = ${JSON.stringify(name)};
			const target = Array.from(document.querySelectorAll('[data-testid="board-session-card"]')).find(
				(element) => visible(element) && (element.textContent ?? "").includes(name),
			);
			if (!(target instanceof HTMLElement)) return false;
			target.click();
			return true;
		})()`);
    // Once a card opens the detail route, the board is unmounted but the same
    // sessions remain user-selectable in the sidebar. Follow that real UI path
    // for subsequent session switches.
    if (!clicked) await this.clickExactText(name);
  }

  async isTestIdVisible(testId: string): Promise<boolean> {
    return this.execute(`(() => {
			const visible = ${visibleElementSource};
			return Array.from(document.querySelectorAll("[data-testid]")).some(
				(element) => element.getAttribute("data-testid") === ${JSON.stringify(testId)} && visible(element),
			);
		})()`);
  }

  async testIdCount(testId: string): Promise<number> {
    return this
      .execute(`Array.from(document.querySelectorAll("[data-testid]")).filter(
			(element) => element.getAttribute("data-testid") === ${JSON.stringify(testId)},
		).length`);
  }

  async visibleExactTextCount(text: string): Promise<number> {
    return this.execute(`(() => {
			const visible = ${visibleElementSource};
			const needle = ${JSON.stringify(text)};
			const normalize = (value) => (value ?? "").replace(/\\s+/g, " ").trim();
			return Array.from(document.querySelectorAll("body *")).filter(
				(element) => visible(element) && normalize(element.textContent) === needle,
			).length;
		})()`);
  }

  async startVisibleExactTextAudit(text: string): Promise<void> {
    await this.execute(`(() => {
			const key = "__aoRestartVisibleTextAudit";
			const previous = window[key];
			previous?.observer?.disconnect();
			const visible = ${visibleElementSource};
			const needle = ${JSON.stringify(text)};
			const normalize = (value) => (value ?? "").replace(/\\s+/g, " ").trim();
			const audit = { sightings: 0, observer: null, scan: null };
			audit.scan = () => {
				if (Array.from(document.querySelectorAll("body *")).some(
					(element) => visible(element) && normalize(element.textContent) === needle,
				)) audit.sightings += 1;
			};
			audit.observer = new MutationObserver(audit.scan);
			audit.observer.observe(document.documentElement, {
				subtree: true,
				childList: true,
				characterData: true,
				attributes: true,
			});
			audit.scan();
			window[key] = audit;
		})()`);
  }

  async stopVisibleExactTextAudit(): Promise<number> {
    return this.execute(`(() => {
			const audit = window.__aoRestartVisibleTextAudit;
			if (!audit) return -1;
			audit.scan();
			audit.observer.disconnect();
			return audit.sightings;
		})()`);
  }

  async startTerminalInputAudit(): Promise<void> {
    await this.execute(`(() => {
			const key = "__aoRestartTerminalInputAudit";
			window[key]?.restore?.();
			const prototype = WebSocket.prototype;
			const descriptor = Object.getOwnPropertyDescriptor(prototype, "send");
			if (!descriptor || typeof descriptor.value !== "function") {
				throw new Error("WebSocket.send descriptor is unavailable");
			}
			const frames = [];
			const original = descriptor.value;
			const wrapped = function(data) {
				if (typeof data === "string") frames.push(data);
				return original.call(this, data);
			};
			Object.defineProperty(prototype, "send", { ...descriptor, value: wrapped });
			window[key] = {
				frames,
				restore: () => {
					if (prototype.send === wrapped) {
						Object.defineProperty(prototype, "send", descriptor);
					}
				},
			};
		})()`);
  }

  async terminalInputAuditFrames(): Promise<string[]> {
    return this.execute(
      `Array.from(window.__aoRestartTerminalInputAudit?.frames ?? [])`,
    );
  }

  async stopTerminalInputAudit(): Promise<string[]> {
    return this.execute(`(() => {
			const key = "__aoRestartTerminalInputAudit";
			const audit = window[key];
			if (!audit) return [];
			const frames = Array.from(audit.frames);
			audit.restore();
			delete window[key];
			return frames;
		})()`);
  }

  async startupRecoveryLayers(): Promise<{
    overlay: number;
    cover: number;
    banner: number;
  } | null> {
    return this.execute(`(() => {
			const cover = document.querySelector('[data-testid="startup-recovery-cover"]');
			const banner = document.querySelector('[role="alert"]');
			if (!(cover instanceof HTMLElement) || !(banner instanceof HTMLElement)) return null;
			const number = (value) => Number.parseFloat(value.trim());
			return {
				overlay: number(getComputedStyle(document.documentElement).getPropertyValue('--z-overlay')),
				cover: number(getComputedStyle(cover).zIndex),
				banner: number(getComputedStyle(banner).zIndex),
			};
		})()`);
  }

  async focusWithinTestId(testId: string, selector: string): Promise<void> {
    const focused = await this.execute<boolean>(`(() => {
			const visible = ${visibleElementSource};
			const owner = Array.from(document.querySelectorAll("[data-testid]")).find(
				(element) => element.getAttribute("data-testid") === ${JSON.stringify(testId)} && visible(element),
			);
			const target = owner?.querySelector(${JSON.stringify(selector)});
			if (!(target instanceof HTMLElement)) return false;
			target.focus();
			return document.activeElement === target;
		})()`);
    if (!focused)
      throw new Error(
        `could not focus ${selector} within [data-testid=${testId}]`,
      );
  }

  async textContentsWithinTestId(
    testId: string,
    selector: string,
  ): Promise<string[]> {
    return this.execute(`(() => {
			const owners = Array.from(document.querySelectorAll("[data-testid]")).filter(
				(element) => element.getAttribute("data-testid") === ${JSON.stringify(testId)},
			);
			return owners.flatMap((owner) =>
				Array.from(owner.querySelectorAll(${JSON.stringify(selector)})).map(
					(element) => element.textContent ?? "",
				),
			);
		})()`);
  }

  async type(text: string): Promise<void> {
    if (this.page) {
      try {
        await this.page.keyboard.type(text);
        return;
      } catch {
        // Fall through to the BaseWindow-safe Electron input path.
      }
    }
    await this.application.evaluate(
      ({ webContents }, input) => {
        const target = webContents.fromId(input.id);
        if (!target || target.isDestroyed())
          throw new Error("AO shell WebContents is unavailable");
        target.focus();
        for (const character of input.text) {
          target.sendInputEvent({ type: "keyDown", keyCode: character });
          target.sendInputEvent({ type: "char", keyCode: character });
          target.sendInputEvent({ type: "keyUp", keyCode: character });
        }
      },
      { id: this.target.id, text },
    );
  }

  async press(key: "Enter" | "Control+U"): Promise<void> {
    if (this.page) {
      try {
        await this.page.keyboard.press(key);
        return;
      } catch {
        // Fall through to the BaseWindow-safe Electron input path.
      }
    }
    await this.application.evaluate(
      ({ webContents }, input) => {
        const target = webContents.fromId(input.id);
        if (!target || target.isDestroyed())
          throw new Error("AO shell WebContents is unavailable");
        target.focus();
        if (input.key === "Control+U") {
          target.sendInputEvent({
            type: "keyDown",
            keyCode: "U",
            modifiers: ["control"],
          });
          target.sendInputEvent({
            type: "keyUp",
            keyCode: "U",
            modifiers: ["control"],
          });
          return;
        }
        target.sendInputEvent({ type: "keyDown", keyCode: "Enter" });
        target.sendInputEvent({ type: "keyUp", keyCode: "Enter" });
      },
      { id: this.target.id, key },
    );
  }

  async screenshot(file: string): Promise<void> {
    if (this.page) {
      try {
        await this.page.screenshot({ path: file });
        return;
      } catch {
        // Fall through to capturePage on the exact WebContentsView.
      }
    }
    const base64 = await this.application.evaluate(
      async ({ webContents }, input) => {
        const target = webContents.fromId(input.id);
        if (!target || target.isDestroyed())
          throw new Error("AO shell WebContents is unavailable");
        return (await target.capturePage()).toPNG().toString("base64");
      },
      { id: this.target.id },
    );
    await fs.writeFile(file, Buffer.from(base64, "base64"));
  }

  async requestQuit(): Promise<void> {
    await this.execute(`window.ao?.menu?.action("app.quit")`);
  }
}

function freePort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const server = net.createServer();
    server.once("error", reject);
    server.listen(0, "127.0.0.1", () => {
      const address = server.address() as net.AddressInfo;
      server.close(() => resolve(address.port));
    });
  });
}

async function waitFor<T>(
  read: () => Promise<T | null>,
  timeoutMs = 45_000,
): Promise<T> {
  const deadline = Date.now() + timeoutMs;
  let lastError: unknown;
  while (Date.now() < deadline) {
    try {
      const value = await read();
      if (value !== null) return value;
    } catch (error) {
      lastError = error;
    }
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  throw new Error(
    `timed out waiting for condition${lastError ? `: ${String(lastError)}` : ""}`,
  );
}

// Starts before Electron launches and continuously samples the state-bearing
// sessions route. A listener that has not appeared yet is expected; once any
// HTTP response is observable, every response must either be readiness-gated or
// expose the complete recovered session set without an Exited fact. Active,
// idle, and needs-input may legitimately change while the surviving agents run,
// so they are not treated as immutable restart facts. This observer is
// deliberately owned by the test process rather than a production preload hook.
function startSessionAPIObservation(
  port: number,
  expectedActivities: ExpectedSessionActivities,
  logs: string[],
  label: string,
): SessionAPIObservation {
  let stopRequested = false;
  let readyResponses = 0;
  let gatedResponses = 0;
  const gateCodes = new Set<string>();
  const violations: string[] = [];
  const expectedIDs = Object.keys(expectedActivities).sort();
  const recordViolation = (message: string) => {
    if (violations.length < 20) violations.push(message);
  };

  const done = (async () => {
    while (!stopRequested) {
      const controller = new AbortController();
      const timeout = setTimeout(() => controller.abort(), 500);
      try {
        const response = await fetch(
          `http://127.0.0.1:${port}/api/v1/sessions`,
          {
            signal: controller.signal,
          },
        );
        const text = await response.text();
        if (response.status === 503) {
          gatedResponses++;
          try {
            const payload = JSON.parse(text) as { code?: unknown };
            const code = typeof payload.code === "string" ? payload.code : "";
            if (
              code !== "startup_recovery_in_progress" &&
              code !== "startup_recovery_failed"
            ) {
              recordViolation(
                `503 was not a startup-readiness gate: ${text.slice(0, 500)}`,
              );
            } else {
              gateCodes.add(code);
            }
          } catch {
            recordViolation(
              `503 returned non-JSON readiness body: ${text.slice(0, 500)}`,
            );
          }
        } else if (response.status === 200) {
          readyResponses++;
          try {
            const payload = JSON.parse(text) as { sessions?: SessionView[] };
            if (!Array.isArray(payload.sessions)) {
              recordViolation(
                `200 response omitted sessions: ${text.slice(0, 500)}`,
              );
            } else {
              const observedIDs = payload.sessions
                .map((session) => session.id)
                .sort();
              if (JSON.stringify(observedIDs) !== JSON.stringify(expectedIDs)) {
                recordViolation(
                  `200 response session set ${JSON.stringify(observedIDs)}, want ${JSON.stringify(expectedIDs)}`,
                );
              }
              for (const session of payload.sessions) {
                if (
                  session.status === "exited" ||
                  session.activity?.state === "exited"
                ) {
                  recordViolation(
                    `200 exposed Exited for ${session.id}: status=${session.status} activity=${session.activity?.state}`,
                  );
                }
              }
            }
          } catch {
            recordViolation(
              `200 returned non-JSON sessions body: ${text.slice(0, 500)}`,
            );
          }
        } else {
          recordViolation(
            `sessions route returned unexpected HTTP ${response.status}: ${text.slice(0, 500)}`,
          );
        }
      } catch {
        // Connection refusal and a bounded request timeout are expected until the
        // daemon publishes its listener, and again only after stop is requested.
      } finally {
        clearTimeout(timeout);
      }
      if (!stopRequested)
        await new Promise((resolve) => setTimeout(resolve, 20));
    }
  })();

  let stopped: Promise<void> | undefined;
  const stop = () => {
    stopRequested = true;
    stopped ??= done;
    return stopped;
  };
  return {
    stop,
    stopAndAssert: async (options = {}) => {
      await stop();
      const requireReady = options.requireReady ?? true;
      if (violations.length > 0) {
        throw new Error(
          `${label} sessions observation failed:\n${violations.join("\n")}`,
        );
      }
      if (requireReady && readyResponses === 0) {
        throw new Error(
          `${label} never observed a recovered 200 sessions response`,
        );
      }
      if (options.forbidReady && readyResponses !== 0) {
        throw new Error(
          `${label} observed ${readyResponses} ready sessions responses while readiness should stay closed`,
        );
      }
      if (options.requireGateCode && !gateCodes.has(options.requireGateCode)) {
        throw new Error(
          `${label} never observed ${options.requireGateCode}; gates=${JSON.stringify([...gateCodes])}`,
        );
      }
      logs.push(
        `[restart-e2e] ${label} API observer: ready=${readyResponses} gated=${gatedResponses} ` +
          `codes=${JSON.stringify([...gateCodes])}`,
      );
    },
  };
}

async function assertNoVisibleExited(renderer: NativeRenderer): Promise<void> {
  expect(await renderer.visibleExactTextCount("Exited")).toBe(0);
}

async function readRunFile(runFile: string): Promise<RunFile | null> {
  try {
    return JSON.parse(await fs.readFile(runFile, "utf8")) as RunFile;
  } catch {
    return null;
  }
}

async function waitReady(
  runFile: string,
  expectedPort: number,
  expectedAppRunId?: string,
): Promise<RunFile> {
  const info = await waitFor(async () => {
    const candidate = await readRunFile(runFile);
    return candidate?.port === expectedPort &&
      (!expectedAppRunId || candidate.appRunId === expectedAppRunId)
      ? candidate
      : null;
  });
  await waitFor(async () => {
    try {
      const response = await fetch(`http://127.0.0.1:${expectedPort}/readyz`);
      if (response.status !== 200) return null;
      const payload = (await response.json()) as {
        status?: string;
        pid?: number;
      };
      return payload.status === "ready" && payload.pid === info.pid
        ? true
        : null;
    } catch {
      return null;
    }
  });
  return info;
}

async function waitStopped(port: number): Promise<void> {
  await waitFor(async () => {
    try {
      await fetch(`http://127.0.0.1:${port}/healthz`);
      return null;
    } catch {
      return true;
    }
  }, 45_000);
}

async function waitStartupRecoveryFailure(
  runFile: string,
  expectedPort: number,
  expectedAppRunId?: string,
): Promise<RunFile> {
  const info = await waitFor(async () => {
    const candidate = await readRunFile(runFile);
    return candidate?.port === expectedPort &&
      (!expectedAppRunId || candidate.appRunId === expectedAppRunId)
      ? candidate
      : null;
  });
  await waitFor(async () => {
    try {
      const response = await fetch(`http://127.0.0.1:${expectedPort}/readyz`);
      const payload = (await response.json()) as {
        status?: string;
        code?: string;
        pid?: number;
      };
      return response.status === 503 &&
        payload.status === "error" &&
        payload.code === "startup_recovery_failed" &&
        payload.pid === info.pid
        ? true
        : null;
    } catch {
      return null;
    }
  }, 45_000);
  return info;
}

function isMissing(error: unknown): boolean {
  return (error as NodeJS.ErrnoException | undefined)?.code === "ENOENT";
}

function processIsAlive(pid: number): boolean {
  try {
    process.kill(pid, 0);
    return true;
  } catch (error) {
    return (error as NodeJS.ErrnoException).code !== "ESRCH";
  }
}

async function assertSupervisorListener(socketPath: string): Promise<void> {
  const stat = await fs.stat(socketPath);
  if (!stat.isSocket())
    throw new Error(`${socketPath} is not a supervisor socket`);

  const socket = await new Promise<net.Socket>((resolve, reject) => {
    const candidate = net.createConnection(socketPath);
    const timer = setTimeout(() => {
      candidate.destroy();
      reject(new Error(`timed out connecting to ${socketPath}`));
    }, 3_000);
    candidate.once("connect", () => {
      clearTimeout(timer);
      resolve(candidate);
    });
    candidate.once("error", (error) => {
      clearTimeout(timer);
      reject(error);
    });
  });
  socket.destroy();
}

async function stopFixtureDaemon(
  runFile: string,
  logs: string[],
): Promise<boolean> {
  const info = await readRunFile(runFile);
  if (!info) return true;
  let owned = false;
  try {
    const health = await fetch(`http://127.0.0.1:${info.port}/healthz`);
    const payload = (await health.json()) as {
      status?: string;
      service?: string;
      pid?: number;
    };
    owned =
      health.ok &&
      payload.status === "ok" &&
      payload.service === "agent-orchestrator-daemon" &&
      payload.pid === info.pid;
  } catch {
    if (!processIsAlive(info.pid)) return true;
    logs.push(
      `[cleanup] preserved daemon ${info.pid}: health was unavailable but the exact PID is still alive`,
    );
    return false;
  }
  if (!owned) {
    logs.push(
      `[cleanup] refused to stop daemon ${info.pid}: health identity did not match`,
    );
    return false;
  }
  await fetch(`http://127.0.0.1:${info.port}/shutdown`, {
    method: "POST",
  }).catch(() => undefined);
  try {
    await waitFor(async () => (!processIsAlive(info.pid) ? true : null), 5_000);
    return true;
  } catch {
    // The identity-safe HTTP control path is the only authority this harness
    // has over the daemon. Never signal a stale numeric PID: it may have been
    // reused by an unrelated process after the health check.
    logs.push(
      `[cleanup] daemon ${info.pid} ignored its authenticated fixture /shutdown request`,
    );
    return false;
  }
}

async function proveFixturePIDsStopped(
  pids: Iterable<number>,
  logs: string[],
): Promise<boolean> {
  let clean = true;
  for (const pid of pids) {
    try {
      await waitFor(async () => (!processIsAlive(pid) ? true : null), 5_000);
    } catch {
      clean = false;
      logs.push(
        `[cleanup] preserved fixture data: prior daemon PID ${pid} is still alive`,
      );
    }
  }
  return clean;
}

async function api<T>(
  port: number,
  route: string,
  init?: RequestInit,
): Promise<T> {
  const response = await fetch(`http://127.0.0.1:${port}${route}`, {
    ...init,
    headers: { "content-type": "application/json", ...(init?.headers ?? {}) },
  });
  const text = await response.text();
  if (!response.ok)
    throw new Error(
      `${init?.method ?? "GET"} ${route}: ${response.status} ${text}`,
    );
  return text ? (JSON.parse(text) as T) : (undefined as T);
}

function sqliteRows(db: string): SessionRow[] {
  const sql = `
		SELECT id, session_mode, activity_state, runtime_handle_id, runtime_launch_id,
		       agent_session_id, agent_session_id_launch_id, provider_conversation_id,
		       controller_generation, workspace_path
		FROM sessions ORDER BY num;
	`;
  const output = execFileSync("sqlite3", [db, "-json", sql], {
    encoding: "utf8",
  }).trim();
  return output ? (JSON.parse(output) as SessionRow[]) : [];
}

function sqlQuote(value: string): string {
  return `'${value.replaceAll("'", "''")}'`;
}

function shellQuote(value: string): string {
  return `'${value.replaceAll("'", `'"'"'`)}'`;
}

function assertCanonicalTmuxHandle(
  handle: string,
  fixture: TmuxFixture,
  target: TmuxHandlePayload["target"],
  value = "",
  legacyBinary = false,
): void {
  if (!handle.startsWith("tmux-v1:"))
    throw new Error(`tmux handle is not canonical: ${handle}`);
  const payload = JSON.parse(
    Buffer.from(handle.slice("tmux-v1:".length), "base64url").toString("utf8"),
  ) as TmuxHandlePayload;
  expect(payload).toMatchObject({
    session: fixture.sessionId,
    target,
    owner_session: fixture.sessionId,
    owner_launch: fixture.launchId,
    tmux_server_pid: fixture.identity.serverPid,
    tmux_session_id: fixture.identity.sessionObjectId,
    tmux_pane_id: fixture.identity.paneObjectId,
  });
  expect(payload.value ?? "").toBe(value);
  expect(payload.legacy_binary ?? false).toBe(legacyBinary);
}

function historicalSocket(runFile: string): string {
  const digest = createHash("sha256")
    .update(path.resolve(runFile))
    .digest()
    .subarray(0, 16)
    .toString("hex");
  return path.join(path.dirname(runFile), `tmux-${digest}.sock`);
}

async function historicalSocketAddress(
  runFile: string,
): Promise<{ address: string; aliasDir?: string }> {
  const rawSocket = historicalSocket(runFile);
  if (Buffer.byteLength(rawSocket) <= 103) return { address: rawSocket };
  if (process.getuid === undefined)
    throw new Error("historical tmux socket alias requires a Unix uid");

  const targetDir = await fs.realpath(path.dirname(rawSocket));
  const canonicalSocket = path.join(targetDir, path.basename(rawSocket));
  const digest = createHash("sha256")
    .update(canonicalSocket)
    .digest()
    .subarray(0, 16)
    .toString("hex");
  const aliasRoot = `/tmp/ao-tmux-${process.getuid()}`;
  const aliasDir = path.join(aliasRoot, digest);
  await fs.mkdir(aliasRoot, { recursive: true, mode: 0o700 });
  await fs.chmod(aliasRoot, 0o700);
  try {
    await fs.symlink(targetDir, aliasDir, "dir");
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code !== "EEXIST") throw error;
    const existingTarget = await fs.realpath(aliasDir);
    if (existingTarget !== targetDir) {
      throw new Error(
        `historical tmux alias ${aliasDir} points to ${existingTarget}, want ${targetDir}`,
      );
    }
  }
  return {
    address: path.join(aliasDir, path.basename(canonicalSocket)),
    aliasDir,
  };
}

function isolatedAppEnv(overrides: NodeJS.ProcessEnv): NodeJS.ProcessEnv {
  const inherited = [
    "PATH",
    "SHELL",
    "LANG",
    "LC_ALL",
    "LC_CTYPE",
    "TMPDIR",
    "USER",
    "LOGNAME",
    "TERM",
  ];
  const env: NodeJS.ProcessEnv = {};
  for (const key of inherited) {
    if (process.env[key] !== undefined) env[key] = process.env[key];
  }
  return { ...env, ...overrides };
}

async function launchApp(
  env: NodeJS.ProcessEnv,
  logs: string[],
  auditForbiddenText?: string,
): Promise<NativeApp> {
  const appRunId = `restart-e2e-${randomUUID()}`;
  const launchEnv = {
    ...env,
    // A real desktop restart is a new supervisor owner. Reusing this value
    // would accidentally test an in-process refresh instead of owner handoff.
    AO_APP_RUN_ID: appRunId,
  };
  let application: ElectronApplication | undefined;
  let child: ChildProcess | undefined;
  try {
    // Use Playwright's Electron main-process transport, as the repository's
    // packaged-app smoke tests do. Raw connectOverCDP can hang forever on
    // Electron's browser websocket. AO's UI is an explicit WebContentsView under
    // BaseWindow, so discover it by WebContents identity instead of depending on
    // a BrowserWindow-oriented `windows()` event.
    application = await electron.launch({
      executablePath: APP_BIN,
      args: ["--use-mock-keychain", "--disable-gpu", "--no-sandbox"],
      env: launchEnv,
      timeout: 30_000,
    });
    const launchedChild = application.process();
    child = launchedChild;
    launchedChild.stdout?.on("data", (chunk) =>
      logs.push(`[stdout] ${String(chunk)}`),
    );
    launchedChild.stderr?.on("data", (chunk) =>
      logs.push(`[stderr] ${String(chunk)}`),
    );
    launchedChild.once("exit", (code, signal) =>
      logs.push(`[app-exit] code=${String(code)} signal=${String(signal)}`),
    );
    const target = await waitFor(async () => {
      return application!.evaluate(({ webContents }) => {
        const shell = webContents
          .getAllWebContents()
          .find((candidate) => candidate.getURL().startsWith("app://renderer"));
        return shell ? { id: shell.id, url: shell.getURL() } : null;
      });
    }, 30_000);
    const page = application
      .windows()
      .find((candidate) => candidate.url() === target.url);
    const renderer = new NativeRenderer(application, target, page);
    if (auditForbiddenText)
      await renderer.startVisibleExactTextAudit(auditForbiddenText);
    return { process: launchedChild, application, renderer, appRunId };
  } catch (error) {
    if (application) {
      await Promise.race([
        application.close().catch(() => undefined),
        new Promise<void>((resolve) => setTimeout(resolve, 2_000)),
      ]);
    }
    if (child?.exitCode === null && child.signalCode === null)
      child.kill("SIGKILL");
    throw error;
  }
}

async function quitApp(app: NativeApp): Promise<void> {
  if (app.process.exitCode === null && app.process.signalCode === null) {
    const exited = new Promise<boolean>((resolve) =>
      app.process.once("exit", () => resolve(true)),
    );
    await app.renderer.requestQuit().catch(() => undefined);
    const graceful = await Promise.race([
      exited,
      new Promise<false>((resolve) => setTimeout(() => resolve(false), 20_000)),
    ]);
    if (
      !graceful &&
      app.process.exitCode === null &&
      app.process.signalCode === null
    ) {
      const forcedExit = new Promise<void>((resolve) =>
        app.process.once("exit", () => resolve()),
      );
      app.process.kill("SIGKILL");
      await forcedExit;
    }
  }
  await Promise.race([
    app.application.close().catch(() => undefined),
    new Promise<void>((resolve) => setTimeout(resolve, 2_000)),
  ]);
}

function loopbackTarget(address: string): { host: string; port: number } {
  const match = /^(127\.0\.0\.1):(\d+)$/.exec(address);
  const port = Number(match?.[2]);
  if (!match || !Number.isInteger(port) || port < 1 || port > 65_535) {
    throw new Error(
      `refusing non-loopback fixture host address ${JSON.stringify(address)}`,
    );
  }
  return { host: match[1], port };
}

async function connectFixtureHost(address: string): Promise<net.Socket> {
  const target = loopbackTarget(address);
  return new Promise((resolve, reject) => {
    const socket = net.createConnection(target);
    const timer = setTimeout(() => {
      socket.destroy();
      reject(new Error(`timed out connecting to fixture host ${address}`));
    }, 3_000);
    socket.once("connect", () => {
      clearTimeout(timer);
      resolve(socket);
    });
    socket.once("error", (error) => {
      clearTimeout(timer);
      reject(error);
    });
  });
}

function ptyFrame(type: number, payload = Buffer.alloc(0)): Buffer {
  const frame = Buffer.alloc(5 + payload.length);
  frame[0] = type;
  frame.writeUInt32BE(payload.length, 1);
  payload.copy(frame, 5);
  return frame;
}

async function readPtyStatus(socket: net.Socket): Promise<PtyHostStatus> {
  return new Promise((resolve, reject) => {
    let buffered = Buffer.alloc(0);
    const timer = setTimeout(
      () => finish(new Error("timed out reading fixture PTY status")),
      3_000,
    );
    const finish = (error?: Error, status?: PtyHostStatus) => {
      clearTimeout(timer);
      socket.off("data", onData);
      socket.off("error", onError);
      socket.off("close", onClose);
      if (error) reject(error);
      else resolve(status!);
    };
    const onError = (error: Error) => finish(error);
    const onClose = () =>
      finish(new Error("fixture PTY host closed before status proof"));
    const onData = (chunk: Buffer) => {
      buffered = Buffer.concat([buffered, chunk]);
      while (buffered.length >= 5) {
        const payloadLength = buffered.readUInt32BE(1);
        if (payloadLength > 4 * 1024 * 1024) {
          finish(
            new Error(
              `fixture PTY host sent oversized frame (${payloadLength} bytes)`,
            ),
          );
          return;
        }
        const frameLength = 5 + payloadLength;
        if (buffered.length < frameLength) return;
        const type = buffered[0];
        const payload = buffered.subarray(5, frameLength);
        buffered = buffered.subarray(frameLength);
        if (type !== 0x07) continue;
        try {
          const status = JSON.parse(
            payload.toString("utf8"),
          ) as Partial<PtyHostStatus>;
          if (
            typeof status.alive !== "boolean" ||
            typeof status.pid !== "number" ||
            !Number.isInteger(status.pid) ||
            typeof status.protocolVersion !== "number" ||
            !Number.isInteger(status.protocolVersion)
          ) {
            finish(
              new Error(
                "fixture PTY host returned an incompatible status proof",
              ),
            );
            return;
          }
          finish(undefined, status as PtyHostStatus);
        } catch (error) {
          finish(error instanceof Error ? error : new Error(String(error)));
        }
        return;
      }
    };
    socket.on("data", onData);
    socket.once("error", onError);
    socket.once("close", onClose);
    socket.write(ptyFrame(0x06));
  });
}

async function readPtyStyledOutput(
  socket: net.Socket,
  lines: number,
): Promise<string> {
  return new Promise((resolve, reject) => {
    let buffered = Buffer.alloc(0);
    const timer = setTimeout(
      () => finish(new Error("timed out reading fixture PTY output")),
      3_000,
    );
    const finish = (error?: Error, output?: string) => {
      clearTimeout(timer);
      socket.off("data", onData);
      socket.off("error", onError);
      socket.off("close", onClose);
      if (error) reject(error);
      else resolve(output!);
    };
    const onError = (error: Error) => finish(error);
    const onClose = () =>
      finish(new Error("fixture PTY host closed before output proof"));
    const onData = (chunk: Buffer) => {
      buffered = Buffer.concat([buffered, chunk]);
      while (buffered.length >= 5) {
        const payloadLength = buffered.readUInt32BE(1);
        if (payloadLength > 4 * 1024 * 1024) {
          finish(
            new Error(
              `fixture PTY host sent oversized frame (${payloadLength} bytes)`,
            ),
          );
          return;
        }
        const frameLength = 5 + payloadLength;
        if (buffered.length < frameLength) return;
        const type = buffered[0];
        const payload = buffered.subarray(5, frameLength);
        buffered = buffered.subarray(frameLength);
        if (type !== 0x0a) continue;
        finish(undefined, payload.toString("utf8"));
        return;
      }
    };
    socket.on("data", onData);
    socket.once("error", onError);
    socket.once("close", onClose);
    socket.write(
      ptyFrame(0x09, Buffer.from(JSON.stringify({ lines }), "utf8")),
    );
  });
}

async function readVerifiedPtyStyledOutput(
  entry: PtyHostRegistryEntry,
): Promise<string> {
  const socket = await connectFixtureHost(entry.pipePath);
  try {
    const status = await readPtyStatus(socket);
    assertPtyHostStatusIdentity(entry, status);
    if (!status.alive) {
      throw new Error(
        `fixture PTY host ${entry.sessionId} reports a dead child`,
      );
    }
    return await readPtyStyledOutput(socket, 5_000);
  } finally {
    socket.destroy();
  }
}

function assertPtyHostStatusIdentity(
  entry: PtyHostRegistryEntry,
  status: PtyHostStatus,
): void {
  if (entry.hostToken) {
    const expectedToken = Buffer.from(entry.hostToken);
    const observedToken = Buffer.from(
      typeof status.hostToken === "string" ? status.hostToken : "",
    );
    if (
      status.protocolVersion < 3 ||
      status.sessionId !== entry.sessionId ||
      status.launchId !== entry.launchId ||
      status.hostPid !== entry.ptyHostPid ||
      expectedToken.length !== observedToken.length ||
      !timingSafeEqual(expectedToken, observedToken)
    ) {
      throw new Error(
        `refusing to control PTY host: authenticated fixture identity did not match`,
      );
    }
    return;
  }
  if (
    status.protocolVersion !== 2 ||
    status.sessionId !== undefined ||
    status.launchId !== undefined ||
    status.hostPid !== undefined ||
    status.hostToken !== undefined
  ) {
    throw new Error(
      `refusing to control PTY host: legacy fixture protocol proof did not match`,
    );
  }
}

async function shutdownPtyHost(
  entry: PtyHostRegistryEntry,
  runFile: string,
  expectedExecutable: string,
): Promise<void> {
  if (!entry.launchId)
    throw new Error(
      `PTY host ${entry.sessionId} has no immutable launch identity`,
    );
  const target = loopbackTarget(entry.pipePath);
  const { stdout: command } = await execFileAsync(
    "/bin/ps",
    ["eww", "-p", String(entry.ptyHostPid), "-o", "command="],
    { encoding: "utf8" },
  );
  if (
    !command.includes(expectedExecutable) ||
    !command.includes(` pty-host ${entry.sessionId} `) ||
    !command.includes(`AO_RUN_FILE=${runFile}`) ||
    !command.includes(`AO_RUNTIME_LAUNCH_ID=${entry.launchId}`)
  ) {
    throw new Error(
      `refusing to control PTY pid ${entry.ptyHostPid}: immutable fixture identity did not match`,
    );
  }
  const { stdout: listeners } = await execFileAsync(
    "/usr/sbin/lsof",
    [
      "-nP",
      "-a",
      "-p",
      String(entry.ptyHostPid),
      `-iTCP:${target.port}`,
      "-sTCP:LISTEN",
      "-Fn",
    ],
    { encoding: "utf8" },
  );
  const listenerFacts = listeners.split("\n");
  if (
    !listenerFacts.includes(`p${entry.ptyHostPid}`) ||
    !listenerFacts.includes(`n127.0.0.1:${target.port}`)
  ) {
    throw new Error(
      `refusing to control PTY pid ${entry.ptyHostPid}: fixture listener ownership did not match`,
    );
  }

  const socket = await connectFixtureHost(entry.pipePath);
  try {
    const status = await readPtyStatus(socket);
    assertPtyHostStatusIdentity(entry, status);
    if (status.alive) {
      const { stdout: parent } = await execFileAsync(
        "/bin/ps",
        ["-p", String(status.pid), "-o", "ppid="],
        { encoding: "utf8" },
      );
      if (Number(parent.trim()) !== entry.ptyHostPid) {
        throw new Error(
          `refusing to control PTY host: managed child ${status.pid} has the wrong owner`,
        );
      }
    }
    await new Promise<void>((resolve, reject) => {
      const timer = setTimeout(
        () => reject(new Error("fixture PTY host ignored graceful shutdown")),
        5_000,
      );
      socket.once("close", () => {
        clearTimeout(timer);
        resolve();
      });
      socket.once("error", (error) => {
        clearTimeout(timer);
        reject(error);
      });
      socket.write(ptyFrame(0x08));
    });
    await waitFor(async () => {
      try {
        const { stdout } = await execFileAsync(
          "/bin/ps",
          ["-p", String(entry.ptyHostPid), "-o", "pid="],
          { encoding: "utf8" },
        );
        return stdout.trim() === "" ? true : null;
      } catch (error) {
        const exitCode = (error as { code?: unknown }).code;
        if (exitCode === 1) return true;
        throw error;
      }
    }, 5_000);
  } finally {
    socket.destroy();
  }
}

async function locateProtocolV2FixtureSource(): Promise<string> {
  const candidates = [
    path.resolve(process.cwd(), "fixtures/pty-v2-host/main.go"),
    path.resolve(process.cwd(), "test/e2e-pod/fixtures/pty-v2-host/main.go"),
  ];
  for (const candidate of candidates) {
    try {
      const stat = await fs.stat(candidate);
      if (stat.isFile()) return candidate;
    } catch (error) {
      if (!isMissing(error)) throw error;
    }
  }
  throw new Error(
    `protocol-v2 fixture source not found; checked ${candidates.join(", ")}`,
  );
}

async function buildProtocolV2Fixture(root: string): Promise<string> {
  const source = await locateProtocolV2FixtureSource();
  // Darwin's shipped-v2 identity verifier requires the historical executable
  // basename. Build the stdlib-only fixture under that exact name.
  const executable = path.join(root, "agent-orchestrator");
  await execFileAsync("go", ["build", "-o", executable, source], {
    cwd: path.dirname(source),
    encoding: "utf8",
  });
  return executable;
}

async function launchProtocolV2Fixture(
  executable: string,
  sessionId: string,
  workspacePath: string,
  launchId: string,
  env: NodeJS.ProcessEnv,
  logs: string[],
  registryPath: string,
  registryEntries: PtyHostRegistryEntry[],
): Promise<{ entry: PtyHostRegistryEntry; childPid: number }> {
  const hostEnv: NodeJS.ProcessEnv = {
    ...env,
    AO_SESSION_ID: sessionId,
    AO_RUNTIME_LAUNCH_ID: launchId,
    AO_SUPERVISED_PROCESS: "1",
  };
  // The dedicated fixture always speaks the released protocol-v2 contract; no
  // production-only downgrade selector is involved.
  delete hostEnv.AO_PTY_HOST_TOKEN;
  const child = spawnProcess(
    executable,
    [
      "pty-host",
      sessionId,
      workspacePath,
      "agent-process",
      "supervise",
      "--session",
      sessionId,
      "--launch",
      launchId,
      "--",
      "/bin/zsh",
      "-f",
    ],
    {
      cwd: workspacePath,
      detached: true,
      env: hostEnv,
      stdio: ["ignore", "pipe", "pipe"],
    },
  );
  const hostPid = child.pid;
  if (!hostPid)
    throw new Error("protocol-v2 fixture did not receive a process id");

  try {
    const ready = await new Promise<{ childPid: number; port: number }>(
      (resolve, reject) => {
        let stdout = "";
        let stderr = "";
        let settled = false;
        const finish = (
          error?: Error,
          value?: { childPid: number; port: number },
        ) => {
          if (settled) return;
          settled = true;
          clearTimeout(timer);
          child.off("error", onError);
          child.off("exit", onExit);
          if (error) reject(error);
          else resolve(value!);
        };
        const onError = (error: Error) => finish(error);
        const onExit = (code: number | null, signal: NodeJS.Signals | null) =>
          finish(
            new Error(
              `protocol-v2 fixture exited before READY (code=${String(code)} ` +
                `signal=${String(signal)} stderr=${JSON.stringify(stderr.slice(-2_000))})`,
            ),
          );
        const timer = setTimeout(
          () =>
            finish(
              new Error(
                `timed out waiting for protocol-v2 fixture READY: ${stderr.slice(-2_000)}`,
              ),
            ),
          10_000,
        );
        child.once("error", onError);
        child.once("exit", onExit);
        child.stdout!.on("data", (chunk) => {
          stdout += String(chunk);
          const match = /READY:(\d+) (\d+)/.exec(stdout);
          if (!match) return;
          finish(undefined, {
            childPid: Number(match[1]),
            port: Number(match[2]),
          });
        });
        child.stderr!.on("data", (chunk) => {
          const text = String(chunk);
          stderr += text;
          logs.push(`[v2-pty-stderr] ${text}`);
        });
      },
    );
    const entry: PtyHostRegistryEntry = {
      sessionId,
      ptyHostPid: hostPid,
      pipePath: `127.0.0.1:${ready.port}`,
      launchId,
      registeredAt: new Date().toISOString(),
    };
    await fs.writeFile(
      registryPath,
      `${JSON.stringify(
        [
          ...registryEntries.filter(
            (candidate) => candidate.sessionId !== sessionId,
          ),
          entry,
        ],
        null,
        2,
      )}\n`,
      { mode: 0o600 },
    );
    child.stdout?.destroy();
    child.stderr?.destroy();
    child.unref();
    return { entry, childPid: ready.childPid };
  } catch (error) {
    if (child.exitCode === null && child.signalCode === null) {
      // `detached` created this exact process group. Stop both the host and its
      // PTY child if fixture publication fails before normal authenticated
      // teardown can own them.
      try {
        process.kill(-hostPid, "SIGKILL");
      } catch (killError) {
        if ((killError as NodeJS.ErrnoException).code !== "ESRCH")
          throw killError;
      }
    }
    throw error;
  }
}

async function readPtyHostChildPID(
  entry: PtyHostRegistryEntry,
): Promise<number> {
  const socket = await connectFixtureHost(entry.pipePath);
  try {
    const status = await readPtyStatus(socket);
    assertPtyHostStatusIdentity(entry, status);
    if (!status.alive)
      throw new Error(
        `fixture PTY host ${entry.sessionId} reports a dead child`,
      );
    const { stdout: parent } = await execFileAsync(
      "/bin/ps",
      ["-p", String(status.pid), "-o", "ppid="],
      { encoding: "utf8" },
    );
    if (Number(parent.trim()) !== entry.ptyHostPid) {
      throw new Error(
        `fixture PTY child ${status.pid} is not owned by host ${entry.ptyHostPid}`,
      );
    }
    return status.pid;
  } finally {
    socket.destroy();
  }
}

async function assertPtyHostRunning(
  entry: PtyHostRegistryEntry,
  expectedChildPid: number,
): Promise<void> {
  const observedChildPid = await readPtyHostChildPID(entry);
  if (observedChildPid !== expectedChildPid) {
    throw new Error(
      `fixture PTY host ${entry.sessionId} child changed from ${expectedChildPid} to ${observedChildPid}`,
    );
  }
}

async function assertPtyRegistryOwnership(
  registryPath: string,
  expected: PtyHostRegistryEntry,
): Promise<void> {
  const entries = JSON.parse(
    await fs.readFile(registryPath, "utf8"),
  ) as PtyHostRegistryEntry[];
  const observed = entries.find(
    (entry) => entry.sessionId === expected.sessionId,
  );
  if (
    !observed ||
    observed.ptyHostPid !== expected.ptyHostPid ||
    observed.pipePath !== expected.pipePath ||
    observed.launchId !== expected.launchId ||
    observed.hostToken !== expected.hostToken ||
    observed.registeredAt !== expected.registeredAt
  ) {
    throw new Error(
      `durable PTY registry ownership changed for ${expected.sessionId}`,
    );
  }
}

async function shutdownChatHost(descriptor: ChatHostDescriptor): Promise<void> {
  if (
    descriptor.version !== 1 ||
    !descriptor.sessionId ||
    !descriptor.token ||
    !Number.isInteger(descriptor.pid)
  ) {
    throw new Error("invalid fixture Chat host descriptor");
  }
  const socket = await connectFixtureHost(descriptor.address);
  try {
    await new Promise<void>((resolve, reject) => {
      let buffered = "";
      const timer = setTimeout(
        () =>
          reject(new Error("fixture Chat host ignored authenticated shutdown")),
        3_000,
      );
      socket.on("data", (chunk) => {
        buffered += String(chunk);
        const newline = buffered.indexOf("\n");
        if (newline < 0) return;
        try {
          const response = JSON.parse(buffered.slice(0, newline)) as {
            ok?: boolean;
            error?: string;
          };
          if (!response.ok)
            throw new Error(
              response.error || "fixture Chat host rejected shutdown",
            );
          clearTimeout(timer);
          resolve();
        } catch (error) {
          clearTimeout(timer);
          reject(error);
        }
      });
      socket.once("error", (error) => {
        clearTimeout(timer);
        reject(error);
      });
      socket.write(
        `${JSON.stringify({ version: 1, token: descriptor.token, action: "shutdown" })}\n`,
      );
    });
  } finally {
    socket.destroy();
  }
  await waitFor(
    async () => (!processIsAlive(descriptor.pid) ? true : null),
    10_000,
  );
}

async function assertChatControllerAttached(
  descriptor: ChatHostDescriptor,
): Promise<void> {
  const socket = await connectFixtureHost(descriptor.address);
  try {
    await new Promise<void>((resolve, reject) => {
      let buffered = "";
      const timer = setTimeout(
        () => reject(new Error("timed out proving Chat controller ownership")),
        3_000,
      );
      socket.on("data", (chunk) => {
        buffered += String(chunk);
        const newline = buffered.indexOf("\n");
        if (newline < 0) return;
        clearTimeout(timer);
        try {
          const response = JSON.parse(buffered.slice(0, newline)) as {
            ok?: boolean;
            error?: string;
          };
          if (
            response.ok ||
            response.error !== "chat host already has a controller"
          ) {
            throw new Error(
              `Chat host did not prove an attached controller: ${JSON.stringify(response)}`,
            );
          }
          resolve();
        } catch (error) {
          reject(error);
        }
      });
      socket.once("error", (error) => {
        clearTimeout(timer);
        reject(error);
      });
      socket.write(
        `${JSON.stringify({ version: 1, token: descriptor.token, action: "attach" })}\n`,
      );
    });
  } finally {
    socket.destroy();
  }
}

async function typeAndObserveNativeTerminal(
  renderer: NativeRenderer,
  host: PtyHostRegistryEntry,
  runtimeHandleID: string,
  root: string,
  label: string,
  displayName = "Native TUI",
): Promise<void> {
  const fixtureSuffix = path
    .basename(root)
    .slice(-8)
    .replaceAll(/[^A-Za-z0-9]/g, "");
  const marker = `native_${label}_${fixtureSuffix}`.toLowerCase();
  const decodeTerminalInput = (frames: string[]) => {
    const input: string[] = [];
    for (const payload of frames) {
      try {
        const frame = JSON.parse(payload) as {
          ch?: unknown;
          id?: unknown;
          type?: unknown;
          data?: unknown;
        };
        if (
          frame.ch === "terminal" &&
          frame.id === runtimeHandleID &&
          frame.type === "data" &&
          typeof frame.data === "string"
        ) {
          input.push(Buffer.from(frame.data, "base64").toString("utf8"));
        }
      } catch {
        // Non-terminal/system frames are irrelevant to this exact input proof.
      }
    }
    return input.join("");
  };
  let terminalInput = "";
  let terminalOutput = "";
  await renderer.clickSessionCard(displayName);
  await waitFor(
    async () => (await renderer.isTestIdVisible("session-terminal")) || null,
    30_000,
  );
  await waitFor(
    async () =>
      (await renderer.testIdCount("terminal-replay-cover")) === 0 || null,
    30_000,
  );
  await renderer.startTerminalInputAudit();
  try {
    await renderer.focusWithinTestId(
      "session-terminal",
      ".xterm-helper-textarea",
    );
    await renderer.type(marker);
    await waitFor(async () => {
      terminalInput = decodeTerminalInput(
        await renderer.terminalInputAuditFrames(),
      );
      return terminalInput.includes(marker) ? true : null;
    }, 30_000);
    await waitFor(async () => {
      terminalOutput = await readVerifiedPtyStyledOutput(host);
      return terminalOutput.includes(marker) ? true : null;
    }, 30_000);
    // Leave the live agent prompt unsubmitted. Ctrl+U exercises the input path a
    // second time and prevents a later restart from interpreting the probe text.
    await renderer.press("Control+U");
  } catch (error) {
    const visible = await renderer.textContentsWithinTestId(
      "session-terminal",
      ".xterm-rows, .xterm-accessibility-tree",
    );
    throw new Error(
      `native terminal input did not round-trip through exact host ${host.sessionId} ` +
        `and runtime ${runtimeHandleID} ` +
        `(visible=${JSON.stringify(visible.join("\n").slice(-500))}, ` +
        `terminalInput=${JSON.stringify(terminalInput.slice(-500))}, ` +
        `terminalOutput=${JSON.stringify(terminalOutput.slice(-500))}): ${String(error)}`,
    );
  } finally {
    await renderer.stopTerminalInputAudit();
  }
}

async function typeAndObserveTmuxTerminal(
  renderer: NativeRenderer,
  root: string,
  label: string,
  displayName: string,
): Promise<void> {
  await renderer.clickSessionCard(displayName);
  await waitFor(
    async () => (await renderer.isTestIdVisible("session-detail")) || null,
    30_000,
  );
  await waitFor(
    async () => (await renderer.isTestIdVisible("session-terminal")) || null,
    30_000,
  );
  await waitFor(
    async () =>
      (await renderer.testIdCount("terminal-replay-cover")) === 0 || null,
    30_000,
  );
  const marker = path.join(
    root,
    `${label.toLowerCase().replaceAll(/[^a-z0-9]+/g, "-")}.ok`,
  );
  await renderer.focusWithinTestId(
    "session-terminal",
    ".xterm-helper-textarea",
  );
  // Exercise the same Xterm onKey path as a user. insertText() would bypass
  // terminal input routing and could make a dead attachment look healthy.
  await renderer.type(`printf attached > ${shellQuote(marker)}`);
  await renderer.press("Enter");
  await waitFor(async () => {
    try {
      return (await fs.readFile(marker, "utf8")) === "attached" ? true : null;
    } catch {
      return null;
    }
  }, 30_000);
}

async function cleanupDetachedHosts(
  root: string,
  dataDir: string,
  runFile: string,
  expectedExecutables: Readonly<Record<string, string>>,
  defaultExecutable: string,
  logs: string[],
): Promise<boolean> {
  let clean = true;
  try {
    const registry = JSON.parse(
      await fs.readFile(path.join(root, "windows-pty-hosts.json"), "utf8"),
    ) as PtyHostRegistryEntry[];
    for (const entry of registry) {
      const expectedExecutable =
        expectedExecutables[entry.sessionId] ?? defaultExecutable;
      await shutdownPtyHost(entry, runFile, expectedExecutable).catch(
        (error) => {
          clean = false;
          logs.push(
            `[cleanup] preserve unproven PTY host ${entry.sessionId}: ${String(error)}`,
          );
        },
      );
    }
  } catch (error) {
    if (!isMissing(error)) {
      clean = false;
      logs.push(`[cleanup] preserve unreadable PTY registry: ${String(error)}`);
    }
  }
  try {
    const chatRoot = path.join(dataDir, "chat-hosts");
    for (const entry of await fs.readdir(chatRoot, { withFileTypes: true })) {
      if (!entry.isDirectory()) continue;
      try {
        const descriptor = JSON.parse(
          await fs.readFile(
            path.join(chatRoot, entry.name, "host.json"),
            "utf8",
          ),
        ) as ChatHostDescriptor;
        if (descriptor.sessionId !== entry.name) {
          throw new Error("fixture Chat host descriptor/session mismatch");
        }
        await shutdownChatHost(descriptor);
      } catch (error) {
        const descriptorPath = path.join(chatRoot, entry.name, "host.json");
        try {
          await fs.access(descriptorPath);
          clean = false;
          logs.push(
            `[cleanup] preserve unproven Chat host ${entry.name}: ${String(error)}`,
          );
        } catch (accessError) {
          if (!isMissing(accessError)) {
            clean = false;
            logs.push(
              `[cleanup] preserve unreadable Chat descriptor ${entry.name}: ${String(accessError)}`,
            );
          }
        }
      }
    }
  } catch (error) {
    if (!isMissing(error)) {
      clean = false;
      logs.push(
        `[cleanup] preserve unreadable Chat host directory: ${String(error)}`,
      );
    }
  }
  return clean;
}

function supervisedTmuxLaunchCommand(options: {
  daemon: string;
  runFile: string;
  sessionId: string;
  launchId: string;
  workspacePath: string;
  pathEnv: string;
  historicalPrivate?: boolean;
}): string {
  const identityExports = options.historicalPrivate
    ? []
    : [
        `export AO_RUN_FILE=${shellQuote(options.runFile)};`,
        `export AO_SESSION_ID=${shellQuote(options.sessionId)};`,
        "export AO_SUPERVISED_PROCESS='1';",
      ];
  return [
    `cd ${shellQuote(options.workspacePath)} || exit;`,
    "unset NO_COLOR;",
    ...identityExports,
    "export COLORTERM='truecolor';",
    `export PATH=${shellQuote(options.pathEnv)};`,
    [
      options.daemon,
      "agent-process",
      "supervise",
      "--session",
      options.sessionId,
      "--launch",
      options.launchId,
      "--",
      "/bin/zsh",
      "-f",
    ]
      .map(shellQuote)
      .join(" ") + ";",
    "exec cat >/dev/null",
  ].join(" ");
}

async function readProcessTable(): Promise<
  Array<{ pid: number; ppid: number; command: string }>
> {
  const { stdout } = await execFileAsync(
    "/bin/ps",
    ["-axww", "-o", "pid=,ppid=,command="],
    { encoding: "utf8" },
  );
  return stdout
    .split("\n")
    .map((line) => /^\s*(\d+)\s+(\d+)\s+(.*)$/.exec(line))
    .filter((match): match is RegExpExecArray => match !== null)
    .map((match) => ({
      pid: Number(match[1]),
      ppid: Number(match[2]),
      command: match[3],
    }));
}

function descendantPIDs(
  processes: Array<{ pid: number; ppid: number; command: string }>,
  ancestorPID: number,
): Set<number> {
  const children = new Map<number, number[]>();
  for (const process of processes) {
    const siblings = children.get(process.ppid) ?? [];
    siblings.push(process.pid);
    children.set(process.ppid, siblings);
  }
  const descendants = new Set<number>();
  const pending = [...(children.get(ancestorPID) ?? [])];
  while (pending.length > 0) {
    const pid = pending.pop()!;
    if (descendants.has(pid)) continue;
    descendants.add(pid);
    pending.push(...(children.get(pid) ?? []));
  }
  return descendants;
}

async function readTmuxWorkloadIdentity(
  tmux: string,
  namespaceArgs: string[],
  sessionId: string,
  launchId: string,
  daemon: string,
  env: NodeJS.ProcessEnv,
): Promise<TmuxWorkloadIdentity> {
  const { stdout } = await execFileAsync(
    tmux,
    [
      ...namespaceArgs,
      "list-panes",
      "-s",
      "-t",
      `=${sessionId}`,
      "-F",
      "#{pid}\t#{session_id}\t#{pane_id}\t#{pane_pid}",
    ],
    { env, encoding: "utf8" },
  );
  const lines = stdout.trim().split("\n").filter(Boolean);
  if (lines.length !== 1)
    throw new Error(
      `tmux ${sessionId} has ${lines.length} panes, want exactly one`,
    );
  const fields = lines[0].split("\t");
  if (
    fields.length !== 4 ||
    !/^\d+$/.test(fields[0]) ||
    !/^\$\d+$/.test(fields[1]) ||
    !/^%\d+$/.test(fields[2]) ||
    !/^\d+$/.test(fields[3])
  ) {
    throw new Error(
      `invalid tmux identity for ${sessionId}: ${JSON.stringify(lines[0])}`,
    );
  }
  const panePid = Number(fields[3]);
  const processes = await readProcessTable();
  const descendants = descendantPIDs(processes, panePid);
  const supervisors = processes.filter(
    (process) =>
      descendants.has(process.pid) &&
      process.command.includes(daemon) &&
      process.command.includes("agent-process") &&
      process.command.includes("supervise") &&
      process.command.includes(`--session ${sessionId}`) &&
      process.command.includes(`--launch ${launchId}`),
  );
  if (supervisors.length !== 1) {
    throw new Error(
      `tmux ${sessionId} has ${supervisors.length} exact supervisors under pane ${panePid}: ` +
        JSON.stringify(supervisors),
    );
  }
  return {
    serverPid: Number(fields[0]),
    sessionObjectId: fields[1],
    paneObjectId: fields[2],
    panePid,
    supervisorPid: supervisors[0].pid,
  };
}

async function launchTmuxFixture(options: {
  tmux: string;
  namespaceArgs: string[];
  displayName: string;
  sessionId: string;
  launchId: string;
  workspacePath: string;
  daemon: string;
  runFile: string;
  env: NodeJS.ProcessEnv;
  historicalPrivate?: boolean;
}): Promise<TmuxFixture> {
  const launchCommand = supervisedTmuxLaunchCommand({
    daemon: options.daemon,
    runFile: options.runFile,
    sessionId: options.sessionId,
    launchId: options.launchId,
    workspacePath: options.workspacePath,
    pathEnv: options.env.PATH ?? "",
    historicalPrivate: options.historicalPrivate,
  });
  await execFileAsync(
    options.tmux,
    [
      ...options.namespaceArgs,
      "new-session",
      "-d",
      "-s",
      options.sessionId,
      "-x",
      "220",
      "-y",
      "50",
      "-c",
      options.workspacePath,
      "/bin/zsh",
      "-c",
      launchCommand,
    ],
    { env: options.env },
  );
  const identity = await waitFor(
    () =>
      readTmuxWorkloadIdentity(
        options.tmux,
        options.namespaceArgs,
        options.sessionId,
        options.launchId,
        options.daemon,
        options.env,
      ),
    10_000,
  );
  return {
    tmux: options.tmux,
    displayName: options.displayName,
    sessionId: options.sessionId,
    launchId: options.launchId,
    namespaceArgs: options.namespaceArgs,
    identity,
  };
}

async function assertTmuxFixtureUnchanged(
  fixture: TmuxFixture,
  daemon: string,
  env: NodeJS.ProcessEnv,
): Promise<void> {
  const observed = await readTmuxWorkloadIdentity(
    fixture.tmux,
    fixture.namespaceArgs,
    fixture.sessionId,
    fixture.launchId,
    daemon,
    env,
  );
  expect(observed).toEqual(fixture.identity);
}

function tmuxHasNoServer(error: unknown): boolean {
  const output = `${String((error as { stdout?: unknown })?.stdout ?? "")}\n${String((error as { stderr?: unknown })?.stderr ?? "")}`;
  return /no server running|failed to connect to server|connection refused/i.test(
    output,
  );
}

async function tmuxSessionNames(
  tmux: string,
  namespaceArgs: string[],
  env: NodeJS.ProcessEnv,
): Promise<string[]> {
  try {
    const { stdout } = await execFileAsync(
      tmux,
      [...namespaceArgs, "list-sessions", "-F", "#{session_name}"],
      {
        env,
        encoding: "utf8",
      },
    );
    return stdout
      .split("\n")
      .map((name) => name.trim())
      .filter(Boolean);
  } catch (error) {
    if (tmuxHasNoServer(error)) return [];
    throw error;
  }
}

async function stopFixtureTmuxSession(
  tmux: string,
  namespaceArgs: string[],
  sessionId: string,
  env: NodeJS.ProcessEnv,
  logs: string[],
  label: string,
): Promise<boolean> {
  try {
    if (!(await tmuxSessionNames(tmux, namespaceArgs, env)).includes(sessionId))
      return true;
    await execFileAsync(
      tmux,
      [...namespaceArgs, "kill-session", "-t", `=${sessionId}`],
      { env },
    );
    if (
      (await tmuxSessionNames(tmux, namespaceArgs, env)).includes(sessionId)
    ) {
      throw new Error("session still exists after kill-session");
    }
    return true;
  } catch (error) {
    logs.push(
      `[cleanup] could not prove ${label} tmux session ${sessionId} stopped: ${String(error)}`,
    );
    return false;
  }
}

test("packaged desktop restart preserves Chat and TUI continuity without an Exited frame @real", async ({}, testInfo) => {
  test.skip(
    !RUN_REAL_RESTART_E2E,
    "set AO_RESTART_CONTINUITY_E2E=1 for the destructive isolated native-app scenario",
  );
  test.skip(
    process.platform !== "darwin",
    "the historical private-socket fixture in this scenario targets macOS",
  );
  test.setTimeout(420_000);
  if (!APP_BIN)
    throw new Error(
      "AO_APP_BIN must point to the packaged Electron executable",
    );

  // Force the historical raw socket beyond macOS's sockaddr_un limit. The
  // #4393 fixture must therefore survive through its exact deterministic /tmp
  // alias, not through the easier short-path case.
  const root = await fs.mkdtemp(
    path.join("/tmp", `ao-restart-continuity-${"x".repeat(50)}-`),
  );
  const home = path.join(root, "home");
  const dataDir = path.join(root, "data");
  const runFile = path.join(root, "running.json");
  const supervisorSocket = path.join(root, "supervise.sock");
  // Keep the named/default tmux socket directory short and independent from
  // the deliberately overlong historical-private socket path below. tmux -L
  // resolves inside TMUX_TMPDIR and macOS rejects the otherwise valid named
  // cohorts before recovery when that inherited path exceeds sockaddr_un.
  const tmuxTmp = path.join("/tmp", `ao-restart-tmux-${randomUUID()}`);
  const repo = path.join(root, "repo");
  const remote = path.join(root, "remote.git");
  const db = path.join(dataDir, "ao.db");
  const port = await freePort();
  const originalHome = os.homedir();
  const sourceCodexHome =
    process.env.CODEX_HOME || path.join(originalHome, ".codex");
  const codexHome = path.join(root, "codex-home");
  const codexAuth = path.join(codexHome, "auth.json");
  const logs: string[] = [];
  const phase = (message: string) => {
    const line = `[restart-e2e] ${message}`;
    logs.push(line);
    console.log(line);
  };
  const apps: NativeApp[] = [];
  const resources = path.resolve(APP_BIN, "../../Resources");
  const daemon = path.join(resources, "daemon", "ao");
  const tmux = path.join(resources, "tmux", "bin", "tmux");
  const historicalTarget = await historicalSocketAddress(runFile);
  expect(Buffer.byteLength(historicalSocket(runFile))).toBeGreaterThan(103);
  expect(Buffer.byteLength(supervisorSocket)).toBeLessThanOrEqual(103);
  const env = isolatedAppEnv({
    HOME: home,
    CODEX_HOME: codexHome,
    AO_DATA_DIR: dataDir,
    AO_RUN_FILE: runFile,
    AO_PORT: String(port),
    AO_DISABLE_GPU: "1",
    AO_TELEMETRY_EVENTS: "off",
    AO_TELEMETRY_REMOTE: "off",
    // The packaged main process has a baked Sentry fallback. Point it at a
    // closed loopback port so even crash/error telemetry cannot leave this test.
    AO_SENTRY_DSN: "http://restart-e2e@127.0.0.1:9/1",
    ELECTRON_DISABLE_SANDBOX: "1",
    TMUX_TMPDIR: tmuxTmp,
  });
  const systemTmux = execFileSync("/usr/bin/which", ["tmux"], {
    env,
    encoding: "utf8",
  }).trim();
  if (!path.isAbsolute(systemTmux))
    throw new Error(`could not resolve production PATH tmux: ${systemTmux}`);

  const apiObservers: SessionAPIObservation[] = [];
  const daemonPIDs = new Set<number>();
  const tmuxCleanupTargets: Array<{
    tmux: string;
    namespaceArgs: string[];
    sessionId: string;
    label: string;
  }> = [];
  const ptyFixtureExecutables: Record<string, string> = {};
  let scenarioCompleted = false;
  try {
    phase(`fixture ${root}`);
    await fs.mkdir(home, { recursive: true, mode: 0o700 });
    await fs.mkdir(codexHome, { recursive: true, mode: 0o700 });
    // A fresh state directory normally asks the user whether to enable updates.
    // This isolated test must never open a native modal or contact an update feed.
    // Persisting the production "Not now" shape makes that choice deterministic.
    await fs.writeFile(
      path.join(root, "update-settings.json"),
      `${JSON.stringify({ enabled: false, channel: "latest", nightlyAck: false, feature: null }, null, 2)}\n`,
      { mode: 0o600 },
    );
    const sourceAuth = path.join(sourceCodexHome, "auth.json");
    const authStat = await fs.stat(sourceAuth);
    if (!authStat.isFile())
      throw new Error(`${sourceAuth} is not a regular file`);
    await fs.copyFile(sourceAuth, codexAuth);
    await fs.chmod(codexAuth, 0o600);
    await fs.mkdir(tmuxTmp, { recursive: true, mode: 0o700 });
    await fs.chmod(tmuxTmp, 0o700);
    await fs.mkdir(repo, { recursive: true });
    execFileSync("git", ["init", "-b", "main"], { cwd: repo, stdio: "ignore" });
    execFileSync(
      "git",
      ["config", "user.email", "restart-e2e@example.invalid"],
      { cwd: repo },
    );
    execFileSync("git", ["config", "user.name", "AO Restart E2E"], {
      cwd: repo,
    });
    await fs.writeFile(path.join(repo, "README.md"), "restart continuity\n");
    execFileSync("git", ["add", "README.md"], { cwd: repo });
    execFileSync("git", ["commit", "-m", "init"], {
      cwd: repo,
      stdio: "ignore",
    });
    execFileSync("git", ["init", "--bare", "-b", "main", remote], {
      stdio: "ignore",
    });
    execFileSync("git", ["remote", "add", "origin", remote], { cwd: repo });
    execFileSync("git", ["push", "-u", "origin", "main"], {
      cwd: repo,
      stdio: "ignore",
    });
    execFileSync("git", ["remote", "set-head", "origin", "main"], {
      cwd: repo,
    });
    const protocolV2Fixture = await buildProtocolV2Fixture(root);
    phase("dedicated protocol-v2 PTY fixture built");

    phase("launching initial app");
    const first = await launchApp(env, logs);
    apps.push(first);
    const firstDaemon = await waitReady(runFile, port, first.appRunId);
    daemonPIDs.add(firstDaemon.pid);
    await assertSupervisorListener(supervisorSocket);
    phase("initial daemon ready");
    await waitFor(
      async () =>
        (await first.renderer.bodyContains("Agent Orchestrator")) || null,
      30_000,
    );

    await api(port, "/api/v1/projects", {
      method: "POST",
      body: JSON.stringify({
        path: repo,
        projectId: "restart-e2e",
        name: "Restart E2E",
      }),
    });
    const spawn = async (displayName: string, mode: "chat" | "tui") =>
      (
        await api<{ session: SessionView }>(port, "/api/v1/sessions", {
          method: "POST",
          body: JSON.stringify({
            projectId: "restart-e2e",
            kind: "worker",
            harness: "codex",
            mode,
            prompt: "",
            displayName,
          }),
        })
      ).session;
    const chat = await spawn("Chat Restart", "chat");
    const modernTUI = await spawn("Native TUI", "tui");
    const protocolV2TUI = await spawn("Protocol v2 TUI", "tui");
    const legacyTUI = await spawn("Private Legacy TUI", "tui");
    const namedLegacyTUI = await spawn("Named Legacy TUI", "tui");
    const defaultLegacyTUI = await spawn("Default Legacy TUI", "tui");
    phase(
      "Chat, native v3/v2 PTY TUIs, and private/named/default legacy TUI cohorts spawned",
    );
    const beforeQuit = sqliteRows(db);
    const chatBefore = beforeQuit.find((row) => row.id === chat.id)!;
    const modernBefore = beforeQuit.find((row) => row.id === modernTUI.id)!;
    const protocolV2Before = beforeQuit.find(
      (row) => row.id === protocolV2TUI.id,
    )!;
    const legacyBefore = beforeQuit.find((row) => row.id === legacyTUI.id)!;
    const namedLegacyBefore = beforeQuit.find(
      (row) => row.id === namedLegacyTUI.id,
    )!;
    const defaultLegacyBefore = beforeQuit.find(
      (row) => row.id === defaultLegacyTUI.id,
    )!;
    expect(chatBefore.session_mode).toBe("chat");
    expect(chatBefore.provider_conversation_id).not.toBe("");
    expect(modernBefore.runtime_handle_id).toMatch(/^ptyhost-v1:/);
    expect(protocolV2Before.runtime_handle_id).toMatch(/^ptyhost-v1:/);
    const expectedChatActivity = chatBefore.activity_state;
    const expectedNativeActivity = "active";
    const expectedProtocolV2Activity = "active";
    const expectedLegacyActivity = "active";
    expect(expectedChatActivity).not.toBe("exited");
    expect(expectedNativeActivity).not.toBe("exited");
    const chatDescriptorPath = path.join(
      dataDir,
      "chat-hosts",
      chat.id,
      "host.json",
    );
    const chatHostBefore = JSON.parse(
      await fs.readFile(chatDescriptorPath, "utf8"),
    ) as ChatHostDescriptor;
    await assertChatControllerAttached(chatHostBefore);

    // Create the stale activity history through the real lifecycle boundary so
    // its timestamps use the same SQLite representation as production. Direct
    // SQL timestamp strings can compare differently from Go-bound time values
    // and would turn the recovery CAS itself into a test-fixture artifact.
    await api(
      port,
      `/api/v1/sessions/${encodeURIComponent(modernBefore.id)}/activity`,
      {
        method: "POST",
        body: JSON.stringify({
          state: expectedNativeActivity,
          launchId: modernBefore.runtime_launch_id,
        }),
      },
    );
    for (const row of [
      protocolV2Before,
      legacyBefore,
      namedLegacyBefore,
      defaultLegacyBefore,
    ]) {
      const activityRoute = `/api/v1/sessions/${encodeURIComponent(row.id)}/activity`;
      await api(port, activityRoute, {
        method: "POST",
        body: JSON.stringify({
          state: "active",
          launchId: row.runtime_launch_id,
        }),
      });
      await api(port, activityRoute, {
        method: "POST",
        body: JSON.stringify({
          state: "exited",
          launchId: row.runtime_launch_id,
        }),
      });
    }

    await quitApp(first);
    await waitStopped(port);
    phase("initial app and daemon stopped cleanly");
    const afterGracefulQuit = sqliteRows(db);
    expect(
      afterGracefulQuit.find((row) => row.id === chat.id)!.activity_state,
    ).toBe(expectedChatActivity);
    expect(
      afterGracefulQuit.find((row) => row.id === modernTUI.id)!.activity_state,
    ).toBe(expectedNativeActivity);
    expect(
      afterGracefulQuit.find((row) => row.id === protocolV2TUI.id)!
        .activity_state,
    ).toBe("exited");
    expect(
      afterGracefulQuit.find((row) => row.id === legacyTUI.id)!.activity_state,
    ).toBe("exited");
    expect(
      afterGracefulQuit.find((row) => row.id === namedLegacyTUI.id)!
        .activity_state,
    ).toBe("exited");
    expect(
      afterGracefulQuit.find((row) => row.id === defaultLegacyTUI.id)!
        .activity_state,
    ).toBe("exited");

    // Preserve one shipped protocol-v3 host exactly. Replace four other native
    // hosts with a faithful protocol-v2 fixture plus historical-private, named,
    // and default tmux cohorts. Every converted row starts with a stale Exited
    // fact; default additionally reproduces #4458's stale durable launch ID.
    const registryPath = path.join(root, "windows-pty-hosts.json");
    const registry = JSON.parse(
      await fs.readFile(registryPath, "utf8"),
    ) as PtyHostRegistryEntry[];
    const modernV3Host = registry.find(
      (entry) => entry.sessionId === modernTUI.id,
    );
    const protocolV2OriginalHost = registry.find(
      (entry) => entry.sessionId === protocolV2TUI.id,
    );
    const convertedHostIDs = new Set([
      protocolV2TUI.id,
      legacyTUI.id,
      namedLegacyTUI.id,
      defaultLegacyTUI.id,
    ]);
    if (
      !modernV3Host ||
      !modernV3Host.hostToken ||
      !modernV3Host.launchId ||
      !modernV3Host.registeredAt
    ) {
      throw new Error(
        "modern protocol-v3 PTY host lacks exact durable identity",
      );
    }
    if (!protocolV2OriginalHost)
      throw new Error("protocol-v2 source session was not durably registered");
    const modernV3ChildPID = await readPtyHostChildPID(modernV3Host);
    await assertPtyRegistryOwnership(registryPath, modernV3Host);
    for (const entry of registry.filter((candidate) =>
      convertedHostIDs.has(candidate.sessionId),
    )) {
      await shutdownPtyHost(entry, runFile, daemon);
    }
    const survivingRegistry = registry.filter(
      (entry) => !convertedHostIDs.has(entry.sessionId),
    );

    const privateLaunch = `private-launch-${randomUUID()}`;
    const namedLaunch = `named-launch-${randomUUID()}`;
    const defaultLiveLaunch = `default-live-launch-${randomUUID()}`;
    const defaultStaleLaunch = `default-stale-launch-${randomUUID()}`;
    const privateNativeSessionID = `private-native-${randomUUID()}`;
    const namedNativeSessionID = `named-native-${randomUUID()}`;
    const defaultNativeSessionID = `default-native-${randomUUID()}`;
    for (const fixture of [
      {
        sessionId: legacyTUI.id,
        durableLaunch: privateLaunch,
        nativeSessionID: privateNativeSessionID,
      },
      {
        sessionId: namedLegacyTUI.id,
        durableLaunch: namedLaunch,
        nativeSessionID: namedNativeSessionID,
      },
      {
        sessionId: defaultLegacyTUI.id,
        durableLaunch: defaultStaleLaunch,
        nativeSessionID: defaultNativeSessionID,
      },
    ]) {
      execFileSync("sqlite3", [
        db,
        `UPDATE sessions SET runtime_handle_id=${sqlQuote(fixture.sessionId)}, runtime_launch_id=${sqlQuote(fixture.durableLaunch)}, agent_session_id=${sqlQuote(fixture.nativeSessionID)}, agent_session_id_launch_id=${sqlQuote(fixture.durableLaunch)} WHERE id=${sqlQuote(fixture.sessionId)};`,
      ]);
    }

    const socket = historicalTarget.address;
    await fs.mkdir(path.dirname(socket), { recursive: true });
    const tmuxSpecs = [
      {
        tmux,
        displayName: "Private Legacy TUI",
        sessionId: legacyTUI.id,
        launchId: privateLaunch,
        workspacePath: legacyBefore.workspace_path,
        namespaceArgs: ["-S", socket, "-f", "/dev/null"],
        historicalPrivate: true,
      },
      {
        tmux,
        displayName: "Named Legacy TUI",
        sessionId: namedLegacyTUI.id,
        launchId: namedLaunch,
        workspacePath: namedLegacyBefore.workspace_path,
        namespaceArgs: ["-L", "ao"],
      },
      {
        tmux: systemTmux,
        displayName: "Default Legacy TUI",
        sessionId: defaultLegacyTUI.id,
        launchId: defaultLiveLaunch,
        workspacePath: defaultLegacyBefore.workspace_path,
        namespaceArgs: ["-L", "default"],
      },
    ];
    const tmuxFixtures: TmuxFixture[] = [];
    for (const spec of tmuxSpecs) {
      tmuxCleanupTargets.push({
        tmux: spec.tmux,
        namespaceArgs: spec.namespaceArgs,
        sessionId: spec.sessionId,
        label: spec.displayName,
      });
      tmuxFixtures.push(
        await launchTmuxFixture({
          ...spec,
          daemon,
          runFile,
          env,
        }),
      );
    }
    // A same-name foreign session on default must not beat the weak historical
    // private candidate. It is replaced with an exact duplicate for the final
    // ambiguity-negative phase.
    tmuxCleanupTargets.push({
      tmux: systemTmux,
      namespaceArgs: ["-L", "default"],
      sessionId: legacyTUI.id,
      label: "foreign",
    });
    await execFileAsync(
      systemTmux,
      [
        "-L",
        "default",
        "new-session",
        "-d",
        "-s",
        legacyTUI.id,
        "/bin/sleep",
        "3600",
      ],
      { env },
    );
    const protocolV2Host = await launchProtocolV2Fixture(
      protocolV2Fixture,
      protocolV2TUI.id,
      protocolV2Before.workspace_path,
      protocolV2Before.runtime_launch_id,
      env,
      logs,
      registryPath,
      survivingRegistry,
    );
    ptyFixtureExecutables[protocolV2TUI.id] = protocolV2Fixture;
    await assertPtyRegistryOwnership(registryPath, modernV3Host);
    await assertPtyHostRunning(modernV3Host, modernV3ChildPID);
    await assertPtyRegistryOwnership(registryPath, protocolV2Host.entry);
    await assertPtyHostRunning(protocolV2Host.entry, protocolV2Host.childPid);
    const expectedActivities: ExpectedSessionActivities = {
      [chat.id]: expectedChatActivity,
      [modernTUI.id]: expectedNativeActivity,
      [protocolV2TUI.id]: expectedProtocolV2Activity,
      [legacyTUI.id]: expectedLegacyActivity,
      [namedLegacyTUI.id]: expectedLegacyActivity,
      [defaultLegacyTUI.id]: expectedLegacyActivity,
    };
    phase(
      "v3/v2 PTY and historical-private/named/default tmux fixtures have exact captured identities",
    );

    const secondAPI = startSessionAPIObservation(
      port,
      expectedActivities,
      logs,
      "first cold restart",
    );
    apiObservers.push(secondAPI);
    const second = await launchApp(env, logs, "Exited");
    apps.push(second);
    const secondDaemon = await waitReady(runFile, port, second.appRunId);
    daemonPIDs.add(secondDaemon.pid);
    await assertSupervisorListener(supervisorSocket);
    phase("first cold restart ready");
    await waitFor(
      async () =>
        (await second.renderer.hasVisibleExactText("Restart E2E")) || null,
      30_000,
    );
    await second.renderer.clickExactText("Restart E2E");
    for (const name of [
      "Chat Restart",
      "Native TUI",
      "Protocol v2 TUI",
      "Private Legacy TUI",
      "Named Legacy TUI",
      "Default Legacy TUI",
    ]) {
      await waitFor(
        async () => (await second.renderer.hasVisibleExactText(name)) || null,
        30_000,
      );
    }
    await assertNoVisibleExited(second.renderer);
    await second.renderer.screenshot(
      testInfo.outputPath("restart-two-ready.png"),
    );

    const afterSecond = sqliteRows(db);
    const chatSecond = afterSecond.find((row) => row.id === chat.id)!;
    const modernSecond = afterSecond.find((row) => row.id === modernTUI.id)!;
    const protocolV2Second = afterSecond.find(
      (row) => row.id === protocolV2TUI.id,
    )!;
    const legacySecond = afterSecond.find((row) => row.id === legacyTUI.id)!;
    const namedLegacySecond = afterSecond.find(
      (row) => row.id === namedLegacyTUI.id,
    )!;
    const defaultLegacySecond = afterSecond.find(
      (row) => row.id === defaultLegacyTUI.id,
    )!;
    expect(chatSecond.activity_state).toBe(expectedChatActivity);
    expect(chatSecond.agent_session_id).toBe(chatBefore.agent_session_id);
    expect(chatSecond.provider_conversation_id).toBe(
      chatBefore.provider_conversation_id,
    );
    expect(chatSecond.controller_generation).not.toBe(
      chatBefore.controller_generation,
    );
    expect(modernSecond.activity_state).not.toBe("exited");
    expect(modernSecond.runtime_handle_id).toBe(modernBefore.runtime_handle_id);
    expect(protocolV2Second.activity_state).toBe(expectedProtocolV2Activity);
    expect(protocolV2Second.runtime_handle_id).toBe(
      protocolV2Before.runtime_handle_id,
    );
    expect(protocolV2Second.runtime_launch_id).toBe(
      protocolV2Before.runtime_launch_id,
    );
    expect(protocolV2Second.agent_session_id_launch_id).toBe(
      protocolV2Before.agent_session_id_launch_id,
    );
    expect(legacySecond.activity_state).toBe(expectedLegacyActivity);
    expect(legacySecond.runtime_launch_id).toBe(privateLaunch);
    expect(legacySecond.agent_session_id).toBe(privateNativeSessionID);
    expect(legacySecond.agent_session_id_launch_id).toBe(privateLaunch);
    expect(legacySecond.runtime_handle_id).toMatch(/^tmux-v1:/);
    expect(namedLegacySecond.activity_state).toBe(expectedLegacyActivity);
    expect(namedLegacySecond.runtime_launch_id).toBe(namedLaunch);
    expect(namedLegacySecond.agent_session_id).toBe(namedNativeSessionID);
    expect(namedLegacySecond.agent_session_id_launch_id).toBe(namedLaunch);
    expect(namedLegacySecond.runtime_handle_id).toMatch(/^tmux-v1:/);
    expect(defaultLegacySecond.activity_state).toBe(expectedLegacyActivity);
    expect(defaultLegacySecond.runtime_launch_id).toBe(defaultLiveLaunch);
    expect(defaultLegacySecond.agent_session_id).toBe(defaultNativeSessionID);
    expect(defaultLegacySecond.agent_session_id_launch_id).toBe(
      defaultLiveLaunch,
    );
    expect(defaultLegacySecond.runtime_launch_id).not.toBe(defaultStaleLaunch);
    expect(defaultLegacySecond.runtime_handle_id).toMatch(/^tmux-v1:/);
    assertCanonicalTmuxHandle(
      legacySecond.runtime_handle_id,
      tmuxFixtures[0],
      "path",
      historicalSocket(runFile),
    );
    assertCanonicalTmuxHandle(
      namedLegacySecond.runtime_handle_id,
      tmuxFixtures[1],
      "named",
      "ao",
    );
    assertCanonicalTmuxHandle(
      defaultLegacySecond.runtime_handle_id,
      tmuxFixtures[2],
      "default",
      "",
      true,
    );
    await execFileAsync(
      systemTmux,
      ["-L", "default", "has-session", "-t", `=${legacyTUI.id}`],
      { env },
    );
    const chatHostSecond = JSON.parse(
      await fs.readFile(chatDescriptorPath, "utf8"),
    ) as ChatHostDescriptor;
    expect(chatHostSecond.pid).toBe(chatHostBefore.pid);
    await assertChatControllerAttached(chatHostSecond);
    await assertPtyRegistryOwnership(registryPath, modernV3Host);
    await assertPtyHostRunning(modernV3Host, modernV3ChildPID);
    await typeAndObserveNativeTerminal(
      second.renderer,
      modernV3Host,
      modernSecond.runtime_handle_id,
      root,
      "SECOND",
    );
    await assertPtyRegistryOwnership(registryPath, protocolV2Host.entry);
    await assertPtyHostRunning(protocolV2Host.entry, protocolV2Host.childPid);
    await typeAndObserveNativeTerminal(
      second.renderer,
      protocolV2Host.entry,
      protocolV2Second.runtime_handle_id,
      root,
      "V2_SECOND",
      "Protocol v2 TUI",
    );
    for (const fixture of tmuxFixtures) {
      await assertTmuxFixtureUnchanged(fixture, daemon, env);
      await typeAndObserveTmuxTerminal(
        second.renderer,
        root,
        `SECOND_${fixture.displayName}`,
        fixture.displayName,
      );
    }
    // Observe through at least one periodic reaper interval while the external
    // API observer continues sampling every state-bearing response.
    await new Promise((resolve) => setTimeout(resolve, 6_000));
    await assertNoVisibleExited(second.renderer);
    await assertPtyRegistryOwnership(registryPath, modernV3Host);
    await assertPtyHostRunning(modernV3Host, modernV3ChildPID);
    for (const fixture of tmuxFixtures)
      await assertTmuxFixtureUnchanged(fixture, daemon, env);
    await secondAPI.stopAndAssert({
      requireGateCode: "startup_recovery_in_progress",
    });
    expect(await second.renderer.stopVisibleExactTextAudit()).toBe(0);
    phase("first cold restart continuity verified through reaper interval");

    await quitApp(second);
    await waitStopped(port);

    const thirdAPI = startSessionAPIObservation(
      port,
      expectedActivities,
      logs,
      "second cold restart",
    );
    apiObservers.push(thirdAPI);
    const thirdLogStart = logs.length;
    const third = await launchApp(env, logs, "Exited");
    apps.push(third);
    const thirdDaemon = await waitReady(runFile, port, third.appRunId);
    daemonPIDs.add(thirdDaemon.pid);
    await assertSupervisorListener(supervisorSocket);
    phase("second cold restart ready");
    await waitFor(
      async () =>
        (await third.renderer.hasVisibleExactText("Restart E2E")) || null,
      30_000,
    );
    await third.renderer.clickExactText("Restart E2E");
    for (const name of [
      "Chat Restart",
      "Native TUI",
      "Protocol v2 TUI",
      "Private Legacy TUI",
      "Named Legacy TUI",
      "Default Legacy TUI",
    ]) {
      await waitFor(
        async () => (await third.renderer.hasVisibleExactText(name)) || null,
        30_000,
      );
    }
    await assertNoVisibleExited(third.renderer);
    const afterThird = sqliteRows(db);
    const chatThird = afterThird.find((row) => row.id === chat.id)!;
    const modernThird = afterThird.find((row) => row.id === modernTUI.id)!;
    const protocolV2Third = afterThird.find(
      (row) => row.id === protocolV2TUI.id,
    )!;
    const legacyThird = afterThird.find((row) => row.id === legacyTUI.id)!;
    const namedLegacyThird = afterThird.find(
      (row) => row.id === namedLegacyTUI.id,
    )!;
    const defaultLegacyThird = afterThird.find(
      (row) => row.id === defaultLegacyTUI.id,
    )!;
    expect(chatThird.activity_state).toBe(expectedChatActivity);
    expect(chatThird.controller_generation).not.toBe(
      chatSecond.controller_generation,
    );
    expect(chatThird.provider_conversation_id).toBe(
      chatBefore.provider_conversation_id,
    );
    expect(modernThird.activity_state).not.toBe("exited");
    expect(modernThird.runtime_handle_id).toBe(modernSecond.runtime_handle_id);
    expect(protocolV2Third.activity_state).not.toBe("exited");
    expect(protocolV2Third.runtime_handle_id).toBe(
      protocolV2Second.runtime_handle_id,
    );
    expect(protocolV2Third.runtime_launch_id).toBe(
      protocolV2Second.runtime_launch_id,
    );
    expect(protocolV2Third.agent_session_id_launch_id).toBe(
      protocolV2Second.agent_session_id_launch_id,
    );
    expect(legacyThird.runtime_handle_id).toBe(legacySecond.runtime_handle_id);
    expect(legacyThird.activity_state).not.toBe("exited");
    expect(legacyThird.agent_session_id).toBe(privateNativeSessionID);
    expect(legacyThird.agent_session_id_launch_id).toBe(privateLaunch);
    expect(namedLegacyThird.runtime_handle_id).toBe(
      namedLegacySecond.runtime_handle_id,
    );
    expect(namedLegacyThird.activity_state).not.toBe("exited");
    expect(namedLegacyThird.runtime_launch_id).toBe(namedLaunch);
    expect(namedLegacyThird.agent_session_id).toBe(namedNativeSessionID);
    expect(namedLegacyThird.agent_session_id_launch_id).toBe(namedLaunch);
    expect(defaultLegacyThird.runtime_handle_id).toBe(
      defaultLegacySecond.runtime_handle_id,
    );
    expect(defaultLegacyThird.activity_state).not.toBe("exited");
    expect(defaultLegacyThird.runtime_launch_id).toBe(defaultLiveLaunch);
    expect(defaultLegacyThird.agent_session_id).toBe(defaultNativeSessionID);
    expect(defaultLegacyThird.agent_session_id_launch_id).toBe(
      defaultLiveLaunch,
    );
    const chatHostThird = JSON.parse(
      await fs.readFile(chatDescriptorPath, "utf8"),
    ) as ChatHostDescriptor;
    expect(chatHostThird.pid).toBe(chatHostBefore.pid);
    await assertChatControllerAttached(chatHostThird);
    await assertPtyRegistryOwnership(registryPath, modernV3Host);
    await assertPtyHostRunning(modernV3Host, modernV3ChildPID);
    await typeAndObserveNativeTerminal(
      third.renderer,
      modernV3Host,
      modernThird.runtime_handle_id,
      root,
      "THIRD",
    );
    await assertPtyRegistryOwnership(registryPath, protocolV2Host.entry);
    await assertPtyHostRunning(protocolV2Host.entry, protocolV2Host.childPid);
    await typeAndObserveNativeTerminal(
      third.renderer,
      protocolV2Host.entry,
      protocolV2Third.runtime_handle_id,
      root,
      "V2_THIRD",
      "Protocol v2 TUI",
    );
    for (const fixture of tmuxFixtures) {
      await assertTmuxFixtureUnchanged(fixture, daemon, env);
      await typeAndObserveTmuxTerminal(
        third.renderer,
        root,
        `THIRD_${fixture.displayName}`,
        fixture.displayName,
      );
    }
    await new Promise((resolve) => setTimeout(resolve, 6_000));
    await assertNoVisibleExited(third.renderer);
    await assertPtyRegistryOwnership(registryPath, modernV3Host);
    await assertPtyHostRunning(modernV3Host, modernV3ChildPID);
    for (const fixture of tmuxFixtures)
      await assertTmuxFixtureUnchanged(fixture, daemon, env);
    await thirdAPI.stopAndAssert({
      requireGateCode: "startup_recovery_in_progress",
    });
    phase("second cold restart continuity verified through reaper interval");

    // Exercise the production relaunch path too. Each Electron launch rotates
    // AO_APP_RUN_ID and the memory-only browser-runtime credential, so the new app
    // must replace (not reuse) the prior daemon, then recover the same workloads
    // without exposing a stale Exited frame.
    await waitFor(
      async () =>
        logs
          .slice(thirdLogStart)
          .some((line) => line.includes("AO: supervisor-link: connected")) ||
        null,
      10_000,
    );
    const daemonBeforeHandoff = await readRunFile(runFile);
    expect(daemonBeforeHandoff).not.toBeNull();
    const fourthAPI = startSessionAPIObservation(
      port,
      expectedActivities,
      logs,
      "immediate daemon handoff",
    );
    apiObservers.push(fourthAPI);
    expect(await third.renderer.stopVisibleExactTextAudit()).toBe(0);
    await quitApp(third);
    const oldDaemonHealth = await api<{
      status: string;
      service: string;
      pid: number;
    }>(port, "/healthz");
    expect(oldDaemonHealth).toMatchObject({
      status: "ok",
      service: "agent-orchestrator-daemon",
      pid: daemonBeforeHandoff!.pid,
    });
    const fourth = await launchApp(env, logs, "Exited");
    apps.push(fourth);
    const daemonAfterHandoff = await waitReady(runFile, port, fourth.appRunId);
    daemonPIDs.add(daemonAfterHandoff.pid);
    await assertSupervisorListener(supervisorSocket);
    expect(daemonAfterHandoff.pid).not.toBe(daemonBeforeHandoff!.pid);
    await waitFor(
      async () =>
        (await fourth.renderer.hasVisibleExactText("Restart E2E")) || null,
      30_000,
    );
    await fourth.renderer.clickExactText("Restart E2E");
    for (const name of [
      "Chat Restart",
      "Native TUI",
      "Protocol v2 TUI",
      "Private Legacy TUI",
      "Named Legacy TUI",
      "Default Legacy TUI",
    ]) {
      await waitFor(
        async () => (await fourth.renderer.hasVisibleExactText(name)) || null,
        30_000,
      );
    }
    await new Promise((resolve) => setTimeout(resolve, 6_000));
    expect((await readRunFile(runFile))?.pid).toBe(daemonAfterHandoff.pid);
    await assertNoVisibleExited(fourth.renderer);
    const afterFourth = sqliteRows(db);
    const chatFourth = afterFourth.find((row) => row.id === chat.id)!;
    const modernFourth = afterFourth.find((row) => row.id === modernTUI.id)!;
    const protocolV2Fourth = afterFourth.find(
      (row) => row.id === protocolV2TUI.id,
    )!;
    const legacyFourth = afterFourth.find((row) => row.id === legacyTUI.id)!;
    const namedLegacyFourth = afterFourth.find(
      (row) => row.id === namedLegacyTUI.id,
    )!;
    const defaultLegacyFourth = afterFourth.find(
      (row) => row.id === defaultLegacyTUI.id,
    )!;
    expect(chatFourth.activity_state).toBe(expectedChatActivity);
    expect(chatFourth.controller_generation).not.toBe(
      chatThird.controller_generation,
    );
    expect(chatFourth.agent_session_id).toBe(chatThird.agent_session_id);
    expect(chatFourth.provider_conversation_id).toBe(
      chatBefore.provider_conversation_id,
    );
    expect(modernFourth.activity_state).not.toBe("exited");
    expect(modernFourth.runtime_handle_id).toBe(modernThird.runtime_handle_id);
    expect(protocolV2Fourth.activity_state).not.toBe("exited");
    expect(protocolV2Fourth.runtime_handle_id).toBe(
      protocolV2Third.runtime_handle_id,
    );
    expect(protocolV2Fourth.runtime_launch_id).toBe(
      protocolV2Third.runtime_launch_id,
    );
    expect(protocolV2Fourth.agent_session_id_launch_id).toBe(
      protocolV2Third.agent_session_id_launch_id,
    );
    expect(legacyFourth.activity_state).not.toBe("exited");
    expect(legacyFourth.runtime_handle_id).toBe(legacyThird.runtime_handle_id);
    expect(legacyFourth.agent_session_id).toBe(privateNativeSessionID);
    expect(legacyFourth.agent_session_id_launch_id).toBe(privateLaunch);
    expect(namedLegacyFourth.activity_state).not.toBe("exited");
    expect(namedLegacyFourth.runtime_handle_id).toBe(
      namedLegacyThird.runtime_handle_id,
    );
    expect(namedLegacyFourth.runtime_launch_id).toBe(namedLaunch);
    expect(namedLegacyFourth.agent_session_id).toBe(namedNativeSessionID);
    expect(namedLegacyFourth.agent_session_id_launch_id).toBe(namedLaunch);
    expect(defaultLegacyFourth.activity_state).not.toBe("exited");
    expect(defaultLegacyFourth.runtime_handle_id).toBe(
      defaultLegacyThird.runtime_handle_id,
    );
    expect(defaultLegacyFourth.runtime_launch_id).toBe(defaultLiveLaunch);
    expect(defaultLegacyFourth.agent_session_id).toBe(defaultNativeSessionID);
    expect(defaultLegacyFourth.agent_session_id_launch_id).toBe(
      defaultLiveLaunch,
    );
    const chatHostFourth = JSON.parse(
      await fs.readFile(chatDescriptorPath, "utf8"),
    ) as ChatHostDescriptor;
    expect(chatHostFourth.pid).toBe(chatHostBefore.pid);
    await assertChatControllerAttached(chatHostFourth);
    await assertPtyRegistryOwnership(registryPath, modernV3Host);
    await assertPtyHostRunning(modernV3Host, modernV3ChildPID);
    await assertPtyRegistryOwnership(registryPath, protocolV2Host.entry);
    await assertPtyHostRunning(protocolV2Host.entry, protocolV2Host.childPid);
    for (const fixture of tmuxFixtures) {
      await assertTmuxFixtureUnchanged(fixture, daemon, env);
    }
    // Re-prove the original #4458/default cohort remains attachable after the
    // supervisor handoff, not merely present in SQLite/tmux metadata.
    await typeAndObserveTmuxTerminal(
      fourth.renderer,
      root,
      "FOURTH_DEFAULT",
      "Default Legacy TUI",
    );
    await fourthAPI.stopAndAssert({
      requireGateCode: "startup_recovery_in_progress",
    });
    expect(await fourth.renderer.stopVisibleExactTextAudit()).toBe(0);
    phase(
      "immediate app handoff safely replaced the daemon and recovered every workload",
    );

    await quitApp(fourth);
    await waitStopped(port);

    // Negative control: a bare legacy handle with two exact AO-owned workloads
    // is genuinely ambiguous. Startup must stay unready, keep the board covered,
    // and leave both workloads and the durable row untouched.
    expect(
      await stopFixtureTmuxSession(
        systemTmux,
        ["-L", "default"],
        legacyTUI.id,
        env,
        logs,
        "foreign",
      ),
    ).toBe(true);
    const duplicateFixture = await launchTmuxFixture({
      tmux: systemTmux,
      namespaceArgs: ["-L", "default"],
      displayName: "Ambiguous duplicate",
      sessionId: legacyTUI.id,
      launchId: privateLaunch,
      workspacePath: legacyBefore.workspace_path,
      daemon,
      runFile,
      env,
    });
    execFileSync("sqlite3", [
      db,
      `UPDATE sessions SET activity_state='active', runtime_handle_id=${sqlQuote(legacyTUI.id)} WHERE id=${sqlQuote(legacyTUI.id)};`,
    ]);
    const ambiguousBefore = sqliteRows(db).find(
      (row) => row.id === legacyTUI.id,
    )!;

    const fifthAPI = startSessionAPIObservation(
      port,
      expectedActivities,
      logs,
      "ambiguous startup",
    );
    apiObservers.push(fifthAPI);
    const fifth = await launchApp(env, logs, "Exited");
    apps.push(fifth);
    const failedDaemon = await waitStartupRecoveryFailure(
      runFile,
      port,
      fifth.appRunId,
    );
    daemonPIDs.add(failedDaemon.pid);
    await assertSupervisorListener(supervisorSocket);
    await waitFor(
      async () =>
        (await fifth.renderer.hasVisibleExactText("startup_recovery_failed")) ||
        null,
      30_000,
    );
    await waitFor(
      async () =>
        (await fifth.renderer.isTestIdVisible("daemon-startup-loader")) || null,
      30_000,
    );
    const recoveryLayers = await fifth.renderer.startupRecoveryLayers();
    expect(recoveryLayers).not.toBeNull();
    expect(recoveryLayers!.cover).toBeGreaterThan(recoveryLayers!.overlay);
    expect(recoveryLayers!.banner).toBeGreaterThan(recoveryLayers!.cover);
    const gatedSessions = await fetch(
      `http://127.0.0.1:${port}/api/v1/sessions`,
    );
    expect(gatedSessions.status).toBe(503);
    expect(await gatedSessions.text()).toContain("startup_recovery_failed");
    expect(
      await fifth.renderer.visibleExactTextCount("Private Legacy TUI"),
    ).toBe(0);
    await assertNoVisibleExited(fifth.renderer);
    const ambiguousAfter = sqliteRows(db).find(
      (row) => row.id === legacyTUI.id,
    )!;
    expect(ambiguousAfter).toEqual(ambiguousBefore);
    expect(
      await tmuxSessionNames(systemTmux, ["-L", "default"], env),
    ).toContain(legacyTUI.id);
    expect(
      await tmuxSessionNames(
        tmux,
        ["-S", historicalTarget.address, "-f", "/dev/null"],
        env,
      ),
    ).toContain(legacyTUI.id);
    for (const fixture of tmuxFixtures)
      await assertTmuxFixtureUnchanged(fixture, daemon, env);
    await assertTmuxFixtureUnchanged(duplicateFixture, daemon, env);
    await assertPtyRegistryOwnership(registryPath, modernV3Host);
    await assertPtyHostRunning(modernV3Host, modernV3ChildPID);
    await assertPtyRegistryOwnership(registryPath, protocolV2Host.entry);
    await assertPtyHostRunning(protocolV2Host.entry, protocolV2Host.childPid);
    await fifthAPI.stopAndAssert({
      requireReady: false,
      forbidReady: true,
      requireGateCode: "startup_recovery_failed",
    });
    expect(await fifth.renderer.stopVisibleExactTextAudit()).toBe(0);
    phase(
      "ambiguous ownership failed closed without exposing or mutating the board",
    );
    await quitApp(fifth);
    await waitStopped(port);
    scenarioCompleted = true;
  } finally {
    let cleanupFailure: Error | undefined;
    for (const observer of apiObservers)
      await observer.stop().catch(() => undefined);
    for (const app of apps.reverse()) await quitApp(app).catch(() => undefined);
    const currentDaemonClean = await stopFixtureDaemon(runFile, logs);
    const priorDaemonsClean = await proveFixturePIDsStopped(daemonPIDs, logs);
    const daemonClean = currentDaemonClean && priorDaemonsClean;
    const hostsClean = await cleanupDetachedHosts(
      root,
      dataDir,
      runFile,
      ptyFixtureExecutables,
      daemon,
      logs,
    );
    let tmuxClean = true;
    let historicalTmuxClean = true;
    for (const target of tmuxCleanupTargets) {
      const clean = await stopFixtureTmuxSession(
        target.tmux,
        target.namespaceArgs,
        target.sessionId,
        env,
        logs,
        target.label,
      );
      tmuxClean = tmuxClean && clean;
      if (target.namespaceArgs[0] === "-S")
        historicalTmuxClean = historicalTmuxClean && clean;
    }
    let tmuxDirClean = false;
    if (tmuxClean) {
      try {
        await fs.rm(tmuxTmp, { recursive: true, force: true });
        tmuxDirClean = true;
      } catch (error) {
        logs.push(`[cleanup] could not remove tmux temp dir: ${String(error)}`);
      }
    }
    let credentialClean = true;
    try {
      // Never preserve a copied login token with a failed fixture. The isolated
      // processes have already been stopped or positively refused above.
      await fs.rm(codexAuth, { force: true });
    } catch (error) {
      credentialClean = false;
      logs.push(
        `[cleanup] could not remove isolated Codex auth: ${String(error)}`,
      );
    }
    let aliasClean = historicalTarget.aliasDir === undefined;
    if (historicalTarget.aliasDir && historicalTmuxClean) {
      try {
        await fs.rm(historicalTarget.aliasDir, { force: true });
        aliasClean = true;
      } catch (error) {
        logs.push(
          `[cleanup] could not remove historical tmux alias: ${String(error)}`,
        );
      }
    }
    if (
      daemonClean &&
      hostsClean &&
      tmuxClean &&
      tmuxDirClean &&
      credentialClean &&
      aliasClean
    ) {
      try {
        await fs.rm(root, {
          recursive: true,
          force: true,
          maxRetries: 10,
          retryDelay: 100,
        });
      } catch (error) {
        logs.push(`[cleanup] remove ${root}: ${String(error)}`);
        if (scenarioCompleted)
          cleanupFailure =
            error instanceof Error ? error : new Error(String(error));
      }
    } else {
      logs.push(
        `[cleanup] preserved isolated fixture root ${root}: one or more fixture owners could not be proven stopped`,
      );
      if (scenarioCompleted) {
        cleanupFailure = new Error(
          `fixture cleanup could not prove all owners stopped; preserved ${root}`,
        );
      }
    }
    await testInfo.attach("native-app-logs", {
      body: logs.join("\n"),
      contentType: "text/plain",
    });
    if (cleanupFailure) throw cleanupFailure;
  }
});
