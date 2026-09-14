import { randomUUID } from "node:crypto";
import { execFile } from "node:child_process";
import { mkdir, readFile, rm } from "node:fs/promises";
import path from "node:path";
import { promisify } from "node:util";
import { createAgentDeviceClient, normalizeAgentDeviceError } from "agent-device";

const MAX_STDIN_BYTES = 64 * 1024;
const execFileAsync = promisify(execFile);
const chunks = [];
let size = 0;
for await (const chunk of process.stdin) {
	size += chunk.length;
	if (size > MAX_STDIN_BYTES) throw new Error("request exceeds 64 KiB");
	chunks.push(chunk);
}

const request = JSON.parse(Buffer.concat(chunks).toString("utf8"));
const stateDir = process.env.AGENT_DEVICE_STATE_DIR;
if (!stateDir) throw new Error("AGENT_DEVICE_STATE_DIR is required");
await mkdir(stateDir, { recursive: true, mode: 0o700 });

const session = String(request.session ?? "").trim();
const platform = request.platform === "ios" || request.platform === "android" ? request.platform : undefined;
const device = typeof request.deviceId === "string" && request.deviceId.trim() ? request.deviceId.trim() : undefined;
const client = createAgentDeviceClient({ stateDir, session: session || undefined, responseLevel: "default" });
const sessionSelection = { ...(session ? { session } : {}) };
// Stopped Android AVDs are selected by name, while running Android emulators
// expose an adb serial. Apple inventory IDs are UDIDs. Once an app is opened,
// the session itself is the authoritative target for captures/interactions.
const targetSelection = {
	...(session ? { session } : {}),
	...(platform ? { platform } : {}),
	...(device && platform === "ios" ? { udid: device } : {}),
	...(device && platform === "android" && /^emulator-\d+$/.test(device) ? { serial: device } : {}),
	...(device && platform === "android" && !/^emulator-\d+$/.test(device) ? { device } : {}),
};

try {
	let result;
	switch (request.action) {
		case "list":
			result = await client.devices.list(platform ? { platform } : undefined);
			break;
		case "attach": {
			// AO renders the device through its embedded live stream. Keep platform
			// boot in the background so no second native window opens outside AO.
			if (platform === "ios" && device) {
				// simctl bootstatus is the authoritative readiness fence. Calling the
				// agent-device boot path again after it succeeds can race CoreSimulator
				// and leave a newly selected simulator waiting until AO's timeout.
				await bootIOSInBackground(device);
				result = { attached: true, deviceId: device };
			} else {
				const booted = await client.devices.boot({ ...targetSelection, headless: true });
				// A stopped Android AVD is selected by name, but serve-emu addresses
				// the running emulator by its adb serial (for example emulator-5554).
				// Return the runtime identifier so AO routes both video and input to
				// the device that was actually booted.
				result = { attached: true, deviceId: booted.id };
			}
			// Opening the human-facing device panel must not start an XCTest app.
			// Agent automation establishes its richer session lazily on first use.
			break;
		}
		case "detach":
			try {
				await client.sessions.close({ session });
			} catch (error) {
				if (normalizeAgentDeviceError(error).code !== "SESSION_NOT_FOUND") throw error;
			}
			result = { detached: true };
			break;
		case "shutdown":
			await client.devices.shutdown(targetSelection);
			result = { shutdown: true };
			break;
		case "ui-tree":
		case "snapshot": {
			const snapshot = await withAgentSession(() => client.capture.snapshot({
				...sessionSelection,
				interactiveOnly: request.interactiveOnly === true,
				forceFull: true,
				timeoutMs: 15_000,
			}));
			result = safeSnapshot(snapshot);
			break;
		}
		case "screenshot": {
			const file = path.join(stateDir, `ao-capture-${randomUUID()}.png`);
			try {
				if (platform === "ios" && device) {
					// Apple exposes screen capture directly through CoreSimulator. This is
					// substantially faster than routing every display refresh through the
					// XCTest automation session and does not foreground its helper app.
					await execFileAsync("xcrun", ["simctl", "io", device, "screenshot", file], {
						timeout: 15_000,
						maxBuffer: 1024 * 1024,
					});
				} else {
					// The agent-device screenshot API intentionally accepts a session, not
					// device selectors. Passing platform/device is rejected as INVALID_ARGS.
					// Keep the source PNG at native resolution. The CLI reports those native
					// dimensions so agents can compensate if their image viewer scales it.
					await client.capture.screenshot({ ...sessionSelection, path: file, scale: 1, stabilize: false });
				}
				const bytes = await readFile(file);
				result = { pngBase64: bytes.toString("base64"), size: bytes.length };
			} finally {
				await rm(file, { force: true });
			}
			break;
		}
		case "launch": {
			const app = boundedText(request.text).trim();
			if (!app) throw Object.assign(new Error("launch requires an app name or bundle identifier"), { code: "INVALID_ARGS" });
			const opened = await client.apps.open({ ...targetSelection, app });
			result = {
				completed: true,
				...(typeof opened.appName === "string" ? { appName: opened.appName } : {}),
				...(typeof opened.appBundleId === "string" ? { appBundleId: opened.appBundleId } : {}),
			};
			break;
		}
		case "tap":
			// AO immediately captures the resulting frame itself. agent-device's
			// optional verification performs another accessibility capture and makes
			// direct manipulation especially slow on iOS.
			await withAgentSession(() => client.interactions.press({ ...sessionSelection, ...target(request), verify: false }));
			result = { completed: true };
			break;
		case "swipe":
			await withAgentSession(() => client.interactions.swipe({
				...sessionSelection,
				from: { x: integer(request.x1, "x1"), y: integer(request.y1, "y1") },
				to: { x: integer(request.x2, "x2"), y: integer(request.y2, "y2") },
			}));
			result = { completed: true };
			break;
		case "fill":
			await withAgentSession(() => client.interactions.fill({ ...sessionSelection, ...target(request), text: boundedText(request.text), verify: false }));
			result = { completed: true };
			break;
		case "type":
			await withAgentSession(() => client.interactions.type({ ...sessionSelection, text: boundedText(request.text) }));
			result = { completed: true };
			break;
		case "key": {
			const key = String(request.key ?? "").toLowerCase();
			if (key !== "enter" && key !== "return") throw Object.assign(new Error("Only Enter and Return are supported"), { code: "UNSUPPORTED_OPERATION" });
			await withAgentSession(() => client.command.keyboard({ ...sessionSelection, action: key }));
			result = { completed: true };
			break;
		}
		case "back":
			await withAgentSession(() => client.command.back(sessionSelection));
			result = { completed: true };
			break;
		case "home":
			await withAgentSession(() => client.command.home(sessionSelection));
			result = { completed: true };
			break;
		default:
			throw Object.assign(new Error("Unsupported AO device action"), { code: "UNSUPPORTED_OPERATION" });
	}
	process.stdout.write(`${JSON.stringify({ ok: true, result })}\n`);
} catch (error) {
	const normalized = normalizeAgentDeviceError(error);
	process.stdout.write(`${JSON.stringify({
		ok: false,
		error: { code: normalized.code, message: normalized.message, hint: normalized.hint },
	})}\n`);
	process.exitCode = 1;
}

