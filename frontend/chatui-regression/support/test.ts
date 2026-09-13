import {
	test as base,
	expect,
	type ConsoleMessage,
	type Page,
	type Request,
	type TestInfo,
} from "@playwright/test";

import { installFakeTerminalMux } from "../../e2e/support/fake-terminal-mux";
import { installFakeAgent } from "./fake-agent";

export type JsonObject = Record<string, unknown>;

export type RecordedRequest = {
	body?: unknown;
	method: string;
	pathname: string;
	url: string;
};

export type ChatUIHarnessOptions = {
	activity?: "active" | "idle" | "waiting_input" | "exited";
	mode?: "chat" | "tui";
	projectId?: string;
	provider?: string;
	secondarySessionId?: string;
	sessionId?: string;
	status?: string;
};

type ResolvedChatUIHarnessOptions = Required<Omit<ChatUIHarnessOptions, "secondarySessionId">> &
	Pick<ChatUIHarnessOptions, "secondarySessionId">;

const now = "2026-08-25T09:00:00.000Z";

export function conversationSnapshot(
	sessionId: string,
	overrides: JsonObject = {},
): JsonObject {
	return {
		conversationId: `conversation-${sessionId}`,
		activeBranchId: "branch-root",
		branchedFromEarlierMessage: false,
		sessionId,
		harness: "codex",
		mode: "chat",
		controller: "ready",
		latestSequence: 0,
		oldestSequence: 0,
		hasMoreBefore: false,
		turns: [],
		queuedTurns: [],
		messages: [],
		activities: [],
		branchPoints: [],
		settings: {},
		capabilities: [
			"streaming",
			"interrupt",
			"steer",
			"history",
			"usage",
			"rate_limits",
			"models",
			"config_options",
			"fork",
			"skills",
			"images",
		],
		...overrides,
	};
}

export function turn(
	id: string,
	state: "completed" | "failed" | "interrupted" | "queued" | "recovered" | "running",
	overrides: JsonObject = {},
): JsonObject {
	return {
		id,
		state,
		requestedAt: now,
		...(state !== "queued" ? { startedAt: now } : {}),
		...(state !== "queued" && state !== "running" ? { completedAt: now } : {}),
		...overrides,
	};
}

export function message(
	id: string,
	turnId: string,
	sequence: number,
	role: "assistant" | "user",
	text: string,
	overrides: JsonObject = {},
): JsonObject {
	return {
		kind: "message",
		id,
		turnId,
		sequence,
		revision: 0,
		role,
		origin: role === "user" ? "human" : "provider",
		text,
		streaming: false,
		createdAt: now,
		...overrides,
	};
}

function clone<T>(value: T): T {
	return JSON.parse(JSON.stringify(value)) as T;
}

async function requestBody(request: Request): Promise<unknown> {
	const raw = request.postData();
	if (!raw) return undefined;
	try {
		return JSON.parse(raw) as unknown;
	} catch {
		return raw;
	}
}

/**
 * Stateful browser-side AO contract fixture.
 *
 * It runs the production renderer and query/mutation code. Only the Electron
 * preload, daemon REST boundary, SSE stream, and terminal PTY are replaced with
 * deterministic in-page equivalents. Every mutation is recorded as evidence.
 */
export class ChatUIRegressionHarness {
	readonly page: Page;
	readonly projectId: string;
	readonly sessionId: string;
	readonly secondarySessionId?: string;
	readonly provider: string;
	readonly requests: RecordedRequest[] = [];
	readonly unexpectedRequests: RecordedRequest[] = [];
	readonly consoleErrors: string[] = [];
	readonly pageErrors: string[] = [];

	conversation: JsonObject;
	secondaryConversation?: JsonObject;
	configOptions: JsonObject[] = [];
	transitionStatus: JsonObject;
	workspaceFiles: JsonObject[] = [];
	stagePaths = [".ao/attachments/attachment-regression.png"];

	private readonly options: ResolvedChatUIHarnessOptions;

