import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { useCloudCp } from "../../hooks/useCloudCp";
import type { CloudCpClient, CloudCpClientEvent } from "../../lib/cloud-cp";
import { CloudCpError } from "../../lib/cloud-cp/errors";
import type { ConversationItem, ConversationMessage, ConversationSnapshot, ConversationTurn, TurnSettings } from "../../types/conversation";
import type { WorkspaceSession } from "../../types/workspace";
import { ChatWorkspace } from "./ChatWorkspace";

type EventPayload = {
	attempt?: unknown;
	clientMessageId?: unknown;
	error?: unknown;
	text?: unknown;
	turnId?: unknown;
};

function readCloudTurnSettings(key: string): TurnSettings {
	try {
		const saved = JSON.parse(localStorage.getItem(key) ?? "null");
		if (!saved || typeof saved !== "object") return {};
		return {
			model: typeof saved.model === "string" ? saved.model : undefined,
			reasoningEffort: typeof saved.reasoningEffort === "string" ? saved.reasoningEffort : undefined,
		};
	} catch {
		return {};
	}
}

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

export async function loadCloudChatEvents(
	client: Pick<CloudCpClient, "listChatEvents">,
	orgId: string,
	sessionId: string,
	existing: CloudCpClientEvent[],
): Promise<CloudCpClientEvent[]> {
	let events = existing;
	let after = existing.at(-1)?.sequence ?? 0;
	for (;;) {
		const page = await client.listChatEvents(orgId, sessionId, { after, limit: 500 });
		events = appendCloudEvents(events, page.events);
		if (!page.hasMore) return events;
		if (page.nextAfter <= after) throw new Error("Cloud event cursor did not advance.");
		after = page.nextAfter;
	}
}

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
	const settingsKey = `cloud-chat-settings:${cloud?.orgId ?? ""}:${session.id}`;
	const [selected, setSelected] = useState(() => ({ key: settingsKey, settings: readCloudTurnSettings(settingsKey) }));
	const settings = selected.key === settingsKey ? selected.settings : readCloudTurnSettings(settingsKey);
	const settingsRef = useRef({ key: settingsKey, settings });
	if (settingsRef.current.key !== settingsKey) settingsRef.current = { key: settingsKey, settings };
	const modelsQuery = useQuery({
		queryKey: ["cloud-chat-models", cloud?.orgId ?? "", session.id],
		enabled: Boolean(cloud && ready && session.provider === "codex"),
		staleTime: 5 * 60 * 1000,
		retry: false,
		queryFn: async ({ signal }) => {
			const orgId = cloud!.orgId;
			try {
				return await client.listChatModels(orgId, session.id, { signal });
			} catch (error) {
				if (!(error instanceof CloudCpError) || error.code !== "WORKER_UNAVAILABLE") throw error;
			}
			// A paused sandbox cannot answer a worker-backed catalog request. Wake
			// this session and wait for its worker before hiding the model picker.
			await client.resumeSession(orgId, session.id, { signal });
			for (let attempt = 0; attempt < 20; attempt++) {
				await new Promise((resolve) => setTimeout(resolve, 500));
				try {
					return await client.listChatModels(orgId, session.id, { signal });
				} catch (error) {
					if (!(error instanceof CloudCpError) || error.code !== "WORKER_UNAVAILABLE" || attempt === 19) throw error;
				}
			}
			throw new Error("The Cloud worker did not become available.");
		},
	});
	const eventsQuery = useQuery({
		queryKey: ["cloud-chat-events", cloud?.orgId ?? "", session.id],
		enabled: Boolean(cloud && ready),
		refetchInterval: 1_000,
		queryFn: async () => {
			if (!cloud) return [] as CloudCpClientEvent[];
			const previous = queryClient.getQueryData<CloudCpClientEvent[]>(["cloud-chat-events", cloud.orgId, session.id]) ?? [];
			return loadCloudChatEvents(client, cloud.orgId, session.id, previous);
		},
	});
	const invalidate = () =>
		queryClient.invalidateQueries({ queryKey: ["cloud-chat-events", cloud?.orgId ?? "", session.id] });
	const send = useMutation({
		mutationFn: async ({ text, clientMessageId }: { text: string; clientMessageId?: string }) => {
			if (!cloud) throw new Error("Cloud session context is unavailable.");
			const selectedSettings = settingsRef.current.key === settingsKey ? settingsRef.current.settings : {};
			return client.sendSessionMessage(cloud.orgId, session.id, {
				text,
				...(selectedSettings.model ? { model: selectedSettings.model } : {}),
				...(selectedSettings.reasoningEffort ? { reasoningEffort: selectedSettings.reasoningEffort } : {}),
			}, { idempotencyKey: clientMessageId });
		},
		onSuccess: () => void invalidate(),
	});
	const snapshot = useMemo(() => ({
		...toSnapshot(session, eventsQuery.data ?? []), settings,
	}), [eventsQuery.data, session, settings]);
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
			models={modelsQuery.data?.models ?? []}
			onChooseSettings={(next) => {
				settingsRef.current = { key: settingsKey, settings: next };
				setSelected({ key: settingsKey, settings: next });
				try {
					localStorage.setItem(settingsKey, JSON.stringify(next));
				} catch {
					// The choice still applies for this mounted session when storage is unavailable.
				}
			}}
			busy={send.isPending}
			controllerTransitioning={controllerTransitioning}
			newWorkDisabled={newWorkDisabled}
			commandError={
				interrupt.error instanceof Error
					? interrupt.error.message
					: steer.error instanceof Error
						? steer.error.message
						: eventsQuery.error instanceof Error
							? eventsQuery.error.message
						: send.error instanceof Error
							? send.error.message
							: modelsQuery.error instanceof Error
								? `Model choices unavailable: ${modelsQuery.error.message}`
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
