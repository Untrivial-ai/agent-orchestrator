import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo, useState, type ReactNode } from "react";
import { useCloudCp } from "../../hooks/useCloudCp";
import type { CloudCpClientEvent } from "../../lib/cloud-cp";
import type { ChatModel, ConversationMessage, ConversationSnapshot, ConversationTurn, TurnSettings } from "../../types/conversation";
import type { WorkspaceSession } from "../../types/workspace";
import { ChatWorkspace } from "./ChatWorkspace";

type EventPayload = {
	attempt?: unknown;
	error?: unknown;
	text?: unknown;
	turnId?: unknown;
};

function eventPayload(event: CloudCpClientEvent): EventPayload {
	return event.payload && typeof event.payload === "object" ? (event.payload as EventPayload) : {};
}

function eventText(event: CloudCpClientEvent): string | undefined {
	const text = eventPayload(event).text;
	return typeof text === "string" && text.trim() !== "" ? text : undefined;
}

function eventTurnID(event: CloudCpClientEvent): string | undefined {
	const turnID = eventPayload(event).turnId;
	return typeof turnID === "string" && turnID !== "" ? turnID : undefined;
}

const CLOUD_MODELS: Record<string, ChatModel[]> = {
	codex: ["gpt-5.6-sol", "gpt-5.6-luna", "gpt-5.5", "gpt-5.4", "gpt-5.4-mini"].map((id, index) => ({ id, displayName: id, default: index === 0, efforts: ["low", "medium", "high", "xhigh"], defaultEffort: "medium" })),
	"claude-code": ["claude-sonnet-4-5", "claude-opus-4-1", "claude-haiku-4-5"].map((id, index) => ({ id, displayName: id, default: index === 0 })),
	cursor: ["auto", "composer-1", "gpt-5"].map((id, index) => ({ id, displayName: id, default: index === 0 })),
};

/** Builds the shared ChatWorkspace projection from Cloud's durable event log. */
function toSnapshot(session: WorkspaceSession, events: CloudCpClientEvent[]): ConversationSnapshot {
	const turns = new Map<string, ConversationTurn>();
	const assistant = new Map<string, ConversationMessage>();
	const items: ConversationMessage[] = [];
	for (const event of events) {
		const turnID = eventTurnID(event);
		if (turnID && !turns.has(turnID)) {
			turns.set(turnID, { id: turnID, state: "queued", requestedAt: event.createdAt });
		}
		if (turnID && event.type === "chat.turn_started") {
			const turn = turns.get(turnID)!;
			turn.state = "running";
			turn.startedAt = event.createdAt;
		}
		if (turnID && (event.type === "chat.turn_completed" || event.type === "chat.turn_interrupted" || event.type === "chat.turn_aborted")) {
			const turn = turns.get(turnID)!;
			turn.state = event.type === "chat.turn_completed" ? "completed" : event.type === "chat.turn_interrupted" ? "interrupted" : "failed";
			turn.completedAt = event.createdAt;
			const error = eventPayload(event).error;
			turn.errorMessage = typeof error === "string" ? error : undefined;
		}
		const text = eventText(event);
		if (!text) continue;
		if (event.type === "chat.user_message") {
			items.push({
				kind: "message", id: `cloud-event-${event.sequence}`, sequence: event.sequence, revision: 1,
				role: "user", origin: "human", text, streaming: false, delivery: "accepted", createdAt: event.createdAt,
			});
			continue;
		}
		if (event.type !== "chat.assistant_delta") continue;
		const assistantKey = turnID ?? `event-${event.sequence}`;
		const previous = assistant.get(assistantKey);
		if (previous) {
			previous.text += text;
			previous.revision += 1;
			continue;
		}
		const message: ConversationMessage = {
			kind: "message", id: `cloud-assistant-${assistantKey}`, turnId: turnID, sequence: event.sequence,
			revision: 1, role: "assistant", origin: "provider", text, streaming: true, createdAt: event.createdAt,
		};
		assistant.set(assistantKey, message);
		items.push(message);
	}
	for (const message of assistant.values()) {
		if (!message.turnId || turns.get(message.turnId)?.state !== "running") message.streaming = false;
	}
	const orderedTurns = [...turns.values()];
	const hasRunningTurn = orderedTurns.some((turn) => turn.state === "running");
	return {
		conversationId: `cloud:${session.id}`, sessionId: session.id, harness: session.provider, mode: "chat",
		controller: { state: hasRunningTurn ? "busy" : "ready" }, turns: orderedTurns, items,
		latestSequence: events.at(-1)?.sequence ?? 0, oldestSequence: events[0]?.sequence ?? 1,
		hasMoreBefore: false, settings: {},
	};
}