async function withAgentSession(operation) {
	try {
		return await operation();
	} catch (error) {
		if (normalizeAgentDeviceError(error).code !== "SESSION_NOT_FOUND") throw error;
		await client.apps.open(targetSelection);
		// The XCTest host only establishes the private automation channel. Keep
		// the screen on SpringBoard before running the requested agent action.
		if (platform === "ios") await client.command.home(sessionSelection);
		return operation();
	}
}

async function bootIOSInBackground(udid) {
	try {
		await execFileAsync("xcrun", ["simctl", "boot", udid], { timeout: 120_000, maxBuffer: 1024 * 1024 });
	} catch (error) {
		const output = `${error?.stdout ?? ""}\n${error?.stderr ?? ""}`.toLowerCase();
		if (!output.includes("current state: booted") && !output.includes("already booted")) throw error;
	}
	await execFileAsync("xcrun", ["simctl", "bootstatus", udid, "-b"], { timeout: 120_000, maxBuffer: 1024 * 1024 });
}

function target(value) {
	if (typeof value.ref === "string" && value.ref.trim()) return { ref: value.ref.trim() };
	return { x: integer(value.x, "x"), y: integer(value.y, "y") };
}

function integer(value, name) {
	if (!Number.isInteger(value) || value < 0 || value > 100_000) throw new Error(`${name} must be an integer between 0 and 100000`);
	return value;
}

function boundedText(value) {
	if (typeof value !== "string" || value.length > 4096) throw new Error("text must be at most 4096 characters");
	return value;
}

function safeSnapshot(snapshot) {
	return {
		nodes: Array.isArray(snapshot.nodes) ? snapshot.nodes : [],
		...(snapshot.truncated === undefined ? {} : { truncated: snapshot.truncated === true }),
		...(typeof snapshot.appName === "string" ? { appName: snapshot.appName } : {}),
		...(typeof snapshot.appBundleId === "string" ? { appBundleId: snapshot.appBundleId } : {}),
		...(snapshot.refsGeneration === undefined ? {} : { refsGeneration: snapshot.refsGeneration }),
	};
}
