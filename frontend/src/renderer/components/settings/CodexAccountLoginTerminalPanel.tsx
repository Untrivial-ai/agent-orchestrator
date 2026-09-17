import { useCallback, useEffect, useRef, useState } from "react";
import type { KeyboardEvent } from "react";
import { useTranslation } from "react-i18next";
import type { CodexActiveLogin } from "../../hooks/useCodexAccountsQuery";
import { codexAccountReasonKey } from "../../hooks/codex-accounts-state";
import type { TerminalSessionState } from "../../hooks/useTerminalSession";
import { useShellMaybe } from "../../lib/shell-context";
import { useResolvedTheme } from "../../stores/ui-store";
import { TerminalPane } from "../TerminalPane";
import { Button } from "../ui/button";

const automaticallyVerified = new Set<string>();

// Closing lives entirely in the section header (+ turns into × while a
// sign-in is open), so the panel exposes no close affordance of its own.
export function CodexAccountLoginTerminalPanel({ activeLogin, pending, onAttached, onCheckAgain, onRetry }: {
	activeLogin: CodexActiveLogin;
	pending: boolean;
	onAttached: (operationKey: string) => void;
	onCheckAgain: () => void;
	onRetry: () => void;
}) {
	const { t } = useTranslation();
	const theme = useResolvedTheme();
	const shell = useShellMaybe();
	const panelRef = useRef<HTMLDivElement>(null);
	const operationKey = `${activeLogin.operationId}:${activeLogin.shellTerminal.handleId}`;
	const verifyRef = useRef(onCheckAgain);
	verifyRef.current = onCheckAgain;
	// The live PTY preloads invisibly at full size and is revealed only once
	// it reports attached: users only ever see a working terminal, never a
	// loader or an empty box. The tree shape never changes, so revealing never
	// remounts (or re-attaches) the terminal.
	const [attachedKey, setAttachedKey] = useState<string | null>(null);
	const handleTerminalState = useCallback((state: TerminalSessionState) => {
		if (state === "attached") {
			setAttachedKey(operationKey);
			onAttached(operationKey);
			return;
		}
		if (state !== "exited" && state !== "error") return;
		if (automaticallyVerified.has(operationKey)) return;
		automaticallyVerified.add(operationKey);
		verifyRef.current();
	}, [onAttached, operationKey]);
	const status = activeLogin.status === "pending"
		? activeLogin.reason
		: activeLogin.status === "verifying"
			? t("settings.codexAccounts.loginVerifying")
			: t(codexAccountReasonKey(activeLogin.reasonCode));
	const retryable = activeLogin.status === "unauthorized" || activeLogin.status === "expired" || activeLogin.status === "failed";
	const checkable = activeLogin.status === "retryable";
	const ready = attachedKey === operationKey;
	useEffect(() => {
		if (!ready) return;
		// Reveal is the only moment worth scrolling for: mounting happens
		// hidden (zero height), so scrolling there is a no-op at best.
		panelRef.current?.scrollIntoView({ behavior: "smooth", block: "nearest" });
		// The user just asked to sign in; land focus in the terminal so the
		// auth prompt is immediately typable. Scoped imperative focus (not the
		// terminal's focusRequested path, which rightly refuses inside dialogs).
		panelRef.current?.querySelector<HTMLTextAreaElement>("textarea.xterm-helper-textarea")?.focus({ preventScroll: true });
	}, [operationKey, ready]);
	// The Codex login prompt advertises Ctrl+C as cancel, but inside settings
	// the header × owns cancelling: letting 0x03 reach the PTY kills the flow
	// out from under the panel (exit → verify churn → Retry loop). Swallow
	// plain Ctrl+C in capture phase before xterm's textarea sees it. Copy is
	// unaffected — it is handled by a window-level capture listener that runs
	// first — as are Ctrl+Shift+C and plain typing.
	const swallowInterrupt = (event: KeyboardEvent<HTMLDivElement>) => {
		if (event.key.toLowerCase() === "c" && event.ctrlKey && !event.metaKey && !event.altKey && !event.shiftKey) {
			event.preventDefault();
			event.stopPropagation();
		}
	};
	return (
		<div ref={panelRef} data-testid="codex-account-login-terminal" onKeyDownCapture={swallowInterrupt} className={ready ? "relative scroll-my-3 overflow-hidden rounded-md border border-border bg-terminal" : "relative h-0 overflow-hidden"}>
			<p className="sr-only" role="status" aria-live="polite">{status}</p>
			<div className={ready ? undefined : "invisible absolute inset-x-0 top-0"}>
				<div className="relative h-[300px] min-h-0"><TerminalPane key={operationKey} daemonReady={shell ? shell.daemonStatus.state === "ready" : true} fontSize={12} hideEndedStrip onTerminalStateChange={handleTerminalState} terminalTarget={{ kind: "shell", handleId: activeLogin.shellTerminal.handleId, generation: activeLogin.shellTerminal.createdAt, title: activeLogin.shellTerminal.title }} theme={theme} /></div>
				{retryable || checkable ? <div className="flex items-center justify-between gap-3 border-t border-border bg-surface/90 px-3 py-2"><p className="min-w-0 text-xs text-muted-foreground" role="alert">{status}</p><div className="flex shrink-0 items-center gap-2">{retryable ? <Button type="button" size="sm" variant="outline" disabled={pending} onClick={onRetry}>{t("settings.codexAccounts.retry")}</Button> : null}{checkable ? <Button type="button" size="sm" variant="outline" disabled={pending} onClick={onCheckAgain}>{t("settings.codexAccounts.loginCheckAgain")}</Button> : null}</div></div> : null}
			</div>
		</div>
	);
}
