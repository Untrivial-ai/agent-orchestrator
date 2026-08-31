import { Loader2, SendHorizontal } from "lucide-react";
import { useMemo, useState, type FormEvent, type ReactNode } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useCloudCp } from "../../hooks/useCloudCp";
import type { CloudCpClientEvent } from "../../lib/cloud-cp";
import type { WorkspaceSession } from "../../types/workspace";
import { cn } from "../../lib/utils";

type ChatLine = {
	id: string;
	role: "assistant" | "user";
	text: string;
};

function eventText(event: CloudCpClientEvent): string | undefined {
	if (!event.payload || typeof event.payload !== "object") return undefined;
	const text = (event.payload as { text?: unknown }).text;
	return typeof text === "string" && text.trim() !== "" ? text : undefined;
}

function toChatLines(events: CloudCpClientEvent[]): ChatLine[] {
	const lines: ChatLine[] = [];
	for (const event of events) {
		const text = eventText(event);
		if (!text) continue;
		if (event.type === "chat.user_message") {
			lines.push({ id: String(event.sequence), role: "user", text });
			continue;
		}
		if (event.type === "chat.assistant_delta") {
			lines.push({ id: String(event.sequence), role: "assistant", text });
		}
	}
	return lines;
}

/**
 * Cloud ChatUI reads the control-plane event projection directly. It must not
 * use the local daemon conversation hooks: a Cloud session id is meaningless
 * to the loopback daemon and would otherwise produce SESSION_NOT_FOUND as soon
 * as a TUI -> ChatUI handoff commits.
 */
export function CloudSessionChatSurface({
	session,
	headerActions,
	sessionTabAction,
}: {
	session: WorkspaceSession;
	headerActions?: ReactNode;
	sessionTabAction?: ReactNode;
}) {
	const cloud = session.cloud;
	const { client, ready } = useCloudCp();
	const queryClient = useQueryClient();
	const [draft, setDraft] = useState("");
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
	const send = useMutation({
		mutationFn: async (text: string) => {
			if (!cloud) throw new Error("Cloud session context is unavailable.");
			return client.sendSessionMessage(cloud.orgId, session.id, { text });
		},
		onSuccess: () => {
			setDraft("");
			void queryClient.invalidateQueries({ queryKey: ["cloud-chat-events", cloud?.orgId ?? "", session.id] });
		},
	});
	const lines = useMemo(() => toChatLines(eventsQuery.data ?? []), [eventsQuery.data]);
	const submit = (event: FormEvent) => {
		event.preventDefault();
		const text = draft.trim();
		if (!text || send.isPending) return;
		send.mutate(text);
	};

	return (
		<div className="flex h-full min-h-0 flex-col bg-background">
			<div className="flex h-inspector-tabs shrink-0 items-center border-b border-border px-2">
				<div className="min-w-0 flex-1 text-sm font-medium">Chat</div>
				{sessionTabAction}
				{headerActions}
			</div>
			<div className="min-h-0 flex-1 overflow-y-auto px-4 py-5">
				{eventsQuery.isLoading ? (
					<div className="flex h-full items-center justify-center gap-2 text-sm text-muted-foreground">
						<Loader2 className="size-4 animate-spin" /> Loading conversation…
					</div>
				) : lines.length === 0 ? (
					<div className="grid h-full place-items-center text-center text-sm text-muted-foreground">
						ChatUI is ready. Send a message to continue this Cloud agent session.
					</div>
				) : (
					<div className="mx-auto flex max-w-3xl flex-col gap-4">
						{lines.map((line) => (
							<div
								className={cn(
									"max-w-[85%] whitespace-pre-wrap rounded-lg px-3 py-2 text-sm leading-relaxed",
									line.role === "user"
										? "self-end bg-primary text-primary-foreground"
										: "self-start bg-muted text-foreground",
								)}
								key={line.id}
							>
								{line.text}
							</div>
						))}
					</div>
				)}
			</div>
			<form className="border-t border-border p-3" onSubmit={submit}>
				<textarea
					className="min-h-20 w-full resize-y rounded-md border border-input bg-background p-2 text-sm outline-none focus:ring-2 focus:ring-ring"
					disabled={send.isPending || !cloud || !ready}
					onChange={(event) => setDraft(event.target.value)}
					placeholder="Message the Cloud agent…"
					value={draft}
				/>
				<div className="mt-2 flex items-center justify-between">
					<span className="text-xs text-destructive">{send.error instanceof Error ? send.error.message : ""}</span>
					<button
						className="inline-flex items-center gap-1 rounded-md bg-primary px-3 py-1.5 text-sm text-primary-foreground disabled:opacity-50"
						disabled={!draft.trim() || send.isPending || !cloud || !ready}
						type="submit"
					>
						<SendHorizontal className="size-4" /> Send
					</button>
				</div>
			</form>
		</div>
	);
}