	private constructor(page: Page, options: ResolvedChatUIHarnessOptions) {
		this.page = page;
		this.options = options;
		this.projectId = options.projectId;
		this.sessionId = options.sessionId;
		this.secondarySessionId = options.secondarySessionId;
		this.provider = options.provider;
		this.conversation = conversationSnapshot(options.sessionId, {
			harness: options.provider,
			mode: options.mode,
		});
		if (options.secondarySessionId) {
			this.secondaryConversation = conversationSnapshot(options.secondarySessionId, {
				harness: options.provider,
				mode: "chat",
			});
		}
		this.transitionStatus = {
			supported: true,
			targetMode: options.mode === "chat" ? "tui" : "chat",
		};
	}

	static async create(page: Page, input: ChatUIHarnessOptions = {}): Promise<ChatUIRegressionHarness> {
		const options: ResolvedChatUIHarnessOptions = {
			activity: input.activity ?? "idle",
			mode: input.mode ?? "chat",
			projectId: input.projectId ?? "chatui-regression",
			provider: input.provider ?? "codex",
			secondarySessionId: input.secondarySessionId,
			sessionId: input.sessionId ?? "chatui-worker",
			status: input.status ?? "idle",
		};
		const harness = new ChatUIRegressionHarness(page, options);
		await harness.install();
		return harness;
	}

