import { ArrowRightLeft, LoaderCircle, Plus, UserRound } from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { useCodexAccountActions } from "../../hooks/useCodexAccountActions";
import { codexAccountAuthorized, codexAccountReasonKey, codexAccountSignedOut, codexSwitchDisplay } from "../../hooks/codex-accounts-state";
import { useCodexAccountsQuery, useEnsureCodexAccounts, type CodexAccount, type CodexAccountSwitch, type CodexActiveLogin } from "../../hooks/useCodexAccountsQuery";
import { ConfirmDialog } from "../ConfirmDialog";
import { Button } from "../ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "../ui/dropdown-menu";
import { AgentProviderGroup } from "./AgentProviderGroup";
import { formatAuthMethod, formatPercentage, formatPlanName } from "./CodexAccountDetails";
import { CodexAccountLoginTerminalPanel } from "./CodexAccountLoginTerminalPanel";
import { CodexAccountRow } from "./CodexAccountRow";
import { SettingsSection } from "./SettingsSection";

export type PendingCodexAccountAction =
	| { kind: "switch"; account: CodexAccount; idempotencyKey: string; submitting: boolean }
	| { kind: "reset"; account: CodexAccount; idempotencyKey: string; submitting: boolean }
	| { kind: "logout"; account: CodexAccount; submitting: boolean }
	| { kind: "delete"; account: CodexAccount; submitting: boolean }
	| null;

