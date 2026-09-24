import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo, type ReactNode } from "react";
import { useCloudCp } from "../../hooks/useCloudCp";
import type { CloudCpClientEvent } from "../../lib/cloud-cp";
import type { ConversationItem, ConversationMessage, ConversationSnapshot, ConversationTurn } from "../../types/conversation";
import type { WorkspaceSession } from "../../types/workspace";
import { ChatWorkspace } from "./ChatWorkspace";

type EventPayload = {
	attempt?: unknown;
	clientMessageId?: unknown;
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

export function appendCloudEvents(existing: CloudCpClientEvent[], incoming: CloudCpClientEvent[]): CloudCpClientEvent[] {
	const lastSequence = existing.at(-1)?.sequence ?? 0;
	return [...existing, ...incoming.filter((event) => event.sequence > lastSequence)];
}

/** Builds the shared ChatWorkspace projection from Cloud's durable event log. */
export function toSnapshot(session: WorkspaceSession, events: CloudCpClientEvent[]): ConversationSnapshot {
	const turns = new Map<string, ConversationTurn>();
	const assistant = new Map<string, ConversationMessage>();
	const items: ConversationItem[] = [];
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
				turnId: turnID, role: "user", origin: "human", text, streaming: false, delivery: "accepted", createdAt: event.createdAt,
			});
			continue;
		}
		if (event.type === "chat.turn_steered") {
			const clientMessageID = eventPayload(event).clientMessageId;
			items.push({
				kind: "activity", id: `cloud-steer-${event.sequence}`, turnId: turnID, sequence: event.sequence,
				revision: 1, activityKind: "system", status: "completed", summary: text,
				detail: { event: "steer", text, origin: "human", clientMessageId: typeof clientMessageID === "string" ? clientMessageID : undefined },
				createdAt: event.createdAt,
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
	const { client, ready } = useCloudCp();
	const queryClient = useQueryClient();
	const eventsQuery = useQuery({
		queryKey: ["cloud-chat-events", cloud?.orgId ?? "", session.id],
		enabled: Boolean(cloud && ready),
		refetchInterval: 1_000,
		queryFn: async () => {
			if (!cloud) return [] as CloudCpClientEvent[];
			const previous = queryClient.getQueryData<CloudCpClientEvent[]>(["cloud-chat-events", cloud.orgId, session.id]) ?? [];
			let events = previous;
			let after = previous.at(-1)?.sequence ?? 0;
			for (;;) {
				const page = await client.listChatEvents(cloud.orgId, session.id, { after, limit: 500 });
				events = appendCloudEvents(events, page.events);
				if (!page.hasMore) return events;
				if (page.nextAfter <= after) throw new Error("Cloud event cursor did not advance.");
				after = page.nextAfter;
			}
		},
	});
	const invalidate = () =>
		queryClient.invalidateQueries({ queryKey: ["cloud-chat-events", cloud?.orgId ?? "", session.id] });
	const send = useMutation({
		mutationFn: async ({ text, clientMessageId }: { text: string; clientMessageId?: string }) => {
			if (!cloud) throw new Error("Cloud session context is unavailable.");
			return client.sendSessionMessage(cloud.orgId, session.id, { text }, { idempotencyKey: clientMessageId });
		},
		onSuccess: () => void invalidate(),
	});
	const snapshot = useMemo(() => toSnapshot(session, eventsQuery.data ?? []), [eventsQuery.data, session]);
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
	const steer = useMutation({
		mutationFn: async ({ text, clientMessageId }: { text: string; clientMessageId?: string }) => {
			if (!cloud || !activeTurn) throw new Error("There is no active Cloud turn to steer.");
			return client.steerTurn(cloud.orgId, session.id, activeTurn.id, { text }, { idempotencyKey: clientMessageId });
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
			onSend={(text, _attachments, clientMessageId) => send.mutateAsync({ text, clientMessageId })}
			onSteer={activeTurn ? (text, _attachments, clientMessageId) => steer.mutateAsync({ text, clientMessageId }).then(() => ({ status: "accepted" as const })) : undefined}
			session={session}
			sessionRole={session.kind}
			sessionTabAction={sessionTabAction}
			sessionTitle={session.title}
		/>
	);
}
