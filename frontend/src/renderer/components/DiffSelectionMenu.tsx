import { useCallback, useEffect, useRef, useState, type KeyboardEvent } from "react";
import { useTranslation } from "react-i18next";
import { formatDiffSelectionMessage, type DiffSelectionLine } from "../../shared/diff-selection";
import { apiClient, apiErrorMessage } from "../lib/api-client";
import { cn } from "../lib/utils";
import {
	DropdownMenu,
	DropdownMenuContent,
	DropdownMenuItem,
	DropdownMenuSeparator,
	DropdownMenuTrigger,
} from "./ui/dropdown-menu";
import { Input } from "./ui/input";

export type DiffSelectionMenuProps = {
	sessionId: string;
	filePath: string;
	/** Ordered lines from `frontend/src/shared/diff-selection.ts`. */
	lines: DiffSelectionLine[];
	/** Raw `window.getSelection().toString()` — used by Copy only. */
	selectedText: string;
	open: boolean;
	/** Viewport coordinates for the fixed, cursor-positioned trigger. */
	position: { x: number; y: number };
	onOpenChange: (open: boolean) => void;
	scope?: string;
	workspaceVersion?: string;
	fileFingerprint?: string;
};

type Mode = "actions" | "input";
type SendStatus = "idle" | "sending" | "sent" | "error";

// Mirrors BrowserPanel's ~2s "Sent" confirmation window before auto-close.
const SENT_AUTO_CLOSE_MS = 2_000;