export function CodexAccountsSection({ titleHidden }: { titleHidden?: boolean }) {
	const { t } = useTranslation();
	const queryClient = useQueryClient();
	const accountsQuery = useCodexAccountsQuery();
	useEnsureCodexAccounts(true);
	const actions = useCodexAccountActions(queryClient);
	const [providerExpanded, setProviderExpanded] = useState(true);
	const [expandedAccount, setExpandedAccount] = useState<string | null>(null);
	const [pendingAction, setPendingAction] = useState<PendingCodexAccountAction>(null);
	const [announcement, setAnnouncement] = useState("");
	const [switchOutcome, setSwitchOutcome] = useState<{ switchId: string; result: "completed" | "restored" | "failed" } | null>(null);
	const previousSwitch = useRef<CodexAccountSwitch | null>(null);
	const data = accountsQuery.data;
	const activeLogin = data?.activeLogin ?? null;
	const activeAccount = data?.accounts.find((account) => account.id === data.activeAccountId);
	const currentSwitch = data?.currentSwitch;
	const switchPresentation = currentSwitch ? codexSwitchDisplay(currentSwitch) : null;
	const switchStatus = switchPresentation ? t(switchPresentation.key) : null;
	const accountsError = accountsQuery.error instanceof Error ? accountsQuery.error.message : null;
	const actionSubmitting = pendingAction?.submitting ?? false;
	const mutationDisabled = Boolean(activeLogin || switchPresentation?.mutationBlocked || actionSubmitting || actions.loginPending || actions.recoverPending);
	const switchSourceAvailable = Boolean(data?.activeAccountId && activeAccount && !data.unmanagedGlobalAccount);
	const switchTargets = data?.accounts.filter((account) => account.id !== data.activeAccountId) ?? [];
	const switchUnsupported = data?.capabilities.globalSwitch.state !== "supported";

	useEffect(() => {
		if (!activeLogin) return;
		setProviderExpanded(true);
		if (activeLogin.accountId) setExpandedAccount(activeLogin.accountId);
	}, [activeLogin?.accountId, activeLogin?.operationId]);

	useEffect(() => {
		if (currentSwitch) {
			previousSwitch.current = currentSwitch;
			setSwitchOutcome(null);
			return;
		}
		const observed = previousSwitch.current;
		if (!data || !observed) return;
		previousSwitch.current = null;
		const result = data.activeAccountId === observed.targetAccountId
			? "completed"
			: data.activeAccountId === observed.sourceAccountId
				? "restored"
				: "failed";
		setSwitchOutcome({ switchId: observed.id, result });
	}, [currentSwitch, data?.activeAccountId]);

	const beginLogin = useCallback(async (accountId?: string) => {
		if (activeLogin || switchPresentation?.mutationBlocked) return;
		setProviderExpanded(true);
		if (accountId) setExpandedAccount(accountId);
		setAnnouncement("");
		await actions.beginLogin(accountId).catch(() => undefined);
	}, [actions, activeLogin, switchPresentation?.mutationBlocked]);

	const verifyLogin = useCallback(async (login: CodexActiveLogin) => {
		const operation = await actions.verifyLogin(login).catch(() => undefined);
		if (operation?.status !== "completed" || !operation.account) return;
		setAnnouncement(t("settings.codexAccounts.loginSuccess", { label: operation.account.label }));
		window.requestAnimationFrame(() => document.getElementById(`codex-account-${operation.account?.id}`)?.focus());
	}, [actions, t]);

	const toggleAccount = useCallback((account: CodexAccount) => {
		const opening = expandedAccount !== account.id;
		setExpandedAccount(opening ? account.id : null);
		if (opening) void actions.ensureAccount(account.id).catch(() => undefined);
	}, [actions, expandedAccount]);

	const openPending = (kind: Exclude<PendingCodexAccountAction, null>["kind"], account: CodexAccount) => {
		if (kind === "switch" || kind === "reset") setPendingAction({ kind, account, idempotencyKey: crypto.randomUUID(), submitting: false });
		else setPendingAction({ kind, account, submitting: false });
	};

	const submitPending = useCallback(async () => {
		const pending = pendingAction;
		if (!pending || pending.submitting || !data) return;
		setPendingAction({ ...pending, submitting: true });
		try {
			switch (pending.kind) {
				case "switch": await actions.switchAccount(pending.account, data.accountRevision, pending.idempotencyKey); break;
				case "reset": await actions.resetAccount(pending.account, pending.idempotencyKey); setAnnouncement(t("settings.codexAccounts.resetSuccess", { label: pending.account.label })); break;
				case "logout": await actions.logoutAccount(pending.account); setAnnouncement(t("settings.codexAccounts.logoutSuccess", { label: pending.account.label })); break;
				case "delete": await actions.deleteAccount(pending.account); if (expandedAccount === pending.account.id) setExpandedAccount(null); setAnnouncement(t("settings.codexAccounts.deleteSuccess", { label: pending.account.label })); break;
			}
			setPendingAction(null);
		} catch {
			setPendingAction({ ...pending, submitting: false });
		}
	}, [actions, data, expandedAccount, pendingAction, t]);

	const dialog = useMemo(() => {
		if (!pendingAction) return null;
		switch (pendingAction.kind) {
			case "switch": return { title: t("settings.codexAccounts.switchTitle"), description: t("settings.codexAccounts.switchDescription", { label: pendingAction.account.label }), confirmLabel: t("settings.codexAccounts.switchConfirm"), destructive: false };
			case "reset": return { title: t("settings.codexAccounts.resetTitle"), description: t("settings.codexAccounts.resetDescription", { label: pendingAction.account.label }), confirmLabel: t("settings.codexAccounts.useReset"), destructive: false };
			case "logout": return { title: t("settings.codexAccounts.logoutTitle"), description: t("settings.codexAccounts.logoutDescription", { label: pendingAction.account.label }), confirmLabel: t("settings.codexAccounts.logout"), destructive: false };
			case "delete": return { title: t("settings.codexAccounts.deleteTitle"), description: t("settings.codexAccounts.deleteDescription", { label: pendingAction.account.label }), confirmLabel: t("settings.codexAccounts.delete"), destructive: true };
		}
	}, [pendingAction, t]);

	const summary = useMemo(() => {
		if (accountsError) return accountsError;
		if (!data) return t("settings.codexAccounts.loading");
		if (switchStatus && switchPresentation?.busy) return switchStatus;
		// The collapsed summary is all the user sees, so it must not read as ready
		// when the active account -- the one every Codex session launches with --
		// needs reauthentication, however healthy the other accounts look.
		if (activeAccount && !codexAccountAuthorized(activeAccount)) return [activeAccount.label, t(codexAccountSignedOut(activeAccount) ? "settings.codexAccounts.signedOut" : "settings.codexAccounts.unknown")].join(" · ");
		if (activeAccount) return [activeAccount.label, formatPlanName(activeAccount.capacity.plan), activeAccount.capacity.remainingPercent == null ? null : `${formatPercentage(activeAccount.capacity.remainingPercent)} ${t("settings.codexAccounts.remaining")}`].filter(Boolean).join(" · ");
		if (data.unmanagedGlobalAccount) return data.unmanagedGlobalAccount.label;
		return t("settings.codexAccounts.count", { count: data.accounts.length });
	}, [accountsError, activeAccount, data, switchPresentation?.busy, switchStatus, t]);

	return <SettingsSection title={t("settings.codexAccounts.title")} sectionId="codex-accounts" titleHidden={titleHidden}>
		<AgentProviderGroup provider="codex" name="Codex" summary={summary} expanded={providerExpanded || Boolean(activeLogin)} onExpandedChange={setProviderExpanded} collapseLocked={Boolean(activeLogin)} action={<div className="flex items-center gap-2">{switchPresentation?.busy && switchStatus ? <LoaderCircle className="size-5 animate-spin text-muted-foreground" aria-label={switchStatus} /> : null}{switchSourceAvailable && switchTargets.length > 0 ? <DropdownMenu><DropdownMenuTrigger asChild><Button type="button" size="sm" variant="outline" disabled={mutationDisabled || switchUnsupported} title={switchUnsupported && data ? t(codexAccountReasonKey(data.capabilities.globalSwitch.reasonCode)) : undefined}><ArrowRightLeft aria-hidden="true" />{t("settings.codexAccounts.switchConfirm")}</Button></DropdownMenuTrigger><DropdownMenuContent align="end" className="min-w-64">{switchTargets.map((account) => { const authorized = codexAccountAuthorized(account); const targetSummary = [formatAuthMethod(account.authMethod), formatPlanName(account.capacity.plan)].filter(Boolean).join(" · "); return <DropdownMenuItem key={account.id} disabled={account.status !== "valid" || !authorized} onSelect={() => openPending("switch", account)}><UserRound aria-hidden="true" /><div className="min-w-0"><p className="truncate text-foreground">{account.label}</p>{targetSummary ? <p className="truncate text-micro text-muted-foreground">{targetSummary}</p> : null}</div></DropdownMenuItem>; })}</DropdownMenuContent></DropdownMenu> : null}<Button type="button" size="sm" title={accountsError ?? undefined} onClick={() => void beginLogin()} disabled={mutationDisabled || data?.capabilities.nativeLogin.state !== "supported"}><Plus aria-hidden="true" />{t("settings.codexAccounts.add")}</Button></div>}>
			{actions.error ? <p role="alert" className="border-b border-border px-4 py-3 text-xs text-error">{actions.error}</p> : null}
			{data?.unmanagedGlobalAccount ? <div className="border-b border-border px-4 py-3 text-xs"><p className="font-medium text-foreground">{data.unmanagedGlobalAccount.label}</p><p className="mt-1 text-muted-foreground">{t(codexAccountReasonKey(data.unmanagedGlobalAccount.reasonCode))}</p></div> : null}
			{announcement ? <p className="sr-only" role="status" aria-live="polite">{announcement}</p> : null}
			{switchOutcome ? <p key={switchOutcome.switchId} className={`border-b border-border px-4 py-3 text-xs ${switchOutcome.result === "failed" ? "text-error" : "text-muted-foreground"}`} role="status" aria-live="polite">{t(`settings.codexAccounts.switch.${switchOutcome.result}`)}</p> : null}
			{activeLogin && !activeLogin.accountId ? <div className="border-b border-border px-4 py-3" data-testid="codex-account-pending-row"><CodexAccountLoginTerminalPanel activeLogin={activeLogin} pending={actions.loginOperationPending} onCheckAgain={() => void verifyLogin(activeLogin)} onClose={() => void actions.closeLogin(activeLogin)} onRetry={() => void actions.retryLogin(activeLogin)} /></div> : null}
			{accountsQuery.isLoading ? <p className="px-4 py-3 text-xs text-muted-foreground">{t("settings.codexAccounts.loading")}</p> : null}{accountsError ? <p className="px-4 py-3 text-xs text-error" role="alert">{accountsError}</p> : null}
			<div className="divide-y divide-border">{data?.accounts.map((account) => <CodexAccountRow key={account.id} account={account} expanded={expandedAccount === account.id} resetCreditSupported={data.capabilities.resetCreditConsume.state === "supported"} mutationDisabled={mutationDisabled} resetBusy={pendingAction?.kind === "reset" && pendingAction.account.id === account.id && pendingAction.submitting} logoutBusy={pendingAction?.kind === "logout" && pendingAction.account.id === account.id && pendingAction.submitting} deleteBusy={pendingAction?.kind === "delete" && pendingAction.account.id === account.id && pendingAction.submitting} activeLogin={activeLogin?.accountId === account.id ? activeLogin : null} loginPending={actions.loginOperationPending} onToggle={() => toggleAccount(account)} onUseReset={() => openPending("reset", account)} onSignIn={() => void beginLogin(account.id)} onLogout={() => openPending("logout", account)} onDelete={() => openPending("delete", account)} onCheckLogin={() => activeLogin && void verifyLogin(activeLogin)} onCloseLogin={() => activeLogin && void actions.closeLogin(activeLogin)} onRetryLogin={() => activeLogin && void actions.retryLogin(activeLogin)} />)}</div>
			{switchPresentation?.canRecover && currentSwitch && switchStatus ? <div className="border-t border-border px-4 py-3"><p className={switchPresentation.tone === "error" ? "text-xs text-error" : "text-xs text-warning"}>{switchStatus}</p><Button className="mt-2" type="button" size="sm" variant="outline" disabled={actions.recoverPending} onClick={() => void actions.recoverSwitch(currentSwitch.id)}>{actions.recoverPending ? <LoaderCircle className="animate-spin" aria-label={t("settings.codexAccounts.recovering")} /> : null}{t("settings.codexAccounts.retryRecovery")}</Button></div> : null}
		</AgentProviderGroup>
		{dialog && pendingAction ? <ConfirmDialog open title={dialog.title} description={dialog.description} confirmLabel={dialog.confirmLabel} destructive={dialog.destructive} busy={pendingAction.submitting} error={actions.error} onConfirm={() => void submitPending()} onOpenChange={(open) => { if (!open && !pendingAction.submitting) setPendingAction(null); }} /> : null}
	</SettingsSection>;
}
