import { useCallback, useEffect, useRef } from "react";
import { X } from "lucide-react";
import { useTranslation } from "react-i18next";
import type { CodexActiveLogin } from "../../hooks/useCodexAccountsQuery";
import { codexAccountReasonKey } from "../../hooks/codex-accounts-state";
import type { TerminalSessionState } from "../../hooks/useTerminalSession";
import { useShellMaybe } from "../../lib/shell-context";
import { useResolvedTheme } from "../../stores/ui-store";
import { TerminalPane } from "../TerminalPane";
import { Button } from "../ui/button";

const automaticallyVerified = new Set<string>();

export function CodexAccountLoginTerminalPanel({ activeLogin, pending, onCheckAgain, onClose, onRetry }: {
	activeLogin: CodexActiveLogin;
	pending: boolean;
	onCheckAgain: () => void;
	onClose: () => void;
	onRetry: () => void;
}) {
	const { t } = useTranslation();
	const theme = useResolvedTheme();
	const shell = useShellMaybe();
	const panelRef = useRef<HTMLDivElement>(null);
	const operationKey = `${activeLogin.operationId}:${activeLogin.shellTerminal.handleId}`;
	const verifyRef = useRef(onCheckAgain);
	verifyRef.current = onCheckAgain;
	const handleTerminalState = useCallback((state: TerminalSessionState) => {
		if (state !== "exited" && state !== "error") return;
		if (automaticallyVerified.has(operationKey)) return;
		automaticallyVerified.add(operationKey);
		verifyRef.current();
	}, [operationKey]);
	useEffect(() => { panelRef.current?.scrollIntoView({ behavior: "smooth", block: "nearest" }); }, [operationKey]);
	const status = activeLogin.status === "pending"
		? t("settings.codexAccounts.loginRunning")
		: activeLogin.status === "verifying"
			? t("settings.codexAccounts.loginVerifying")
			: t(codexAccountReasonKey(activeLogin.reasonCode));
	const retryable = activeLogin.status === "unauthorized" || activeLogin.status === "expired" || activeLogin.status === "failed";
	const checkable = activeLogin.status === "unverified";
	return (
		<div ref={panelRef} className="scroll-my-3 overflow-hidden rounded-md border border-border bg-terminal" data-testid="codex-account-login-terminal">
			<div className="flex min-h-10 items-center justify-between gap-3 border-b border-border bg-surface/90 px-3 py-2"><div className="min-w-0"><p className="truncate text-xs font-medium text-foreground">{t("settings.codexAccounts.loginTerminalTitle")}</p><p className="truncate text-[11px] text-muted-foreground" aria-live="polite" role="status">{status}</p></div><button type="button" aria-label={t("settings.codexAccounts.loginClose")} className="grid size-7 shrink-0 place-items-center rounded text-muted-foreground hover:bg-interactive-hover hover:text-foreground disabled:opacity-50" disabled={pending} onClick={onClose}><X className="size-4" aria-hidden="true" /></button></div>
			<div className="h-[300px] min-h-0"><TerminalPane key={operationKey} daemonReady={shell ? shell.daemonStatus.state === "ready" : true} fontSize={12} onTerminalStateChange={handleTerminalState} terminalTarget={{ kind: "shell", handleId: activeLogin.shellTerminal.handleId, generation: activeLogin.shellTerminal.createdAt, title: activeLogin.shellTerminal.title }} theme={theme} /></div>
			{retryable || checkable ? <div className="flex items-center justify-between gap-3 border-t border-border bg-surface/90 px-3 py-2"><p className="min-w-0 text-xs text-muted-foreground" role="alert">{status}</p><div className="flex shrink-0 items-center gap-2">{retryable ? <Button type="button" size="sm" variant="outline" disabled={pending} onClick={onRetry}>{t("settings.codexAccounts.retry")}</Button> : null}{checkable ? <Button type="button" size="sm" variant="outline" disabled={pending} onClick={onCheckAgain}>{t("settings.codexAccounts.loginCheckAgain")}</Button> : null}<Button type="button" size="sm" variant="ghost" disabled={pending} onClick={onClose}>{t("settings.codexAccounts.loginClose")}</Button></div></div> : null}
		</div>
	);
}
