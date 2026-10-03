import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { useCloudCp } from "../../hooks/useCloudCp";
import type { CloudCpClient, CloudCpClientEvent } from "../../lib/cloud-cp";
import { CloudCpError } from "../../lib/cloud-cp/errors";
import type { ApprovalMode, ConversationActivity, ConversationItem, ConversationMessage, ConversationSnapshot, ConversationTurn, DiffFile, FileChangeFile, TurnSettings } from "../../types/conversation";
import type { WorkspaceSession } from "../../types/workspace";
import { ChatWorkspace } from "./ChatWorkspace";

type EventPayload = {
	attempt?: unknown;
	clientMessageId?: unknown;
	requestId?: unknown;
	decision?: unknown;
	decisions?: unknown;
	summary?: unknown;
	toolKind?: unknown;
	steering?: unknown;
	error?: unknown;
	text?: unknown;
	origin?: unknown;
	senderLabel?: unknown;
	displayText?: unknown;
	turnId?: unknown;
	activity?: unknown;
	itemId?: unknown;
};

type CloudTurnSettings = TurnSettings;

function allowedApprovalModes(harness: string, ceiling?: "read-only" | "standard" | "trusted"): ApprovalMode[] {
	if (ceiling === "read-only" || !ceiling) return [];
	if (ceiling === "standard") return harness === "codex" ? ["accept-edits", "auto"] : ["default", "accept-edits", "auto"];
	return ["default", "accept-edits", "auto", "bypass-permissions"];
}