/**
 * The Cloud adapter deliberately renders the same ChatWorkspace as local AO.
 * Only the data/command transport differs: Cloud reads its durable event log
 * and posts messages to the control plane instead of calling the loopback daemon.
 */
export function CloudSessionChatSurface({
	session,
	headerActions,
	sessionTabAction,
	controllerTransitioning,
	newWorkDisabled,
	onConversationWorkChange,
}: {
	session: WorkspaceSession;
	headerActions?: ReactNode;
	sessionTabAction?: ReactNode;
	controllerTransitioning?: boolean;
	newWorkDisabled?: boolean;
	onConversationWorkChange?: (state: {
		controllerBusy: boolean;
		hasRunningTurn: boolean;
		queuedTurnCount: number;
	}) => void;
}) {
	const cloud = session.cloud;
	const models = CLOUD_MODELS[session.provider] ?? [];
	const [selectedModel, setSelectedModel] = useState(
		session.model ?? models.find((model) => model.default)?.id,
	);
	const selectedModelInfo = models.find((model) => model.id === selectedModel) ?? models.find((model) => model.default);
	const [selectedEffort, setSelectedEffort] = useState(
		session.reasoningEffort ?? selectedModelInfo?.defaultEffort,
	);
	const { client, ready } = useCloudCp();
	const queryClient = useQueryClient();
	const eventsQuery = useQuery({
		queryKey: ["cloud-chat-events", cloud?.orgId ?? "", session.id],
		enabled: Boolean(cloud && ready),
		refetchInterval: 1_000,
		queryFn: async () => {
			if (!cloud) return [] as CloudCpClientEvent[];
			const events: CloudCpClientEvent[] = [];
			let after = 0;
			for (;;) {
				const page = await client.listChatEvents(cloud.orgId, session.id, { after, limit: 500 });
				events.push(...page.events);
				if (!page.hasMore) return events;
				after = page.nextAfter;
			}
		},
	});
	const invalidate = () =>
		queryClient.invalidateQueries({ queryKey: ["cloud-chat-events", cloud?.orgId ?? "", session.id] });
	const send = useMutation({
		mutationFn: async (text: string) => {
			if (!cloud) throw new Error("Cloud session context is unavailable.");
			return client.sendSessionMessage(cloud.orgId, session.id, { text, model: selectedModel, reasoningEffort: selectedEffort });
		},
		onSuccess: () => void invalidate(),
	});
	const snapshot = useMemo(
		() => ({ ...toSnapshot(session, eventsQuery.data ?? []), settings: { model: selectedModel, reasoningEffort: selectedEffort } }),
		[eventsQuery.data, selectedEffort, selectedModel, session],
	);
	const activeTurn = snapshot.turns.find((turn) => turn.state === "running");
	const queuedTurnCount = snapshot.turns.filter((turn) => turn.state === "queued").length;
	useEffect(() => {
		onConversationWorkChange?.({
			controllerBusy: snapshot.controller.state === "busy",
			hasRunningTurn: Boolean(activeTurn),
			queuedTurnCount,
		});
	}, [activeTurn, onConversationWorkChange, queuedTurnCount, snapshot.controller.state]);
	const interrupt = useMutation({
		mutationFn: async () => {
			if (!cloud || !activeTurn) return;
			await client.cancelTurn(cloud.orgId, session.id, activeTurn.id);
		},
		onSettled: () => void invalidate(),
	});

	return (
		<ChatWorkspace
			snapshot={snapshot}
			busy={send.isPending}
			controllerTransitioning={controllerTransitioning}
			newWorkDisabled={newWorkDisabled}
			commandError={
				eventsQuery.error instanceof Error
					? eventsQuery.error.message
					: send.error instanceof Error
						? send.error.message
						: undefined
			}
			headerActions={headerActions}
			onInterrupt={activeTurn ? () => interrupt.mutate() : undefined}
			onSend={(text) => send.mutateAsync(text)}
			models={models}
			onChooseSettings={(settings: TurnSettings) => {
				setSelectedModel(settings.model);
				setSelectedEffort(settings.reasoningEffort);
			}}
			session={session}
			sessionRole={session.kind}
			sessionTabAction={sessionTabAction}
			sessionTitle={session.title}
		/>
	);
}