	private async install(): Promise<void> {
		this.page.on("pageerror", (error) => this.pageErrors.push(error.stack || error.message));
		this.page.on("console", (entry: ConsoleMessage) => {
			if (entry.type() === "error") this.consoleErrors.push(entry.text());
		});

		await this.page.emulateMedia({ reducedMotion: "reduce" });
		const workers = [
			{
				id: this.sessionId,
				provider: this.provider,
				title: "ChatUI regression worker",
				createdAt: now,
				mode: this.options.mode,
				status: this.options.status,
				activity: this.options.activity,
			},
		];
		if (this.secondarySessionId) {
			workers.push({
				id: this.secondarySessionId,
				provider: this.provider,
				title: "ChatUI regression secondary worker",
				createdAt: now,
				mode: "chat",
				status: "idle",
				activity: "idle",
			});
		}
		await installFakeAgent(this.page, {
			projectId: this.projectId,
			projectName: this.projectId,
			workers,
		});
		await installFakeTerminalMux(this.page, {
			[`${this.sessionId}/terminal_0`]: "AO deterministic terminal ready\r\n",
		});
		await this.page.route("http://127.0.0.1:8080/api/v1/**", async (route) => {
			const request = route.request();
			const url = new URL(request.url());
			const pathname = url.pathname;
			const method = request.method();
			this.requests.push({
				body: await requestBody(request),
				method,
				pathname,
				url: request.url(),
			});

			if (method === "GET" && pathname === `/api/v1/projects/${this.projectId}`) {
				await route.fulfill({
					json: {
						status: "ok",
						project: {
							id: this.projectId,
							agent: this.provider,
							config: { worker: { agent: this.provider } },
						},
					},
				});
				return;
			}
			if (method === "GET" && pathname === "/api/v1/notifications") {
				await route.fulfill({
					json: { notifications: [], unreadCount: 0, unresolvedCount: 0 },
				});
				return;
			}
			if (method === "GET" && pathname === "/api/v1/settings") {
				await route.fulfill({
					json: {
						defaultSessionMode: "tui",
						chatHarnesses: ["codex", "claude-code"],
						client: "",
						localEnabled: true,
						cloudOffering: false,
						cloudEnabled: false,
						cloudControlPlaneUrl: "",
					},
				});
				return;
			}
			if (method === "GET" && pathname === "/api/v1/agents") {
				const agents = [
					{ id: "codex", label: "Codex", authStatus: "authorized" },
					{ id: "claude-code", label: "Claude Code", authStatus: "authorized" },
				];
				await route.fulfill({
					json: { supported: agents, installed: agents, authorized: agents },
				});
				return;
			}
			if (method === "GET") {
				const conversationPath = pathname.match(/^\/api\/v1\/sessions\/([^/]+)\/conversation$/);
				if (conversationPath) {
					const requestedSessionId = decodeURIComponent(conversationPath[1] ?? "");
					const snapshot =
						requestedSessionId === this.sessionId
							? this.conversation
							: requestedSessionId === this.secondarySessionId
								? this.secondaryConversation
								: undefined;
					if (snapshot) {
						await route.fulfill({ json: clone(snapshot) });
						return;
					}
				}
			}
			if (method === "GET" && pathname.endsWith("/conversation/models")) {
				await route.fulfill({ json: { models: [], selected: {} } });
				return;
			}
			if (method === "GET" && pathname.endsWith("/conversation/config-options")) {
				await route.fulfill({ json: { options: clone(this.configOptions) } });
				return;
			}
			if (method === "GET" && pathname.endsWith("/conversation/skills")) {
				await route.fulfill({ json: { skills: [] } });
				return;
			}
			if (method === "GET" && pathname.endsWith("/workspace/files")) {
				await route.fulfill({ json: { files: clone(this.workspaceFiles), truncated: false } });
				return;
			}
			if (method === "GET" && pathname.endsWith("/interface-transition")) {
				await route.fulfill({ json: clone(this.transitionStatus) });
				return;
			}
			if (method === "GET" && pathname.endsWith("/agent-switches")) {
				await route.fulfill({ json: { switches: [] } });
				return;
			}
			if (method === "GET" && pathname.endsWith("/reviews")) {
				await route.fulfill({ json: { reviewerHandleId: "", reviews: [], runs: [] } });
				return;
			}
			if (method === "GET" && pathname.startsWith("/api/v1/agents/") && pathname.endsWith("/models")) {
				const agentId = pathname.split("/")[4] ?? "codex";
				await route.fulfill({
					json: {
						agentId,
						allowCustom: true,
						fetchedAt: now,
						models: [],
						selectionMode: "catalog",
						source: "regression-fixture",
						stale: false,
					},
				});
				return;
			}
			if (method === "POST" && pathname.endsWith("/attachments")) {
				await route.fulfill({ json: { paths: clone(this.stagePaths) } });
				return;
			}
			if (method === "POST" && pathname.endsWith("/conversation/messages")) {
				await route.fulfill({ json: { status: "accepted", turnId: "turn-accepted" } });
				return;
			}
			if (method === "POST" && pathname.endsWith("/conversation/interrupt")) {
				const body = (this.requests.at(-1)?.body ?? {}) as JsonObject;
				const queuedTurnIds = Array.isArray(body.queuedTurnIds)
					? body.queuedTurnIds.filter((id): id is string => typeof id === "string")
					: [];
				const queuedScope = new Set(queuedTurnIds);
				const turns = Array.isArray(this.conversation.turns)
					? (this.conversation.turns as JsonObject[])
					: [];
				const queuedTurns = Array.isArray(this.conversation.queuedTurns)
					? (this.conversation.queuedTurns as JsonObject[])
					: [];
				this.conversation.turns = turns.map((entry) =>
					entry.state === "running" || queuedScope.has(String(entry.id ?? ""))
						? { ...entry, state: "interrupted", completedAt: now }
						: entry,
				);
				this.conversation.queuedTurns = queuedTurns.filter(
					(entry) => !queuedScope.has(String(entry.turnId ?? "")),
				);
				await route.fulfill({ json: { status: "accepted" } });
				return;
			}
			if (method === "PATCH" && pathname.endsWith("/conversation/settings")) {
				const body = (this.requests.at(-1)?.body ?? {}) as JsonObject;
				const current = (this.conversation.settings ?? {}) as JsonObject;
				this.conversation.settings = { ...clone(current), ...clone(body) };
				await route.fulfill({ json: clone(this.conversation.settings) });
				return;
			}
			if (
				method === "POST" &&
				pathname.includes("/conversation/turns/") &&
				pathname.endsWith("/steer")
			) {
				const encodedTurnId = pathname.split("/turns/")[1]?.replace(/\/steer$/, "") ?? "";
				const turnId = decodeURIComponent(encodedTurnId);
				const turns = Array.isArray(this.conversation.turns)
					? (this.conversation.turns as JsonObject[])
					: [];
				const messages = Array.isArray(this.conversation.messages)
					? (this.conversation.messages as JsonObject[])
					: [];
				const activities = Array.isArray(this.conversation.activities)
					? (this.conversation.activities as JsonObject[])
					: [];
				const queuedTurns = Array.isArray(this.conversation.queuedTurns)
					? (this.conversation.queuedTurns as JsonObject[])
					: [];
				const source = messages.find((entry) => entry.turnId === turnId);
				const sequence = Number(this.conversation.latestSequence ?? 0) + 1;
				this.conversation.turns = turns.filter((turn) => turn.id !== turnId);
				this.conversation.messages = messages.filter((entry) => entry.turnId !== turnId);
				this.conversation.queuedTurns = queuedTurns.filter((entry) => entry.turnId !== turnId);
				this.conversation.activities = [
					...activities,
					{
						id: `activity-promoted-${turnId}`,
						turnId: "turn-running",
						sequence,
						revision: 0,
						activityKind: "system",
						status: "completed",
						summary: String(source?.text ?? "Queued guidance delivered"),
						detail: {
							event: "steer",
							text: String(source?.text ?? ""),
							origin: "human",
							sourceTurnId: turnId,
						},
						createdAt: now,
					},
				];
				this.conversation.latestSequence = sequence;
				await route.fulfill({
					json: {
						status: "accepted",
						sourceTurnId: turnId,
						providerTurnId: "turn-running",
						activityId: `activity-promoted-${turnId}`,
					},
				});
				return;
			}
			if (
				method === "POST" &&
				pathname.includes("/conversation/turns/") &&
				pathname.endsWith("/cancel")
			) {
				const encodedTurnId = pathname.split("/turns/")[1]?.replace(/\/cancel$/, "") ?? "";
				const turnId = decodeURIComponent(encodedTurnId);
				const turns = Array.isArray(this.conversation.turns)
					? (this.conversation.turns as JsonObject[])
					: [];
				const messages = Array.isArray(this.conversation.messages)
					? (this.conversation.messages as JsonObject[])
					: [];
				const queuedTurns = Array.isArray(this.conversation.queuedTurns)
					? (this.conversation.queuedTurns as JsonObject[])
					: [];
				this.conversation.turns = turns.filter((turn) => turn.id !== turnId);
				this.conversation.messages = messages.filter((entry) => entry.turnId !== turnId);
				this.conversation.queuedTurns = queuedTurns.filter((entry) => entry.turnId !== turnId);
				await route.fulfill({ json: { status: "accepted", turnId } });
				return;
			}
			if (method === "POST" && pathname.endsWith("/interface-transition")) {
				const body = this.requests.at(-1)?.body as JsonObject | undefined;
				const transition = {
					id: "transition-requested",
					sessionId: this.sessionId,
					sourceMode: this.options.mode,
					targetMode: body?.targetMode ?? "tui",
					policy: body?.policy ?? "drain",
					phase: "requested",
					createdAt: now,
					updatedAt: now,
				};
				this.transitionStatus = {
					supported: true,
					targetMode: transition.targetMode,
					transition,
				};
				await route.fulfill({
					status: 202,
					json: { transition },
				});
				return;
			}
			if (method === "PATCH" && pathname.endsWith("/conversation/config-options")) {
				await route.fulfill({ json: { options: clone(this.configOptions) } });
				return;
			}
			if (method === "POST" && pathname.includes("/conversation/branches/")) {
				const encoded = pathname.split("/branches/")[1]?.replace(/\/activate$/, "") ?? "";
				const activeBranchId = decodeURIComponent(encoded);
				this.conversation.activeBranchId = activeBranchId;
				await route.fulfill({ json: { activeBranchId } });
				return;
			}
			if (method === "PUT" && pathname.includes("/notice-acknowledgement")) {
				await route.fulfill({ json: { transition: clone(this.transitionStatus.transition ?? {}) } });
				return;
			}

			const unexpected = this.requests.at(-1);
			if (unexpected) this.unexpectedRequests.push(unexpected);
			await route.fulfill({
				status: 501,
				json: {
					code: "CHATUI_REGRESSION_UNEXPECTED_REQUEST",
					message: `No deterministic response registered for ${method} ${pathname}`,
				},
			});
		});
	}

