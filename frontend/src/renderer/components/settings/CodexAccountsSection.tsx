import { ArrowRightLeft, LoaderCircle, Plus, UserRound } from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { useCodexAccountActions } from "../../hooks/useCodexAccountActions";
import { codexAccountReasonKey, codexAuthenticationDisplay, codexSwitchDisplay } from "../../hooks/codex-accounts-state";
import { getCodexAccountSwitch, useCodexAccountsQuery, useEnsureCodexAccounts, type CodexAccount, type CodexAccountSwitch, type CodexActiveLogin } from "../../hooks/useCodexAccountsQuery";
import { ConfirmDialog } from "../ConfirmDialog";
import { Button } from "../ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "../ui/dropdown-menu";
import { Switch } from "../ui/switch";
import { AgentProviderGroup } from "./AgentProviderGroup";
import { formatAuthMethod, formatPercentage, formatPlanName } from "./CodexAccountDetails";
import { CodexAccountLoginTerminalPanel } from "./CodexAccountLoginTerminalPanel";
import { CodexAccountRow } from "./CodexAccountRow";
import { SettingsSection } from "./SettingsSection";

export type PendingCodexAccountAction =
	| { kind: "switch"; account: CodexAccount; idempotencyKey: string; restartIdleSessions: boolean; submitting: boolean }
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
	const [showDeviceRefresh, setShowDeviceRefresh] = useState(false);
	const [switchOutcome, setSwitchOutcome] = useState<{ switchId: string; result: "completed" | "completed_with_restart_warning" | "restored" | "failed"; label?: string } | null>(null);
	const previousSwitch = useRef<CodexAccountSwitch | null>(null);
	const data = accountsQuery.data;
	const deviceReconciliation = data?.deviceReconciliation;
	const deviceVerified = deviceReconciliation?.status === "verified";
	const deviceRefreshing = deviceReconciliation?.status === "checking" || deviceReconciliation?.status === "temporarily_unavailable";
	const deviceBlocked = deviceReconciliation?.status === "blocked" && !data?.unmanagedGlobalAccount;
	const activeLogin = data?.activeLogin ?? null;
	const deviceAccount = data?.unmanagedGlobalAccount;
	const activeAccount = data?.accounts.find((account) => account.active);
	const activeAuthentication = activeAccount ? codexAuthenticationDisplay(activeAccount) : null;
	const currentSwitch = data?.currentSwitch;
	const switchPresentation = currentSwitch ? codexSwitchDisplay(currentSwitch) : null;
	const switchTarget = currentSwitch ? data?.accounts.find((account) => account.id === currentSwitch.targetAccountId) : null;
	const switchSource = currentSwitch ? data?.accounts.find((account) => account.id === currentSwitch.sourceAccountId) : null;
	const switchStatus = switchPresentation ? t(switchPresentation.key, {
		label: switchPresentation.busy && currentSwitch?.phase === "rollback_required"
			? switchSource?.label
			: switchTarget?.label,
	}) : null;
	const accountsError = accountsQuery.error instanceof Error ? accountsQuery.error.message : null;
	const actionSubmitting = pendingAction?.submitting ?? false;
	const mutationDisabled = Boolean(activeLogin || switchPresentation?.mutationBlocked || actionSubmitting || actions.loginPending || actions.recoverPending || actions.authenticationRetryAccountId || actions.deviceRefreshPending);
	const switchSourceAvailable = Boolean(data && (deviceVerified || deviceAccount));
	const switchTargets = data?.accounts.filter((account) => account.id !== data.activeAccountId) ?? [];
	const switchUnsupported = data?.capabilities.globalSwitch.state !== "supported";

	useEffect(() => {
		if (!activeLogin) return;
		setProviderExpanded(true);
		if (activeLogin.accountId) setExpandedAccount(activeLogin.accountId);
	}, [activeLogin?.accountId, activeLogin?.operationId]);

	useEffect(() => {
		if (!deviceRefreshing) {
			setShowDeviceRefresh(false);
			return;
		}
		const timer = window.setTimeout(() => setShowDeviceRefresh(true), 1_200);
		return () => window.clearTimeout(timer);
	}, [deviceRefreshing]);

	useEffect(() => {
		if (currentSwitch) {
			previousSwitch.current = currentSwitch;
			setSwitchOutcome(null);
			return;
		}
		const observed = previousSwitch.current;
		if (!data || !observed) return;
		previousSwitch.current = null;
		let cancelled = false;
		const settleOutcome = (terminal: CodexAccountSwitch | null) => {
			if (cancelled) return;
			const switched = data.activeAccountId === observed.targetAccountId;
			const result = switched
				? terminal?.failureCode === "idle_session_restart_incomplete" ? "completed_with_restart_warning" : "completed"
				: data.activeAccountId === observed.sourceAccountId || (observed.sourceKind !== "managed" && !data.activeAccountId)
					? "restored"
					: "failed";
			const label = result === "completed" || result === "completed_with_restart_warning"
				? data.accounts.find((account) => account.id === observed.targetAccountId)?.label
				: result === "restored"
					? data.accounts.find((account) => account.id === observed.sourceAccountId)?.label
					: undefined;
			setSwitchOutcome({ switchId: observed.id, result, label });
		};
		void getCodexAccountSwitch(observed.id).then(settleOutcome).catch(() => settleOutcome(null));
		return () => { cancelled = true; };
	}, [currentSwitch, data?.activeAccountId, data?.accounts]);

	const beginLogin = useCallback(async (accountId?: string) => {
		if (activeLogin || switchPresentation?.mutationBlocked) return;
		setProviderExpanded(true);
		if (accountId) setExpandedAccount(accountId);
		setAnnouncement("");
		await actions.beginLogin(accountId).catch(() => undefined);
	}, [actions, activeLogin, switchPresentation?.mutationBlocked]);

	const beginDeviceLogin = useCallback(async () => {
		if (activeLogin || switchPresentation?.mutationBlocked) return;
		setProviderExpanded(true);
		setAnnouncement("");
		await actions.beginDeviceLogin().catch(() => undefined);
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
		if (kind === "switch") setPendingAction({ kind, account, idempotencyKey: crypto.randomUUID(), restartIdleSessions: false, submitting: false });
		else if (kind === "reset") setPendingAction({ kind, account, idempotencyKey: crypto.randomUUID(), submitting: false });
		else setPendingAction({ kind, account, submitting: false });
	};

	const submitPending = useCallback(async () => {
		const pending = pendingAction;
		if (!pending || pending.submitting || !data) return;
		setPendingAction({ ...pending, submitting: true });
		try {
			switch (pending.kind) {
				case "switch": await actions.switchAccount(pending.account, data.accountRevision, pending.idempotencyKey, pending.restartIdleSessions); break;
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
			case "switch": return {
				title: t("settings.codexAccounts.switchTitle", { label: pendingAction.account.label }),
				description: <div className="space-y-4">
					<p>{t("settings.codexAccounts.switchDescription")}</p>
					<div className="rounded-lg border border-border bg-background/40 p-3">
						<div className="flex items-center justify-between gap-4">
							<label htmlFor="restart-idle-codex-sessions" className="font-medium text-foreground">{t("settings.codexAccounts.restartIdleSessions")}</label>
							<Switch id="restart-idle-codex-sessions" checked={pendingAction.restartIdleSessions} disabled={pendingAction.submitting} onCheckedChange={(checked) => setPendingAction((current) => current?.kind === "switch" ? { ...current, restartIdleSessions: checked } : current)} />
						</div>
						<p className="mt-2 text-caption leading-4 text-settings-muted">{t("settings.codexAccounts.restartIdleSessionsDescription")}</p>
					</div>
				</div>,
				confirmLabel: t("settings.codexAccounts.switchConfirm"),
				destructive: false,
			};
			case "reset": return { title: t("settings.codexAccounts.resetTitle"), description: t("settings.codexAccounts.resetDescription", { label: pendingAction.account.label }), confirmLabel: t("settings.codexAccounts.useReset"), destructive: false };
			case "logout": return { title: t("settings.codexAccounts.logoutTitle"), description: t("settings.codexAccounts.logoutDescription", { label: pendingAction.account.label }), confirmLabel: t("settings.codexAccounts.logout"), destructive: false };
			case "delete": return { title: t("settings.codexAccounts.deleteTitle"), description: t("settings.codexAccounts.deleteDescription", { label: pendingAction.account.label }), confirmLabel: t("settings.codexAccounts.delete"), destructive: true };
		}
	}, [pendingAction, t]);

	const summary = useMemo(() => {
		if (accountsError) return accountsError;
		if (!data) return t("settings.codexAccounts.loading");
		if (switchStatus && switchPresentation?.busy) return switchStatus;
		if (!deviceVerified) {
			if (!deviceAccount && deviceRefreshing && showDeviceRefresh) return t("settings.codexAccounts.reconciliationChecking");
			if (deviceReconciliation?.status === "blocked" && !data.unmanagedGlobalAccount) return t("settings.codexAccounts.reconciliationBlocked");
		}
		// The collapsed summary is all the user sees, so it must not read as ready
		// when the active account -- the one every Codex session launches with --
		// needs reauthentication, however healthy the other accounts look.
		if (activeAccount && activeAuthentication?.key !== "settings.codexAccounts.signedIn") return [activeAccount.label, t(activeAuthentication?.key ?? "settings.codexAccounts.authenticationCheckFailed")].join(" · ");
		if (activeAccount) return [activeAccount.label, formatPlanName(activeAccount.capacity.plan), activeAccount.capacity.remainingPercent == null ? null : `${formatPercentage(activeAccount.capacity.remainingPercent)} ${t("settings.codexAccounts.remaining")}`].filter(Boolean).join(" · ");
		if (deviceAccount) return [deviceAccount.accountEmail ?? deviceAccount.label, t("settings.codexAccounts.inUse")].join(" · ");
		if (deviceVerified && !data.activeAccountId) return t("settings.codexAccounts.noActiveAccount");
		return t("settings.codexAccounts.count", { count: data.accounts.length });
	}, [accountsError, activeAccount, activeAuthentication?.key, data, deviceAccount, deviceReconciliation?.status, deviceRefreshing, deviceVerified, showDeviceRefresh, switchPresentation?.busy, switchStatus, t]);

	return <SettingsSection title={t("settings.codexAccounts.title")} sectionId="codex-accounts" titleHidden={titleHidden}>
		<AgentProviderGroup provider="codex" name="Codex" summary={summary} expanded={providerExpanded || Boolean(activeLogin)} onExpandedChange={setProviderExpanded} collapseLocked={Boolean(activeLogin)} action={<div className="flex items-center gap-2">{switchPresentation?.busy && switchStatus ? <LoaderCircle className="size-5 animate-spin text-muted-foreground" aria-label={switchStatus} /> : null}{deviceBlocked ? <Button type="button" size="sm" variant="outline" disabled={actions.deviceRefreshPending} onClick={() => void actions.retryDeviceRefresh().catch(() => undefined)}>{actions.deviceRefreshPending ? <LoaderCircle className="animate-spin" aria-label={t("settings.codexAccounts.reconciliationChecking")} /> : null}{t("settings.codexAccounts.tryAgain")}</Button> : null}{switchSourceAvailable && switchTargets.length > 0 ? <DropdownMenu><DropdownMenuTrigger asChild><Button type="button" size="sm" variant="outline" disabled={mutationDisabled || switchUnsupported} title={switchUnsupported && data ? t(codexAccountReasonKey(data.capabilities.globalSwitch.reasonCode)) : undefined}><ArrowRightLeft aria-hidden="true" />{t("settings.codexAccounts.switchConfirm")}</Button></DropdownMenuTrigger><DropdownMenuContent align="end" className="min-w-64">{switchTargets.map((account) => { const authenticated = codexAuthenticationDisplay(account).key === "settings.codexAccounts.signedIn"; const targetSummary = [formatAuthMethod(account.authMethod), formatPlanName(account.capacity.plan)].filter(Boolean).join(" · "); return <DropdownMenuItem key={account.id} disabled={account.status !== "valid" || !authenticated} onSelect={() => openPending("switch", account)}><UserRound aria-hidden="true" /><div className="min-w-0"><p className="truncate text-foreground">{account.label}</p>{targetSummary ? <p className="truncate text-micro text-muted-foreground">{targetSummary}</p> : null}</div></DropdownMenuItem>; })}</DropdownMenuContent></DropdownMenu> : null}<Button type="button" size="sm" title={accountsError ?? undefined} onClick={() => void beginLogin()} disabled={mutationDisabled || data?.capabilities.nativeLogin.state !== "supported"}><Plus aria-hidden="true" />{t("settings.codexAccounts.add")}</Button></div>}>
			{actions.error ? <p role="alert" className="border-b border-border px-4 py-3 text-xs text-error">{actions.error}</p> : null}
			{deviceAccount ? <div className="border-b border-border px-4 py-3" data-testid="codex-device-account"><div className="flex items-start gap-3"><UserRound className="mt-0.5 size-6 shrink-0 text-muted-foreground" aria-hidden="true" /><div className="min-w-0 flex-1"><div className="flex items-center gap-2"><p className="truncate text-sm font-medium">{deviceAccount.accountEmail ?? deviceAccount.label}</p><span className="rounded-full border border-success/30 bg-success/10 px-2 py-0.5 text-[10px] font-medium text-success">{t("settings.codexAccounts.inUse")}</span></div><p className="mt-1 text-xs text-muted-foreground" role="status" aria-live="polite">{deviceRefreshing && !showDeviceRefresh ? t("settings.codexAccounts.reconciliationChecking") : t(codexAccountReasonKey(deviceAccount.reasonCode))}</p><div className="mt-3 flex items-center gap-2"><Button type="button" size="sm" variant="outline" disabled={mutationDisabled} onClick={() => void beginDeviceLogin()}>{t("settings.codexAccounts.signInAgain")}</Button>{deviceReconciliation?.retryable ? <Button type="button" size="sm" variant="outline" disabled={actions.deviceRefreshPending} onClick={() => void actions.retryDeviceRefresh().catch(() => undefined)}>{actions.deviceRefreshPending ? <LoaderCircle className="animate-spin" aria-label={t("settings.codexAccounts.reconciliationChecking")} /> : null}{t("settings.codexAccounts.tryAgain")}</Button> : null}</div></div></div></div> : null}
			{data && !deviceAccount && !activeAccount && deviceVerified ? <p className="border-b border-border px-4 py-3 text-xs text-muted-foreground" role="status">{data.accounts.length > 0 ? t("settings.codexAccounts.chooseAccount") : t("settings.codexAccounts.signInToUse")}</p> : null}
			{announcement ? <p className="sr-only" role="status" aria-live="polite">{announcement}</p> : null}
			{switchOutcome ? <p key={switchOutcome.switchId} className={`border-b border-border px-4 py-3 text-xs ${switchOutcome.result === "completed" ? "text-muted-foreground" : switchOutcome.result === "completed_with_restart_warning" ? "text-warning" : "text-error"}`} role="status" aria-live="polite">{t(`settings.codexAccounts.switch.${switchOutcome.result}`, { label: switchOutcome.label })}</p> : null}
			{activeLogin && !activeLogin.accountId ? <div className="border-b border-border px-4 py-3" data-testid="codex-account-pending-row"><CodexAccountLoginTerminalPanel activeLogin={activeLogin} pending={actions.loginOperationPending} onCheckAgain={() => void verifyLogin(activeLogin)} onClose={() => void actions.closeLogin(activeLogin)} onRetry={() => void actions.retryLogin(activeLogin)} /></div> : null}
			{accountsQuery.isLoading ? <p className="px-4 py-3 text-xs text-muted-foreground">{t("settings.codexAccounts.loading")}</p> : null}{accountsError ? <p className="px-4 py-3 text-xs text-error" role="alert">{accountsError}</p> : null}
			<div className="divide-y divide-border">{data?.accounts.map((account) => <CodexAccountRow key={account.id} account={account} expanded={expandedAccount === account.id} resetCreditSupported={data.capabilities.resetCreditConsume.state === "supported"} mutationDisabled={mutationDisabled} deviceMutationDisabled={mutationDisabled || (!deviceVerified && account.id === data.activeAccountId)} canUse={!activeAccount && !deviceAccount && deviceVerified} resetBusy={pendingAction?.kind === "reset" && pendingAction.account.id === account.id && pendingAction.submitting} authenticationRetryBusy={actions.authenticationRetryAccountId === account.id} logoutBusy={pendingAction?.kind === "logout" && pendingAction.account.id === account.id && pendingAction.submitting} deleteBusy={pendingAction?.kind === "delete" && pendingAction.account.id === account.id && pendingAction.submitting} activeLogin={activeLogin?.accountId === account.id ? activeLogin : null} loginPending={actions.loginOperationPending} onToggle={() => toggleAccount(account)} onUseAccount={() => openPending("switch", account)} onUseReset={() => openPending("reset", account)} onRetryAuthentication={() => void actions.retryAuthentication(account.id).catch(() => undefined)} onSignIn={() => void beginLogin(account.id)} onLogout={() => openPending("logout", account)} onDelete={() => openPending("delete", account)} onCheckLogin={() => activeLogin && void verifyLogin(activeLogin)} onCloseLogin={() => activeLogin && void actions.closeLogin(activeLogin)} onRetryLogin={() => activeLogin && void actions.retryLogin(activeLogin)} />)}</div>
			{switchPresentation?.canRecover && currentSwitch && switchStatus ? <div className="border-t border-border px-4 py-3"><p className={switchPresentation.tone === "error" ? "text-xs text-error" : "text-xs text-warning"}>{switchStatus}</p><Button className="mt-2" type="button" size="sm" variant="outline" disabled={actions.recoverPending} onClick={() => void actions.recoverSwitch(currentSwitch.id)}>{actions.recoverPending ? <LoaderCircle className="animate-spin" aria-label={t("settings.codexAccounts.recovering")} /> : null}{t("settings.codexAccounts.retryRecovery")}</Button></div> : null}
		</AgentProviderGroup>
		{dialog && pendingAction ? <ConfirmDialog open title={dialog.title} description={dialog.description} confirmLabel={dialog.confirmLabel} destructive={dialog.destructive} busy={pendingAction.submitting} error={actions.error} onConfirm={() => void submitPending()} onOpenChange={(open) => { if (!open && !pendingAction.submitting) setPendingAction(null); }} /> : null}
	</SettingsSection>;
}
