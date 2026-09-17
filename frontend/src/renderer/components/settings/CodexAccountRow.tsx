import { ChevronDown, CircleAlert, CircleCheck, LoaderCircle, LogOut, Trash2, UserRound } from "lucide-react";
import { useTranslation } from "react-i18next";
import { codexAccountAuthorized, codexAuthenticationDisplay } from "../../hooks/codex-accounts-state";
import type { CodexAccount, CodexActiveLogin } from "../../hooks/useCodexAccountsQuery";
import { Button } from "../ui/button";
import { Tooltip, TooltipContent, TooltipTrigger } from "../ui/tooltip";
import { CodexAccountDetails, formatAuthMethod, formatPercentage, formatPlanName } from "./CodexAccountDetails";
import { CodexAccountLoginTerminalPanel } from "./CodexAccountLoginTerminalPanel";

export function CodexAccountRow({ account, expanded, resetCreditSupported, mutationDisabled, deviceMutationDisabled, canUse, resetBusy, authenticationRetryBusy, logoutBusy, deleteBusy, activeLogin, loginPending, onToggle, onUseAccount, onUseReset, onRetryAuthentication, onSignIn, onLogout, onDelete, onCheckLogin, onCloseLogin, onRetryLogin }: {
	account: CodexAccount;
	expanded: boolean;
	resetCreditSupported: boolean;
	mutationDisabled: boolean;
	deviceMutationDisabled: boolean;
	canUse: boolean;
	resetBusy: boolean;
	authenticationRetryBusy: boolean;
	logoutBusy: boolean;
	deleteBusy: boolean;
	activeLogin: CodexActiveLogin | null;
	loginPending: boolean;
	onToggle: () => void;
	onUseAccount: () => void;
	onUseReset: () => void;
	onRetryAuthentication: () => void;
	onSignIn: () => void;
	onLogout: () => void;
	onDelete: () => void;
	onCheckLogin: () => void;
	onCloseLogin: () => void;
	onRetryLogin: () => void;
}) {
	const { t } = useTranslation();
	const authorized = codexAccountAuthorized(account);
	const authentication = codexAuthenticationDisplay(account);
	const confirmed = authorized && authentication.key === "settings.codexAccounts.signedIn";
	const needsSignIn = authentication.action === "reauthenticate" && account.status !== "broken";
	const finishingSetup = confirmed && account.authMethod === "chatgpt" && !account.accountEmail;
	const remaining = account.capacity.remainingPercent;
	const authenticationLabel = t(authentication.key);
	const summary = needsSignIn ? "" : finishingSetup ? t("settings.codexAccounts.finishingSetup") : [formatAuthMethod(account.authMethod), formatPlanName(account.capacity.plan), remaining == null ? null : `${formatPercentage(remaining)} ${t("settings.codexAccounts.remaining")}`].filter(Boolean).join(" · ");
	const identity = <><UserRound data-testid="codex-account-avatar" className="mt-0.5 size-6 shrink-0 text-muted-foreground" aria-hidden="true" /><div className="min-w-0"><div className="flex items-center gap-2"><p className="truncate text-sm font-medium">{account.label}</p>{account.active ? <span className="rounded-full border border-success/30 bg-success/10 px-2 py-0.5 text-[10px] font-medium text-success">{t("settings.codexAccounts.inUse")}</span> : null}</div><p className="mt-1 flex min-w-0 items-center gap-1 text-xs text-muted-foreground">{confirmed ? <CircleCheck className="size-3.5 shrink-0 text-success" aria-hidden="true" /> : <CircleAlert className="size-3.5 shrink-0" aria-hidden="true" />}<span className="shrink-0">{authenticationLabel}</span>{summary ? <><span aria-hidden="true">·</span><span className="truncate">{summary}</span></> : null}{authentication.checking || authenticationRetryBusy ? <LoaderCircle className="size-3.5 shrink-0 animate-spin" aria-label={t("settings.codexAccounts.checking")} /> : null}</p></div></>;
	return (
		<div id={`codex-account-${account.id}`} data-account-id={account.id} tabIndex={-1} className="px-4 py-3 outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring">
			<div className={`flex justify-between gap-3 ${canUse ? "items-start" : "items-center"}`}>
				{needsSignIn
					? <div className="flex min-w-0 flex-1 items-start gap-3">{identity}</div>
					: <button type="button" className="flex min-w-0 flex-1 items-start gap-3 rounded-sm text-left focus:outline-none focus-visible:ring-2 focus-visible:ring-ring" aria-expanded={expanded} onClick={onToggle}>{identity}<ChevronDown className={`ml-auto size-4 shrink-0 text-muted-foreground transition-transform ${canUse ? "mt-1.5" : "self-center"} ${expanded ? "" : "-rotate-90"}`} aria-hidden="true" /></button>}
				{!needsSignIn && canUse ? <Button type="button" size="sm" variant="outline" aria-label={`${t("settings.codexAccounts.useAccount")} ${account.label}`} disabled={deviceMutationDisabled} onClick={onUseAccount}>{t("settings.codexAccounts.useAccountShort")}</Button> : null}
				{needsSignIn ? <div className="flex shrink-0 items-center gap-2"><Button type="button" size="sm" variant="outline" disabled={deviceMutationDisabled} onClick={onSignIn}>{t("settings.codexAccounts.signInAgain")}</Button><Tooltip><TooltipTrigger asChild><Button type="button" size="icon-sm" variant="outline" className="border-error/30 text-error hover:text-error" aria-label={t("settings.codexAccounts.delete")} disabled={deviceMutationDisabled || deleteBusy} onClick={onDelete}>{deleteBusy ? <LoaderCircle className="animate-spin" aria-hidden="true" /> : <Trash2 aria-hidden="true" />}</Button></TooltipTrigger><TooltipContent>{t("settings.codexAccounts.delete")}</TooltipContent></Tooltip></div> : null}
			</div>
			{expanded && !needsSignIn ? <><CodexAccountDetails account={account} resetCreditSupported={resetCreditSupported} mutationDisabled={mutationDisabled} resetBusy={resetBusy} retryBusy={authenticationRetryBusy} onUseReset={onUseReset} onRetry={onRetryAuthentication} /><div className="ml-9 mt-4 flex items-center gap-2 pb-1">{authentication.action === "retry" ? <Button type="button" size="sm" variant="outline" disabled={mutationDisabled || authenticationRetryBusy} onClick={onRetryAuthentication}>{authenticationRetryBusy ? <LoaderCircle className="animate-spin" aria-label={t("settings.codexAccounts.retryingAuthentication")} /> : null}{t("settings.codexAccounts.tryAgain")}</Button> : null}{authorized ? <Button type="button" size="sm" variant="outline" disabled={deviceMutationDisabled || logoutBusy} onClick={onLogout}>{logoutBusy ? <LoaderCircle className="animate-spin" aria-label={t("settings.codexAccounts.loggingOut")} /> : <LogOut aria-hidden="true" />}{t("settings.codexAccounts.logout")}</Button> : null}</div></> : null}
			{activeLogin ? <div className="ml-9 mt-4 pb-1"><CodexAccountLoginTerminalPanel activeLogin={activeLogin} pending={loginPending} onCheckAgain={onCheckLogin} onClose={onCloseLogin} onRetry={onRetryLogin} /></div> : null}
		</div>
	);
}
