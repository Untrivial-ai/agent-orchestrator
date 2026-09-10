import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@react-native-async-storage/async-storage", () => ({ default: { getItem: vi.fn(), setItem: vi.fn(), removeItem: vi.fn() } }));
vi.mock("expo-secure-store", () => ({ getItemAsync: vi.fn(), setItemAsync: vi.fn(), deleteItemAsync: vi.fn() }));
vi.mock("expo/fetch", () => ({ fetch: vi.fn() }));

import { fetch as expoFetch } from "expo/fetch";
import { ApiError, apiRequest, delegateTask, getAgentModels, getPreview, getSessions, getSettings, launchOrchestrator, mobileReachablePreviewURL, restoreSession, resumeSessionAgent, spawnSession } from "./api";
import * as chatApi from "./chat/api";
import type { ServerConfig } from "./config";

const {
	acknowledgeSessionInterfaceTransitionNotice,
	getConversationPage,
	getWorkspacePaths,
} = chatApi;

const cfg: ServerConfig = { host: "ao.test", httpPort: "3011", muxPort: "3011", secure: false, password: "secret12" };

describe("mobile Chat API boundaries", () => {
	beforeEach(() => vi.stubGlobal("fetch", vi.fn()));
	afterEach(() => vi.unstubAllGlobals());

	it("explicitly creates workers in Chat mode by default", async () => {
		vi.mocked(fetch).mockResolvedValue(response({ session: { id: "w-1", projectId: "p-1", mode: "chat" } }, 201));
		const session = await spawnSession(cfg, { projectId: "p-1", harness: "codex" });
		const [, init] = vi.mocked(fetch).mock.calls[0];
		expect(JSON.parse(String(init?.body))).toMatchObject({ projectId: "p-1", harness: "codex", kind: "worker", mode: "chat" });
		expect(session.mode).toBe("chat");
		expect(init?.headers).toMatchObject({ Authorization: "Bearer secret12" });
	});

	it("loads a project-scoped model catalog for the selected agent", async () => {
		vi.mocked(fetch).mockResolvedValue(response({
			agentId: "codex", selectionMode: "catalog", allowCustom: true,
			models: [{ id: "gpt-5", label: "GPT-5", isDefault: true }],
			source: "codex", stale: false, fetchedAt: "2026-08-09T00:00:00Z",
		}));
		const catalog = await getAgentModels(cfg, "codex", "project one");
		expect(vi.mocked(fetch).mock.calls[0]?.[0]).toBe("http://ao.test:3011/api/v1/agents/codex/models?projectId=project%20one");
		expect(catalog.models[0]).toMatchObject({ id: "gpt-5", isDefault: true });
	});

	it("preserves daemon pin facts used by the Agents ordering", async () => {
		vi.mocked(fetch)
			.mockResolvedValueOnce(response({ sessions: [{ id: "w-1", projectId: "p-1", mode: "chat", activity: { state: "idle", lastActivityAt: "2026-08-08T10:00:00Z" }, updatedAt: "2026-08-09T10:00:00Z", isPinned: true, pinnedAt: "2026-08-09T10:00:00Z" }] }))
			.mockResolvedValueOnce(response({ sessions: [] }))
			.mockResolvedValueOnce(response({ projects: [] }));
		const result = await getSessions(cfg);
		expect(result.sessions[0]).toMatchObject({ isPinned: true, pinnedAt: "2026-08-09T10:00:00Z", lastActivityAt: "2026-08-08T10:00:00Z" });
	});

	it("preserves the daemon terminal handle used to attach native macOS PTYs", async () => {
		vi.mocked(fetch)
			.mockResolvedValueOnce(response({
				sessions: [{
					id: "w-1",
					projectId: "p-1",
					mode: "tui",
					terminalHandleId: "ptyhost-v1:w-1",
				}],
			}))
			.mockResolvedValueOnce(response({ sessions: [] }))
			.mockResolvedValueOnce(response({ projects: [] }));

		const result = await getSessions(cfg);

		expect(result.sessions[0]).toMatchObject({
			id: "w-1",
			terminalHandleId: "ptyhost-v1:w-1",
		});
	});

	it("delegates an optional empty task with explicit interface and model", async () => {
		vi.mocked(fetch)
			.mockResolvedValueOnce(response({ ok: true, workerId: "w-2" }, 202))
			.mockResolvedValueOnce(response({ session: { id: "w-2", projectId: "p-1", harness: "codex", mode: "chat" } }));
		const session = await delegateTask(cfg, { projectId: "p-1", brief: "", agent: "codex", model: "gpt-5", mode: "chat" });
		const [url, init] = vi.mocked(fetch).mock.calls[0];
		expect(url).toBe("http://ao.test:3011/api/v1/orchestrators/delegate");
		expect(JSON.parse(String(init?.body))).toEqual({ projectId: "p-1", brief: "", agent: "codex", model: "gpt-5", mode: "chat" });
		expect(session).toMatchObject({ id: "w-2", projectId: "p-1", mode: "chat" });
	});

	it("keeps an explicit TUI orchestrator request explicit", async () => {
		vi.mocked(fetch).mockResolvedValue(response({ orchestrator: { id: "o-1", projectId: "p-1", mode: "tui" } }, 201));
		const orchestrator = await launchOrchestrator(cfg, "p-1", true, "tui");
		const [, init] = vi.mocked(fetch).mock.calls[0];
		expect(JSON.parse(String(init?.body))).toMatchObject({ projectId: "p-1", clean: true, mode: "tui" });
		expect(orchestrator.mode).toBe("tui");
	});

	it("resumes a stopped Chat controller without restoring the AO session", async () => {
		vi.mocked(fetch).mockResolvedValue(response({ ok: true }));
		await resumeSessionAgent(cfg, "chat-1");
		const [url, init] = vi.mocked(fetch).mock.calls[0];
		expect(url).toBe("http://ao.test:3011/api/v1/sessions/chat-1/resume-agent");
		expect(init?.method).toBe("POST");
	});

	it("keeps terminated-session restoration as a distinct lifecycle operation", async () => {
		vi.mocked(fetch).mockResolvedValue(response({ ok: true }));
		await restoreSession(cfg, "chat-terminated");
		const [url, init] = vi.mocked(fetch).mock.calls[0];
		expect(url).toBe("http://ao.test:3011/api/v1/sessions/chat-terminated/restore");
		expect(init?.method).toBe("POST");
	});

	it("acknowledges the exact durable interface-transition notice", async () => {
		vi.mocked(fetch).mockResolvedValue(
			response({
				transition: {
					id: "transition/1",
					sessionId: "chat/1",
					sourceMode: "chat",
					targetMode: "tui",
					policy: "drain",
					phase: "recovery_required",
					createdAt: "2026-08-12T10:00:00Z",
					updatedAt: "2026-08-12T10:01:00Z",
					noticeAcknowledgedAt: "2026-08-13T08:00:00Z",
				},
			}),
		);
		const transition = await acknowledgeSessionInterfaceTransitionNotice(
			cfg,
			"chat/1",
			"transition/1",
		);
		const [url, init] = vi.mocked(fetch).mock.calls[0];
		expect(url).toBe(
			"http://ao.test:3011/api/v1/sessions/chat%2F1/interface-transition/transition%2F1/notice-acknowledgement",
		);
		expect(init?.method).toBe("PUT");
		expect(transition.noticeAcknowledgedAt).toBe("2026-08-13T08:00:00Z");
	});

	it("preserves daemon request IDs on API errors", async () => {
		vi.mocked(fetch).mockResolvedValue(response({
			error: "conflict",
			code: "SWITCH_IN_PROGRESS",
			message: "A controller switch is already running.",
			requestId: "request-mobile-1",
		}, 409));
		let thrown: unknown;
		try {
			await apiRequest(cfg, "/api/v1/sessions/w-1/interface-transition", { method: "POST" });
		} catch (error) {
			thrown = error;
		}
		expect(thrown).toBeInstanceOf(ApiError);
		expect(thrown).toMatchObject({
			status: 409,
			code: "SWITCH_IN_PROGRESS",
			requestId: "request-mobile-1",
		});
	});

	it("uses daemon-advertised Chat harnesses and preserves workspace truncation", async () => {
		vi.mocked(fetch)
			.mockResolvedValueOnce(response({ defaultSessionMode: "tui", chatHarnesses: ["codex", "claude-code", 42] }))
			.mockResolvedValueOnce(response({ files: [{ path: "src/app.ts", status: "modified" }, { path: "old.ts", status: "deleted" }], truncated: true }));
		expect(await getSettings(cfg)).toEqual({ defaultSessionMode: "tui", chatHarnesses: ["codex", "claude-code"] });
		expect(await getWorkspacePaths(cfg, "w-1")).toEqual({ paths: ["src/app.ts"], truncated: true });
	});

	it("keeps explicit README previews and never authenticates external preview origins", async () => {
		vi.mocked(fetch).mockResolvedValueOnce(response({ entry: "README.md" }));
		expect(await getPreview(cfg, "w-1")).toEqual({
			entry: "README.md",
			url: "http://ao.test:3011/api/v1/sessions/w-1/preview/files/README.md",
			authenticated: true,
		});
		vi.mocked(fetch).mockResolvedValueOnce(response({}));
		expect(await getPreview(cfg, "w-1", "https://example.com/demo")).toMatchObject({
			url: "https://example.com/demo",
			authenticated: false,
		});
		expect(mobileReachablePreviewURL("http://127.0.0.1:5173", "ao.test")?.href).toBe("http://ao.test:5173/");
		expect(mobileReachablePreviewURL("http://localhost:5173", "2001:db8::5")?.href).toBe("http://[2001:db8::5]:5173/");
		expect(mobileReachablePreviewURL("http://127.0.0.1:5173", "https://macbook.local/")?.href).toBe("http://macbook.local:5173/");
		expect(mobileReachablePreviewURL("file:///tmp/demo.html", "ao.test")).toBeUndefined();
	});

	it("maps the provider-neutral conversation wire model without inventing protocol state", async () => {
		vi.mocked(fetch).mockResolvedValue(response({
			conversationId: "c-1", sessionId: "w-1", harness: "claude-code", mode: "chat", controller: "busy",
			latestSequence: 2, oldestSequence: 1, hasMoreBefore: false, settings: {}, turns: [], messages: [],
			capabilities: ["config_options", "steer"],
			activities: [{ kind: "activity", id: "a-1", sequence: 2, revision: 1, activityKind: "approval", status: "pending", summary: "Run command", requestId: "req-1", detail: { output: { text: "legacy" }, decisions: [{ id: "accept" }] }, createdAt: "2026-08-05T00:00:00Z" }],
		}));
		const page = await getConversationPage(cfg, "w-1");
		expect(page.controller).toEqual({ state: "busy" });
		expect(page.capabilities).toEqual(["config_options", "steer"]);
		expect(page.items[0]).toMatchObject({ activityKind: "approval", requestId: "req-1", decisions: [{ id: "accept", label: "accept" }], detail: { output: { text: "legacy" } } });
	});

	it("keeps the first Chat payload small while allowing larger history pages", async () => {
		const wire = {
			conversationId: "c-1", sessionId: "w-1", harness: "codex", mode: "chat", controller: "idle",
			latestSequence: 0, oldestSequence: 0, hasMoreBefore: false, settings: {}, turns: [], messages: [], activities: [],
		};
		vi.mocked(fetch)
			.mockResolvedValueOnce(response(wire))
			.mockResolvedValueOnce(response(wire));

		await getConversationPage(cfg, "w-1");
		await getConversationPage(cfg, "w-1", 100);

		expect(vi.mocked(fetch).mock.calls.map(([url]) => url)).toEqual([
			"http://ao.test:3011/api/v1/sessions/w-1/conversation?limit=50",
			"http://ao.test:3011/api/v1/sessions/w-1/conversation?limit=200&beforeSequence=100",
		]);
	});

	it("delivers the global event stream without filtering out other sessions", async () => {
		vi.mocked(expoFetch).mockResolvedValue(new Response([
			'id: 1\ndata: {"seq":1,"projectId":"p-1","sessionId":"w-1","type":"session_updated","payload":{"conversationId":"c-1"},"createdAt":"2026-08-11"}\n\n',
			'id: 2\ndata: {"seq":2,"projectId":"p-1","sessionId":"w-2","type":"session_updated","payload":{"conversationId":"c-2"},"createdAt":"2026-08-11"}\n\n',
		].join("")) as unknown as Awaited<ReturnType<typeof expoFetch>>);
		const streamGlobalEvents = (
			chatApi as unknown as {
				streamGlobalConversationEvents?: (
					cfg: ServerConfig,
					after: number,
					signal: AbortSignal,
					onEvent: (event: { sessionId?: string }) => void,
				) => Promise<number>;
			}
		).streamGlobalConversationEvents;
		const sessions: string[] = [];

		const cursor = await streamGlobalEvents?.(cfg, 0, new AbortController().signal, (event) => {
			if (event.sessionId) sessions.push(event.sessionId);
		});

		expect(sessions).toEqual(["w-1", "w-2"]);
		expect(cursor).toBe(2);
	});

	// A cold-start replay against a busy daemon is ~200k events. Draining it in one
	// unbroken run starves the JS thread, which is also what services touches — the
	// board renders but ignores taps until the OS or the renderer gives up. The reader
	// must therefore hand the event loop back periodically, no matter how much is queued.
	it("yields to the event loop while draining a large replay", async () => {
		vi.mocked(expoFetch).mockResolvedValue(new Response(
			replayFrames(300),
		) as unknown as Awaited<ReturnType<typeof expoFetch>>);

		const streaming = chatApi.streamGlobalConversationEvents(
			cfg, 0, new AbortController().signal, () => {}, undefined, { yieldEvery: 50 },
		);
		// Queued after the read loop starts. It can only run if the loop yields a
		// macrotask mid-drain; a loop that only awaits already-resolved promises stays
		// entirely in the microtask queue and starves this.
		let eventLoopGotATurn = false;
		setTimeout(() => { eventLoopGotATurn = true; }, 0);

		await streaming;

		expect(eventLoopGotATurn).toBe(true);
	});

	it("advances the cursor without parsing payloads when nothing is subscribed", async () => {
		vi.mocked(expoFetch).mockResolvedValue(new Response(
			replayFrames(3),
		) as unknown as Awaited<ReturnType<typeof expoFetch>>);
		const parsed: number[] = [];
		const advanced: number[] = [];

		const cursor = await chatApi.streamGlobalConversationEvents(
			cfg, 0, new AbortController().signal, (event) => parsed.push(event.seq), undefined,
			{ wantsPayload: () => false, onCursorAdvance: (seq) => advanced.push(seq) },
		);

		expect(parsed).toEqual([]);
		expect(advanced).toEqual([1, 2, 3]);
		expect(cursor).toBe(3);
	});

	it("accepts the daemon's reset cursor after its event database is replaced", async () => {
		vi.mocked(expoFetch).mockResolvedValue(new Response(
			'id: 1\ndata: {"seq":1,"projectId":"p-1","sessionId":"w-1","type":"session_updated","createdAt":"2026-08-11"}\n\n',
			{ headers: { "X-AO-Event-After": "0" } },
		) as unknown as Awaited<ReturnType<typeof expoFetch>>);
		const received: number[] = [];

		const cursor = await chatApi.streamGlobalConversationEvents(
			cfg,
			100,
			new AbortController().signal,
			(event) => received.push(event.seq),
		);

		expect(received).toEqual([1]);
		expect(cursor).toBe(1);
	});
});

/** `count` well-formed SSE frames with sequences 1..count. */
function replayFrames(count: number): string {
	let out = "";
	for (let seq = 1; seq <= count; seq++) {
		out += `id: ${seq}\ndata: {"seq":${seq},"projectId":"p-1","sessionId":"w-1","type":"session_updated","payload":{"conversationId":"c-1"},"createdAt":"2026-08-11"}\n\n`;
	}
	return out;
}

function response(body: unknown, status = 200): Response {
	return new Response(JSON.stringify(body), { status, headers: { "content-type": "application/json" } });
}
