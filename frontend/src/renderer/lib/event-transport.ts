import type { QueryClient } from "@tanstack/react-query";
import { aoBridge } from "./bridge";
import { getApiBaseUrl, hasTrustedApiBaseUrl, subscribeApiBaseUrl } from "./api-client";
import { setEventsConnectionState } from "./events-connection";
import { workspaceQueryKey } from "../hooks/useWorkspaceQuery";
import { sessionScmSummaryQueryKey } from "../hooks/useSessionScmSummary";
import { conversationQueryKey } from "../hooks/useConversation";
import { agentSwitchesQueryRoot } from "../hooks/useAgentSwitches";
import { sessionUsageQueryRoot } from "../hooks/useSessionUsageSummaries";

export type EventTransport = {
	connect: () => () => void;
};

const INVALIDATE_DEBOUNCE_MS = 150;
// How long to wait before rebuilding an EventSource the browser gave up on
// (readyState CLOSED — e.g. the daemon answered with a non-SSE response).
const SSE_RETRY_MS = 5_000;
// EventSource.CLOSED, referenced numerically so test stubs without the static
// constants still work.
const EVENTSOURCE_CLOSED = 2;

// CDC event types the daemon pushes over the SSE stream (see
// backend/internal/cdc/event.go). The SSE writer tags each frame with
// `event: <type>`, so named events bypass EventSource.onmessage and must be
// subscribed explicitly. Every one of these can change the project/session list
// the sidebar renders, so they all trigger a (debounced) workspace refetch.
const CDC_EVENT_TYPES = [
	"session_created",
	"session_updated",
	"pr_created",
	"pr_updated",
	"pr_check_recorded",
	"pr_session_changed",
	"pr_review_thread_added",
	"pr_review_thread_resolved",
] as const;

/**
 * Wires live server state into the TanStack Query cache. Two sources feed it:
 *   - daemon lifecycle over Electron IPC (coming up/down changes session availability)
 *   - the backend CDC stream over SSE (project/session/PR changes)
 * Both invalidate the ["workspaces"] query so the UI refetches. Invalidations are
 * debounced because a single user action can emit a burst of CDC events.
 */
export function createEventTransport(queryClient: QueryClient): EventTransport {
	return {
		connect() {
			let debounce: ReturnType<typeof setTimeout> | undefined;
			const pendingConversationSessions = new Set<string>();
			const pendingInterfaceTransitionSessions = new Set<string>();
			let workspaceInvalidationPending = false;
			let retryTimer: ReturnType<typeof setTimeout> | undefined;
			let source: EventSource | undefined;
			let sourceBaseUrl: string | undefined;
			const refreshWorkspaces = (event?: Event) => {
				let conversationOnly = false;
				if (event && "data" in event) {
					try {
						const decoded = JSON.parse(String((event as MessageEvent).data)) as {
							sessionId?: unknown;
							payload?: unknown;
						};
						// The SSE endpoint sends the complete durable CDC event. Routing
						// fields such as sessionId live on that envelope, while trigger-built
						// details such as conversationId live inside its payload. Do not
						// mistake the payload for the entire event: doing so refreshes the
						// sidebar but leaves a Chat timeline frozen on its pre-turn snapshot.
						const payload =
							typeof decoded.payload === "object" && decoded.payload !== null
								? (decoded.payload as {
										conversationId?: unknown;
										interfaceTransitionId?: unknown;
								  })
								: undefined;
						if (
							typeof decoded.sessionId === "string" &&
							decoded.sessionId &&
							typeof payload?.interfaceTransitionId === "string" &&
							payload.interfaceTransitionId
						) {
							pendingInterfaceTransitionSessions.add(decoded.sessionId);
						}
						if (
							typeof decoded.sessionId === "string" &&
							decoded.sessionId &&
							typeof payload?.conversationId === "string" &&
							payload.conversationId
						) {
							pendingConversationSessions.add(decoded.sessionId);
							conversationOnly = true;
						}
					} catch {
						// A malformed CDC payload still invalidates workspaces; it simply
						// cannot target a conversation cache precisely.
					}
				}
				if (!conversationOnly) workspaceInvalidationPending = true;
				if (debounce) clearTimeout(debounce);
				debounce = setTimeout(() => {
					if (workspaceInvalidationPending) {
						void queryClient.invalidateQueries({ queryKey: workspaceQueryKey });
						void queryClient.invalidateQueries({ queryKey: agentSwitchesQueryRoot });
						void queryClient.invalidateQueries({ queryKey: sessionScmSummaryQueryKey() });
						void queryClient.invalidateQueries({ queryKey: sessionUsageQueryRoot });
						workspaceInvalidationPending = false;
					}
					for (const sessionId of pendingConversationSessions) {
						void queryClient.invalidateQueries({ queryKey: conversationQueryKey(sessionId) });
					}
					pendingConversationSessions.clear();
					for (const sessionId of pendingInterfaceTransitionSessions) {
						void queryClient.invalidateQueries({
							queryKey: ["session-interface-transition", sessionId],
						});
					}
					pendingInterfaceTransitionSessions.clear();
				}, INVALIDATE_DEBOUNCE_MS);
			};

			const scheduleRetry = () => {
				if (retryTimer) return;
				retryTimer = setTimeout(() => {
					retryTimer = undefined;
					connectSource();
				}, SSE_RETRY_MS);
			};

			const connectSource = () => {
				// EventSource is unavailable in jsdom (tests) and some preview surfaces; guard it.
				if (typeof EventSource === "undefined") return;
				if (!hasTrustedApiBaseUrl()) {
					source?.close();
					source = undefined;
					sourceBaseUrl = undefined;
					setEventsConnectionState("disconnected");
					return;
				}
				const baseUrl = getApiBaseUrl();
				// Keep a still-usable source on the same base URL; replace one the
				// browser abandoned (CLOSED) or one bound to a stale port.
				if (source && sourceBaseUrl === baseUrl && source.readyState !== EVENTSOURCE_CLOSED) return;
				source?.close();
				source = undefined;
				sourceBaseUrl = baseUrl;
				try {
					source = new EventSource(`${baseUrl.replace(/\/+$/, "")}/api/v1/events`);
					source.onopen = () => {
						setEventsConnectionState("connected");
						// Events emitted during the gap were lost; refetch once on (re)open.
						refreshWorkspaces();
					};
					source.onerror = () => {
						// While readyState is CONNECTING the browser retries on its own;
						// either way the stream is not delivering, so surface it instead
						// of looping silently against a dead daemon.
						setEventsConnectionState("disconnected");
						if (source?.readyState === EVENTSOURCE_CLOSED) scheduleRetry();
					};
					source.onmessage = refreshWorkspaces; // unnamed events, if any
					for (const type of CDC_EVENT_TYPES) {
						source.addEventListener(type, refreshWorkspaces);
					}
					// EventSource auto-reconnects and resumes via Last-Event-ID while
					// CONNECTING; scheduleRetry only covers the terminal CLOSED state.
				} catch {
					source = undefined;
				}
			};

			const removeDaemonListener = aoBridge.daemon.onStatus(() => {
				connectSource();
				refreshWorkspaces();
			});
			// Rebind when the daemon comes back on a different port, independent of
			// status-event ordering.
			const removeBaseUrlListener = subscribeApiBaseUrl(connectSource);
			connectSource();

			return () => {
				if (debounce) clearTimeout(debounce);
				if (retryTimer) clearTimeout(retryTimer);
				removeDaemonListener();
				removeBaseUrlListener();
				source?.close();
				setEventsConnectionState("idle");
			};
		},
	};
}
