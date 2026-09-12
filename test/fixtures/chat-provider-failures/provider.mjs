#!/usr/bin/env node
// Manual desktop fault injection only. Never contacts a provider or reads credentials.
// Expose this as `codex` on a test-only PATH, or pass --acp via AO_CLAUDE_ACP_COMMAND.
import { createInterface } from "node:readline";
import { randomUUID } from "node:crypto";

const acp = process.argv.includes("--acp");
if (process.argv.includes("--version")) {
	console.log("codex-cli 0.146.0");
	process.exit(0);
}
if (process.argv.includes("status")) {
	console.log(process.argv.includes("--json") ? JSON.stringify({ loggedIn: true, authMethod: "oauth" }) : "Logged in using ChatGPT");
	process.exit(0);
}
const thread = { id: randomUUID(), turns: [] };
const send = (value) => process.stdout.write(`${JSON.stringify({ jsonrpc: "2.0", ...value })}\n`);
const notify = (method, params) => send({ method, params });
const reply = (id, result = {}) => send({ id, result });
const meta = (failure) => ({ jetbrains: { air: { version: 1, sessionFailure: failure } } });
let pending;

function finish(text, id, turn) {
	const success = text.includes("success");
	const auth = text.includes("auth");
	const title = auth ? "Login expired (simulated)" : "Usage limit reached (simulated)";
	const details = auth ? "Sign in to your provider to continue." : "No credits remain. Manage billing at https://example.com/billing.";
	if (acp) {
		if (success) notify("session/update", { sessionId: thread.id, update: { sessionUpdate: "agent_message_chunk", content: { type: "text", text: "Recovered successfully. This was a scripted provider response." } } });
		reply(id, { stopReason: "end_turn", ...(success ? {} : { _meta: meta({ id: "incident-1", severity: "error", title, details, actions: auth ? ["login"] : [] }) }) });
	} else {
		turn.status = success ? "completed" : "failed";
		if (success) {
			const item = { type: "agentMessage", id: randomUUID(), text: "Recovered successfully. This was a scripted provider response." };
			turn.items.push(item);
			notify("item/completed", { threadId: thread.id, turnId: turn.id, item });
		} else {
			turn.error = { message: title, additionalDetails: details, ...(auth ? { codexErrorInfo: "unauthorized" } : {}) };
			notify("error", { threadId: thread.id, turnId: turn.id, willRetry: false, error: turn.error });
		}
		notify("turn/completed", { threadId: thread.id, turn });
	}
	pending = undefined;
}

for await (const line of createInterface({ input: process.stdin })) {
	const { id, method, params = {} } = JSON.parse(line);
	if (id === undefined && method !== "session/cancel") continue;
	switch (method) {
		case "initialize":
			reply(id, acp ? { protocolVersion: 1, agentCapabilities: { loadSession: true }, authMethods: [], agentInfo: { name: "AO fault injection", version: "1" } } : { userAgent: "ao-fault-injection/1" });
			break;
		case "session/new": case "session/load":
			reply(id, { sessionId: thread.id });
			break;
		case "thread/start": case "thread/resume": case "thread/read":
			reply(id, { thread, model: "gpt-test", cwd: process.cwd() });
			break;
		case "model/list":
			reply(id, { data: [{ id: "gpt-test", model: "gpt-test", displayName: "Fault-injection provider", isDefault: true }], nextCursor: null });
			break;
		case "account/read":
			reply(id, { account: { type: "apiKey" }, requiresOpenaiAuth: false });
			break;
		case "turn/start": case "session/prompt": {
			const text = (params.input ?? params.prompt ?? []).map((part) => part.text ?? "").join(" ");
			const turn = { id: randomUUID(), status: "inProgress", items: [] };
			thread.turns.push(turn);
			if (acp) {
				notify("session/update", { sessionId: thread.id, update: { sessionUpdate: "session_info_update", _meta: meta({ id: "incident-1", severity: "warning", title: "Reconnecting 1/5 (simulated)", details: "The fixture will settle this turn shortly." }) } });
			} else {
				reply(id, { turn });
				notify("turn/started", { threadId: thread.id, turn });
				 notify("error", { threadId: thread.id, turnId: turn.id, willRetry: true, error: { message: "Reconnecting 1/5 (simulated)" } });
			}
			if (text.includes("episodes")) {
				// Recovery ends A; B must not overwrite its historical warning.
				if (acp) {
					notify("session/update", { sessionId: thread.id, update: { sessionUpdate: "agent_message_chunk", content: { type: "text", text: "Recovered first episode." } } });
					notify("session/update", { sessionId: thread.id, update: { sessionUpdate: "session_info_update", _meta: meta({ id: "incident-1", severity: "warning", title: "Second retry episode (simulated)" }) } });
				} else {
					notify("item/completed", { threadId: thread.id, turnId: turn.id, item: { type: "agentMessage", id: randomUUID(), text: "Recovered first episode." } });
					notify("error", { threadId: thread.id, turnId: turn.id, willRetry: true, error: { message: "Second retry episode (simulated)" } });
				}
			}
			pending = { id, turn, timer: setTimeout(() => finish(text, id, turn), 12000) };
			break;
		}
		case "session/cancel": case "turn/interrupt":
			if (pending) {
				clearTimeout(pending.timer);
				if (acp) reply(pending.id, { stopReason: "cancelled" });
				else notify("turn/completed", { threadId: thread.id, turn: { ...pending.turn, status: "interrupted" } });
				pending = undefined;
			}
			if (id !== undefined) reply(id);
			break;
		case "skills/list": case "mcpServerStatus/list":
			reply(id, { data: [] });
			break;
		default:
			reply(id);
	}
}
