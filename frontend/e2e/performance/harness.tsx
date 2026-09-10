/// <reference path="../../src/renderer/global.d.ts" />
// Browser-only workload fixture: real AO rendering, deterministic synthetic data.
// This entry is never imported by the application or included in its build.
import { Profiler, type ReactNode } from "react";
import { createRoot } from "react-dom/client";
import { flushSync } from "react-dom";
import {
	QueryClient,
	QueryClientProvider,
	QueryObserver,
} from "@tanstack/react-query";
import { I18nextProvider } from "react-i18next";
import { appI18n } from "../../src/renderer/i18n";
import { TooltipProvider } from "../../src/renderer/components/ui/tooltip";
import { AssistantMessage } from "../../src/renderer/components/chat/ChatTimelineItems";
import { ChatComposer } from "../../src/renderer/components/chat/ChatComposer";
import { ChatWorkspace } from "../../src/renderer/components/chat/ChatWorkspace";
import { chatFixtureLongHistory } from "../../src/renderer/lib/chat-fixture";
import { highlight } from "../../src/renderer/lib/code-highlight";
import { createEventTransport } from "../../src/renderer/lib/event-transport";
import { useUiStore } from "../../src/renderer/stores/ui-store";
import { setApiBaseUrl } from "../../src/renderer/lib/api-client";
import { useConversationCommands } from "../../src/renderer/hooks/useConversation";
import { buildCommands, displayGroups } from "../../src/renderer/lib/command-palette";
import { settingsQueryKey } from "../../src/renderer/hooks/useSettings";
import { SettingsDialog } from "../../src/renderer/components/SettingsDialog";
import type { ChatSkill, ConversationMessage } from "../../src/renderer/types/conversation";
import type { WorkspaceSummary } from "../../src/renderer/types/workspace";
import "../../src/renderer/styles.css";
import { DndContext } from "@dnd-kit/core";
import {
	SortableContext,
	useSortable,
	verticalListSortingStrategy,
} from "@dnd-kit/sortable";

const root = createRoot(document.getElementById("performance-root")!);
const client = new QueryClient({
	defaultOptions: { queries: { retry: false } },
});
const wait = (ms: number) =>
	new Promise<void>((resolve) => setTimeout(resolve, ms));
const frame = () =>
	new Promise<number>((resolve) => requestAnimationFrame(resolve));
let composerSample: {
	frameGaps: number[];
	beforeInputDurations: number[];
	longTasks: number[];
	lastFrame: number;
	observer?: PerformanceObserver;
	removeBeforeInputTiming: () => void;
	frame: number;
} | undefined;
const LOCAL_ECHO_TEXT = "Show a local acknowledgement before the daemon responds";
const inspectorTimelineSnapshot = chatFixtureLongHistory(250);
let localEchoSample:
	| {
		startedAt?: number;
		rowCommittedAt?: number;
		firstPaintAt?: number;
		requestPending: boolean;
		sendError?: string;
		requestFinished: Promise<void>;
		observer: MutationObserver;
		originalFetch: typeof window.fetch;
	}
	| undefined;

function LocalEchoWorkload() {
	const { send, localEchos } = useConversationCommands("performance-session");
	return (
		<>
			<button
				type="button"
				onClick={() => {
					if (localEchoSample) localEchoSample.startedAt = performance.now();
					void send(LOCAL_ECHO_TEXT).catch((error) => {
						if (localEchoSample) localEchoSample.sendError = String(error);
					});
				}}
			>
				Benchmark local echo
			</button>
			<ChatWorkspace
				snapshot={{
					...chatFixtureLongHistory(0),
					sessionId: "performance-session",
					controller: { state: "ready" },
				}}
				localEchos={localEchos}
			/>
		</>
	);
}

