import { randomUUID } from "node:crypto";
import { mkdir, readFile, rm } from "node:fs/promises";
import path from "node:path";
import { createAgentDeviceClient, normalizeAgentDeviceError } from "agent-device";

const MAX_STDIN_BYTES = 64 * 1024;
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
const selection = { ...(session ? { session } : {}), ...(platform ? { platform } : {}), ...(device ? { device } : {}) };

try {
	let result;
	switch (request.action) {
		case "list":
			result = await client.devices.list(platform ? { platform } : undefined);
			break;
		case "attach": {
			await client.devices.boot(selection);
			try {
				await client.apps.open({ ...selection, foreground: true });
			} catch {
				await client.apps.open({
					...selection,
					app: platform === "ios" ? "com.apple.Preferences" : "com.android.settings",
					foreground: true,
				});
			}
			result = { attached: true };
			break;
		}
		case "detach":
			await client.sessions.close({ session });
			result = { detached: true };
			break;
		case "shutdown":
			await client.devices.shutdown(selection);
			result = { shutdown: true };
			break;
		case "ui-tree":
		case "snapshot": {
			const snapshot = await client.capture.snapshot({
				...selection,
				interactiveOnly: request.interactiveOnly === true,
				forceFull: true,
				timeoutMs: 15_000,
			});
			result = safeSnapshot(snapshot);
			break;
		}
		case "screenshot": {
			const file = path.join(stateDir, `ao-capture-${randomUUID()}.png`);
			try {
				await client.capture.screenshot({ ...selection, path: file, stabilize: false });
				const bytes = await readFile(file);
				result = { pngBase64: bytes.toString("base64"), size: bytes.length };
			} finally {
				await rm(file, { force: true });
			}
			break;
		}
		case "tap":
			await client.interactions.press({ ...selection, ...target(request), verify: true });
			result = { completed: true };
			break;
		case "swipe":
			await client.interactions.swipe({
				...selection,
				from: { x: integer(request.x1, "x1"), y: integer(request.y1, "y1") },
				to: { x: integer(request.x2, "x2"), y: integer(request.y2, "y2") },
			});
			result = { completed: true };
			break;
		case "fill":
			await client.interactions.fill({ ...selection, ...target(request), text: boundedText(request.text), verify: true });
			result = { completed: true };
			break;
		case "type":
			await client.interactions.type({ ...selection, text: boundedText(request.text) });
			result = { completed: true };
			break;
		case "key": {
			const key = String(request.key ?? "").toLowerCase();
			if (key !== "enter" && key !== "return") throw Object.assign(new Error("Only Enter and Return are supported"), { code: "UNSUPPORTED_OPERATION" });
			await client.command.keyboard({ ...selection, action: key });
			result = { completed: true };
			break;
		}
		case "back":
			await client.command.back(selection);
			result = { completed: true };
			break;
		case "home":
			await client.command.home(selection);
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