export function readCloudTurnSettings(key: string): CloudTurnSettings {
	try {
		const saved = JSON.parse(localStorage.getItem(key) ?? "null");
		if (!saved || typeof saved !== "object") return {};
		return {
			model: typeof saved.model === "string" ? saved.model : undefined,
			reasoningEffort: typeof saved.reasoningEffort === "string" ? saved.reasoningEffort : undefined,
			approvalMode: saved.approvalMode === "default" || saved.approvalMode === "accept-edits" || saved.approvalMode === "auto" || saved.approvalMode === "bypass-permissions" ? saved.approvalMode : undefined,
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

function cloudTurnDiff(patch: string): (DiffFile & { patch: string })[] {
	const files: (DiffFile & { patch: string })[] = [];
	let current: (DiffFile & { patch: string }) | undefined;
	let inHunk = false;
	for (const line of patch.split("\n")) {
		if (line.startsWith("diff --git ")) {
			if (current) files.push(current);
			const path = line.match(/^diff --git a\/.+ b\/(.+)$/)?.[1];
			current = path ? { path, status: "modified", additions: 0, deletions: 0, patch: line + "\n" } : undefined;
			inHunk = false;
		} else if (current) {
			current.patch += line + "\n";
			if (line.startsWith("new file mode")) current.status = "added";
			else if (line.startsWith("deleted file mode")) current.status = "deleted";
			else if (line.startsWith("rename from ")) { current.status = "renamed"; current.oldPath = line.slice(12); }
			else if (line.startsWith("rename to ")) { current.status = "renamed"; current.path = line.slice(10); }
			else if (line.startsWith("+++ b/")) current.path = line.slice(6);
			else if (line.startsWith("@@")) inHunk = true;
			else if (inHunk && line.startsWith("+")) current.additions++;
			else if (inHunk && line.startsWith("-")) current.deletions++;
		}
	}
	if (current) files.push(current);
	return files;
}

export function toSnapshot(session: WorkspaceSession, events: CloudCpClientEvent[]): ConversationSnapshot {
	const turns = new Map<string, ConversationTurn>();
	const assistant = new Map<string, ConversationMessage>();
	const approvals = new Map<string, ConversationActivity>();
	const activities = new Map<string, ConversationActivity>();
	const turnPatches = new Map<string, (DiffFile & { patch: string })[]>();
	const steerableTurns = new Set<string>();
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
		if (turnID && event.type === "chat.turn_capabilities" && eventPayload(event).steering === true) {
			steerableTurns.add(turnID);
		}
		if (turnID && (event.type === "chat.turn_completed" || event.type === "chat.turn_interrupted" || event.type === "chat.turn_aborted")) {
			const turn = turns.get(turnID)!;
			turn.state = event.type === "chat.turn_completed" ? "completed" : event.type === "chat.turn_interrupted" ? "interrupted" : "failed";
			turn.completedAt = event.createdAt;
			const error = eventPayload(event).error;
			turn.errorMessage = typeof error === "string" ? error : undefined;
		}
		if (event.type === "chat.approval_requested") {
			const payload = eventPayload(event);
			const requestId = typeof payload.requestId === "string" ? payload.requestId : undefined;
			const summary = typeof payload.summary === "string" ? payload.summary : "Permission required";
			const decisions = Array.isArray(payload.decisions) ? payload.decisions.filter((value): value is { id: string; label: string; kind?: "allow_once" | "allow_always" | "reject_once" | "reject_always" } =>
				Boolean(value && typeof value === "object" && typeof value.id === "string" && typeof value.label === "string")) : [];
			if (requestId) {
				const activity: ConversationActivity = {
					kind: "activity", id: `cloud-approval-${requestId}`, turnId: turnID, sequence: event.sequence,
					revision: 1, activityKind: "approval", status: "pending", summary, requestId,
					decisions, detail: { method: "ACP session/request_permission", toolKind: typeof payload.toolKind === "string" ? payload.toolKind : undefined },
					createdAt: event.createdAt,
				};
				approvals.set(requestId, activity);
				items.push(activity);
			}
			continue;
		}
		if (event.type === "chat.approval_decided") {
			const payload = eventPayload(event);
			const requestId = typeof payload.requestId === "string" ? payload.requestId : "";
			const activity = approvals.get(requestId);
			if (activity) {
				activity.status = "completed";
				activity.revision += 1;
				activity.detail = { ...activity.detail, decision: typeof payload.decision === "string" ? payload.decision : undefined };
			}
			continue;
		}
		if (turnID && event.type === "chat.activity") {
			const raw = eventPayload(event).activity;
			if (!raw || typeof raw !== "object") continue;
			const value = raw as Record<string, unknown>;
			if (typeof value.id !== "string" || typeof value.kind !== "string") continue;
			const detail = value.detail && typeof value.detail === "object" ? value.detail as Record<string, unknown> : {};
			if (value.kind === "turn_diff") {
				if (typeof detail.diff === "string") {
					const files = cloudTurnDiff(detail.diff);
					turns.get(turnID)!.diff = { files, truncated: detail.truncated === true };
					turnPatches.set(turnID, files);
				}
				continue;
			}
			if (!["command", "file_change", "reasoning", "plan", "mcp_tool"].includes(value.kind)) continue;
			const key = `${turnID}:${value.id}`;
			const previous = activities.get(key);
			if (previous) {
				previous.status = value.status === "completed" || value.status === "failed" ? value.status : "running";
				previous.summary = typeof value.summary === "string" ? value.summary : previous.summary;
				previous.detail = { ...previous.detail, ...detail };
				if (typeof detail.textDelta === "string") previous.detail.text = (previous.detail.text ?? "") + detail.textDelta;
				if (typeof detail.outputDelta === "string") {
					previous.detail.output = (previous.detail.output ?? "") + detail.outputDelta;
					previous.detail.outputSource = "stream";
					previous.detail.outputMayBePartial = true;
				}
				previous.revision++;
			} else {
				const activity: ConversationActivity = {
					kind: "activity", id: `cloud-activity-${key}`, turnId: turnID, sequence: event.sequence,
					revision: 1, activityKind: value.kind as ConversationActivity["activityKind"],
					status: value.status === "completed" || value.status === "failed" ? value.status : "running",
					summary: typeof value.summary === "string" ? value.summary : value.kind,
					detail: { ...detail, text: typeof detail.textDelta === "string" ? detail.textDelta : undefined,
						output: typeof detail.outputDelta === "string" ? detail.outputDelta : typeof detail.output === "string" ? detail.output : undefined },
					createdAt: event.createdAt,
				};
				activities.set(key, activity);
				items.push(activity);
			}
			continue;
		}
		const text = eventText(event);
		if (!text) continue;
		if (event.type === "chat.user_message") {
			const payload = eventPayload(event);
			const automation = payload.origin === "automation";
			items.push({
				kind: "message", id: `cloud-event-${event.sequence}`, sequence: event.sequence, revision: 1,
				turnId: turnID, role: "user", origin: automation ? "automation" : "human",
				text: automation && typeof payload.displayText === "string" ? payload.displayText : text,
				senderLabel: automation && typeof payload.senderLabel === "string" ? payload.senderLabel : undefined,
				streaming: false, delivery: "accepted", createdAt: event.createdAt,
			});
			continue;
		}
		if (event.type === "chat.turn_steered") {
			const clientMessageID = eventPayload(event).clientMessageId;
			items.push({
				kind: "activity", id: `cloud-steer-${event.sequence}`, turnId: turnID, sequence: event.sequence,
				revision: 1, activityKind: "system", status: "completed", summary: `Steered: ${text}`,
				detail: { event: "steer", text, origin: "human", clientMessageId: typeof clientMessageID === "string" ? clientMessageID : undefined },
				createdAt: event.createdAt,
			});
			continue;
		}
		if (event.type !== "chat.assistant_delta") continue;
		// Older Cloud workers persisted this Codex CLI status as assistant text.
		if (session.provider === "codex" && text.trim() === "Reading additional input from stdin...") continue;
		const itemID = eventPayload(event).itemId;
		const assistantKey = `${turnID ?? `event-${event.sequence}`}:${typeof itemID === "string" ? itemID : "reply"}`;
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
	for (const activity of activities.values()) {
		if (activity.activityKind !== "file_change" || !activity.turnId || !Array.isArray(activity.detail?.files)) continue;
		const files = activity.detail.files;
		if (!files.every((file): file is FileChangeFile => typeof file === "object" && file !== null && "path" in file)) continue;
		const patches = turnPatches.get(activity.turnId) ?? [];
		activity.detail.files = files.map((file) => {
			const match = patches.find((patch) => patch.path === file.path);
			return match ? { ...file, patch: file.patch ?? match.patch } : file;
		});
	}
	for (const activity of activities.values()) {
		if (activity.status === "running" && activity.turnId && turns.get(activity.turnId)?.state !== "running") {
			activity.status = "recovered";
			activity.revision++;
		}
	}
	for (const message of assistant.values()) {
		if (!message.turnId || turns.get(message.turnId)?.state !== "running") message.streaming = false;
	}
	for (const approval of approvals.values()) {
		if (approval.status === "pending" && approval.turnId && ["completed", "interrupted", "failed"].includes(turns.get(approval.turnId)?.state ?? "")) {
			approval.status = "cancelled";
			approval.revision += 1;
		}
	}
	const orderedTurns = [...turns.values()];
	// The first durable send may wait briefly for the worker to claim it. It is
	// the active request; only later messages should appear in the queue.
	if (!orderedTurns.some((turn) => turn.state === "running")) {
		const next = orderedTurns.find((turn) => turn.state === "queued");
		if (next) next.state = "running";
	}
	const hasRunningTurn = orderedTurns.some((turn) => turn.state === "running");
	const activeTurn = orderedTurns.find((turn) => turn.state === "running");
	return {
		conversationId: `cloud:${session.id}`, sessionId: session.id, harness: session.provider, mode: "chat",
		controller: { state: hasRunningTurn ? "busy" : "ready" }, turns: orderedTurns, items,
		latestSequence: events.at(-1)?.sequence ?? 0, oldestSequence: events[0]?.sequence ?? 1,
		hasMoreBefore: false, settings: {}, capabilities: activeTurn && steerableTurns.has(activeTurn.id) ? ["steer"] : [],
	};
}

export function CloudSessionChatSurface({
	session,
	headerActions,
	sessionTabAction,
	onOpenFiles,
	onOpenFile,
	controllerTransitioning,
	newWorkDisabled,
	onConversationWorkChange,
}: {
	session: WorkspaceSession;
	headerActions?: ReactNode;
	sessionTabAction?: ReactNode;
	onOpenFiles?: () => void;
	onOpenFile?: (path: string) => void;
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
	const settingsKey = `cloud-chat-settings:${cloud?.orgId ?? ""}:${session.id}:${session.provider}`;
	const projectKey = `cloud-chat-approval:${cloud?.orgId ?? ""}:${session.workspaceId}:${session.provider}`;
	const approvalModes = useMemo(() => allowedApprovalModes(session.provider, cloud?.permissionMode), [session.provider, cloud?.permissionMode]);
	const readSettings = () => {
		const saved = readCloudTurnSettings(settingsKey);
		const remembered = readCloudTurnSettings(projectKey).approvalMode;
		const selectedMode = saved.approvalMode ?? remembered;
		return { ...saved, approvalMode: selectedMode && approvalModes.includes(selectedMode) ? selectedMode : approvalModes[0] };
	};
	const [selected, setSelected] = useState<{ key: string; settings: CloudTurnSettings }>(() => ({ key: settingsKey, settings: readSettings() }));
	const settings = selected.key === settingsKey ? selected.settings : readSettings();
	const settingsRef = useRef({ key: settingsKey, settings });
	if (settingsRef.current.key !== settingsKey) settingsRef.current = { key: settingsKey, settings };
	const manualSelectionRef = useRef({ key: settingsKey, version: 0 });
	if (manualSelectionRef.current.key !== settingsKey) manualSelectionRef.current = { key: settingsKey, version: 0 };
	const updateSettings = (next: CloudTurnSettings) => {
		manualSelectionRef.current.version++;
		settingsRef.current = { key: settingsKey, settings: next };
		setSelected({ key: settingsKey, settings: next });
		try {
			localStorage.setItem(settingsKey, JSON.stringify(next));
		} catch {
			// The choice still applies for this mounted session when storage is unavailable.
		}
	};
	const modelsQuery = useQuery({
		queryKey: ["cloud-chat-models", cloud?.orgId ?? "", session.id],
		enabled: Boolean(cloud && ready && session.provider === "codex"),
		// The TUI may change its native model while Chat is unmounted.
		staleTime: 0,
		refetchOnMount: "always",
		retry: false,
		queryFn: async ({ signal }) => {
			const selectionVersionAtFetch = manualSelectionRef.current.version;
			const orgId = cloud!.orgId;
			try {
				const catalog = await client.listChatModels(orgId, session.id, { signal });
				return { ...catalog, selectionVersionAtFetch };
			} catch (error) {
				if (!(error instanceof CloudCpError) || error.code !== "WORKER_UNAVAILABLE") throw error;
			}
			// A paused sandbox cannot answer a worker-backed catalog request. Wake
			// this session and wait for its worker before hiding the model picker.
			await client.resumeSession(orgId, session.id, { signal });
			for (let attempt = 0; attempt < 20; attempt++) {
				await new Promise((resolve) => setTimeout(resolve, 500));
				try {
					const catalog = await client.listChatModels(orgId, session.id, { signal });
					return { ...catalog, selectionVersionAtFetch };
				} catch (error) {
					if (!(error instanceof CloudCpError) || error.code !== "WORKER_UNAVAILABLE" || attempt === 19) throw error;
				}
			}
			throw new Error("The Cloud worker did not become available.");
		},
	});
	useEffect(() => {
		const native = modelsQuery.data;
		if (!modelsQuery.isFetchedAfterMount || !native || native.selectionVersionAtFetch !== manualSelectionRef.current.version || (!native.model && !native.reasoningEffort)) return;
		const current = settingsRef.current.key === settingsKey ? settingsRef.current.settings : readSettings();
		const next: CloudTurnSettings = {
			...current,
			...(native.model ? { model: native.model } : {}),
			...(native.model || native.reasoningEffort ? { reasoningEffort: native.reasoningEffort || undefined } : {}),
		};
		settingsRef.current = { key: settingsKey, settings: next };
		setSelected({ key: settingsKey, settings: next });
		try { localStorage.setItem(settingsKey, JSON.stringify(next)); } catch { /* keep the mounted choice */ }
	}, [modelsQuery.dataUpdatedAt, settingsKey]);
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
			const selectedSettings: CloudTurnSettings = settingsRef.current.key === settingsKey ? settingsRef.current.settings : {};
			const approvalMode = selectedSettings.approvalMode && approvalModes.includes(selectedSettings.approvalMode)
				? selectedSettings.approvalMode : approvalModes[0];
			return client.sendSessionMessage(cloud.orgId, session.id, {
				text,
				...(selectedSettings.model ? { model: selectedSettings.model } : {}),
				...(selectedSettings.reasoningEffort ? { reasoningEffort: selectedSettings.reasoningEffort } : {}),
				...(cloud.permissionMode ? { mode: cloud.permissionMode } : {}),
				...(approvalMode ? { approvalMode } : {}),
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
	const decide = useMutation({
		mutationFn: async ({ requestId, decisionId }: { requestId: string; decisionId: string }) => {
			if (!cloud) throw new Error("Cloud session context is unavailable.");
			await client.decideChatApproval(cloud.orgId, session.id, requestId, decisionId);
		},
		onSettled: () => void invalidate(),
	});
	const steer = useMutation({
		mutationFn: async ({ text, clientMessageId }: { text: string; clientMessageId?: string }) => {
			if (!cloud || !activeTurn) return { status: "not-accepted" as const, reason: "There is no active turn." };
			const key = clientMessageId ?? crypto.randomUUID();
			const accepted = await client.steerTurn(cloud.orgId, session.id, activeTurn.id, { text }, { idempotencyKey: key });
			let after = accepted.event.sequence;
			for (let attempt = 0; attempt < 60; attempt++) {
				const page = await client.listChatEvents(cloud.orgId, session.id, { after, limit: 100 });
				for (const event of page.events) {
					if (eventPayload(event).clientMessageId !== key) continue;
					if (event.type === "chat.turn_steered") return { status: "accepted" as const };
					if (event.type === "chat.turn_steer_failed") return { status: "not-accepted" as const, reason: String(eventPayload(event).error ?? "The provider declined the steer.") };
				}
				after = page.nextAfter > after ? page.nextAfter : after;
				await new Promise((resolve) => setTimeout(resolve, 500));
			}
			throw new Error("The provider's steer result is still unknown. Your text was not queued as a new turn.");
		},
		onSettled: () => void invalidate(),
	});
	return (
		<ChatWorkspace
			snapshot={snapshot}
			models={modelsQuery.data?.models ?? []}
			onChooseSettings={(next) => updateSettings({ ...settingsRef.current.settings, ...next })}
			showApprovalMode={approvalModes.length > 0}
			approvalModes={approvalModes}
			onRememberPermissions={approvalModes.length > 0 ? (mode) => {
				try { localStorage.setItem(projectKey, JSON.stringify({ approvalMode: mode })); } catch { /* keep this session's choice */ }
				updateSettings({ ...settingsRef.current.settings, approvalMode: mode });
			} : undefined}
			onDecide={(requestId, decisionId) => decide.mutate({ requestId, decisionId })}
			busy={send.isPending}
			controllerTransitioning={controllerTransitioning}
			newWorkDisabled={newWorkDisabled}
			commandError={
				interrupt.error instanceof Error
					? interrupt.error.message
					: decide.error instanceof Error
						? decide.error.message
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
			onOpenFiles={onOpenFiles}
			onOpenFile={onOpenFile}
			onInterrupt={activeTurn ? () => interrupt.mutate() : undefined}
			onSteer={activeTurn && snapshot.capabilities?.includes("steer") ? (text, attachments, clientMessageId) => {
				if (attachments?.length) return Promise.resolve({ status: "not-accepted" as const, reason: "Cloud steering currently accepts text only." });
				return steer.mutateAsync({ text, clientMessageId });
			} : undefined}
			steerPending={steer.isPending}
			showSteerButton
			onSend={(text, _attachments, clientMessageId) => send.mutateAsync({ text, clientMessageId })}
			session={session}
			sessionRole={session.kind}
			sessionTabAction={sessionTabAction}
			sessionTitle={session.title}
		/>
	);
}
