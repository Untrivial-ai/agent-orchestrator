import { ArrowRightLeft, LoaderCircle, Plus, UserRound } from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { useCodexAccountActions } from "../../hooks/useCodexAccountActions";
import { codexAccountCanSwitch, codexAccountReasonKey, codexAuthenticationDisplay, codexSwitchDisplay } from "../../hooks/codex-accounts-state";
import { useCodexAccountsQuery, useEnsureCodexAccounts, type CodexAccount, type CodexAccountSwitch, type CodexActiveLogin } from "../../hooks/useCodexAccountsQuery";
import { ConfirmDialog } from "../ConfirmDialog";
import { Button } from "../ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "../ui/dropdown-menu";
import { Tooltip, TooltipContent, TooltipTrigger } from "../ui/tooltip";
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
	// Global Settings already performs the initial full refresh. Keep only the
	// focus/visibility backup while this page is mounted.
	useEnsureCodexAccounts();
	const actions = useCodexAccountActions(queryClient);
	const [providerExpanded, setProviderExpanded] = useState(true);
	const [expandedAccount, setExpandedAccount] = useState<string | null>(null);
	const [pendingAction, setPendingAction] = useState<PendingCodexAccountAction>(null);
	const [announcement, setAnnouncement] = useState("");
	const [showDeviceRefresh, setShowDeviceRefresh] = useState(false);
	const [switchOutcome, setSwitchOutcome] = useState<{ switchId: string; result: "completed" | "unchanged" | "failed" | "unknown"; label?: string } | null>(null);
	const previousSwitch = useRef<CodexAccountSwitch | null>(null);
	const switchStatusRequest = useRef<string | null>(null);
	const sectionMounted = useRef(true);
	const data = accountsQuery.data;
	const deviceReconciliation = data?.deviceReconciliation;
	const deviceVerified = deviceReconciliation?.status === "verified";
	const deviceRefreshing = deviceReconciliation?.status === "checking";
	const deviceBlocked = deviceReconciliation?.status === "blocked" || deviceReconciliation?.status === "temporarily_unavailable";
	const deviceRetryable = deviceReconciliation?.retryable === true;
	const activeLogin = data?.activeLogin ?? null;
	const activeAccount = data?.accounts.find((account) => account.active);
	const activeAuthentication = activeAccount ? codexAuthenticationDisplay(activeAccount) : null;
	const currentSwitch = data?.currentSwitch;
	const switchPresentation = currentSwitch ? codexSwitchDisplay(currentSwitch) : null;
	const switchTarget = currentSwitch ? data?.accounts.find((account) => account.id === currentSwitch.targetAccountId) : null;
	const switchStatus = switchPresentation ? t(switchPresentation.key, {
		label: switchTarget?.label,
	}) : null;
	const accountsError = accountsQuery.error instanceof Error ? accountsQuery.error.message : null;
	const actionSubmitting = pendingAction?.submitting ?? false;
	const mutationDisabled = Boolean(activeLogin || switchPresentation?.mutationBlocked || actionSubmitting || actions.loginPending || actions.authenticationRetryAccountId || actions.deviceRefreshPending);
	const switchSourceAvailable = Boolean(activeAccount);
	const switchTargets = data?.accounts.filter((account) => account.id !== data.activeAccountId) ?? [];
	const switchUnsupported = data?.capabilities.globalSwitch.state !== "supported";
	const providerHasContent = Boolean(activeLogin || data?.accounts.length || actions.error || switchOutcome || accountsQuery.isLoading || accountsError);

	useEffect(() => {
		if (!activeLogin) return;
		setProviderExpanded(true);
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
		sectionMounted.current = true;
		return () => { sectionMounted.current = false; };
	}, []);

	useEffect(() => {
		if (currentSwitch) {
			previousSwitch.current = currentSwitch;
			setSwitchOutcome(null);
			return;
		}
		const observed = previousSwitch.current;
		if (!data || !observed || switchStatusRequest.current === observed.id) return;
		switchStatusRequest.current = observed.id;
		void actions.getAccountSwitch(observed.id).then((settled) => {
			if (!sectionMounted.current || previousSwitch.current?.id !== observed.id) return;
			switchStatusRequest.current = null;
			if (settled.phase !== "completed" && settled.phase !== "failed") return;
			previousSwitch.current = null;
			if (settled.phase === "completed") {
				const label = data.accounts.find((account) => account.id === observed.targetAccountId)?.label;
				setSwitchOutcome({ switchId: observed.id, result: "completed", label });
				// Activation deliberately invalidates the target's cached capacity.
				void actions.ensureAccount(observed.targetAccountId).catch(() => undefined);
				return;
			}
			const sourceUnchanged = data.deviceReconciliation.status === "verified"
				&& (data.activeAccountId === observed.sourceAccountId || (observed.sourceKind !== "managed" && !data.activeAccountId));
			const label = sourceUnchanged
				? data.accounts.find((account) => account.id === observed.sourceAccountId)?.label
				: undefined;
			setSwitchOutcome({ switchId: observed.id, result: sourceUnchanged ? "unchanged" : "failed", label });
		}).catch(() => {
			if (!sectionMounted.current || previousSwitch.current?.id !== observed.id) return;
			switchStatusRequest.current = null;
			previousSwitch.current = null;
			setSwitchOutcome({ switchId: observed.id, result: "unknown" });
		});
	}, [actions.ensureAccount, actions.getAccountSwitch, currentSwitch, data]);

	const beginLogin = useCallback(async (accountId?: string) => {
		if (activeLogin || switchPresentation?.mutationBlocked) return;
		setProviderExpanded(true);
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
		if (kind === "switch") setPendingAction({ kind, account, idempotencyKey: crypto.randomUUID(), submitting: false });
		else if (kind === "reset") setPendingAction({ kind, account, idempotencyKey: crypto.randomUUID(), submitting: false });
		else setPendingAction({ kind, account, submitting: false });
	};

	const submitPending = useCallback(async () => {
		const pending = pendingAction;
		if (!pending || pending.submitting || !data) return;
		setPendingAction({ ...pending, submitting: true });
		try {
			switch (pending.kind) {
				case "switch": await actions.switchAccount(pending.account, pending.idempotencyKey); break;
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
				description: t("settings.codexAccounts.switchDescription"),
				confirmLabel: t("settings.codexAccounts.switchConfirm"),
				destructive: false,
			};
			case "reset": return { title: t("settings.codexAccounts.resetTitle"), description: t("settings.codexAccounts.resetDescription", { label: pendingAction.account.label }), confirmLabel: t("settings.codexAccounts.useReset"), destructive: false };
			case "logout": return {
				title: t("settings.codexAccounts.logoutTitle"),
				description: t(pendingAction.account.active
					? "settings.codexAccounts.logoutActiveDescription"
					: "settings.codexAccounts.logoutInactiveDescription", { label: pendingAction.account.label }),
				confirmLabel: t("settings.codexAccounts.logout"),
				destructive: false,
			};
			case "delete": return { title: t("settings.codexAccounts.deleteTitle"), description: t("settings.codexAccounts.deleteDescription", { label: pendingAction.account.label }), confirmLabel: t("settings.codexAccounts.delete"), destructive: true };
		}
	}, [pendingAction, t]);

	const summary = useMemo(() => {
		if (accountsError) return accountsError;
		if (!data) return t("settings.codexAccounts.loading");
		if (switchStatus && switchPresentation?.busy) return switchStatus;
		if (!deviceVerified) {
			if (deviceRefreshing && showDeviceRefresh) return t("settings.codexAccounts.reconciliationChecking");
			if (deviceBlocked) return t("settings.codexAccounts.reconciliationBlocked");
		}
		// The collapsed summary is all the user sees, so it must not read as ready
		// when the active account -- the one every Codex session launches with --
		// needs reauthentication, however healthy the other accounts look.
		if (activeAccount && activeAuthentication?.key !== "settings.codexAccounts.signedIn") return [activeAccount.label, t(activeAuthentication?.key ?? "settings.codexAccounts.authenticationCheckFailed")].join(" · ");
		if (activeAccount) return [activeAccount.label, formatPlanName(activeAccount.capacity.plan), activeAccount.capacity.remainingPercent == null ? null : `${formatPercentage(activeAccount.capacity.remainingPercent)} ${t("settings.codexAccounts.remaining")}`].filter(Boolean).join(" · ");
		if (deviceVerified && !data.activeAccountId) return data.accounts.length > 0
			? t("settings.codexAccounts.chooseAccount")
			: t("settings.codexAccounts.signInToUse");
		return t("settings.codexAccounts.count", { count: data.accounts.length });
	}, [accountsError, activeAccount, activeAuthentication?.key, data, deviceBlocked, deviceRefreshing, deviceVerified, showDeviceRefresh, switchPresentation?.busy, switchStatus, t]);

	return <SettingsSection title={t("settings.codexAccounts.title")} sectionId="codex-accounts" titleHidden={titleHidden}>
		<AgentProviderGroup provider="codex" name="Codex" summary={summary} expanded={providerHasContent && (providerExpanded || Boolean(activeLogin))} onExpandedChange={setProviderExpanded} collapsible={providerHasContent} collapseLocked={Boolean(activeLogin)} action={<div className="flex items-center gap-2">{switchPresentation?.busy && switchStatus ? <LoaderCircle className="size-5 animate-spin text-muted-foreground" aria-label={switchStatus} /> : null}{deviceBlocked && deviceRetryable ? <Button type="button" size="sm" variant="outline" disabled={actions.deviceRefreshPending} onClick={() => void actions.retryDeviceRefresh().catch(() => undefined)}>{actions.deviceRefreshPending ? <LoaderCircle className="animate-spin" aria-label={t("settings.codexAccounts.reconciliationChecking")} /> : null}{t("settings.codexAccounts.tryAgain")}</Button> : null}{switchSourceAvailable && switchTargets.length > 0 ? <DropdownMenu><DropdownMenuTrigger asChild><Button type="button" size="sm" variant="outline" aria-label={t("settings.codexAccounts.switchConfirm")} disabled={mutationDisabled || switchUnsupported} title={switchUnsupported && data ? t(codexAccountReasonKey(data.capabilities.globalSwitch.reasonCode)) : undefined}><ArrowRightLeft aria-hidden="true" />{t("settings.codexAccounts.switchAction")}</Button></DropdownMenuTrigger><DropdownMenuContent align="end" className="min-w-64">{switchTargets.map((account) => { const targetSummary = [formatAuthMethod(account.authMethod), formatPlanName(account.capacity.plan)].filter(Boolean).join(" · "); return <DropdownMenuItem key={account.id} disabled={!codexAccountCanSwitch(account)} onSelect={() => openPending("switch", account)}><UserRound aria-hidden="true" /><div className="min-w-0"><p className="truncate text-foreground">{account.label}</p>{targetSummary ? <p className="truncate text-micro text-muted-foreground">{targetSummary}</p> : null}</div></DropdownMenuItem>; })}</DropdownMenuContent></DropdownMenu> : null}<Tooltip><TooltipTrigger asChild><Button type="button" size="icon-sm" variant="secondary" aria-label={t("settings.codexAccounts.add")} title={accountsError ?? undefined} data-terminal-focus-handoff="true" onClick={() => void beginLogin()} disabled={mutationDisabled || data?.capabilities.nativeLogin.state !== "supported"}><Plus aria-hidden="true" /></Button></TooltipTrigger><TooltipContent>{t("settings.codexAccounts.add")}</TooltipContent></Tooltip></div>}>
			{actions.error ? <p role="alert" className="border-b border-border px-4 py-3 text-xs text-error">{actions.error}</p> : null}
			{announcement ? <p className="sr-only" role="status" aria-live="polite">{announcement}</p> : null}
			{switchOutcome ? <p key={switchOutcome.switchId} className={`border-b border-border px-4 py-3 text-xs ${switchOutcome.result === "failed" || switchOutcome.result === "unchanged" ? "text-error" : "text-muted-foreground"}`} role="status" aria-live="polite">{t(`settings.codexAccounts.switch.${switchOutcome.result}`, { label: switchOutcome.label })}</p> : null}
			{activeLogin && !activeLogin.accountId ? <div className="border-b border-border px-4 py-3" data-testid="codex-account-pending-row"><CodexAccountLoginTerminalPanel activeLogin={activeLogin} pending={actions.loginOperationPending} onCheckAgain={() => void verifyLogin(activeLogin)} onClose={() => void actions.closeLogin(activeLogin)} onRetry={() => void actions.retryLogin(activeLogin)} /></div> : null}
			{accountsQuery.isLoading ? <p className="px-4 py-3 text-xs text-muted-foreground">{t("settings.codexAccounts.loading")}</p> : null}{accountsError ? <p className="px-4 py-3 text-xs text-error" role="alert">{accountsError}</p> : null}
			<div className="divide-y divide-border">{data?.accounts.map((account) => <CodexAccountRow key={account.id} account={account} expanded={expandedAccount === account.id} resetCreditSupported={data.capabilities.resetCreditConsume.state === "supported"} mutationDisabled={mutationDisabled} deviceMutationDisabled={mutationDisabled || switchUnsupported} canUse={!activeAccount && !deviceRefreshing && !switchUnsupported && codexAccountCanSwitch(account)} resetBusy={pendingAction?.kind === "reset" && pendingAction.account.id === account.id && pendingAction.submitting} authenticationRetryBusy={actions.authenticationRetryAccountId === account.id} logoutBusy={pendingAction?.kind === "logout" && pendingAction.account.id === account.id && pendingAction.submitting} deleteBusy={pendingAction?.kind === "delete" && pendingAction.account.id === account.id && pendingAction.submitting} activeLogin={activeLogin?.accountId === account.id ? activeLogin : null} loginPending={actions.loginOperationPending} onToggle={() => toggleAccount(account)} onUseAccount={() => openPending("switch", account)} onUseReset={() => openPending("reset", account)} onRetryAuthentication={() => void actions.retryAuthentication(account.id).catch(() => undefined)} onSignIn={() => void beginLogin(account.id)} onLogout={() => openPending("logout", account)} onDelete={() => openPending("delete", account)} onCheckLogin={() => activeLogin && void verifyLogin(activeLogin)} onCloseLogin={() => activeLogin && void actions.closeLogin(activeLogin)} onRetryLogin={() => activeLogin && void actions.retryLogin(activeLogin)} />)}</div>
		</AgentProviderGroup>
		{dialog && pendingAction ? <ConfirmDialog open title={dialog.title} description={dialog.description} confirmLabel={dialog.confirmLabel} destructive={dialog.destructive} busy={pendingAction.submitting} error={actions.error} onConfirm={() => void submitPending()} onOpenChange={(open) => { if (!open && !pendingAction.submitting) setPendingAction(null); }} /> : null}
	</SettingsSection>;
}
