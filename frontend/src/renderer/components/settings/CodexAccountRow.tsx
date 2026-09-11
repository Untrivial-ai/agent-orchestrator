import { ChevronDown, CircleAlert, CircleCheck, LoaderCircle, LogOut, Trash2, UserRound } from "lucide-react";
import { useTranslation } from "react-i18next";
import { codexAccountAuthorized, codexAccountSignedOut, codexAuthenticationDisplay } from "../../hooks/codex-accounts-state";
import type { CodexAccount, CodexActiveLogin } from "../../hooks/useCodexAccountsQuery";
import { Button } from "../ui/button";
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
	const remaining = account.capacity.remainingPercent;
	const authenticationLabel = t(authentication.key);
	const summary = [formatAuthMethod(account.authMethod), formatPlanName(account.capacity.plan), remaining == null ? null : `${formatPercentage(remaining)} ${t("settings.codexAccounts.remaining")}`].filter(Boolean).join(" · ");
	return (
		<div id={`codex-account-${account.id}`} data-account-id={account.id} tabIndex={-1} className="px-4 py-3 outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring">
			<div className="flex items-start justify-between gap-3"><button type="button" className="flex min-w-0 flex-1 items-start gap-3 rounded-sm text-left focus:outline-none focus-visible:ring-2 focus-visible:ring-ring" aria-expanded={expanded} onClick={onToggle}><UserRound data-testid="codex-account-avatar" className="mt-0.5 size-6 shrink-0 text-muted-foreground" aria-hidden="true" /><div className="min-w-0"><div className="flex items-center gap-2"><p className="truncate text-sm font-medium">{account.label}</p>{account.active ? <span className="rounded-full border border-success/30 bg-success/10 px-2 py-0.5 text-[10px] font-medium text-success">{t("settings.codexAccounts.inUse")}</span> : null}</div><p className="mt-1 flex items-center gap-1 text-xs text-muted-foreground">{confirmed ? <CircleCheck className="size-3.5 text-success" aria-hidden="true" /> : <CircleAlert className="size-3.5" aria-hidden="true" />}{authenticationLabel}{authentication.checking || authenticationRetryBusy ? <LoaderCircle className="size-3.5 animate-spin" aria-label={t("settings.codexAccounts.checking")} /> : null}</p>{summary ? <p className="mt-1 truncate text-xs text-muted-foreground">{summary}</p> : null}</div><ChevronDown className={`ml-auto mt-1 size-4 shrink-0 text-muted-foreground transition-transform ${expanded ? "" : "-rotate-90"}`} aria-hidden="true" /></button></div>
			{expanded ? <><CodexAccountDetails account={account} resetCreditSupported={resetCreditSupported} mutationDisabled={mutationDisabled} resetBusy={resetBusy} onUseReset={onUseReset} /><div className="ml-9 mt-4 flex items-center gap-2 pb-1">{canUse && confirmed ? <Button type="button" size="sm" variant="outline" disabled={deviceMutationDisabled} onClick={onUseAccount}>{t("settings.codexAccounts.useAccount")}</Button> : null}{authentication.action === "retry" ? <Button type="button" size="sm" variant="outline" disabled={mutationDisabled || authenticationRetryBusy} onClick={onRetryAuthentication}>{authenticationRetryBusy ? <LoaderCircle className="animate-spin" aria-label={t("settings.codexAccounts.retryingAuthentication")} /> : null}{t("settings.codexAccounts.tryAgain")}</Button> : null}{authorized ? <Button type="button" size="sm" variant="outline" disabled={deviceMutationDisabled || logoutBusy} onClick={onLogout}>{logoutBusy ? <LoaderCircle className="animate-spin" aria-label={t("settings.codexAccounts.loggingOut")} /> : <LogOut aria-hidden="true" />}{t("settings.codexAccounts.logout")}</Button> : authentication.action === "reauthenticate" && account.status !== "broken" ? <><Button type="button" size="sm" variant="outline" disabled={deviceMutationDisabled} onClick={onSignIn}>{t("settings.codexAccounts.signInAgain")}</Button>{codexAccountSignedOut(account) ? <Button type="button" size="sm" variant="outline" className="text-error hover:text-error" disabled={deviceMutationDisabled || deleteBusy} onClick={onDelete}>{deleteBusy ? <LoaderCircle className="animate-spin" aria-label={t("settings.codexAccounts.deleting")} /> : <Trash2 aria-hidden="true" />}{t("settings.codexAccounts.delete")}</Button> : null}</> : null}</div>{activeLogin ? <div className="ml-9 mt-4 pb-1"><CodexAccountLoginTerminalPanel activeLogin={activeLogin} pending={loginPending} onCheckAgain={onCheckLogin} onClose={onCloseLogin} onRetry={onRetryLogin} /></div> : null}</> : null}
		</div>
	);
}
