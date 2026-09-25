import { useLayoutEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import {
	activateChatDraftScope,
	readChatSessionDraft,
	writeChatComposerText,
	type ChatDraftScope,
} from "../lib/chat-drafts";
import type { LastSessionRecord } from "../lib/last-session";

/**
 * Shown in place of the branded daemon loader when the previous launch left a
 * session. Typing updates the same local draft the live composer will read.
 * Send stays disabled: nothing is delivered until the daemon is ready and the
 * user sends from the real composer.
 */
export function StartupSessionDraft({ session }: { session: LastSessionRecord }) {
	const { t } = useTranslation();
	const scope = useMemo<ChatDraftScope>(
		() => ({ sessionId: session.sessionId, incarnation: session.incarnation }),
		[session.incarnation, session.sessionId],
	);
	const [text, setText] = useState("");
	const [canEdit, setCanEdit] = useState(false);

	useLayoutEffect(() => {
		const result = activateChatDraftScope(scope);
		if (!result.ok) {
			setCanEdit(false);
			return;
		}
		setText(readChatSessionDraft(scope).composer.text);
		setCanEdit(true);
	}, [scope]);

	return (
		<div
			aria-busy="true"
			aria-label={t("startup.aria", { brand: "Agent Orchestrator" })}
			className="ao-startup-screen flex h-full w-full flex-col bg-background text-foreground"
			data-testid="startup-session-draft"
			role="status"
		>
			<div className="flex min-h-0 flex-1 flex-col items-center justify-center px-6">
				<h1 className="max-w-xl truncate text-base font-semibold tracking-tight">{session.title}</h1>
				<p className="mt-2 text-md-sm text-muted-foreground">{t("startup.connectingDaemon")}</p>
				<form
					className="mt-6 flex w-full max-w-xl flex-col gap-2"
					onSubmit={(event) => {
						event.preventDefault();
					}}
				>
					<textarea
						className="min-h-24 w-full resize-none rounded-md border border-border bg-background px-3 py-2 text-sm text-foreground"
						data-testid="startup-session-composer"
						disabled={!canEdit}
						value={text}
						onChange={(event) => {
							const next = event.target.value;
							setText(next);
							if (!canEdit) return;
							writeChatComposerText(scope, next);
						}}
					/>
					<button
						className="self-end rounded-md bg-primary px-3 py-1.5 text-sm text-primary-foreground opacity-50"
						data-testid="startup-session-send"
						disabled
						type="submit"
					>
						{t("startup.connectingDaemon")}
					</button>
				</form>
			</div>
		</div>
	);
}