function SettingsDialogWorkload({
	onCommit,
}: {
	onCommit: (actualDuration: number, commitTime: number) => void;
}) {
	return (
		<>
			<div aria-hidden="true" data-testid="settings-shell-fixture">
				{Array.from({ length: 1_500 }, (_, index) => <div key={index}>Shell item {index}</div>)}
			</div>
			<Profiler id="settings-dialog" onRender={(_id, _phase, actualDuration, _baseDuration, _startTime, commitTime) => onCommit(actualDuration, commitTime)}>
				<SettingsDialog />
			</Profiler>
		</>
	);
}

function InspectorTimelineWorkload() {
	return (
		<>
			<button
				type="button"
				onClick={() => useUiStore.getState().setInspectorOpen(inspectorTimelineSnapshot.sessionId, true)}
			>
				Open inspector for long timeline
			</button>
			<ChatWorkspace
				snapshot={inspectorTimelineSnapshot}
				sessionTitle="AO responsiveness benchmark"
			/>
		</>
	);
}

function render(node: ReactNode) {
	flushSync(() =>
		root.render(
			<QueryClientProvider client={client}>
				<I18nextProvider i18n={appI18n}>
					<TooltipProvider>{node}</TooltipProvider>
				</I18nextProvider>
			</QueryClientProvider>,
		),
	);
}
function assistant(text: string, streaming = true): ConversationMessage {
	return {
		kind: "message",
		id: "performance-message",
		sequence: 1,
		revision: 0,
		role: "assistant",
		origin: "provider",
		text,
		streaming,
		createdAt: "2026-09-05T00:00:00Z",
	};
}

async function prepareComposerCompletion(node: ReactNode) {
	composerSample?.observer?.disconnect();
	composerSample?.removeBeforeInputTiming();
	if (composerSample) cancelAnimationFrame(composerSample.frame);
	render(node);
	await frame();
	const sample = {
		frameGaps: [] as number[],
		beforeInputDurations: [] as number[],
		longTasks: [] as number[],
		lastFrame: performance.now(),
		observer: undefined as PerformanceObserver | undefined,
		removeBeforeInputTiming: () => {},
		frame: 0,
	};
	// React delegates editor events at the root container. A document-level
	// capture/bubble pair therefore brackets Lexical plus the composer's
	// synchronous React work for every actual browser `beforeinput` event.
	const starts = new WeakMap<Event, number>();
	const onBeforeInputCapture = (event: Event) => starts.set(event, performance.now());
	const onBeforeInputBubble = (event: Event) => {
		const started = starts.get(event);
		if (started !== undefined) sample.beforeInputDurations.push(performance.now() - started);
	};
	document.addEventListener("beforeinput", onBeforeInputCapture, true);
	document.addEventListener("beforeinput", onBeforeInputBubble);
	sample.removeBeforeInputTiming = () => {
		document.removeEventListener("beforeinput", onBeforeInputCapture, true);
		document.removeEventListener("beforeinput", onBeforeInputBubble);
	};
	if (typeof PerformanceObserver !== "undefined") {
		try {
			sample.observer = new PerformanceObserver((list) => {
				for (const entry of list.getEntries()) sample.longTasks.push(entry.duration);
			});
			// Collection begins only after the workload has mounted. Replaying
			// buffered entries would incorrectly attribute an expensive long-history
			// mount to the subsequent typing interaction.
			sample.observer.observe({ type: "longtask" });
		} catch {
			// Long Task entries are unavailable in some Chromium configurations.
		}
	}
	const tick = (now: number) => {
		sample.frameGaps.push(now - sample.lastFrame);
		sample.lastFrame = now;
		sample.frame = requestAnimationFrame(tick);
	};
	sample.frame = requestAnimationFrame(tick);
	composerSample = sample;
}

