import { useEffect, useRef, useState, type KeyboardEvent } from "react";
import { ArrowUp, Loader2, X } from "lucide-react";
import { useTranslation } from "react-i18next";
import type { ConversationContentSummary } from "../../types/conversation";
import { Button } from "../ui/button";
import { ConversationContentItems } from "./ConversationContentItems";

export interface HumanMessageEditorProps {
	text: string;
	content: ConversationContentSummary[];
	pending: boolean;
	busy: boolean;
	error?: string;
	onDraftChange?: (text: string) => void;
	onCancel: () => void;
	onSend: (text: string) => Promise<unknown> | void;
}

export function HumanMessageEditor({
	text,
	content,
	pending,
	busy,
	error,
	onDraftChange,
	onCancel,
	onSend,
}: HumanMessageEditorProps) {
	const { t } = useTranslation();
	const [draft, setDraft] = useState(text);
	const textarea = useRef<HTMLTextAreaElement>(null);
	const sendDisabled = pending || busy || draft.trim().length === 0;
	const busyMessage = busy ? t("chat.edit.stopCurrentTurn") : undefined;

	useEffect(() => {
		const node = textarea.current;
		if (!node) return;
		node.style.height = "0px";
		node.style.height = `${Math.min(node.scrollHeight, 224)}px`;
	}, [draft]);

	function submit() {
		if (sendDisabled) return;
		void Promise.resolve(onSend(draft.trimEnd())).catch(() => {});
	}

	function onKeyDown(event: KeyboardEvent<HTMLTextAreaElement>) {
		if (event.key === "Escape") {
			event.preventDefault();
			onCancel();
			return;
		}
		if (event.key === "Enter" && (event.metaKey || event.ctrlKey)) {
			event.preventDefault();
			submit();
		}
	}

	return (
		<div className="cursor-chat-composer relative flex w-full max-w-3xl flex-col gap-2 rounded-[10px] border border-border-strong p-2.5 transition-[background,border-color,box-shadow]">
		<textarea
			ref={textarea}
			value={draft}
			onChange={(event) => {
				setDraft(event.target.value);
				onDraftChange?.(event.target.value);
			}}
			onKeyDown={onKeyDown}
			aria-label={t("chat.edit.label")}
			autoFocus
			rows={2}
			className="chat-composer-scrollbar max-h-56 min-h-[3.25rem] w-full resize-none overflow-y-auto overscroll-contain bg-transparent px-1.5 py-1.5 text-sm leading-relaxed text-foreground outline-none"
		/>
		<ConversationContentItems
			content={content}
			ariaLabel={t("chat.edit.preservedContent")}
			imageLabel={t("chat.edit.image")}
			className="mt-2"
		/>
		<div className="mt-2 flex min-h-7 items-center justify-end gap-1.5">
			{error ? (
				<span role="alert" className="mr-auto text-[11px] text-destructive">
					{error}
				</span>
			) : busyMessage ? (
				<span className="mr-auto text-[11px] text-muted-foreground">{busyMessage}</span>
			) : null}
			<Button
				type="button"
				size="icon-sm"
				variant="ghost"
				onClick={onCancel}
				disabled={pending}
				aria-label={t("chat.edit.cancel")}
				title={t("chat.edit.cancel")}
				className="size-7"
			>
				<X aria-hidden="true" className="size-3.5" />
			</Button>
			<Button
				type="button"
				size="icon-sm"
				onClick={submit}
				disabled={sendDisabled}
				aria-label={t("chat.edit.send")}
				title={busyMessage ?? t("chat.edit.sendShortcut")}
				className="size-7 rounded-full"
			>
				{pending ? <Loader2 aria-hidden="true" className="size-3.5 animate-spin" /> : <ArrowUp aria-hidden="true" className="size-3.5" />}
			</Button>
		</div>
	</div>
	);
}