export function DiffSelectionMenu({
	sessionId,
	filePath,
	lines,
	selectedText,
	open,
	position,
	onOpenChange,
	scope,
	workspaceVersion,
	fileFingerprint,
}: DiffSelectionMenuProps) {
	const { t } = useTranslation();
	const [mode, setMode] = useState<Mode>("actions");
	const [status, setStatus] = useState<SendStatus>("idle");
	const [errorMessage, setErrorMessage] = useState("");
	const [inputValue, setInputValue] = useState("");
	const inputRef = useRef<HTMLInputElement | null>(null);
	const sentTimerRef = useRef<number | null>(null);
	const openGenerationRef = useRef(0);

	const clearSentTimer = useCallback(() => {
		if (sentTimerRef.current !== null) {
			window.clearTimeout(sentTimerRef.current);
			sentTimerRef.current = null;
		}
	}, []);

	// Shared by every transition away from a prior send's outcome (Copy,
	// Explain-then-Make-changes, and both Escape-from-input paths below): a
	// still-armed "sent" auto-close timer or leftover status/error must never
	// leak into the next action — otherwise a stale "Sent"/error label renders
	// under the new state, and the old timer can later close the whole menu
	// out from under the user with no action on their part.
	const resetSendState = useCallback(() => {
		clearSentTimer();
		setStatus("idle");
		setErrorMessage("");
	}, [clearSentTimer]);

	// One active selection at a time: a fresh open always starts at the action
	// list, even if the previous open ended in the input state or an error.
	// This effect's dependency is only `open`, so it fires on mount and on every
	// false -> true transition, but not on unrelated re-renders while open.
	useEffect(() => {
		openGenerationRef.current += 1;
		if (!open) return;
		clearSentTimer();
		setMode("actions");
		setStatus("idle");
		setErrorMessage("");
		setInputValue("");
	}, [open, clearSentTimer]);

	useEffect(() => clearSentTimer, [clearSentTimer]);

	useEffect(() => {
		if (mode === "input") inputRef.current?.focus();
	}, [mode]);

	const send = useCallback(
		async (instruction: string) => {
			const openGeneration = openGenerationRef.current;
			clearSentTimer();
			setStatus("sending");
			setErrorMessage("");
			const message = formatDiffSelectionMessage({ instruction, filePath, lines, scope, workspaceVersion, fileFingerprint });
			try {
				const { error } = await apiClient.POST("/api/v1/sessions/{sessionId}/send", {
					params: { path: { sessionId } },
					body: { message },
				});
				if (openGeneration !== openGenerationRef.current) return;
				if (error) {
					setStatus("error");
					setErrorMessage(apiErrorMessage(error, t("diffSelection.error.send")));
					return;
				}
				setStatus("sent");
				sentTimerRef.current = window.setTimeout(() => {
					sentTimerRef.current = null;
					onOpenChange(false);
				}, SENT_AUTO_CLOSE_MS);
			} catch (thrown) {
				if (openGeneration !== openGenerationRef.current) return;
				setStatus("error");
				setErrorMessage(apiErrorMessage(thrown, t("diffSelection.error.send")));
			}
		},
		[clearSentTimer, fileFingerprint, filePath, lines, onOpenChange, scope, sessionId, t, workspaceVersion],
	);

	const handleCopy = useCallback(() => {
		void navigator.clipboard?.writeText(selectedText);
		onOpenChange(false);
	}, [onOpenChange, selectedText]);

	const submitInput = useCallback(() => {
		const trimmed = inputValue.trim();
		if (!trimmed || status === "sending") return;
		void send(trimmed);
	}, [inputValue, send, status]);

	const handleInputKeyDown = useCallback(
		(event: KeyboardEvent<HTMLInputElement>) => {
			// Defensive: DropdownMenuContent has its own roving-focus/typeahead
			// keyboard handling that a plain text input shouldn't feed into.
			event.stopPropagation();
			if (event.key === "Escape") {
				// A step back to the action list, not a dismiss — must not let
				// DropdownMenuContent's own Escape handling close the whole menu.
				event.preventDefault();
				resetSendState();
				setMode("actions");
				return;
			}
			if (event.key === "Enter") {
				event.preventDefault();
				submitInput();
			}
		},
		[resetSendState, submitInput],
	);

	const statusLabel =
		status === "sending"
			? t("diffSelection.sending")
			: status === "sent"
				? t("diffSelection.sent")
				: status === "error"
					? errorMessage
					: "";

	return (
		<DropdownMenu modal={false} open={open} onOpenChange={onOpenChange}>
			<DropdownMenuTrigger asChild>
				<button
					aria-hidden="true"
					style={{
						border: 0,
						height: 0,
						left: position.x,
						opacity: 0,
						padding: 0,
						pointerEvents: "none",
						position: "fixed",
						top: position.y,
						width: 0,
					}}
					tabIndex={-1}
					type="button"
				/>
			</DropdownMenuTrigger>
			<DropdownMenuContent
				align="start"
				className="min-w-56"
				onCloseAutoFocus={(event) => event.preventDefault()}
				// DismissableLayer listens for Escape on `document` in the capture
				// phase, which runs before the input's own onKeyDown (a bubble-phase
				// React handler) ever sees the event — so stopPropagation() there is
				// too late to stop the auto-close. This prop fires synchronously
				// inside that same capture-phase handler, before Radix checks
				// `defaultPrevented`, so it's the only hook that can actually cancel
				// the dismiss for input mode's "step back" behavior.
				onEscapeKeyDown={(event) => {
					if (mode !== "input") return;
					event.preventDefault();
					resetSendState();
					setMode("actions");
				}}
				side="right"
				sideOffset={2}
			>
				{mode === "actions" ? (
					<>
						<DropdownMenuItem
							disabled={selectedText.length === 0}
							onSelect={(event) => {
								event.preventDefault();
								// Guards against a stale "sent" auto-close timer (armed by a
								// prior Explain/Make changes send) firing after Copy has
								// already closed the menu itself — see resetSendState above.
								resetSendState();
								handleCopy();
							}}
						>
							{t("diffSelection.copy")}
						</DropdownMenuItem>
						<DropdownMenuSeparator />
						<DropdownMenuItem
							disabled={status === "sending"}
							onSelect={(event) => {
								event.preventDefault();
								void send(t("diffSelection.explainInstruction"));
							}}
						>
							{t("diffSelection.explain")}
						</DropdownMenuItem>
						<DropdownMenuItem
							disabled={status === "sending"}
							onSelect={(event) => {
								event.preventDefault();
								// Unlike Explain/Enter-to-send, this path doesn't go through
								// send(), so it must clear a still-armed "sent" auto-close
								// timer (e.g. Explain succeeded, then the user immediately
								// clicked Make changes) and reset the leftover status/error
								// itself — otherwise the stale "Sent" label renders under the
								// fresh input, and the old timer later closes the whole menu
								// out from under the user's in-progress draft.
								resetSendState();
								setMode("input");
							}}
						>
							{t("diffSelection.makeChanges")}
						</DropdownMenuItem>
					</>
				) : (
					<div className="p-1.5">
						<Input
							aria-label={t("diffSelection.describeChange")}
							disabled={status === "sending"}
							onChange={(event) => setInputValue(event.target.value)}
							onKeyDown={handleInputKeyDown}
							placeholder={t("diffSelection.placeholder")}
							ref={inputRef}
							value={inputValue}
						/>
					</div>
				)}
				{statusLabel ? (
					<div className={cn("px-2 py-1.5 text-caption", status === "error" ? "text-error" : "text-passive")}>
						{statusLabel}
					</div>
				) : null}
			</DropdownMenuContent>
		</DropdownMenu>
	);
}