async function finishComposerCompletion() {
	const sample = composerSample;
	if (!sample) throw new Error("Composer-completion benchmark was not prepared");
	cancelAnimationFrame(sample.frame);
	sample.observer?.disconnect();
	sample.removeBeforeInputTiming();
	composerSample = undefined;
	await frame();
	const items = document.querySelectorAll('[role="listbox"] [role="option"]');
	return {
		visibleSuggestions: items.length,
		beforeInputDurationsMs: sample.beforeInputDurations,
		maxFrameGapMs: Math.max(...sample.frameGaps, 0),
		longTaskCount: sample.longTasks.length,
		maxLongTaskMs: Math.max(...sample.longTasks, 0),
	};
}

// Reproduces the sidebar's nested-DnD reorder pattern to measure the cost of
// the two ways to neutralize session sortables during a project drag:
//   "swap"     — unmount the DndContext/SortableContext and mount plain rows
//                (current Sidebar behavior), then remount on drop
//   "disabled" — keep the DnD tree mounted and toggle dnd-kit's `disabled`
// The real Sidebar mocks dnd-kit in unit tests, so this is the only place the
// real mount/unmount cost is exercised.
type ChurnMode = "swap" | "disabled";

function ChurnSortableRow({ id, disabled }: { id: string; disabled: boolean }) {
	const { setNodeRef, transform, transition, attributes, listeners, isDragging } =
		useSortable({ id, disabled });
	return (
		<li
			ref={setNodeRef}
			{...attributes}
			{...listeners}
			className="px-2 py-1 text-sm"
			style={{
				transform: transform ? `translateY(${transform.y}px)` : undefined,
				transition,
				opacity: isDragging ? 0.5 : 1,
			}}
		>
			{id}
		</li>
	);
}

function ChurnProjectBlock({
	projectId,
	rows,
	dragging,
	mode,
}: {
	projectId: string;
	rows: string[];
	dragging: boolean;
	mode: ChurnMode;
}) {
	if (mode === "swap" && dragging) {
		return (
			<ul>
				{rows.map((id) => (
					<li key={id} className="px-2 py-1 text-sm">
						{id}
					</li>
				))}
			</ul>
		);
	}
	const disabled = mode === "disabled" && dragging;
	return (
		<DndContext id={`dnd-${projectId}`}>
			<SortableContext items={rows} strategy={verticalListSortingStrategy} disabled={disabled}>
				<ul>
					{rows.map((id) => (
						<ChurnSortableRow key={id} id={id} disabled={disabled} />
					))}
				</ul>
			</SortableContext>
		</DndContext>
	);
}

function ChurnFixture({
	projects,
	dragging,
	mode,
}: {
	projects: { id: string; rows: string[] }[];
	dragging: boolean;
	mode: ChurnMode;
}) {
	return (
		<DndContext id="dnd-projects">
			<ul>
				{projects.map((project) => (
					<ChurnProjectBlock
						key={project.id}
						projectId={project.id}
						rows={project.rows}
						dragging={dragging}
						mode={mode}
					/>
				))}
			</ul>
		</DndContext>
	);
}