	async open(): Promise<void> {
		await this.page.goto(`/#/projects/${this.projectId}/sessions/${this.sessionId}`);
		if (this.options.mode === "chat") {
			await expect(this.page.getByRole("region", { name: "Chat" })).toBeVisible();
		} else {
			await expect(this.page.getByTestId("session-terminal-region")).toBeVisible();
		}
	}

	async navigateToSession(sessionId: string): Promise<void> {
		const hash = `#/projects/${encodeURIComponent(this.projectId)}/sessions/${encodeURIComponent(sessionId)}`;
		await this.page.evaluate((nextHash) => {
			window.location.hash = nextHash;
		}, hash);
		await expect.poll(() => this.page.evaluate(() => window.location.hash)).toBe(hash);
		await expect(this.page.getByRole("region", { name: "Chat" })).toBeVisible();
		const sessionTab = this.page
			.getByRole("tablist", { name: "Chat tabs" })
			.getByRole("tab", { selected: true });
		await expect(sessionTab).toHaveCount(1);
		await expect(sessionTab).toHaveAccessibleName(this.provider === "claude-code" ? "Claude Code" : "Codex");
	}

	requestsMatching(method: string, suffix: string): RecordedRequest[] {
		return this.requests.filter(
			(request) => request.method === method && request.pathname.endsWith(suffix),
		);
	}

