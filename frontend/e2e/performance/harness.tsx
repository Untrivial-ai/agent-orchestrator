/// <reference path="../../src/renderer/global.d.ts" />
// Browser-only workload fixture: real AO rendering, deterministic synthetic data.
// This entry is never imported by the application or included in its build.
import { type ReactNode } from "react";
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
import { ChatWorkspace } from "../../src/renderer/components/chat/ChatWorkspace";
import { chatFixtureLongHistory } from "../../src/renderer/lib/chat-fixture";
import { highlight } from "../../src/renderer/lib/code-highlight";
import { createEventTransport } from "../../src/renderer/lib/event-transport";
import { useUiStore } from "../../src/renderer/stores/ui-store";
import { setApiBaseUrl } from "../../src/renderer/lib/api-client";
import type { ConversationMessage } from "../../src/renderer/types/conversation";
import "../../src/renderer/styles.css";

const root = createRoot(document.getElementById("performance-root")!);
const client = new QueryClient({
	defaultOptions: { queries: { retry: false } },
});
const wait = (ms: number) =>
	new Promise<void>((resolve) => setTimeout(resolve, ms));
const frame = () =>
	new Promise<number>((resolve) => requestAnimationFrame(resolve));
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

export const performanceHarness = {
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
};
(
	window as unknown as { performanceHarness: typeof performanceHarness }
).performanceHarness = performanceHarness;