export const performanceHarness = {
	async projectDragChurn() {
		const projects = Array.from({ length: 8 }, (_, project) => ({
			id: `project-${project}`,
			rows: Array.from({ length: 12 }, (_, session) => `project-${project}-session-${session}`),
		}));
		const measure = async (mode: ChurnMode) => {
			render(<ChurnFixture projects={projects} dragging={false} mode={mode} />);
			await frame();
			await frame();
			const startBefore = performance.now();
			render(<ChurnFixture projects={projects} dragging={true} mode={mode} />);
			const startMs = performance.now() - startBefore;
			await frame();
			const dropBefore = performance.now();
			render(<ChurnFixture projects={projects} dragging={false} mode={mode} />);
			const dropMs = performance.now() - dropBefore;
			await frame();
			return { startMs, dropMs };
		};
		const best = async (mode: ChurnMode) => {
			// Warm up the module/JIT, then take the median of three toggles so a
			// single GC pause does not decide the comparison.
			await measure(mode);
			const runs = [await measure(mode), await measure(mode), await measure(mode)];
			return {
				startMs: runs.map((run) => run.startMs).sort((a, b) => a - b)[1],
				dropMs: runs.map((run) => run.dropMs).sort((a, b) => a - b)[1],
			};
		};
		const swap = await best("swap");
		const disabled = await best("disabled");
		return {
			projects: projects.length,
			rowsPerProject: projects[0].rows.length,
			swap,
			disabled,
		};
	},
	async prepareInspectorTimeline() {
		useUiStore.setState({
			inspectorSessions: {
				[inspectorTimelineSnapshot.sessionId]: { isOpen: false, view: "summary" },
			},
		});
		render(<InspectorTimelineWorkload />);
		await document.fonts.ready;
		await wait(500);
		const timeline = document.querySelector<HTMLElement>('[role="log"][aria-label="Conversation"]');
		if (!timeline) throw new Error("Long conversation did not mount");
		return {
			loadedTurns: 250,
			mountedTurns: timeline.querySelectorAll("[data-chat-scroll-anchor]").length,
			domElementCount: document.querySelectorAll("*").length,
		};
	},
	async commandPalette() {
		const workspaceCount = 100;
		const sessionsPerWorkspace = 50;
		const workspaces: WorkspaceSummary[] = Array.from({ length: workspaceCount }, (_, workspaceIndex) => {
			const workspaceId = `perf-project-${workspaceIndex}`;
			return {
				id: workspaceId,
				name: `Performance project ${workspaceIndex}`,
				path: `/repos/performance-project-${workspaceIndex}`,
				type: "main",
				sessions: Array.from({ length: sessionsPerWorkspace }, (_, sessionIndex) => {
					const sessionId = `${workspaceId}-session-${sessionIndex}`;
					const prNumber = workspaceIndex * sessionsPerWorkspace + sessionIndex + 1;
					return {
						id: sessionId,
						workspaceId,
						workspaceName: `Performance project ${workspaceIndex}`,
						title: `Fix performance task ${sessionIndex}`,
						provider: "codex",
						kind: "worker",
						branch: `perf/${workspaceIndex}/${sessionIndex}`,
						status: "working",
						updatedAt: "2026-09-09T00:00:00Z",
						prs: sessionIndex % 5 === 0
							? [{
								url: `https://github.com/aoagents/performance/pull/${prNumber}`,
								number: prNumber,
								state: "open",
								ci: "passing",
								review: "pending",
								mergeability: "clean",
								reviewComments: false,
								updatedAt: "2026-09-09T00:00:00Z",
							}]
							: [],
					};
				}),
			};
		});
		const buildStarted = performance.now();
		const items = buildCommands({ workspaces, currentProjectId: workspaces[0]?.id });
		const buildMs = performance.now() - buildStarted;
		const queries = ["fix", "project 42", "#100", "copy pr", "no-matches"];
		const querySamples = queries.map((query) => {
			const started = performance.now();
			const groups = displayGroups(items, query);
			return {
				query,
				elapsedMs: performance.now() - started,
				visibleItems: groups.reduce((count, group) => count + group.items.length, 0),
			};
		});
		return {
			workspaceCount,
			sessionCount: workspaceCount * sessionsPerWorkspace,
			commandItemCount: items.length,
			buildMs,
			querySamples,
		};
	},
	async settingsDialog() {
		// This isolates renderer mount work. The live daemon settings query is
		// intentionally warm because SettingsDialog subscribes to it even while
		// closed in the application shell.
		client.setQueryDefaults(settingsQueryKey, { staleTime: Infinity });
		client.setQueryData(settingsQueryKey, {
			defaultSessionMode: "chat",
			chatHarnesses: ["codex"],
			client: "performance-harness",
			localEnabled: true,
			cloudOffering: false,
			cloudEnabled: false,
			cloudControlPlaneUrl: "",
		});
		useUiStore.setState({ settingsModal: null });
		const commits: Array<{ actualDurationMs: number; commitOffsetMs: number }> = [];
		let startedAt = 0;
		render(
			<SettingsDialogWorkload
				onCommit={(actualDuration, commitTime) => {
					if (startedAt > 0) commits.push({
						actualDurationMs: actualDuration,
						commitOffsetMs: commitTime - startedAt,
					});
				}}
			/>,
		);
		await frame();
		startedAt = performance.now();
		useUiStore.getState().openGlobalSettings("general");
		await frame();
		const dialogFirstPaintMs = performance.now() - startedAt;
		await frame();
		const bodyFirstPaintMs = performance.now() - startedAt;
		useUiStore.getState().closeSettings();
		return { dialogFirstPaintMs, bodyFirstPaintMs, commits };
	},
	async events() {
		const original = window.EventSource;
		const sources: BenchSource[] = [];
		class BenchSource {
			readyState = 1;
			onopen: (() => void) | null = null;
			onerror: (() => void) | null = null;
			onmessage: ((event: Event) => void) | null = null;
			listeners = new Map<string, EventListener[]>();
			constructor(public url: string) {
				sources.push(this);
			}
			addEventListener(type: string, listener: EventListener) {
				this.listeners.set(type, [
					...(this.listeners.get(type) ?? []),
					listener,
				]);
			}
			close() {
				this.readyState = 2;
			}
			emit(data: string) {
				for (const listener of this.listeners.get("session_updated") ?? [])
					listener(new MessageEvent("session_updated", { data }));
			}
		}
		window.EventSource = BenchSource as unknown as typeof EventSource;
		setApiBaseUrl("http://127.0.0.1:8080");
		const queryClient = new QueryClient();
		const queryKey = ["conversation", "performance-session"];
		queryClient.setQueryData(queryKey, 0);
		const flushes: number[] = [];
		const completions: number[] = [];
		let started = performance.now();
		const observer = new QueryObserver(queryClient, {
			queryKey,
			staleTime: Infinity,
			queryFn: async () => {
				flushes.push(performance.now() - started);
				await wait(30);
				completions.push(performance.now() - started);
				return completions.length;
			},
		});
		const unsubscribe = observer.subscribe(() => {});
		const disconnect = createEventTransport(queryClient).connect();
		try {
			// The bridge delivers an initial daemon-status callback. Let its full
			// lifecycle refresh finish before measuring conversation-only traffic.
			await wait(500);
			flushes.length = 0;
			completions.length = 0;
			started = performance.now();
			const source = sources.find((entry) =>
				entry.url.endsWith("/api/v1/events"),
			)!;
			for (let index = 0; index < 20; index++) {
				source.emit(
					JSON.stringify({
						sessionId: "performance-session",
						payload: { conversationId: "performance-conversation" },
					}),
				);
				await wait(100);
			}
			const streamEndedMs = performance.now() - started;
			await wait(250);
			return {
				firstRefreshMs: flushes[0] ?? null,
				refreshCount: flushes.length,
				refreshesDuringStream: flushes.filter((time) => time < streamEndedMs)
					.length,
				streamEndedMs,
				flushes,
				completions,
			};
		} finally {
			disconnect();
			unsubscribe();
			queryClient.clear();
			window.EventSource = original;
		}
	},
	async streaming() {
		render(<AssistantMessage message={assistant("Start ")} />);
		await frame();
		await frame();
		const target = "Start " + "responsive output 👨‍👩‍👧‍👦 é ".repeat(30) + ".";
		const started = performance.now();
		render(<AssistantMessage message={assistant(target)} />);
		let frames = 0;
		while (
			document.querySelector(".chat-md")?.textContent !== target &&
			performance.now() - started < 15000
		) {
			await frame();
			frames++;
		}
		const visibleMs = performance.now() - started;
		const exact = document.querySelector(".chat-md")?.textContent === target;
		render(<AssistantMessage message={assistant(target, false)} showCopy />);
		await frame();
		return {
			receivedCharacters: target.length,
			visibleMs,
			frames,
			exact,
			completedCopyAvailable: Boolean(
				document.querySelector('[aria-label="Copy message as markdown"]'),
			),
		};
	},
	async highlighting() {
		// Load the existing grammar chunk first so this isolates large-block work,
		// not network/module fetch differences. The worker still starts lazily.
		await highlight("const warmup = 1", "typescript");
		const code = Array.from(
			{ length: 10000 },
			(_, index) => `const value${index}: string = "Unicode 👨‍👩‍👧‍👦 ${index}";`,
		).join("\n");
		const gaps: number[] = [];
		let previous = performance.now();
		const timer = setInterval(() => {
			const now = performance.now();
			gaps.push(now - previous);
			previous = now;
		}, 4);
		await wait(30);
		const started = performance.now();
		const tree = await highlight(code, "typescript");
		const elapsedMs = performance.now() - started;
		await wait(30);
		clearInterval(timer);
		function flatten(node: {
			type: string;
			value?: string;
			children?: unknown[];
		}): string {
			return node.type === "text"
				? (node.value ?? "")
				: (node.children ?? [])
						.map((child) => flatten(child as typeof node))
						.join("");
		}
		return {
			codeCharacters: code.length,
			elapsedMs,
			maxEventLoopGapMs: Math.max(...gaps),
			samples: gaps,
			textPreserved: Boolean(tree) && flatten(tree!) === code,
		};
	},
	async history() {
		useUiStore.setState({
			inspectorSessions: { "ao-long": { isOpen: false, view: "summary" } },
		});
		render(
			<ChatWorkspace
				snapshot={chatFixtureLongHistory(250)}
				sessionTitle="AO responsiveness benchmark"
			/>,
		);
		await document.fonts.ready;
		await wait(500);
		const scroller = document.querySelector<HTMLElement>(
			'[role="log"][aria-label="Conversation"]',
		)!;
		if (!scroller) throw new Error("Conversation did not mount");
		scroller.scrollTop = 0;
		scroller.dispatchEvent(new Event("scroll"));
		await frame();
		await frame();
		const original = Element.prototype.getBoundingClientRect;
		let anchorReads = 0;
		Element.prototype.getBoundingClientRect = function () {
			if (this.hasAttribute("data-chat-scroll-anchor")) anchorReads++;
			return original.call(this);
		};
		const frameGaps: number[] = [];
		let last = await frame();
		try {
			for (let index = 0; index < 120; index++) {
				scroller.scrollTop =
					((scroller.scrollHeight - scroller.clientHeight) * index) / 119;
				scroller.dispatchEvent(new Event("scroll"));
				const now = await frame();
				frameGaps.push(now - last);
				last = now;
			}
		} finally {
			Element.prototype.getBoundingClientRect = original;
		}
		return {
			loadedTurns: 250,
			anchorReads,
			mountedTurns: scroller.querySelectorAll("[data-chat-scroll-anchor]")
				.length,
			maxFrameGapMs: Math.max(...frameGaps),
			frameGaps,
		};
	},
	/**
	 * Split preparation from collection so Playwright produces real keyboard input
	 * against Lexical, rather than this fixture simulating an editor event.
	 */
	async prepareFileCompletion() {
		const filePaths = Array.from(
			{ length: 5_000 },
			(_, index) => `packages/workspace-${index % 100}/src/components/ChatComposer${index}.tsx`,
		);
		await prepareComposerCompletion(<ChatComposer autoFocus={false} filePaths={filePaths} onSend={() => undefined} />);
		return { candidatePaths: filePaths.length };
	},
	async finishFileCompletion() {
		return {
			candidatePaths: 5_000,
			...(await finishComposerCompletion()),
		};
	},
	async prepareLongHistoryTyping() {
		useUiStore.setState({
			inspectorSessions: { "ao-long": { isOpen: false, view: "summary" } },
		});
		await prepareComposerCompletion(
			<ChatWorkspace
				snapshot={chatFixtureLongHistory(250)}
				sessionTitle="AO responsiveness benchmark"
			/>,
		);
		const timeline = document.querySelector<HTMLElement>('[role="log"][aria-label="Conversation"]');
		if (!timeline) throw new Error("Long conversation did not mount");
		return {
			mountedTurns: timeline.querySelectorAll("[data-chat-scroll-anchor]").length,
			domElementCount: document.querySelectorAll("*").length,
		};
	},
	async finishLongHistoryTyping() {
		return finishComposerCompletion();
	},
	async prepareSkillCompletion() {
		const skills: ChatSkill[] = Array.from({ length: 5_000 }, (_, index) => ({
			name: `chatcomposer-${index}`,
			displayName: `Chat composer ${index}`,
			description: `Benchmark command ${index} for testing a large completion catalog.`,
			source: "workspace",
		}));
		await prepareComposerCompletion(<ChatComposer autoFocus={false} onSend={() => undefined} skills={skills} />);
		return { candidateSkills: skills.length };
	},
	async finishSkillCompletion() {
		return {
			candidateSkills: 5_000,
			...(await finishComposerCompletion()),
		};
	},
	async prepareLocalEcho() {
		localEchoSample?.observer.disconnect();
		if (localEchoSample) window.fetch = localEchoSample.originalFetch;
		client.clear();
		const originalFetch = window.fetch;
		let resolveRequest!: () => void;
		const requestFinished = new Promise<void>((resolve) => {
			resolveRequest = resolve;
		});
		const sample = {
			requestPending: true,
			requestFinished,
			observer: new MutationObserver(() => {
				if (
					sample.startedAt !== undefined &&
					sample.rowCommittedAt === undefined &&
					document.body.textContent?.includes(LOCAL_ECHO_TEXT)
				) {
					sample.rowCommittedAt = performance.now();
					requestAnimationFrame(() => {
						sample.firstPaintAt = performance.now();
					});
				}
			}),
			originalFetch,
			startedAt: undefined as number | undefined,
			rowCommittedAt: undefined as number | undefined,
			firstPaintAt: undefined as number | undefined,
		};
		sample.observer.observe(document.body, { childList: true, subtree: true, characterData: true });
		window.fetch = async (input, init) => {
			const url = typeof input === "string" ? input : input instanceof URL ? input.toString() : input.url;
			if (!url.includes("/conversation/messages")) return originalFetch(input, init);
			await wait(600);
			sample.requestPending = false;
			resolveRequest();
			return new Response(JSON.stringify({ turnId: "performance-local-echo" }), {
				status: 200,
				headers: { "content-type": "application/json" },
			});
		};
		localEchoSample = sample;
		setApiBaseUrl(window.location.origin);
		render(<LocalEchoWorkload />);
		await frame();
		return { daemonDelayMs: 600 };
	},
	async finishLocalEcho() {
		const sample = localEchoSample;
		if (!sample || sample.startedAt === undefined) throw new Error("Local-echo benchmark was not started");
		while (sample.firstPaintAt === undefined && performance.now() - sample.startedAt < 5_000) await frame();
		const result = {
			localRowCommitMs: sample.rowCommittedAt === undefined ? null : sample.rowCommittedAt - sample.startedAt,
			localRowFirstPaintMs: sample.firstPaintAt === undefined ? null : sample.firstPaintAt - sample.startedAt,
			httpWasPendingAtFirstPaint: sample.requestPending,
			sendError: sample.sendError,
			rowPresentAtEnd: document.body.textContent?.includes(LOCAL_ECHO_TEXT) ?? false,
		};
		await sample.requestFinished;
		sample.observer.disconnect();
		window.fetch = sample.originalFetch;
		localEchoSample = undefined;
		return result;
	},
};
(
	window as unknown as { performanceHarness: typeof performanceHarness }
).performanceHarness = performanceHarness;