	async setMode(mode: "chat" | "tui"): Promise<void> {
		this.conversation.mode = mode;
		this.transitionStatus.targetMode = mode === "chat" ? "tui" : "chat";
		await this.page.evaluate(
			({ id, mode }) => window.__aoFakeAgent?.setMode(id, mode),
			{ id: this.sessionId, mode },
		);
	}

	async emitCDC(type = "session_updated"): Promise<void> {
		await this.page.evaluate(
			({ type, sessionId, conversationId }) =>
				window.__aoFakeAgent?.emitCDC({ type, sessionId, conversationId }),
			{
				type,
				sessionId: this.sessionId,
				conversationId: String(this.conversation.conversationId ?? ""),
			},
		);
	}

	async delayFileReads(milliseconds: number): Promise<void> {
		await this.page.addInitScript((delay) => {
			const original = FileReader.prototype.readAsDataURL;
			FileReader.prototype.readAsDataURL = function delayedRead(blob: Blob) {
				window.setTimeout(() => original.call(this, blob), delay);
			};
		}, milliseconds);
	}

	async attachEvidence(testInfo: TestInfo): Promise<void> {
		try {
			await testInfo.attach("chatui-final-screen", {
				body: await this.page.screenshot({ fullPage: true }),
				contentType: "image/png",
			});
		} catch {
			// A navigation/browser failure is already represented in the trace and logs.
		}
		const evidence = {
			consoleErrors: this.consoleErrors,
			conversation: this.conversation,
			pageErrors: this.pageErrors,
			requests: this.requests,
			transitionStatus: this.transitionStatus,
			unexpectedRequests: this.unexpectedRequests,
		};
		await testInfo.attach("chatui-state-and-requests", {
			body: Buffer.from(`${JSON.stringify(evidence, null, 2)}\n`),
			contentType: "application/json",
		});
	}
}

type ChatUIFixtures = {
	chatUI: ChatUIRegressionHarness;
	chatUIOptions: ChatUIHarnessOptions;
};

export const test = base.extend<ChatUIFixtures>({
	chatUIOptions: [{}, { option: true }],
	chatUI: async ({ page, chatUIOptions }, use, testInfo) => {
		const harness = await ChatUIRegressionHarness.create(page, chatUIOptions);
		try {
			await use(harness);
		} finally {
			await harness.attachEvidence(testInfo);
		}
		const gateState = {
			consoleErrors: [...harness.consoleErrors],
			pageErrors: [...harness.pageErrors],
			unexpectedRequests: [...harness.unexpectedRequests],
		};
		if (
			gateState.pageErrors.length > 0 ||
			gateState.consoleErrors.length > 0 ||
			gateState.unexpectedRequests.length > 0
		) {
			const encodedState = Buffer.from(JSON.stringify(gateState)).toString("base64");
			throw new Error(
				`ChatUI strict gate rejected browser state. CHATUI_STRICT_GATE_STATE_B64:${encodedState}`,
			);
		}
	},
});

export { expect };
